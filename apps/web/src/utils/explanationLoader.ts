// The load-and-staleness rules behind SessionExplanation.vue, kept free of Vue
// so tests/explanationLoader.test.mjs can drive them with deferred promises.
//
// A result is published only if it still belongs to the id the rider is
// looking at: any change of id (A -> B -> A included) starts a new ticket, and
// a reply from an older ticket is dropped. The same goes for a load still in
// flight when the host unmounts (cancel). A repeat show of the id already in
// flight or already loaded makes no second call.

export interface LoaderState<T> {
  value: T | null
  loading: boolean
  failed: boolean
}

export interface ExplanationLoader {
  /** Shows the explanation for `id`. An empty id clears the state. */
  show(id: string): void
  /** Drops any load still in flight. Called when the host unmounts. */
  cancel(): void
}

export interface RetryOptions<T> {
  /** True while the shown value is a stand-in and the real one is on its way. */
  retryIf: (value: T) => boolean
  delayMs: number
  maxRetries: number
}

// With `retry`, a result that is still a stand-in is shown at once and fetched
// again after delayMs, up to maxRetries times. A retry follows the same ticket
// rules as a first load, and it never flips the state back to loading or to
// failed: a failed retry just keeps what is already on screen.
export function createExplanationLoader<T>(
  fetchFn: (id: string) => Promise<T>,
  publish: (state: LoaderState<T>) => void,
  retry?: RetryOptions<T>,
): ExplanationLoader {
  let ticket = 0
  let shownId = ''
  let value: T | null = null
  let loading = false
  let failed = false

  let timer: ReturnType<typeof setTimeout> | undefined

  const push = () => publish({ value, loading, failed })
  const stopTimer = () => {
    if (timer !== undefined) clearTimeout(timer)
    timer = undefined
  }
  const scheduleRetry = (mine: number, id: string, left: number) => {
    if (!retry || left <= 0 || value === null || !retry.retryIf(value)) return
    timer = setTimeout(() => {
      fetchFn(id).then(
        (result) => {
          if (mine !== ticket) return
          value = result
          push()
          scheduleRetry(mine, id, left - 1)
        },
        () => {
          if (mine !== ticket) return
          scheduleRetry(mine, id, left - 1)
        },
      )
    }, retry.delayMs)
  }

  return {
    show(id: string) {
      if (!id) {
        stopTimer()
        ticket++
        shownId = ''
        value = null
        loading = false
        failed = false
        push()
        return
      }
      // Same id, already here or on its way: nothing to do.
      if (id === shownId && (value !== null || loading)) return

      stopTimer()
      const mine = ++ticket
      shownId = id
      value = null
      loading = true
      failed = false
      push()

      fetchFn(id).then(
        (result) => {
          if (mine !== ticket) return
          value = result
          loading = false
          push()
          scheduleRetry(mine, id, retry?.maxRetries ?? 0)
        },
        () => {
          if (mine !== ticket) return
          value = null
          loading = false
          failed = true
          push()
        },
      )
    },
    cancel() {
      stopTimer()
      ticket++
      loading = false
    },
  }
}
