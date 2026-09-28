// Pure math for the Fitness page redesign: form status, CTL/ATL/TSB deltas,
// weekly training hours, the chart's range filter, and the three training
// zone sets. No Vue, no API calls — Tasks 3-4 build UI on these exports.
import type { CompletedSession, FitnessSnapshot } from '@/api/types'

// ---------- Local date helpers ----------
// Snapshot/session dates are plain 'YYYY-MM-DD' strings with no timezone of
// their own; parsing them as UTC (or via `new Date(ymd)`) shifts the
// calendar day for anyone west of UTC. Always go through midnight-local.
export function parseLocalDate(ymd: string): Date {
  return new Date(`${ymd}T00:00:00`)
}

function toLocalYmd(date: Date): string {
  const y = date.getFullYear()
  const m = String(date.getMonth() + 1).padStart(2, '0')
  const d = String(date.getDate()).padStart(2, '0')
  return `${y}-${m}-${d}`
}

function startOfLocalDay(date: Date): Date {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate())
}

function addDays(date: Date, days: number): Date {
  const result = new Date(date)
  result.setDate(result.getDate() + days)
  return result
}

// ---------- Form status (intervals.icu TSB bands) ----------

export type FormStatusKey = 'detraining' | 'fresh' | 'maintaining' | 'productive' | 'high-risk'

export interface FormStatus {
  key: FormStatusKey
  label: string
  color: 'info' | 'success' | 'neutral' | 'error'
  explanation: string
}

// Bands and copy from the spec's status-card table (intervals.icu's widely
// used TSB bands). Boundaries belong to the lower band's upper edge: −10 is
// Productive, 5 and 25 are Fresh, −30 is Productive.
export function formStatus(tsb: number): FormStatus {
  if (tsb > 25) {
    return {
      key: 'detraining',
      label: 'Detraining',
      color: 'info',
      explanation: "You're very fresh — fitness is starting to slip. Fine before a race, not for long.",
    }
  }
  if (tsb >= 5) {
    return {
      key: 'fresh',
      label: 'Fresh',
      color: 'success',
      explanation: 'Rested and ready. A good time for a hard session or an event.',
    }
  }
  if (tsb > -10) {
    return {
      key: 'maintaining',
      label: 'Maintaining',
      color: 'neutral',
      explanation: 'Load and recovery are balanced — fitness is holding steady.',
    }
  }
  if (tsb >= -30) {
    return {
      key: 'productive',
      label: 'Productive',
      color: 'success',
      explanation: 'Fitness is building and fatigue is in a healthy range. Keep the plan as it is.',
    }
  }
  return {
    key: 'high-risk',
    label: 'High risk',
    color: 'error',
    explanation: 'Fatigue is well ahead of fitness. Ease off before it turns into illness or injury.',
  }
}

// ---------- Latest value + 7/whatever-day delta ----------

export interface Latest {
  value: number
  delta?: number
}

// `latest` is the snapshot with the max date. The comparison snapshot is
// found by exact calendar date (latest date minus `days`, local) — not by
// index or "closest available" — so a gap in the data omits the delta
// rather than comparing against the wrong day.
export function latestWithDelta(
  snapshots: FitnessSnapshot[],
  key: 'ctl' | 'atl' | 'tsb',
  days: number,
): Latest | null {
  if (snapshots.length === 0) return null

  const sorted = [...snapshots].sort((a, b) => a.date.localeCompare(b.date))
  const latest = sorted[sorted.length - 1]!
  const value = latest[key]

  const compareYmd = toLocalYmd(addDays(parseLocalDate(latest.date), -days))
  const compareSnapshot = snapshots.find((s) => s.date === compareYmd)
  if (!compareSnapshot) return { value }

  return { value, delta: value - compareSnapshot[key] }
}

// ---------- Weekly hours ----------

// Monday-based local weeks relative to `today`'s local date, regardless of
// what time of day `today` carries.
export function weeklyHours(sessions: CompletedSession[], today: Date): { thisWeek: number; lastWeek: number } {
  const todayStart = startOfLocalDay(today)
  const daysSinceMonday = (todayStart.getDay() + 6) % 7 // getDay(): 0 = Sunday
  const thisWeekStart = addDays(todayStart, -daysSinceMonday)
  const thisWeekEnd = addDays(thisWeekStart, 7) // exclusive
  const lastWeekStart = addDays(thisWeekStart, -7)
  const lastWeekEnd = thisWeekStart // exclusive

  let thisWeekSeconds = 0
  let lastWeekSeconds = 0
  for (const session of sessions) {
    const date = parseLocalDate(session.date)
    if (date >= thisWeekStart && date < thisWeekEnd) {
      thisWeekSeconds += session.durationSeconds
    } else if (date >= lastWeekStart && date < lastWeekEnd) {
      lastWeekSeconds += session.durationSeconds
    }
  }

  const toHours = (seconds: number) => Math.round((seconds / 3600) * 10) / 10
  return { thisWeek: toHours(thisWeekSeconds), lastWeek: toHours(lastWeekSeconds) }
}

// ---------- Chart range filter ----------

export type ChartRange = '6w' | '3m' | '6m' | '1y'

const RANGE_DAYS: Record<ChartRange, number> = { '6w': 42, '3m': 91, '6m': 182, '1y': 365 }

