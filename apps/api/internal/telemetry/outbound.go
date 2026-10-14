package telemetry

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// OutboundTransport is the transport every outbound HTTP client of this app
// wraps http.DefaultTransport in, so its calls show up as spans under the
// request that triggered them.
//
// otelhttp records the request URL on the client span, query string included.
// For this app that is a weather API key, ride coordinates, a rider's typed
// address and a single-use Garmin SSO ticket, none of which belong in a trace
// backend. The library has no option to change that attribute (and sets it
// after the span starts, so a span processor cannot either), so the span is
// named from method and host only here, and the URL attributes are rewritten
// on the way out by RedactingExporter, which Setup puts in front of the OTLP
// exporter. Use this instead of otelhttp.NewTransport; a test fails if a
// client does not.
func OutboundTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Host
		}),
	)
}

// RedactingExporter wraps inner so that no span reaches it with a query string
// or a token in its URL attributes.
func RedactingExporter(inner sdktrace.SpanExporter) sdktrace.SpanExporter {
	return redactingExporter{inner}
}

type redactingExporter struct{ sdktrace.SpanExporter }

func (e redactingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		out[i] = redactedSpan{s}
	}
	return e.SpanExporter.ExportSpans(ctx, out)
}

// redactedSpan is a span whose URL attributes have been rewritten; everything
// else is the original.
type redactedSpan struct{ sdktrace.ReadOnlySpan }

func (s redactedSpan) Attributes() []attribute.KeyValue {
	in := s.ReadOnlySpan.Attributes()
	out := make([]attribute.KeyValue, len(in))
	for i, a := range in {
		switch string(a.Key) {
		case "url.full", "http.url":
			a = attribute.String(string(a.Key), RedactURL(a.Value.AsString()))
		case "url.query":
			if a.Value.AsString() != "" {
				a = attribute.String(string(a.Key), redacted)
			}
		case "http.target":
			a = attribute.String(string(a.Key), redactPath(strings.SplitN(a.Value.AsString(), "?", 2)[0]))
		}
		out[i] = a
	}
	return out
}

const redacted = "[redacted]"

// RedactURL keeps a URL's scheme, host and route and drops everything else
// that can identify a person or open a door: userinfo, query, fragment, and
// any path segment that looks like a token or an id.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return redacted
	}
	return u.Scheme + "://" + u.Host + redactPath(u.EscapedPath())
}

// redactPath replaces path segments that are credentials or identifiers
// rather than part of the route: anything long enough to be a token or
// presigned-URL key, and long numeric ids. Short, human-readable segments
// (v1, forecast, search, file.fit) are the route and stay.
func redactPath(p string) string {
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		switch {
		case len(seg) >= 20:
			segs[i] = redacted
		case len(seg) >= 5 && strings.Trim(seg, "0123456789") == "":
			segs[i] = "{id}"
		}
	}
	return strings.Join(segs, "/")
}
