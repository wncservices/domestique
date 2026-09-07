import { getWebInstrumentations, initializeFaro } from '@grafana/faro-web-sdk'
import { TracingInstrumentation } from '@grafana/faro-web-tracing'

// Shared by both Vite entries (main.ts and landing/main.ts) rather than
// duplicated inline — they're otherwise deliberately independent bundles
// (see landing/main.ts's own comment), but there's nothing app-specific
// here for either to diverge on.
//
// One Faro app.name for both: they're the same product's RUM signal, and
// Faro's own page/view instrumentation already distinguishes which page an
// event came from, so splitting the name would only fragment the
// domestique-overview dashboard's frontend panels for no benefit.
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
export function initTelemetry(): void {
  initializeFaro({
    url: '/faro/collect',
    app: {
      name: 'domestique-frontend',
      environment: import.meta.env.MODE,
    },
    instrumentations: [...getWebInstrumentations(), new TracingInstrumentation()],
  })
}
