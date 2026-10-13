package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/wncservices/domestique/apps/api/internal/model"
)

// Package-level, registered against a private registry: one process, one set
// of metrics, no reason to thread a registry through Server for this.
// Recorded through the OTel metrics API rather than prometheus/client_golang
// directly, so this app's metrics and traces (internal/telemetry) share one
// instrumentation story — but still exposed as Prometheus text at
// GET /api/metrics, because that is what this deployment's ServiceMonitor
// scrapes, not an OTLP metrics pipeline.
var (
	metricsRegistry = prometheus.NewRegistry()
	meter           = newMeter(metricsRegistry)

	httpRequestsTotal = must(meter.Int64Counter(
		"domestique_http_requests_total",
		metric.WithDescription("HTTP requests by method and status."),
	))

	// No path label, deliberately: several routes carry a route slug or
	// account id in the path (/api/routes/{slug}, /api/accounts/{id}), and
	// labeling by raw path would give each distinct route its own metrics
	// series forever — unbounded cardinality that grows with the library,
	// not with the code. method+status stays bounded by the handful of
	// verbs and status codes this API actually returns.
	httpRequestDuration = must(meter.Float64Histogram(
		"domestique_http_request_duration_seconds",
		metric.WithDescription("HTTP request duration by method and status."),
	))

	// format is bounded ("json" or "image"), not a slug — same
	// no-unbounded-label reasoning as httpRequestDuration above. Exists so a
	// regression like the one this metric was added for (every track-preview
	// response running 1.5-2.6MB of JSON, discovered only by manually pulling
	// Tempo traces one at a time) shows up in a dashboard instead.
	//
	// Explicit byte-sized bucket boundaries: the OTel SDK's default bucket
	// set is tuned for second-denominated durations (largest boundary 10),
	// so every sample here — bytes, five to seven digits — would otherwise
	// land in the same +Inf overflow bucket, making a histogram in name
	// only (sum/count still work, but no meaningful p50/p95).
	trackPreviewResponseBytes = must(meter.Float64Histogram(
		"domestique_track_preview_response_bytes",
		metric.WithDescription("Track-preview response size in bytes, by format (json/image)."),
		metric.WithExplicitBucketBoundaries(
			1_000, 10_000, 50_000, 100_000, 250_000, 500_000, 1_000_000, 2_000_000, 5_000_000,
		),
	))

	// The two metrics docs/plan.md's own "Phase 6" describes: a staleness
	// gauge and a per-account error counter, so an alert can catch "pushes
	// stopped" instead of a rider noticing a route missing at the start of
	// a ride. Labeled by provider and op, never by account or rider: the
	// account id is "<provider>:<rider>" and these series are readable by
	// anyone who can scrape. The trade is that "pushes stopped" is now
	// per provider, not per rider; the push log names who failed.
	pushLastSuccessTimestamp = must(meter.Float64Gauge(
		"domestique_push_last_success_timestamp_seconds",
		metric.WithDescription("Unix time of the last successful push per account, by op (create/update/delete)."),
	))

	// pushAccountsFailing and garminSessionsExpired exist because the labels
	// above deliberately name no rider, which hides one stuck account behind
	// another that keeps succeeding. They count, and name nobody: how many linked
	// accounts' latest push failed, and how many Garmin sessions are already past
	// expiry. Alert on these being above zero.
	pushAccountsFailing = must(meter.Float64Gauge(
		"domestique_push_accounts_failing",
		metric.WithDescription("Linked accounts whose most recent push attempt failed, by provider."),
	))
	garminSessionsExpired = must(meter.Float64Gauge(
		"domestique_garmin_sessions_expired",
		metric.WithDescription("Linked Garmin sessions already past their expected expiry."),
	))

	pushErrorsTotal = must(meter.Int64Counter(
		"domestique_push_errors_total",
		metric.WithDescription("Failed push attempts per account."),
	))

	// routeBuilderErrorsTotal is the same "an operator should see this in a
	// dashboard, not learn about it from a rider" reasoning as
	// pushErrorsTotal above, for the route builder's own outbound calls —
	// a per-request span (via otelhttp, see internal/routing) already
	// covers "did this one call succeed," so this is only for the
	// aggregate: is the routing engine reliably reachable at all.
	// Labeled by endpoint (preview/suggest), not by rider — an outage
	// shows up the same way regardless of who triggered a given request.
	routeBuilderErrorsTotal = must(meter.Int64Counter(
		"domestique_routebuilder_errors_total",
		metric.WithDescription("Failed route-builder calls to the routing engine, by endpoint."),
	))

	// garminSessionExpiry mirrors the push-staleness gauges' own reasoning:
	// a value an alert rule watches, not a number only a person reads.
	// Recorded every reconcile tick, expiring soon or not, so the threshold
	// for "soon" lives in the alert rule rather than being baked into what
	// gets exposed here. One series: the earliest expiry among connected
	// riders. It used to carry a rider label so an operator could see whom to
	// ask to reconnect, but that put every rider's name and session lifetime on
	// an endpoint; the Warn log from checkGarminExpiry names the rider instead.
	garminSessionExpiry = must(meter.Float64Gauge(
		"domestique_garmin_session_expiry_timestamp_seconds",
		metric.WithDescription("Unix time each rider's stored Garmin session is expected to stop working."),
	))
)

