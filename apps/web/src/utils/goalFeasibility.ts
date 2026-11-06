import type { GoalCheck, GoalCheckRequest, GoalPriority, Sport } from '@/api/types'

/** The goal form's fields the check reads (the form holds distance in km). */
interface FeasibilityForm {
  sport: Sport
  eventDate: string
  priority: GoalPriority
  targetDistanceKm: string
  targetElevationM: string
}

function num(s: string): number {
  const n = Number(s)
  return Number.isFinite(n) && n > 0 ? n : 0
}

/** The body for POST /api/training/goals/check, or null when there is nothing
 *  to check: no event date (a rolling plan has no target) or a sport the server
 *  does not judge yet. */
export function feasibilityRequest(form: FeasibilityForm): GoalCheckRequest | null {
  if (!form.eventDate || form.sport !== 'cycling') return null
  return {
    eventDate: form.eventDate,
    sport: form.sport,
    targetDistanceM: Math.round(num(form.targetDistanceKm) * 1000),
    targetElevationM: num(form.targetElevationM),
    priority: form.priority,
  }
}

export function showFeasibility(r: GoalCheck | null): boolean {
  return r !== null && r.key !== 'none'
}