// Inclusive of the boundary day: a snapshot exactly `days` old is kept, one
// day older is dropped.
export function filterByRange(snapshots: FitnessSnapshot[], range: ChartRange, today: Date): FitnessSnapshot[] {
  const cutoff = addDays(startOfLocalDay(today), -RANGE_DAYS[range])
  return snapshots
    .filter((s) => parseLocalDate(s.date) >= cutoff)
    .sort((a, b) => a.date.localeCompare(b.date))
}

// ---------- Training zones ----------

export interface Zone {
  name: string
  low: number
  high: number | null // null = open-ended top zone
}

// `edges` are fractions of threshold, one per zone boundary from low to
// high, in ascending order. When `openTop` is true the last zone's high is
// null (unbounded above); otherwise the last edge is that zone's high.
// `round` lets power/HR (whole units) and pace (a m/s speed, 2 decimals)
// share this without either one rounding to the wrong precision.
function buildZones(threshold: number, names: string[], edges: number[], openTop: boolean, round: (n: number) => number = Math.round): Zone[] {
  const zones: Zone[] = []
  for (let i = 0; i < names.length; i++) {
    const low = round(threshold * edges[i]!)
    const isLast = i === names.length - 1
    const high = isLast && openTop ? null : round(threshold * edges[i + 1]!)
    zones.push({ name: names[i]!, low, high })
  }
  return zones
}

// Coggan 7-zone power model (% FTP). Z7 has no upper bound.
const POWER_ZONE_NAMES = [
  'Z1 Active recovery',
  'Z2 Endurance',
  'Z3 Tempo',
  'Z4 Threshold',
  'Z5 VO2max',
  'Z6 Anaerobic',
  'Z7 Neuromuscular',
]
const POWER_ZONE_EDGES = [0, 0.55, 0.75, 0.9, 1.05, 1.2, 1.5]

export function powerZones(ftpWatts?: number): Zone[] | null {
  if (!ftpWatts) return null
  return buildZones(ftpWatts, POWER_ZONE_NAMES, POWER_ZONE_EDGES, true)
}

// 5-zone % max HR model. Bounded top and bottom — there is no "below Z1" or
// "above Z5" band, unlike the power and pace models.
const HR_ZONE_NAMES = ['Z1 Recovery', 'Z2 Endurance', 'Z3 Tempo', 'Z4 Threshold', 'Z5 VO2max']
const HR_ZONE_EDGES = [0.5, 0.6, 0.7, 0.8, 0.9, 1.0]

export function hrZones(maxHr?: number): Zone[] | null {
  if (!maxHr) return null
  return buildZones(maxHr, HR_ZONE_NAMES, HR_ZONE_EDGES, false)
}

// 5-zone % threshold speed model. Threshold speed (m/s) is the inverse of
// the rider's threshold pace (sec/km). Z5 has no upper bound.
const PACE_ZONE_NAMES = ['Z1 Recovery', 'Z2 Endurance', 'Z3 Tempo', 'Z4 Threshold', 'Z5 Speed']
const PACE_ZONE_EDGES = [0, 0.78, 0.88, 0.95, 1.03]

export function paceZones(thresholdPaceSecPerKm?: number): Zone[] | null {
  if (!thresholdPaceSecPerKm) return null
  const thresholdSpeed = 1000 / thresholdPaceSecPerKm
  const round2 = (n: number) => Math.round(n * 100) / 100
  return buildZones(thresholdSpeed, PACE_ZONE_NAMES, PACE_ZONE_EDGES, true, round2)
}

export function formatPace(metersPerSecond: number): string {
  // Round the total seconds first, then split — rounding minutes and
  // seconds independently can produce e.g. "3:60" when the leftover
  // seconds round up to 60 instead of carrying into the minute.
  const totalSeconds = Math.round(1000 / metersPerSecond)
  const minutes = Math.floor(totalSeconds / 60)
  const secs = totalSeconds % 60
  return `${minutes}:${String(secs).padStart(2, '0')} /km`
}

// ---------- Threshold detection (Task 4: suggestion banner, sync toast,
// profile provenance) ----------
//
// One place for "which field is this" formatting, shared by
// ThresholdSuggestions.vue, ProfileForm.vue's provenance line and
// TrainingFitnessPage.vue's sync toast — see DetectedThreshold/
// ThresholdSuggestion in api/types.ts.
export type ThresholdField = 'ftp' | 'max_hr' | 'threshold_pace'

// Lowercase, mid-sentence form ("New max heart rate detected") — FTP is
// already the right case either way.
export function thresholdFieldLabel(field: ThresholdField): string {
  switch (field) {
    case 'ftp':
      return 'FTP'
    case 'max_hr':
      return 'max heart rate'
    case 'threshold_pace':
      return 'threshold pace'
  }
}

// Start-of-sentence form ("Max heart rate set to 191 bpm") — FTP has no
// lowercase form to capitalise from.
export function thresholdFieldTitle(field: ThresholdField): string {
  const label = thresholdFieldLabel(field)
  return label === 'FTP' ? label : label.charAt(0).toUpperCase() + label.slice(1)
}

// A threshold's own value, in the unit that field displays elsewhere —
// watts, bpm, or a pace string via formatPace (thresholdPaceSecPerKm is
// stored as sec/km; formatPace wants the equivalent speed in m/s).
export function formatThresholdValue(field: ThresholdField, value: number): string {
  switch (field) {
    case 'ftp':
      return `${Math.round(value)} W`
    case 'max_hr':
      return `${Math.round(value)} bpm`
    case 'threshold_pace':
      return formatPace(1000 / value)
  }
}
