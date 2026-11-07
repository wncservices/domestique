// Shared shapes for the goal and workout slide-over forms — split out of
// PlanPage.vue (Task 6) so GoalSlideover.vue and WorkoutSlideover.vue
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
  /** The library route this goal is for, or NO_ROUTE. */
  routeSlug: string
  /** The rider's pacing intensity factor for the route; 0 means derived from
   *  how long the event takes. Edited only on demand. */
  pacingIf: number
}

// Reka UI's <SelectItem> forbids '' (see NO_GOAL below), so "no route" needs
// a sentinel too.
export const NO_ROUTE = 'none'

/** What priority a goal gets when the rider has not picked one: a dated event is
 *  a race to peak for (A), an undated goal only a direction to train in (B).
 *  The server applies the same default. */
export function defaultPriority(eventDate: string): GoalPriority {
  return eventDate ? 'A' : 'B'
}

/** The priority the select shows after the event date changes. A new goal
 *  follows its date. Editing an undated goal stored as B follows it too (a date
 *  makes it a race, A), as the server does on save; a goal that was already dated
 *  or has another priority keeps its own. A priority the rider picked in this edit
 *  is never overridden. */
export function priorityAfterDateChange(o: {
  editing: boolean
  wasUndated: boolean
  storedPriority: GoalPriority
  chosen: boolean
  current: GoalPriority
  eventDate: string
}): GoalPriority {
  if (o.chosen) return o.current
  if (!o.editing || (o.wasUndated && o.storedPriority === 'B')) return defaultPriority(o.eventDate)
  return o.current
}

export function freshGoalForm(): GoalForm {
  return {
    name: '', sport: 'cycling', eventDate: '', priority: 'B', targetDistanceKm: '', targetElevationM: '', notes: '',
    routeSlug: NO_ROUTE,
    pacingIf: 0,
  }
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
