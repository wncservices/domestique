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

export function createExplanationLoader<T>(
  fetchFn: (id: string) => Promise<T>,
  publish: (state: LoaderState<T>) => void,
): ExplanationLoader {
  let ticket = 0
  let shownId = ''
  let value: T | null = null
  let loading = false
  let failed = false

  const push = () => publish({ value, loading, failed })

  return {
    show(id: string) {
      if (!id) {
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
      ticket++
      loading = false
    },
  }
}
