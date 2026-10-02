// Pure math for the Fitness page redesign: form status, CTL/ATL/TSB deltas,
// weekly training hours, the chart's range filter, and the three training
// zone sets. No Vue, no API calls — Tasks 3-4 build UI on these exports.
import type {
  CompletedSession,
  FitnessSnapshot,
  ProjectionBand,
  ProjectionEvent,
  ProjectionResponse,
  ProjectionVerdict,
  RiderProfile,
} from '@/api/types'

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

// 'race' is the "To race" range: six weeks of history, then the projection to
// the event. It only makes sense with a projection, so the chart offers it
// only then.
export type ChartRange = '6w' | '3m' | '6m' | '1y' | 'race'

const RANGE_DAYS: Record<ChartRange, number> = { '6w': 42, '3m': 91, '6m': 182, '1y': 365, race: 42 }

// Inclusive of the boundary day: a snapshot exactly `days` old is kept, one
// day older is dropped.
export function filterByRange(snapshots: FitnessSnapshot[], range: ChartRange, today: Date): FitnessSnapshot[] {
  const cutoff = addDays(startOfLocalDay(today), -RANGE_DAYS[range])
  return snapshots
    .filter((s) => parseLocalDate(s.date) >= cutoff)
    .sort((a, b) => a.date.localeCompare(b.date))
}

// ---------- Race-day projection (display only) ----------
//
// The maths lives on the server (internal/projection); these helpers only
// shape its series for the chart and format its numbers.

export interface ChartPoint {
  date: string
  ctl: number
  atl: number
  tsb: number
  /** True for a day that has not happened yet: drawn dashed, read as "Projected". */
  projected: boolean
}

// A history range reaches back from today, so "the event falls within it"
// means the event is no further ahead than the range is long. "To race"
// always reaches the event.
export function eventWithinRange(eventDate: string, range: ChartRange, today: Date): boolean {
  if (range === 'race') return true
  const limit = addDays(startOfLocalDay(today), RANGE_DAYS[range])
  return parseLocalDate(eventDate) <= limit
}

// The chart's series: the range's history, then (when there is a projection
// and the event is inside the range) the projected days after the last
// snapshot. A projected day a snapshot already covers is dropped, so the solid
// line never doubles back over the dashed one.
export function extendSeries(
  snapshots: FitnessSnapshot[],
  projection: ProjectionResponse | null,
  range: ChartRange,
  today: Date,
): ChartPoint[] {
  const history: ChartPoint[] = filterByRange(snapshots, range, today).map((s) => ({
    date: s.date,
    ctl: s.ctl,
    atl: s.atl,
    tsb: s.tsb,
    projected: false,
  }))
  if (!projection || !projection.available || !projection.goal || projection.points.length === 0) return history
  if (!eventWithinRange(projection.goal.eventDate, range, today)) return history

  const lastHistory = history.length ? history[history.length - 1]!.date : ''
  const future: ChartPoint[] = projection.points
    .filter((p) => p.date > lastHistory)
    .map((p) => ({ date: p.date, ctl: p.ctl, atl: p.atl, tsb: p.tsb, projected: true }))
  return [...history, ...future]
}

// The target band clipped to the part of the TSB axis actually plotted; null
// when none of it is in view, so no empty strip is drawn.
export function clampBand(band: ProjectionBand, min: number, max: number): ProjectionBand | null {
  const low = Math.max(band.low, min)
  const high = Math.min(band.high, max)
  return high > low ? { low, high } : null
}

// Whole points with an explicit sign and a typographic minus, as the server
// words its verdicts ("form −6", "+12").
export function formatSigned(value: number): string {
  const rounded = Math.round(value)
  if (rounded > 0) return `+${rounded}`
  if (rounded < 0) return `−${Math.abs(rounded)}`
  return '0'
}

export function formatBand(band: ProjectionBand): string {
  return `${formatSigned(band.low)} to ${formatSigned(band.high)}`
}

// A verdict's Nuxt UI colour; neutral for "unavailable" and for B/C events,
// which have no verdict.
export function projectionTone(verdict: ProjectionVerdict | null | undefined): 'success' | 'warning' | 'info' | 'neutral' {
  return verdict?.tone ?? 'neutral'
}

// A season-timeline marker's colour. An A event is held to its band (edges
// inclusive, judged on whole points like the server's verdict); B and C are
// information only, so they never read as good or bad.
export function eventTone(event: ProjectionEvent): 'success' | 'warning' | 'info' {
  if (event.priority !== 'A') return 'info'
  const tsb = Math.round(event.tsb)
  if (tsb < event.band.low) return 'warning'
  if (tsb > event.band.high) return 'info'
  return 'success'
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

// Friel's %LTHR zones collapsed to the five bands this page shows (his Z5a-c
// together, >= 100 %), per sport. Each array is Z1's floor (0 — Friel gives
// none), then the four zone boundaries; Z5 is open-ended. Mirrored by
// lthrZoneEdges in apps/api/internal/rideanalysis/metrics.go, which counts a
// ride's stored time-in-zone against the same edges — a Go test reads these
// two constants, so keep the `NAME = [...]` shape.
export const LTHR_CYCLING_EDGES = [0, 0.81, 0.9, 0.94, 1.0]
export const LTHR_RUNNING_EDGES = [0, 0.85, 0.9, 0.95, 1.0]

type HrProfile = Pick<RiderProfile, 'thresholdHr' | 'maxHr' | 'ftpWatts' | 'thresholdPaceSecPerKm'>

// What the HR zones are a percentage of: threshold HR when the rider has one
// (the Friel table), max HR otherwise (today's table). Switches per rider.
export function hrZoneBasis(profile: HrProfile): { kind: 'threshold' | 'max'; bpm: number } | null {
  if (profile.thresholdHr) return { kind: 'threshold', bpm: profile.thresholdHr }
  if (profile.maxHr) return { kind: 'max', bpm: profile.maxHr }
  return null
}

// The profile carries no sport, so the running table is used only for a
// rider with a threshold pace and no FTP — anyone else gets the cycling one.
export function hrZonesSport(profile: HrProfile): 'cycling' | 'running' {
  return profile.thresholdPaceSecPerKm && !profile.ftpWatts ? 'running' : 'cycling'
}

export function hrZones(profile: HrProfile): Zone[] | null {
  const basis = hrZoneBasis(profile)
  if (!basis) return null
  if (basis.kind === 'max') return buildZones(basis.bpm, HR_ZONE_NAMES, HR_ZONE_EDGES, false)
  const edges = hrZonesSport(profile) === 'running' ? LTHR_RUNNING_EDGES : LTHR_CYCLING_EDGES
  return buildZones(basis.bpm, HR_ZONE_NAMES, edges, true)
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
export type ThresholdField = 'ftp' | 'max_hr' | 'threshold_pace' | 'threshold_hr'

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
    case 'threshold_hr':
      return 'threshold heart rate'
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
    case 'threshold_hr':
      return `${Math.round(value)} bpm`
    case 'threshold_pace':
      return formatPace(1000 / value)
  }
}

// The same value without its unit, for the "(was 255)" half of a banner
// title where the unit has already been said once.
export function formatThresholdNumber(field: ThresholdField, value: number): string {
  return formatThresholdValue(field, value).replace(/ (W|bpm|\/km)$/, '')
}
