// A YYYY-MM-DD from the API (a week day, a goal's event date) needs parsing
// as a *local* calendar date, never a UTC instant — `new Date('2026-03-01')`
// parses as UTC midnight, which renders as the previous day for anyone west
// of UTC. Appending T00:00:00 forces the local-time constructor path instead.
// WeekStrip, TodayCard, PlanGoalHeader and TrainingPlanPage each used to wrap
// this themselves; consolidated here so there's one place to get it right.
export function localDate(ymd: string): Date {
  return new Date(`${ymd}T00:00:00`)
}

/** "5 Mar" — a week's start/end (WeekStrip's title) and a goal's event date
 *  (PlanGoalHeader's fallback-focus meta line). */
export function shortDate(ymd: string): string {
  return localDate(ymd).toLocaleDateString(undefined, { day: 'numeric', month: 'short' })
}

/** "Mon" — WeekStrip's day-tile header and move-menu labels. */
export function weekdayShort(ymd: string): string {
  return localDate(ymd).toLocaleDateString(undefined, { weekday: 'short' })
}

/** The day-of-month number — WeekStrip's day-tile header and move-menu labels. */
export function dayNumber(ymd: string): number {
  return localDate(ymd).getDate()
}

/** "Mon 5" — TodayCard's own move-menu labels, one call rather than
 *  weekdayShort + dayNumber since TodayCard never needs the number alone. */
export function weekdayAndDay(ymd: string): string {
  return localDate(ymd).toLocaleDateString(undefined, { weekday: 'short', day: 'numeric' })
}

/** "Mon, 5 Mar" — TodayCard's "Today · …" eyebrow. */
export function weekdayDateShort(ymd: string): string {
  return localDate(ymd).toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short' })
}

/** "Monday" — TrainingPlanPage's "Moved <workout> to <weekday>" toast. */
export function weekdayLong(ymd: string): string {
  return localDate(ymd).toLocaleDateString(undefined, { weekday: 'long' })
}
