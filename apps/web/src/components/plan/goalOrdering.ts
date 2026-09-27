// The same ordering internal/api/trainingweek.go's weekFocus uses to pick
// which goal a week's header talks about — priority A>B>C, dated goals
// before undated, nearest event first. TrainingPlanPage.vue reuses it as a
// page-side fallback for a week the server didn't compute a focus for (the
// browsed week falls outside every goal's periodized plan weeks), so the
// header still names *a* goal instead of showing a bare icon-chip. Kept in
// one place rather than re-derived in the page and the header both.
import type { Goal } from '@/api/types'

export function pickFallbackGoal(goals: Goal[]): Goal | undefined {
  if (goals.length === 0) return undefined
  return [...goals].sort((a, b) => {
    if (a.priority !== b.priority) return a.priority < b.priority ? -1 : 1 // 'A' < 'B' < 'C'
    const aDated = a.eventDate ? 0 : 1
    const bDated = b.eventDate ? 0 : 1
    if (aDated !== bDated) return aDated - bDated
    return (a.eventDate ?? '').localeCompare(b.eventDate ?? '')
  })[0]
}
