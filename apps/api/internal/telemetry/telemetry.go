// Package telemetry wires distributed tracing into the server: spans
// exported over OTLP to the OTel Collector, the same way Traefik already
// does it (see lab/AGENTS.md's Observability wiring). Metrics are not this
// package's concern — internal/api/metrics.go owns its own OTel meter and
// Prometheus bridge, because GET /api/metrics has to work in every test and
// in `just demo` on a laptop, neither of which calls Setup.
package telemetry

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
)

// Shutdown flushes any spans still buffered in the batch exporter. Call
// before the process exits — nil is fine when Setup never ran (tracing not
// configured), so callers do not need their own guard.
type Shutdown func(context.Context) error

// Setup installs the global TracerProvider and text-map propagator.
//
// A no-op Shutdown is returned unless OTEL_EXPORTER_OTLP_ENDPOINT or
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is set — otlptracehttp reads those
// standard env vars itself, so this package never hardcodes a collector
// address — because without a collector configured, a batch exporter would
// just retry against its http://localhost:4318 default forever: one more
// thing logging failures nobody deployed anything to fix.
func Setup(ctx context.Context, serviceName, version string) (Shutdown, error) {
	noop := func(context.Context) error { return nil }
	if !tracingConfigured() {
		// Still worth a real propagator: an inbound traceparent header
		// (from Traefik, or from a future upstream that does export) should
		// still thread through outbound calls this app makes, even though
		// nothing here starts new spans of its own.
		otel.SetTextMapPropagator(propagation.TraceContext{})
		return noop, nil
	}

	// OTEL_SERVICE_NAME overrides the caller's serviceName, not the other
	// way around: resource.Default() below already reads that env var on
	// its own, but resource.Merge's "b wins" semantics (confirmed against
	// its own doc comment) mean the explicit semconv.ServiceName(...) two
	// lines down would always beat it silently otherwise. Lets one
	// deployment (preview.domestique.dev's env, set in domestique-infra's
	// values.yaml) distinguish itself from production's traces without a
	// second call-site-specific parameter threading through main.go.
	if v := os.Getenv("OTEL_SERVICE_NAME"); v != "" {
		serviceName = v
	}

	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(version),
	))
	if err != nil {
		return noop, fmt.Errorf("telemetry resource: %w", err)
	}

	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return noop, fmt.Errorf("otlp trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return tp.Shutdown, nil
}

func tracingConfigured() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}