// recordGarminSessionExpiry is checkGarminExpiry's own metric-recording
// call, kept here for the same reason recordTrackPreviewSize is: metrics.go
// owns every instrument, callers just report a value against one.
//
// earliest is the first expiry among sessions that still work, or the zero time
// when there is none (nobody connected, or every session already lapsed): then
// the gauge reads 0, not the last rider's value. An alert on this gauge needs
// `> 0`; "a session has already lapsed" is garminSessionsExpired's job.
func recordGarminSessionExpiry(ctx context.Context, earliest time.Time, expired int) {
	var v float64
	if !earliest.IsZero() {
		v = float64(earliest.Unix())
	}
	garminSessionExpiry.Record(ctx, v)
	garminSessionsExpired.Record(ctx, float64(expired))
}

// pushOutcomes remembers, per account, whether its latest push attempt failed.
// The per-provider counter and timestamp cannot show one account stuck while
// another of the same provider succeeds (the success refreshes the shared
// timestamp), so the number of accounts whose latest attempt failed is kept
// alongside. Only the count leaves this struct: it holds account ids, which
// carry a rider's name, and they are never a label.
var pushOutcomes = struct {
	sync.Mutex
	failing map[string]bool
}{failing: map[string]bool{}}

// recordPushOutcome notes one attempt and republishes the failing count for
// every provider (so a provider that just recovered reads 0, not its last value).
func recordPushOutcome(ctx context.Context, accountID string, failed bool) {
	pushOutcomes.Lock()
	defer pushOutcomes.Unlock()
	if failed {
		pushOutcomes.failing[accountID] = true
	} else {
		delete(pushOutcomes.failing, accountID)
	}
	publishPushFailingLocked(ctx)
}

// forgetUnlinkedPushOutcomes drops accounts that are no longer linked, so an
// account that failed and was then unlinked does not hold the count above zero
// for good. Run on the reconcile tick, with the ids that exist now.
func forgetUnlinkedPushOutcomes(ctx context.Context, linked map[string]bool) {
	pushOutcomes.Lock()
	defer pushOutcomes.Unlock()
	for id := range pushOutcomes.failing {
		if !linked[id] {
			delete(pushOutcomes.failing, id)
		}
	}
	publishPushFailingLocked(ctx)
}

func publishPushFailingLocked(ctx context.Context) {
	counts := map[string]int{string(model.ProviderGarmin): 0, string(model.ProviderWahoo): 0}
	for id := range pushOutcomes.failing {
		counts[providerOf(id)]++
	}
	for provider, n := range counts {
		pushAccountsFailing.Record(ctx, float64(n), metric.WithAttributes(attribute.String("provider", provider)))
	}
}

// newMeter gives metrics.go its own MeterProvider bound to reg, independent
// of whatever internal/telemetry sets as the *global* one for traces. Kept
// separate on purpose: GET /api/metrics has to work in every test and in
// `just demo` on a laptop, none of which call telemetry.Setup, and a package
// var initializer has no error path to fail through if it depended on that
// call having happened first.
func newMeter(reg *prometheus.Registry) metric.Meter {
	exporter, err := otelprom.New(
		otelprom.WithRegisterer(reg),
		// Prometheus has no first-class notion of instrumentation scope or
		// target resource — without these, the bridge invents an
		// otel_scope_* label on every single series plus a target_info
		// gauge, none of which this deployment's dashboards or alerts have
		// any use for. One process, one meter: which scope produced a
		// metric is not information worth a label.
		otelprom.WithoutScopeInfo(),
		otelprom.WithoutTargetInfo(),
	)
	if err != nil {
		panic(err)
	}
	return sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter)).
		Meter("github.com/wncservices/domestique/apps/api/internal/api")
}

// must panics on the error a package-level instrument constructor can only
// return for a malformed name or a conflicting redeclaration — both are bugs
// in this file, not something a request can trigger, so there is nothing a
// caller could do with the error that panicking at startup does not already
// do better.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// metricsHandler is /api/metrics itself — exempted from the rider session in
// authenticate, because a scraper has no rider identity to present, and put
// behind metricsGate instead.
func metricsHandler() http.Handler {
	return promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{Registry: metricsRegistry})
}

