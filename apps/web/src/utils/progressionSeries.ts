// Turns the progression history into what the Progression card draws: per
// zone, the level held at each moment of a window, plus the moves inside it.
// Pure, so the card's template only lays things out.
import type { ProgressionLevel, ProgressionPoint } from '@/api/types'

export type ProgressionRange = '1m' | '3m' | '1y'

export const RANGE_DAYS: Record<ProgressionRange, number> = { '1m': 30, '3m': 91, '1y': 365 }

export interface SeriesPoint {
  /** ms since epoch */
  t: number
  level: number
  reason?: string
  /** true for a move made inside the window; false for the carried-in start
   *  value and the "now" end point */
  move: boolean
}

export interface ZoneSeries {
  key: string
  sport: string
  zone: string
  current: number
  /** level held when the window opened; undefined if the zone had none yet */
  start?: number
  points: SeriesPoint[]
}

export interface LevelChange {
  key: string
  sport: string
  zone: string
  at: number
  from?: number
  to: number
  reason?: string
}

const keyOf = (sport: string, zone: string) => `${sport}:${zone}`

// Easiest to hardest, so the tiles read up the intensity ladder rather than
// alphabetically. An unknown zone goes last.
const ZONE_ORDER = ['tempo', 'sweet_spot', 'threshold', 'vo2max', 'anaerobic', 'intervals']
function zoneOrder(zone: string): number {
  const i = ZONE_ORDER.indexOf(zone)
  return i === -1 ? ZONE_ORDER.length : i
}

/** One series per current level. The line steps: a level holds until the
 *  next move, and runs on to now at the current value. */
export function buildSeries(levels: ProgressionLevel[], history: ProgressionPoint[], range: ProgressionRange, now: number): ZoneSeries[] {
  const from = now - RANGE_DAYS[range] * 86_400_000
  return [...levels].sort((a, b) => zoneOrder(a.zone) - zoneOrder(b.zone)).map((l) => {
    const key = keyOf(l.sport, l.zone)
    const own = history.filter((p) => keyOf(p.sport, p.zone) === key).map((p) => ({ ...p, t: Date.parse(p.at) }))
    const before = own.filter((p) => p.t < from).at(-1)
    const inside = own.filter((p) => p.t >= from && p.t <= now)
    const points: SeriesPoint[] = []
    if (before) points.push({ t: from, level: before.level, move: false })
    for (const p of inside) points.push({ t: p.t, level: p.level, reason: p.reason, move: true })
    // A level saved before history existed and never moved has no points:
    // draw it flat across the window rather than nothing.
    if (points.length === 0) points.push({ t: from, level: l.level, move: false })
    points.push({ t: now, level: l.level, move: false })
    return { key, sport: l.sport, zone: l.zone, current: l.level, start: points[0].level, points }
  })
}

/** Every move inside the window, newest first, with the value it left. */
export function buildChanges(history: ProgressionPoint[], range: ProgressionRange, now: number): LevelChange[] {
  const from = now - RANGE_DAYS[range] * 86_400_000
  const last = new Map<string, number>()
  const out: LevelChange[] = []
  for (const p of history) {
    const key = keyOf(p.sport, p.zone)
    const t = Date.parse(p.at)
    const prev = last.get(key)
    last.set(key, p.level)
    if (t < from || t > now || prev === p.level) continue
    out.push({ key, sport: p.sport, zone: p.zone, at: t, from: prev, to: p.level, reason: p.reason })
  }
  return out.reverse()
}

/** Signed one-decimal delta, with a real minus sign. */
export function formatDelta(d: number): string {
  const r = Math.round(d * 10) / 10
  if (r === 0) return '0.0'
  return r > 0 ? `+${r.toFixed(1)}` : `−${Math.abs(r).toFixed(1)}`
}
