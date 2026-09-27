// Shared math for a structured workout's step list — the interval-profile
// chart (WorkoutProfile.vue), the step editor's %-of-threshold inputs
// (Task 6), and the plan/library workout cards (Task 4/5) all need the same
// duration formatting and target/threshold arithmetic, so it lives once
// here rather than being re-derived per component.
import type { CompletedSession, RiderProfile, StepIntensity, StepTarget, WorkoutStep } from '@/api/types'

/** One timed step, after repeat blocks are expanded — what the profile
 *  chart actually draws one rect per. `level` is 0..1.5, a relative-effort
 *  height for the chart, not a physical unit. */
export interface FlatStep {
  seconds: number
  intensity: StepIntensity | undefined
  level: number
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

// Falls back to intensity when there's no target/threshold to compute a
// real relative-effort level from — a scheduler-generated workout may have
// no rider profile yet (no FTP on file), and a hand-built one may use
// 'open' targets throughout, but the chart still needs a shape either way.
function levelFor(step: WorkoutStep, profile?: RiderProfile): number {
  if (profile && step.target !== 'open' && step.targetLow !== undefined && step.targetHigh !== undefined) {
    const threshold = thresholdFor(step.target, profile)
    if (threshold !== null && threshold > 0) {
      const midpoint = (step.targetLow + step.targetHigh) / 2
      return clamp(midpoint / threshold, 0.3, 1.5)
    }
  }
  switch (step.intensity) {
    case 'warmup':
    case 'cooldown':
      return 0.5
    case 'recovery':
    case 'rest':
      return 0.4
    case 'active':
      return 0.7
    case 'interval':
      return 1.0
    default:
      return 0.6
  }
}

/** Expands repeat blocks (`repeat >= 2` → its `steps` repeated that many
 *  times, recursively) and keeps only timed steps with a real duration —
 *  distance/open steps have no honest seconds figure to draw a rect for. */
export function flattenSteps(steps: WorkoutStep[], profile?: RiderProfile): FlatStep[] {
  const result: FlatStep[] = []
  for (const step of steps) {
    if ((step.repeat ?? 0) >= 2) {
      const children = flattenSteps(step.steps ?? [], profile)
      for (let i = 0; i < (step.repeat as number); i++) result.push(...children)
      continue
    }
    if (step.duration !== 'time' || !step.seconds || step.seconds <= 0) continue
    result.push({ seconds: step.seconds, intensity: step.intensity, level: levelFor(step, profile) })
  }
  return result
}

/** A short, human duration for a card/summary line: "45s", "15m", "1h 15m". */
export function formatDuration(seconds: number): string {
  if (seconds <= 0) return '—'
  // Round to whole seconds before branching, not after — 59.5 rounds to 60,
  // which belongs in the "under a minute" branch as "1m", not the seconds
  // branch as the nonsensical "60s".
  const rounded = Math.round(seconds)
  if (rounded < 60) return `${rounded}s`
  const totalMinutes = Math.round(rounded / 60)
  if (totalMinutes < 60) return `${totalMinutes}m`
  const hours = Math.floor(totalMinutes / 60)
  const minutes = totalMinutes % 60
  return minutes === 0 ? `${hours}h` : `${hours}h ${minutes}m`
}

/** A stopwatch-style clock for editing: "1:30", "1:02:05". */
export function formatClock(seconds: number): string {
  const total = Math.max(0, Math.round(seconds))
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const secs = total % 60
  const pad = (n: number) => String(n).padStart(2, '0')
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(secs)}` : `${minutes}:${pad(secs)}`
}

/** The inverse of formatClock, plus a bare-number shorthand: typing "90"
 *  in a duration field means 90 minutes (what a rider actually means by a
 *  round number there), while "1:30" means 1 minute 30 seconds. */
export function parseClock(text: string): number | null {
  const trimmed = text.trim()
  if (trimmed === '') return null

  if (/^-?\d+(\.\d+)?$/.test(trimmed)) {
    const minutes = Number(trimmed)
    if (!Number.isFinite(minutes) || minutes < 0) return null
    return Math.round(minutes * 60)
  }

  const parts = trimmed.split(':')
  if (parts.length < 2 || parts.length > 3 || !parts.every((p) => /^\d+$/.test(p))) return null
  const nums = parts.map(Number)
  const seconds = nums[nums.length - 1]
  if (seconds < 0 || seconds > 59) return null

  if (nums.length === 2) {
    const [minutes, secs] = nums
    return minutes * 60 + secs
  }
  const [hours, minutes, secs] = nums
  if (minutes < 0 || minutes > 59) return null
  return hours * 3600 + minutes * 60 + secs
}

/** The rider's own threshold for a target type, or null when it isn't on
 *  file (or is 0, which the profile treats as unset — see RiderProfile's
 *  own doc comment) — the caller falls back to an absolute input rather
 *  than dividing by zero or a missing value. Pace's threshold is a speed
 *  (m/s) so it is directly comparable to a pace step's own m/s target. */
export function thresholdFor(target: StepTarget, profile: RiderProfile): number | null {
  switch (target) {
    case 'power':
      return profile.ftpWatts ? profile.ftpWatts : null
    case 'heart_rate':
      return profile.maxHr ? profile.maxHr : null
    case 'pace':
      return profile.thresholdPaceSecPerKm ? 1000 / profile.thresholdPaceSecPerKm : null
    default:
      return null
  }
}

export function toPercent(value: number, threshold: number): number {
  return Math.round((value / threshold) * 100)
}

/** The inverse of toPercent. Pace keeps 2 decimal places (m/s at typical
 *  running/riding speeds needs sub-integer precision to round-trip through
 *  a % field without visibly drifting); every other target is whole units. */
export function fromPercent(percent: number, threshold: number, target: StepTarget): number {
  const value = (percent / 100) * threshold
  return target === 'pace' ? Math.round(value * 100) / 100 : Math.round(value)
}

function formatPaceMinPerKm(speedMps: number): string {
  const secPerKm = 1000 / speedMps
  const minutes = Math.floor(secPerKm / 60)
  const secs = Math.round(secPerKm % 60)
  return `${minutes}:${String(secs).padStart(2, '0')}`
}

/** A one-line target summary for a step row: "248–270 W · 92–100% FTP",
 *  "140–150 bpm", "4:10–4:30 /km", "" for an open (untargeted) step. The
 *  % suffix only appears when the rider has the matching threshold on
 *  file — cadence never gets one, since there's no such thing as %
 *  threshold cadence. */
export function describeTarget(step: WorkoutStep, profile: RiderProfile): string {
  const { target, targetLow, targetHigh } = step
  if (target === 'open' || targetLow === undefined || targetHigh === undefined) return ''

  const threshold = thresholdFor(target, profile)

  if (target === 'pace') {
    // A higher speed is a faster (smaller) pace number, so the low/high
    // speed pair displays in the opposite order to read low-to-high pace.
    const range =
      targetLow === targetHigh
        ? formatPaceMinPerKm(targetLow)
        : `${formatPaceMinPerKm(targetHigh)}–${formatPaceMinPerKm(targetLow)}`
    const base = `${range} /km`
    if (threshold === null) return base
    return `${base} · ${percentRange(targetLow, targetHigh, threshold)}% threshold`
  }

  const unit = target === 'power' ? 'W' : target === 'heart_rate' ? 'bpm' : 'rpm'
  const valueRange = targetLow === targetHigh ? `${targetLow}` : `${targetLow}–${targetHigh}`
  const base = `${valueRange} ${unit}`
  if (target === 'cadence' || threshold === null) return base

  const label = target === 'power' ? 'FTP' : 'max HR'
  return `${base} · ${percentRange(targetLow, targetHigh, threshold)}% ${label}`
}

function percentRange(low: number, high: number, threshold: number): string {
  const pLow = toPercent(low, threshold)
  const pHigh = toPercent(high, threshold)
  return pLow === pHigh ? `${pLow}` : `${pLow}–${pHigh}`
}

// A workout the plan changed on its own carries the reason in its description,
// after this marker (see internal/scheduler.AdjustedMarker) — shown so a rider
// is never left wondering why Thursday's session is not what it was on Monday.
const ADJUSTED_MARKER = 'Adjusted automatically:'

export function adjustmentNote(description?: string): string {
  const at = (description ?? '').indexOf(ADJUSTED_MARKER)
  return at < 0 ? '' : description!.slice(at + ADJUSTED_MARKER.length).trim()
}

/** Which of a day's completed sessions the outcome chip / step table speak
 *  for: the one whose analysis actually matched a planned workout (a real
 *  'nailed'/'completed'/'struggled'/'incomplete' verdict, not 'unplanned' —
 *  see rideanalysis.Analyze), falling back to the first analysed session so
 *  an unplanned-only day still gets a chip. Undefined when nothing on the
 *  day has been analysed yet (not synced from a FIT source, or older than
 *  the analysis window — see CompletedSession.analysis's own doc comment). */
export function pickAnalysedSession(completed: CompletedSession[]): CompletedSession | undefined {
  return completed.find((c) => c.analysis && c.analysis.outcome !== 'unplanned') ?? completed.find((c) => c.analysis)
}
