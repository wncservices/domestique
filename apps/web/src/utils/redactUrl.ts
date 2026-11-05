import type { TransportItem } from '@grafana/faro-core'

// Scrubs share and calendar tokens out of anything Faro sends. It mirrors the
// server's redactPath (apps/api/internal/api/calendar.go): a segment shaped
// like a token is secret wherever it sits, and so is the segment right after
// "shared", "api/shares" or "api/calendar". Segments are matched
// case-insensitively and empty, "." and ".." segments are skipped, so
// non-canonical spellings cannot carry a token past the rule.
//
// Why here: Faro's default instrumentations put location.href into every
// payload's meta.page.url, and the tracing instrumentation records fetch
// URLs, so a share link would otherwise leave the browser in telemetry.

const TOKEN_RE = /^[A-Za-z0-9_-]{43}$/
const ICS = '.ics'
const REDACTED = 'REDACTED'
const URLISH_KEY = /url|target|route/i

function redactPath(p: string): string {
  const segs = p.split('/')
  const sig: string[] = []
  for (let i = 0; i < segs.length; i++) {
    const seg = segs[i]
    if (seg === '' || seg === '.' || seg === '..') continue
    let body = seg
    let suffix = ''
    if (seg.length >= ICS.length && seg.slice(-ICS.length).toLowerCase() === ICS) {
      body = seg.slice(0, -ICS.length)
      suffix = seg.slice(-ICS.length)
    }
    const last = sig[sig.length - 1]?.toLowerCase()
    const beforeLast = sig[sig.length - 2]?.toLowerCase()
    const follows =
      last === 'shared' ||
      (beforeLast === 'api' && (last === 'calendar' || last === 'shares'))
    if (follows || TOKEN_RE.test(body)) {
      segs[i] = REDACTED + suffix
    }
    sig.push(seg)
  }
  return segs.join('/')
}

function safeDecode(s: string): string {
  try {
    return decodeURIComponent(s)
  } catch {
    return s
  }
}

function redactQuery(query: string): string {
  if (query === '') return ''
  const parts = query.slice(1).split('&').map((part) => {
    const eq = part.indexOf('=')
    if (eq < 0) return part
    const value = part.slice(eq + 1)
    const decoded = safeDecode(value)
    const redacted = redactPath(decoded)
    // Only re-encode a value that changed, so untouched URLs keep their bytes.
    return part.slice(0, eq + 1) + (redacted === decoded ? value : encodeURIComponent(redacted))
  })
  return '?' + parts.join('&')
}

// redactUrl works on absolute and relative URLs alike, and on the bare span
// names the tracing instrumentation produces ("GET /api/shares/...").
export function redactUrl(url: string): string {
  const hashAt = url.indexOf('#')
  const hash = hashAt >= 0 ? url.slice(hashAt) : ''
  const beforeHash = hashAt >= 0 ? url.slice(0, hashAt) : url
  const queryAt = beforeHash.indexOf('?')
  const base = queryAt >= 0 ? beforeHash.slice(0, queryAt) : beforeHash
  const query = queryAt >= 0 ? beforeHash.slice(queryAt) : ''

  const m = /^([a-z][a-z0-9+.-]*:\/\/[^/]*)(.*)$/i.exec(base)
  const origin = m ? m[1] : ''
  const path = m ? m[2] : base

  return origin + redactPath(path) + redactQuery(query) + (hash ? '#' + redactPath(hash.slice(1)) : '')
}

type Attribute = { key: string; value: { stringValue?: string | null } }
type Span = { name?: string; attributes?: Attribute[] }
type TracePayload = {
  resourceSpans: { scopeSpans?: { spans?: Span[] }[] }[]
}

function scrubSpan(span: Span): Span {
  return {
    ...span,
    ...(span.name !== undefined ? { name: redactUrl(span.name) } : {}),
    ...(span.attributes
      ? {
          attributes: span.attributes.map((a) =>
            URLISH_KEY.test(a.key) && typeof a.value?.stringValue === 'string'
              ? { ...a, value: { ...a.value, stringValue: redactUrl(a.value.stringValue) } }
              : a,
          ),
        }
      : {}),
  }
}

function scrubPayload<P>(payload: P): P {
  const traces = payload as unknown as Partial<TracePayload>
  if (!traces || !Array.isArray(traces.resourceSpans)) return payload
  const scrubbed: TracePayload = {
    ...(traces as TracePayload),
    resourceSpans: traces.resourceSpans.map((rs) => ({
      ...rs,
      scopeSpans: rs.scopeSpans?.map((ss) => ({
        ...ss,
        ...(ss.spans ? { spans: ss.spans.map(scrubSpan) } : {}),
      })),
    })),
  }
  return scrubbed as unknown as P
}

// scrubItem is the beforeSend hook body: it returns a scrubbed copy of a
// Faro transport item and never drops one.
export function scrubItem<P>(item: TransportItem<P>): TransportItem<P> {
  const page = item.meta?.page
  const meta =
    page?.url !== undefined
      ? { ...item.meta, page: { ...page, url: redactUrl(page.url) } }
      : item.meta
  return { ...item, meta, payload: scrubPayload(item.payload) }
}
