package api

import (
	"context"
	"net/http"
	"strconv"
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
	// a ride. Labeled by account (already "<provider>:<rider>", the same
	// id used everywhere else in this codebase) rather than splitting
	// provider/rider into two labels — one id an operator already
	// recognizes, not two they have to mentally rejoin.
	pushLastSuccessTimestamp = must(meter.Float64Gauge(
		"domestique_push_last_success_timestamp_seconds",
		metric.WithDescription("Unix time of the last successful push per account, by op (create/update/delete)."),
	))

	pushErrorsTotal = must(meter.Int64Counter(
		"domestique_push_errors_total",
		metric.WithDescription("Failed push attempts per account."),
	))

	// garminSessionExpiry mirrors the push-staleness gauges' own reasoning:
	// a value an alert rule watches, not a number only a person reads.
	// Recorded every reconcile tick for every connected Garmin rider,
	// expiring soon or not, so the threshold for "soon" lives in the alert
	// rule rather than being baked into what gets exposed here. Labeled by
	// rider, not by account id — a Garmin session is one per rider by
	// construction (one account per rider per provider), and "rider" is
	// what an operator reading the alert would want to reconnect.
	garminSessionExpiry = must(meter.Float64Gauge(
		"domestique_garmin_session_expiry_timestamp_seconds",
		metric.WithDescription("Unix time each rider's stored Garmin session is expected to stop working."),
	))
)

// recordGarminSessionExpiry is checkGarminExpiry's own metric-recording
// call, kept here for the same reason recordTrackPreviewSize is: metrics.go
// owns every instrument, callers just report a value against one.
func recordGarminSessionExpiry(ctx context.Context, rider string, expiry time.Time) {
	garminSessionExpiry.Record(ctx, float64(expiry.Unix()), metric.WithAttributes(attribute.String("rider", rider)))
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

// metricsHandler is /api/metrics itself — exempted from auth in authenticate,
// the same way /api/health and /api/config already are: a scraper has no
// rider identity to present.
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
			attribute.String("method", r.Method),
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
	attrs := metric.WithAttributes(
		attribute.String("account", item.AccountID),
		attribute.String("op", string(item.Op)),
	)
	ctx := context.Background()
	if err != nil {
		pushErrorsTotal.Add(ctx, 1, attrs)
		return
	}
	pushLastSuccessTimestamp.Record(ctx, float64(time.Now().Unix()), attrs)
}
