package telemetry_test

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/wncservices/domestique/apps/api/internal/geocoding"
	"github.com/wncservices/domestique/apps/api/internal/telemetry"
	"github.com/wncservices/domestique/apps/api/internal/weather"
)

// spanValues is every string value on every recorded span, attribute keys
// included, so a test can assert a secret appears nowhere.
func spanValues(exp *tracetest.InMemoryExporter) []string {
	var out []string
	for _, s := range exp.GetSpans() {
		out = append(out, s.Name)
		for _, a := range s.Attributes {
			out = append(out, string(a.Key)+"="+a.Value.String())
		}
	}
	return out
}

func newExporter(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.RedactingExporter(exp)))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return exp
}

// Audit N2: otelhttp records the full request URL on a client span, so the
// weather API key, ride coordinates and a typed address reached the trace
// backend. The real weather and geocoding clients are fired at a test server.
func TestOutboundSpansCarryNoQueryString(t *testing.T) {
	exp := newExporter(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hourly":{"time":[]}}`))
	}))
	defer srv.Close()

	_, _ = weather.New(srv.URL, "SECRETKEY123", nil).Forecast(context.Background(), weather.Location{Lat: 51.2345, Lon: 4.5678})
	_, _ = geocoding.New(srv.URL).Search(context.Background(), "12 Rue Privee, Home Town")

	spans := spanValues(exp)
	if len(exp.GetSpans()) < 2 {
		t.Fatalf("recorded %d spans, want a client span per call", len(exp.GetSpans()))
	}
	for _, v := range spans {
		for _, leak := range []string{"SECRETKEY123", "apikey", "latitude", "longitude", "51.23", "Rue", "Privee", "?"} {
			if strings.Contains(v, leak) {
				t.Errorf("span carries %q in %q", leak, v)
			}
		}
	}
}

func TestOutboundSpansKeepSchemeHostAndRoute(t *testing.T) {
	exp := newExporter(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	token := strings.Repeat("a1B2", 11)[:43]
	client := &http.Client{Transport: telemetry.OutboundTransport(http.DefaultTransport)}
	resp, err := client.Get(srv.URL + "/v1/forecast/" + token + "/file.fit?ticket=ST-1-secret&x=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	var full string
	for _, s := range exp.GetSpans() {
		for _, a := range s.Attributes {
			if a.Key == attribute.Key("url.full") {
				full = a.Value.AsString()
			}
		}
	}
	if !strings.HasPrefix(full, "http://127.0.0.1:") || !strings.Contains(full, "/v1/forecast/") || !strings.HasSuffix(full, "/file.fit") {
		t.Errorf("url.full = %q, want scheme, host and the route kept", full)
	}
	for _, v := range spanValues(exp) {
		if strings.Contains(v, token) || strings.Contains(v, "ST-1-secret") {
			t.Errorf("span carries a secret in %q", v)
		}
	}
}

// Every outbound client must build its transport through telemetry
// .OutboundTransport; a client that calls otelhttp.NewTransport itself skips
// the span name formatter, and a future change to that helper would miss it.
// This is the twelve-client checklist from AGENTS.md as a test.
func TestEveryOutboundClientUsesTheRedactingTransport(t *testing.T) {
	root := ".."
	var users []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		if strings.Contains(filepath.ToSlash(path), "/telemetry/") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		if strings.Contains(src, "otelhttp.NewTransport(") {
			t.Errorf("%s builds its own otelhttp transport, use telemetry.OutboundTransport", path)
		}
		if strings.Contains(src, "telemetry.OutboundTransport(") {
			users = append(users, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 12 {
		t.Errorf("%d clients use telemetry.OutboundTransport (%v), want the 12 outbound clients", len(users), users)
	}
}
