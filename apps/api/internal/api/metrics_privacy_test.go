package api

import (
	"github.com/wncservices/domestique/apps/api/internal/testdb"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// scrape renders the process-wide registry the way Prometheus would read it.
func scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/metrics", nil))
	return rec.Body.String()
}

// A metric label is readable by whoever can scrape; a rider's name is personal
// data and does not belong in one. Push metrics are per provider.
func TestPushMetricsCarryNoRiderName(t *testing.T) {
	s := &Server{}
	s.recordPushResult(model.PlanItem{Op: model.OpCreate, AccountID: "garmin:zorbatron"}, nil)
	s.recordPushResult(model.PlanItem{Op: model.OpUpdate, AccountID: "wahoo:zorbatron"}, http.ErrBodyNotAllowed)

	out := scrape(t)
	if strings.Contains(out, "zorbatron") {
		t.Fatalf("a rider's name is in the metrics:\n%s", grepLines(out, "zorbatron"))
	}
	if !strings.Contains(out, `domestique_push_errors_total{op="update",provider="wahoo"}`) {
		t.Errorf("push errors are not labelled by provider:\n%s", grepLines(out, "push_errors"))
	}
	if !strings.Contains(out, `domestique_push_last_success_timestamp_seconds{op="create",provider="garmin"}`) {
		t.Errorf("push success is not labelled by provider:\n%s", grepLines(out, "push_last_success"))
	}
}

// The Garmin session gauge answers "does anyone's session lapse soon", not
// "whose": one series, the earliest expiry, no rider.
func TestGarminExpiryGaugeCarriesNoRiderName(t *testing.T) {
	db, err := source.OpenDB(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	links, err := providerlink.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	for _, rider := range []string{"zorbatron", "quillfeather"} {
		if _, err := links.Save("garmin", rider, providerlink.Connection{Email: rider + "@example.com", Secret: "x"}); err != nil {
			t.Fatal(err)
		}
	}

	(&Server{Links: links}).checkGarminExpiry(t.Context())

	out := scrape(t)
	for _, name := range []string{"zorbatron", "quillfeather"} {
		if strings.Contains(out, name) {
			t.Fatalf("rider %q is in the metrics:\n%s", name, grepLines(out, name))
		}
	}
	if !strings.Contains(out, "domestique_garmin_session_expiry_timestamp_seconds") {
		t.Fatalf("the gauge is gone:\n%s", grepLines(out, "garmin_session"))
	}
}

func TestMetricsMethodLabelIsBounded(t *testing.T) {
	h := instrument(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, m := range []string{"GET", "POST", "BREW-COFFEE", "xyzzy", "PURGE"} {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Method = m
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	out := scrape(t)
	for _, bad := range []string{`method="BREW-COFFEE"`, `method="xyzzy"`, `method="PURGE"`} {
		if strings.Contains(out, bad) {
			t.Errorf("arbitrary method became a label: %s", bad)
		}
	}
	for _, want := range []string{`method="GET"`, `method="POST"`, `method="OTHER"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestNormalizeMethod(t *testing.T) {
	for in, want := range map[string]string{
		"GET": "GET", "HEAD": "HEAD", "POST": "POST", "PUT": "PUT", "PATCH": "PATCH",
		"DELETE": "DELETE", "OPTIONS": "OPTIONS", "CONNECT": "CONNECT", "TRACE": "TRACE",
		"get": "OTHER", "PROPFIND": "OTHER", "": "OTHER", "G\x00T": "OTHER",
	} {
		if got := normalizeMethod(in); got != want {
			t.Errorf("normalizeMethod(%q) = %q, want %q", in, got, want)
		}
	}
}

func grepLines(s, sub string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