// instrument records the two HTTP metrics above around every request. Wraps
// outside authenticate/logRequests so a request that never reaches a route
// (an unmatched path, a panic recovered elsewhere) still counts — the same
// reasoning logRequests already applies by sitting outermost.
func instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		attrs := metric.WithAttributes(
			attribute.String("method", normalizeMethod(r.Method)),
			attribute.String("status", strconv.Itoa(rec.status)),
		)
		httpRequestsTotal.Add(r.Context(), 1, attrs)
		httpRequestDuration.Record(r.Context(), time.Since(started).Seconds(), attrs)
	})
}

// statusRecorder captures the status code a handler wrote — plain
// http.ResponseWriter has no way to ask afterward, since WriteHeader only
// sends it, it does not store it anywhere the caller can read back.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// recordTrackPreviewSize is handleTrackPreview/handleTrackPreviewImage's
// shared metric-recording call, kept here rather than importing
// metric/attribute into server.go just for this one call site.
func recordTrackPreviewSize(ctx context.Context, format string, bytes int) {
	trackPreviewResponseBytes.Record(ctx, float64(bytes), metric.WithAttributes(attribute.String("format", format)))
}

// recordRouteBuilderError is handleRouteBuilderPreview/Suggest's own
// metric-recording call — endpoint is "preview" or "suggest".
func recordRouteBuilderError(ctx context.Context, endpoint string) {
	routeBuilderErrorsTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("endpoint", endpoint)))
}

// recordPushResult is sync.Apply's onResult callback for handlePush — the
// seam that lets internal/sync stay pure and unaware that metrics exist at
// all, per its own package doc. Noop items are skipped: they mean nothing
// changed, not that a push succeeded or failed.
//
// context.Background(), not a request context: sync.Apply's onResult
// callback carries none today. Metric recording does not need span linkage
// the way a trace does, so this is a placeholder for the eventual real
// context, not a gap that needs its own fix first.
func (s *Server) recordPushResult(item model.PlanItem, err error) {
	if item.Op == model.OpNoop {
		return
	}
	// By provider, not by account: an account id is "provider:rider" and a
	// metric label is readable by anyone who can scrape. Which rider's push
	// failed is in the log line push finished with failures; the metric only
	// has to say that pushes to a provider are failing or have stopped.
	attrs := metric.WithAttributes(
		attribute.String("provider", providerOf(item.AccountID)),
		attribute.String("op", string(item.Op)),
	)
	ctx := context.Background()
	recordPushOutcome(ctx, item.AccountID, err != nil)
	if err != nil {
		pushErrorsTotal.Add(ctx, 1, attrs)
		return
	}
	pushLastSuccessTimestamp.Record(ctx, float64(time.Now().Unix()), attrs)
}

// normalizeMethod keeps the method label to the verbs HTTP defines. A scrape
// of /api/metrics is cheap only while the number of series is bounded, and the
// raw method is attacker-chosen: any token a client sends, before it has been
// authenticated, would otherwise become a series held in memory for good.
func normalizeMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return m
	}
	return "OTHER"
}

// providerOf is the provider half of an account id ("garmin:rider"). Only the
// providers this app has an adapter for come out; anything else is "other", so
// a malformed id can neither leak a rider's name through the label nor add a
// series.
func providerOf(accountID string) string {
	provider, _, _ := strings.Cut(accountID, ":")
	switch provider {
	case string(model.ProviderGarmin), string(model.ProviderWahoo):
		return provider
	}
	return "other"
}

// metricsGate guards GET /api/metrics. The endpoint is outside the rider
// session (a scraper has none), so it carries its own credential: the bearer
// token in DOMESTIQUE_METRICS_TOKEN, which a ServiceMonitor sends as
// `authorization.credentials`.
//
// A token was chosen over a trusted-peer CIDR because, behind Traefik, every
// public request reaches the app from the ingress's address, so "trusted peer"
// would have to mean "the Prometheus pod's range", which is a cluster detail
// that moves, where a Secret reference does not.
//
//   - A token is configured: it is required, whatever the auth mode.
//   - No token, auth on: closed. Metrics are off until someone sets one,
//     rather than open to whoever can reach the port.
//   - No token, auth off (mode none: a laptop, `just demo`): open, as before.
func (s *Server) metricsGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.TrimSpace(s.MetricsToken) != "":
			if !bearerMatches(r.Header.Get("Authorization"), strings.TrimSpace(s.MetricsToken)) {
				metricsDenied(w)
				return
			}
		case s.authenticator().Enabled():
			metricsDenied(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func metricsDenied(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "metrics need a bearer token"})
}

// bearerMatches compares in constant time, over digests so the length of the
// expected token is not observable either.
func bearerMatches(header, token string) bool {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	got := sha256.Sum256([]byte(header[len(prefix):]))
	want := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}
