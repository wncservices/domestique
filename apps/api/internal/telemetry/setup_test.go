package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Setup must put the redaction in front of the real exporter, not leave it to
// callers: this drives the provider Setup builds with an in-memory exporter in
// place of OTLP.
func TestSetupsProviderRedactsBeforeExporting(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := newProvider(exp, resource.Empty())
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	resp, err := (&http.Client{Transport: OutboundTransport(http.DefaultTransport)}).Get(srv.URL + "/search?q=home+address")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}

	spans := exp.GetSpans()
	if len(spans) == 0 {
		t.Fatal("no spans exported")
	}
	for _, s := range spans {
		for _, a := range s.Attributes {
			if strings.Contains(a.Value.Emit(), "home") {
				t.Errorf("exported span carries %s=%s", a.Key, a.Value.Emit())
			}
		}
	}
}
