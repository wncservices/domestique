// What a failed request says to the rider. The server's own message wins
// whenever it is specific ("Garmin did not accept those details", "only this
// crew's owner or an admin may do that"): the handlers write those for the
// situation, and a flagged body (blocked, mfa, reconnect, code) carries advice
// the UI relies on. Fixed wording is only for an empty or generic message
// ("internal error", "forbidden", a bare status text), by status.
//
// ApiError and NetworkError are recognised structurally (a numeric `status`; a
// `network` flag) rather than imported, so this file stays free of the client's
// own imports and can be tested with node:test.

const FIXED: Record<number, string> = {
  401: 'Your session has ended. Sign in again.',
  403: 'You are not allowed to do that.',
  404: 'That could not be found. It may have been removed.',
  429: 'Too many requests. Wait a moment and try again.',
}

// What the Go handlers say when they have nothing specific to add, plus the
// standard status texts a proxy in front of them would send.
const GENERIC = new Set([
  'internal error',
  'internal server error',
  'unauthorized',
  'forbidden',
  'not found',
  'unavailable',
  'service unavailable',
  'bad gateway',
  'gateway timeout',
  'too many requests',
  'bad request',
  'conflict',
  'precondition failed',
  'not implemented',
])

const FLAGS = ['blocked', 'mfa', 'reconnect', 'code']

function isGeneric(message: string): boolean {
  const m = message.trim().toLowerCase()
  return m === '' || GENERIC.has(m) || /^request to \S+ failed$/.test(m)
}

export function describeError(err: unknown): string {
  if (!(err instanceof Error)) return String(err)
  const flags = err as Error & { status?: unknown; network?: unknown; body?: Record<string, unknown> }
  if (flags.network === true) return 'Could not reach the server. Check your connection and try again.'
  const status = flags.status
  if (typeof status !== 'number') return err.message

  const flagged = FLAGS.some((f) => flags.body?.[f] !== undefined && flags.body[f] !== false)
  if (flagged || !isGeneric(err.message)) return err.message || 'The request was refused.'
  if (FIXED[status]) return FIXED[status]
  if (status === 500) return 'Something went wrong on our side. Try again in a moment.'
  if (status >= 500) return 'A service this depends on is not answering. Try again shortly.'
  return 'The request was refused.'
}
