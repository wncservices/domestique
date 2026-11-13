package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The request duration is recorded in seconds, so its buckets must be in
// seconds too. On the OTel defaults (0, 5, 10, 25 ... 10000, meant for
// milliseconds) every request landed in le="5" and p99 always read about
// 4.95 s. A fast request has to fall in a sub-second bucket.
func TestRequestDurationBucketsAreInSeconds(t *testing.T) {
	// A status nothing else in the suite answers keeps this series to this
	// test, whatever else has recorded into the shared registry.
	const labels = `method="GET",status="418"`
	bucket := `domestique_http_request_duration_seconds_bucket{` + labels + `,le="0.1"} `
	count := `domestique_http_request_duration_seconds_count{` + labels + `} `
	before, _ := metricValue(t, scrape(t), bucket)

	h := instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	out := scrape(t)
	got, ok := metricValue(t, out, bucket)
	if !ok {
		t.Fatalf("no le=\"0.1\" bucket: the histogram is not on second boundaries\n%s",
			grepLines(out, "request_duration_seconds_bucket{"+labels))
	}
	if got != before+1 {
		t.Fatalf("a fast request is not in the 100 ms bucket: %v, want %v", got, before+1)
	}
	if n, _ := metricValue(t, out, count); n != got {
		t.Fatalf("count %v, 100 ms bucket %v: a no-op request took longer than 100 ms?", n, got)
	}
}
