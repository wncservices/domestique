// What a failed request says to the rider. The server's own text is kept where
// it was written for the situation (400, 409, 412, 422: "That day is your crew
// ride."); for the statuses where it is either generic or an internal detail,
// the wording is fixed here so it reads the same everywhere.
//
// ApiError is recognised by its numeric `status` rather than imported, so this
// file stays free of the client's own imports and can be tested with node:test.

const FIXED: Record<number, string> = {
  401: 'Your session has ended. Sign in again.',
  403: 'You are not allowed to do that.',
  404: 'That could not be found. It may have been removed.',
  429: 'Too many requests. Wait a moment and try again.',
}

export function describeError(err: unknown): string {
  if (err instanceof Error) {
    const status = (err as Error & { status?: unknown }).status
    if (typeof status === 'number') {
      if (FIXED[status]) return FIXED[status]
      if (status >= 500) {
        return status === 500
          ? 'Something went wrong on our side. Try again in a moment.'
          : 'A service this depends on is not answering. Try again shortly.'
      }
      return err.message || 'The request was refused.'
    }
    // fetch() itself rejecting: no response at all.
    if (err instanceof TypeError) return 'Could not reach the server. Check your connection and try again.'
    return err.message
  }
  return String(err)
}
