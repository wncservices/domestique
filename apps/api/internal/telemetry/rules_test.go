package telemetry_test

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/wncservices/domestique/apps/api/internal/telemetry"
)

// The exporter also rewrites the older semconv spellings and the query
// attribute, which a client span does not carry today but a newer or older
// otelhttp would. Driven with hand-made spans so each rule is exercised.
func TestRedactingExporterRewritesEveryURLAttribute(t *testing.T) {
	mem := tracetest.NewInMemoryExporter()
	exp := telemetry.RedactingExporter(mem)

	stubs := tracetest.SpanStubs{{
		Name: "GET example",
		Attributes: []attribute.KeyValue{
			attribute.String("url.full", "https://user:pw@api.example/v1/thing/"+strings.Repeat("a", 43)+"?ticket=ST-1&q=home"),
			attribute.String("http.url", "https://api.example/v1/thing?apikey=K"),
			attribute.String("url.query", "ticket=ST-1&q=home"),
			attribute.String("http.target", "/v1/thing/12345678?apikey=K"),
			attribute.String("server.address", "api.example"),
			attribute.Int("http.response.status_code", 200),
		},
	}}
	if err := exp.ExportSpans(context.Background(), stubs.Snapshots()); err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, a := range mem.GetSpans()[0].Attributes {
		got[string(a.Key)] = a.Value.String()
	}
	want := map[string]string{
		"url.full":                  "https://api.example/v1/thing/[redacted]",
		"http.url":                  "https://api.example/v1/thing",
		"url.query":                 "[redacted]",
		"http.target":               "/v1/thing/{id}",
		"server.address":            "api.example",
		"http.response.status_code": "200",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	for _, v := range got {
		if strings.Contains(v, "ST-1") || strings.Contains(v, "apikey") || strings.Contains(v, "pw@") {
			t.Errorf("a secret survived: %q", v)
		}
	}
}
