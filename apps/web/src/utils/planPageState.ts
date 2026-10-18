export type PlanPageState = 'loading' | 'failed' | 'empty' | 'ready'

/** What the Plan page shows in place of its content. Goals already in hand win
 *  over everything, so a reload after a save never blanks the page; and a
 *  failed read is never reported as an empty plan, which would invite a rider
 *  with a plan to start a new one. */
export function planPageState(input: { goalCount: number; loading: boolean; settled: boolean; failed: boolean }): PlanPageState {
  if (input.goalCount > 0) return 'ready'
  if (input.loading || !input.settled) return 'loading'
  return input.failed ? 'failed' : 'empty'
}
