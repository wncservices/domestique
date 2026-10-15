// Display helpers for the record book: how a best reads in a table cell, the
// toast for a new best, the chip on a ride, and the power curve's log axis.
// Pure (no Vue, no API calls) so node:test can pin the copy.
import type { BestEffortAward, BestEffortScope, EffortCell, SessionAward } from '@/api/types'

/** The durations rideanalysis's power curve reports, in seconds (5 s, 1, 5, 8,
 *  20 and 60 minutes). Mirrors internal/bestefforts. */
export const CURVE_SECONDS = [5, 60, 300, 480, 1200, 3600] as const

const MIN_SECONDS = CURVE_SECONDS[0]
const MAX_SECONDS = CURVE_SECONDS[CURVE_SECONDS.length - 1]!

/** Where a duration sits along the power curve's x axis, 0 to 1. The axis is
 *  logarithmic: a sprint and an hour effort cannot share a linear axis. */
export function curveX(seconds: number): number {
  const s = Math.min(Math.max(seconds, MIN_SECONDS), MAX_SECONDS)
  return (Math.log(s) - Math.log(MIN_SECONDS)) / (Math.log(MAX_SECONDS) - Math.log(MIN_SECONDS))
}

/** "1 min", "20 min", "5 s": a window's name on the curve's axis. */
export function windowLabel(seconds: number): string {
  if (seconds < 60) return `${seconds} s`
  if (seconds < 3600) return `${Math.round(seconds / 60)} min`
  return `${Math.round(seconds / 3600)} h`
}

/** 4:54 /km for a running speed in m/s; power and heart rate are whole numbers.
 *  Runners think in pace, not metres per second. */
export function formatEffortValue(metric: string, value: number, unit: string): string {
  if (metric.startsWith('speed_') || unit === 'm/s') {
    const total = Math.round(1000 / value)
    return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')} /km`
  }
  return `${Math.round(value)} ${unit}`
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

/** "14 Aug", or "14 Aug 2024" when the year is not the current one. Read from
 *  the date string, never through a timezone: a ride on 31 December is on
 *  31 December. */
export function formatEffortDate(ymd: string, now: Date = new Date()): string {
  const [y, m, d] = ymd.split('-').map(Number)
  if (!y || !m || !d) return ymd
  const day = `${d} ${MONTHS[m - 1] ?? ''}`
  return y === now.getFullYear() ? day : `${day} ${y}`
}

/** A table cell: "312 W, 14 Aug", or a dash when the rider has no value. */
export function formatEffortCell(metric: string, unit: string, cell: EffortCell | null, now: Date = new Date()): string {
  if (!cell) return '–'
  return `${formatEffortValue(metric, cell.value, unit)}, ${formatEffortDate(cell.date, now)}`
}

function scopeWord(scope: BestEffortScope): string {
  return scope === 'all_time' ? 'all-time' : 'season'
}

function capitalise(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}

/** "All-time 20-min power": the chip on a ride that set a record. */
export function awardChipText(a: Pick<SessionAward, 'label' | 'scope'> & { metric?: string }): string {
  return `${capitalise(scopeWord(a.scope))} ${a.label}`
}

export interface BestEffortToastCopy {
  title: string
  description: string | undefined
}

/** The toast for the new bests a sync (or a visit) turns up. One award reads on
 *  its own; several make one toast that names the top two, all-time first, and
 *  counts the rest. Null when there is nothing to say. */
export function bestEffortToast(awards: BestEffortAward[]): BestEffortToastCopy | null {
  if (awards.length === 0) return null
  const first = awards[0]!
  if (awards.length === 1) {
    return {
      title: `New ${scopeWord(first.scope)} best: ${first.label}, ${formatEffortValue(first.metric, first.value, first.unit)}`,
      description: undefined,
    }
  }
  // All-time outranks season; within a scope the server's order stands.
  const ranked = [...awards].sort((a, b) => Number(b.scope === 'all_time') - Number(a.scope === 'all_time'))
  const lines = ranked.slice(0, 2).map((a) => `${capitalise(scopeWord(a.scope))} ${a.label}, ${formatEffortValue(a.metric, a.value, a.unit)}`)
  // The first line keeps its capital; the rest of a sentence reads lower-case.
  const [one, two] = lines
  const text = [one, two!.charAt(0).toLowerCase() + two!.slice(1)]
  const rest = awards.length - 2
  return {
    title: `${awards.length} new bests`,
    description: rest > 0 ? `${text.join('; ')}; and ${rest} more` : text.join('; '),
  }
}
