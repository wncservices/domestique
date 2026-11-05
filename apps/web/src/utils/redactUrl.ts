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

function redactPath(p: string): string {
  const segs = p.split('/')
  const sig: string[] = []
  for (let i = 0; i < segs.length; i++) {
    const seg = segs[i]
    if (seg === '' || seg === '.' || seg === '..') continue
    // A URL has no whitespace, but free text can reach here (scrubItem walks
    // every string). Redact only the word, so a sentence after a share path
    // keeps its text.
    const cut = seg.search(/[\s,;"'<>()[\]{}]/)
    const word = cut >= 0 ? seg.slice(0, cut) : seg
    const rest = cut >= 0 ? seg.slice(cut) : ''
    let body = word
    let suffix = ''
    if (word.length >= ICS.length && word.slice(-ICS.length).toLowerCase() === ICS) {
      body = word.slice(0, -ICS.length)
      suffix = word.slice(-ICS.length)
    }
    const last = sig[sig.length - 1]?.toLowerCase()
    const beforeLast = sig[sig.length - 2]?.toLowerCase()
    const follows =
      last === 'shared' ||
      (beforeLast === 'api' && (last === 'calendar' || last === 'shares'))
    if (follows || TOKEN_RE.test(body)) {
      segs[i] = REDACTED + suffix + rest
    }
    sig.push(word)
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

// Free text has no URL to parse, so a share path or a bare token can sit
// anywhere inside a log message, an exception value or a measurement context.
// Separators may be "%2F" because a path can arrive encoded inside return_to.
const SEP = '(?:\\/|%2F)'
const SHARE_PATH_RE = new RegExp(
  `(${SEP}(?:shared|api${SEP}(?:shares|calendar))${SEP})((?:(?!\\/|%2F)[A-Za-z0-9_.~!$*+=:@-])+)`,
  'gi',
)
const RETURN_TO_RE = /(return_to=)([^\s"'<>&#]+)/gi
// A 43-character base64url run is a token wherever it sits.
const TOKEN_RUN_RE = /(?<![A-Za-z0-9_-])[A-Za-z0-9_-]{43}(?![A-Za-z0-9_-])/g

function redactSegment(seg: string): string {
  // Keep the calendar suffix and any sentence punctuation after the token.
  if (/\.ics$/i.test(seg)) return REDACTED + '.ics'
  const trail = /\.+$/.exec(seg)
  return REDACTED + (trail ? trail[0] : '')
}

// redactInText redacts share paths and token-shaped runs inside free text.
// redactUrl alone only sees a string that is a whole URL or path.
export function redactInText(s: string): string {
  return s
    .replace(SHARE_PATH_RE, (_m, prefix: string, seg: string) => prefix + redactSegment(seg))
    .replace(RETURN_TO_RE, (m, key: string, value: string) => {
      const decoded = safeDecode(value)
      const red = redactUrl(decoded)
      if (red === decoded) return m
      return key + (decoded !== value ? encodeURIComponent(red) : red)
    })
    .replace(TOKEN_RUN_RE, REDACTED)
}

// Strings in a Faro payload are URLs, paths, messages or plain values, so
// every one goes through both passes.
function scrubString(s: string): string {
  return redactInText(redactUrl(s))
}

// Faro payloads are shallow. A value nested deeper than this is replaced
// rather than copied, so a token cannot survive by sitting below the cap.
const MAX_DEPTH = 20

function scrubValue(v: unknown, depth: number, ancestors: WeakSet<object>): unknown {
  if (typeof v === 'string') return scrubString(v)
  if (v === null || typeof v !== 'object') return v
  // A cycle would never finish, and copying it would keep its strings.
  if (ancestors.has(v)) return '[circular]'
  if (depth >= MAX_DEPTH) return '[truncated]'
  ancestors.add(v)
  try {
    if (Array.isArray(v)) return v.map((x) => scrubValue(x, depth + 1, ancestors))
    const out: Record<string, unknown> = {}
    for (const [k, x] of Object.entries(v)) out[k] = scrubValue(x, depth + 1, ancestors)
    return out
  } finally {
    ancestors.delete(v)
  }
}

// scrubItem is the beforeSend hook body: it returns a scrubbed copy of a
// Faro transport item and never drops one. The walk covers meta and payload,
// so event attributes, exception frames, log messages and measurement context
// are scrubbed alongside meta.page.url and trace spans.
export function scrubItem<P>(item: TransportItem<P>): TransportItem<P> {
  const ancestors = new WeakSet<object>()
  return {
    ...item,
    meta: scrubValue(item.meta, 0, ancestors) as TransportItem<P>['meta'],
    payload: scrubValue(item.payload, 0, ancestors) as P,
  }
}
