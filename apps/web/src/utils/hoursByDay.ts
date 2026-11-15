// Per-weekday training hours. The profile carries three views of one fact
// (hoursByDay, availableDays, hoursPerAvailableDay); every helper returns all
// three together so a form edit never leaves them disagreeing.
import type { RiderProfile } from '@/api/types'

export const WEEKDAYS: readonly string[] = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun']

type HoursMap = Record<string, number>
type Derived = Pick<RiderProfile, 'hoursByDay' | 'availableDays' | 'hoursPerAvailableDay'>

/** The stated map, or the old single figure spread over the old days. */
export function hoursMapOf(p: RiderProfile): HoursMap {
  const stated = p.hoursByDay ?? {}
  const keys = Object.keys(stated)
  if (keys.length > 0) {
    const out: HoursMap = {}
    for (const d of WEEKDAYS) {
      const h = stated[d]
      if (h !== undefined) out[d] = h
    }
    return out
  }
  const out: HoursMap = {}
  const each = p.hoursPerAvailableDay ?? 0
  if (each > 0) {
    for (const d of WEEKDAYS) if ((p.availableDays ?? []).includes(d)) out[d] = each
  }
  return out
}

function derive(map: HoursMap): Derived {
  const days = WEEKDAYS.filter((d) => map[d] !== undefined)
  const positive = days.filter((d) => map[d]! > 0)
  const total = positive.reduce((s, d) => s + map[d]!, 0)
  const ordered: HoursMap = {}
  for (const d of days) ordered[d] = map[d]!
  return {
    hoursByDay: ordered,
    availableDays: days,
    // Rounded to the cent of an hour: the server compares with a 0.01 h tolerance.
    hoursPerAvailableDay: positive.length ? Math.round((total / positive.length) * 100) / 100 : 0,
  }
}

// Sunday and Monday are one day apart.
function distance(a: string, b: string): number {
  const d = Math.abs(WEEKDAYS.indexOf(a) - WEEKDAYS.indexOf(b))
  return Math.min(d, 7 - d)
}

export function withDayToggled(p: RiderProfile, day: string): Partial<RiderProfile> {
  const map = hoursMapOf(p)
  if (map[day] !== undefined) {
    delete map[day]
    return derive(map)
  }
  let best: string | undefined
  for (const d of WEEKDAYS) {
    if (map[d] === undefined || !(map[d]! > 0)) continue
    // Strict less-than keeps the earlier day on a tie.
    if (best === undefined || distance(day, d) < distance(day, best)) best = d
  }
  map[day] = best === undefined ? 1 : map[best]!
  return derive(map)
}

/** An empty or 0 value keeps the row and sends 0, which the server drops;
 *  switching a day off is the day button's job. */
export function withDayHours(p: RiderProfile, day: string, hours: number | undefined): Partial<RiderProfile> {
  const map = hoursMapOf(p)
  map[day] = hours !== undefined && hours > 0 ? hours : 0
  return derive(map)
}

export function weekTotal(p: RiderProfile): number {
  const map = hoursMapOf(p)
  return Math.round(Object.values(map).reduce((s, h) => s + h, 0) * 100) / 100
}

export function uniformHours(days: string[], hours: number): HoursMap {
  const out: HoursMap = {}
  for (const d of WEEKDAYS) if (days.includes(d)) out[d] = hours
  return out
}
