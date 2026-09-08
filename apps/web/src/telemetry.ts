import { getWebInstrumentations, initializeFaro } from '@grafana/faro-web-sdk'
import { TracingInstrumentation } from '@grafana/faro-web-tracing'

// Shared by both Vite entries (main.ts and landing/main.ts) rather than
// duplicated inline — they're otherwise deliberately independent bundles
// (see landing/main.ts's own comment), but there's nothing app-specific
// here for either to diverge on.
//
// app.name matches whatever service.name the backend uses for the same
// deployment (internal/telemetry.Setup, overridable via OTEL_SERVICE_NAME —
// see domestique-infra's values.yaml dev: block): "domestique" on
// production, so a trace started by a click continues into the backend
// under one Tempo service rather than two, distinguished at the span level
// by span.kind rather than by service name. preview.domestique.dev is the
// one exception — kept as its own "preview-domestique" service so preview
// traffic doesn't mix into production's RED metrics on the
// domestique-overview dashboard. There's no build-time env var that
// differs per host (all three hosts serve the same built bundle), so this
// reads window.location.hostname at runtime instead — the same thing the
// backend's OTEL_SERVICE_NAME override effectively encodes per-deployment,
// just decided in the browser instead of at pod-start.
//
// url is a same-origin relative path (Alloy's faro.receiver behind
// lab/traefik's PathPrefix(`/faro`) route — see lab/alloy/templates/
// ingressroute.yaml) rather than an absolute hostname, so this works
// unchanged across domestique.dev, app.domestique.dev and
// preview.domestique.dev.
//
// No propagateTraceHeaderCorsUrls: the API is same-origin (vite.config.ts's
// dev proxy and the production Go server both serve /api on the same host
// as the page), and OpenTelemetry's web SDK already propagates trace
// headers to the current origin by default (see shouldPropagateTraceHeaders
// in @opentelemetry/sdk-trace-web) — that option only extends propagation
// to a *different* origin, which would mean leaking trace context to
// Auth0/the protomaps CDN/Garmin/Wahoo. Leaving it unset is the correct
// default here, not an oversight.
const isPreview = window.location.hostname === 'preview.domestique.dev'

export function initTelemetry(): void {
  initializeFaro({
    url: '/faro/collect',
    app: {
      name: isPreview ? 'preview-domestique' : 'domestique',
      environment: isPreview ? 'preview' : 'production',
    },
    instrumentations: [...getWebInstrumentations(), new TracingInstrumentation()],
  })
}
