// A YYYY-MM-DD from the API (a week day, a goal's event date) needs parsing
// as a *local* calendar date, never a UTC instant — `new Date('2026-03-01')`
// parses as UTC midnight, which renders as the previous day for anyone west
// of UTC. Appending T00:00:00 forces the local-time constructor path instead.
// WeekStrip, TodayCard, PlanGoalHeader and PlanPage each used to wrap
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

/** "Monday" — PlanPage's "Moved <workout> to <weekday>" toast. */
export function weekdayLong(ymd: string): string {
  return localDate(ymd).toLocaleDateString(undefined, { weekday: 'long' })
}

// One date format family for the app: "Mon 26 Oct", ranges "Thu 29 Oct – Sun 1 Nov",
// and the year only when it is not the current one. Built from parts rather than
// a locale pattern so the order never flips with the browser's language.
function dateParts(d: Date, withYear: boolean): string {
  const weekday = d.toLocaleDateString('en-GB', { weekday: 'short' })
  const month = d.toLocaleDateString('en-GB', { month: 'short' })
  return `${weekday} ${d.getDate()} ${month}${withYear ? ` ${d.getFullYear()}` : ''}`
}

/** "Mon 26 Oct", or "Mon 26 Oct 2025" when the year is not this one. Takes a
 *  YYYY-MM-DD calendar date or a Date. */
export function formatDate(value: string | Date, now: Date = new Date()): string {
  const d = typeof value === 'string' ? localDate(value) : value
  return dateParts(d, d.getFullYear() !== now.getFullYear())
}

/** The same for an API timestamp (RFC 3339 instant), read in the browser's zone. */
export function formatInstant(iso: string, now: Date = new Date()): string {
  return formatDate(new Date(iso), now)
}

/** "Thu 29 Oct – Sun 1 Nov". The year appears on both ends when either is not
 *  this year, so a range never reads half-dated. */
export function formatDateRange(from: string, to: string, now: Date = new Date()): string {
  const a = localDate(from)
  const b = localDate(to)
  const withYear = a.getFullYear() !== now.getFullYear() || b.getFullYear() !== now.getFullYear()
  return `${dateParts(a, withYear)} – ${dateParts(b, withYear)}`
}
