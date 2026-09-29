// Shared shapes for the goal and workout slide-over forms — split out of
// TrainingPlanPage.vue (Task 6) so GoalSlideover.vue and WorkoutSlideover.vue
// can both import them without either owning the other's type.
import type { GoalPriority, Sport, WorkoutStep } from '@/api/types'

// Shared by GoalSlideover.vue and WorkoutSlideover.vue's Sport selects — one
// list rather than two copies drifting apart.
export const sports: { value: Sport; label: string }[] = [
  { value: 'cycling', label: 'Cycling' },
  { value: 'running', label: 'Running' },
]

export interface GoalForm {
  name: string
  sport: Sport
  eventDate: string
  priority: GoalPriority
  targetDistanceKm: string
  targetElevationM: string
  notes: string
}

export function freshGoalForm(): GoalForm {
  return { name: '', sport: 'cycling', eventDate: '', priority: 'B', targetDistanceKm: '', targetElevationM: '', notes: '' }
}

export interface WorkoutForm {
  name: string
  sport: Sport
  date: string
  goalId: string
  description: string
  steps: WorkoutStep[]
  /** Carried through from the workout being edited, purely for
   *  WorkoutSlideover's header badge — never editable here and never sent
   *  back in an UpdateWorkoutRequest (the scheduler owns these). Absent on
   *  a fresh/create form. */
  zone?: string
  level?: number
  /** Likewise carried through only for the header badge; the convert and
   *  revert actions own it, not this form. */
  indoor?: boolean
}

// Reka UI's <SelectItem> forbids an empty-string value — it reserves '' to
// mean "no selection, show the placeholder" — so "no goal" needs its own
// sentinel rather than '', or the Goal select throws on mount and the whole
// modal locks up (Close/Cancel stop responding, though the save itself still
// goes through).
export const NO_GOAL = 'none'

export function freshWorkoutForm(): WorkoutForm {
  return { name: '', sport: 'cycling', date: '', goalId: NO_GOAL, description: '', steps: [] }
}
