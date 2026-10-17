// The words and numbers on the Today page, kept apart from the page so the
// choices (what to say, which suggestion wins, when a tile is "behind") are
// testable without a DOM. Everything here reads the API shapes already used
// by the Plan and Fitness pages; none of it invents a new one.
import type {
  FitnessSnapshot,
  ProjectionResponse,
  ReadinessResponse,
  RiderProfile,
  Workout,
} from '@/api/types'
import { formatSigned, formStatus, latestWithDelta } from './fitnessMath.ts'
import { flattenSteps } from './workoutMath.ts'

export function greeting(hour: number, name?: string): string {
  const part = hour < 12 ? 'morning' : hour < 18 ? 'afternoon' : 'evening'
  const first = (name ?? '').trim().split(/\s+/)[0]
  return first ? `Good ${part}, ${first}` : `Good ${part}`
}

export const REST_HEADLINE = 'Rest day. Recovery is part of the plan.'
export const REST_TIP = 'Sleep well, eat properly and keep moving gently if you feel like it.'

const ZONE_HEADLINES: Record<string, string> = {
  recovery: 'Zone 1, very easy. Spin the legs out.',
  endurance: 'Zone 2, steady and conversational.',
  tempo: 'Zone 3, comfortably hard. Steady, not a sprint.',
  sweet_spot: 'Sweet spot, hard but sustainable.',
  threshold: 'Threshold, close to your limit in controlled blocks.',
  vo2max: 'VO2max, short and hard with full recoveries.',
  anaerobic: 'Short, sharp efforts with long rests.',
  intervals: 'Short, sharp efforts with long rests.',
}

/** The one plain sentence under the session name. Readiness only speaks up
 *  when it disagrees with the plan. */
export function todayHeadline(
  workout: Pick<Workout, 'zone' | 'testProtocol'> | null,
  readiness: Pick<ReadinessResponse, 'today'> | null,
): string {
  if (!workout) return REST_HEADLINE
  const verdict = readiness?.today.verdict
  if (verdict === 'rest') return 'Readiness says rest. Keep it very easy or take the day off.'
  const base = workout.testProtocol
    ? 'FTP test. Ride it fresh and give it everything.'
    : (workout.zone && ZONE_HEADLINES[workout.zone]) || 'A structured session. Follow the steps on your device.'
  return verdict === 'caution' ? `${base} You are a bit run down, so ease off if it feels hard.` : base
}

// ---- Tiles ----

export type Tone = 'default' | 'good' | 'behind'

export interface Tile {
  label: string
  value: string
  caption?: string
  tone: Tone
}

/** Form is fitness minus fatigue (TSB), banded and worded exactly as the
 *  Fitness page does; only its two clearly good/bad bands get a tone. */
export function formTile(snapshots: FitnessSnapshot[]): Tile {
  const tsb = latestWithDelta(snapshots, 'tsb', 7)
  if (!tsb) return { label: 'Form', value: '—', caption: 'Appears after your first synced ride', tone: 'default' }
  const status = formStatus(tsb.value)
  const ctl = latestWithDelta(snapshots, 'ctl', 7)
  const parts = [status.label]
  if (ctl) {
    const delta = ctl.delta === undefined ? '' : `, ${Math.round(ctl.delta) >= 0 ? 'up' : 'down'} ${Math.abs(Math.round(ctl.delta))} this week`
    parts.push(`fitness ${Math.round(ctl.value)}${delta}`)
  }
  const tone: Tone = status.key === 'high-risk' ? 'behind' : status.key === 'fresh' || status.key === 'productive' ? 'good' : 'default'
  return { label: 'Form', value: formatSigned(tsb.value), caption: parts.join(' · '), tone }
}

export type CountdownTile = (Tile & { kind: 'countdown' }) | { kind: 'set-goal'; label: string; caption: string; href: string }

function daysBetween(from: string, to: string): number {
  const ms = (s: string) => Date.UTC(Number(s.slice(0, 4)), Number(s.slice(5, 7)) - 1, Number(s.slice(8, 10)))
  return Math.round((ms(to) - ms(from)) / 86_400_000)
}

/** Days to the focus goal, or the prompt that replaces the tile when there is
 *  none. The projection only colours the tile when it is about this goal. */
export function countdownTile(input: {
  goal: { id: string; name: string; eventDate?: string } | undefined
  projection: Pick<ProjectionResponse, 'available' | 'goal' | 'verdict'> | null
  today: string
}): CountdownTile {
  const { goal, projection, today } = input
  if (!goal) return { kind: 'set-goal', label: 'Set a goal', caption: 'Say what you are training for and the weeks build toward it.', href: '/plan' }
  if (!goal.eventDate) return { kind: 'countdown', label: goal.name, value: '—', caption: 'No event date yet', tone: 'default' }
  const days = Math.max(0, daysBetween(today, goal.eventDate))
  const own = projection?.available && projection.goal?.id === goal.id ? projection.verdict : null
  return {
    kind: 'countdown',
    label: goal.name,
    value: String(days),
    caption: own?.message,
    tone: own?.key === 'undertrained' ? 'behind' : 'default',
  }
}

// ---- Week ----

/** "2.6 of 3.4 h" */
export function weekHoursLabel(completedSeconds: number, plannedSeconds: number): string {
  if (plannedSeconds <= 0 && completedSeconds <= 0) return 'Nothing planned'
  const h = (s: number) => (s / 3600).toFixed(1)
  return `${h(completedSeconds)} of ${h(plannedSeconds)} h`
}

// ---- Session details (behind the Details toggle) ----

/** TSS and intensity factor, estimated from the steps against the rider's
 *  FTP. Null without an FTP: the steps then carry no honest watts, and a
 *  guessed load would read as fact. */
export function workoutLoad(workout: Pick<Workout, 'steps'>, profile: RiderProfile): { tss: number; if: number } | null {
  if (!profile.ftpWatts || profile.ftpWatts <= 0) return null
  const steps = flattenSteps(workout.steps, profile)
  const total = steps.reduce((sum, s) => sum + s.seconds, 0)
  if (total <= 0) return null
  const np = Math.pow(steps.reduce((sum, s) => sum + Math.pow(s.level, 4) * s.seconds, 0) / total, 0.25)
  return { tss: Math.round((total / 3600) * np * np * 100), if: Math.round(np * 100) / 100 }
}

/** The watts the working steps ask for; warm-up, cool-down and recoveries
 *  would only widen it. Null (not 0 W) when there is no FTP or no power step. */
export function powerRange(workout: Pick<Workout, 'steps'>, profile: RiderProfile): { low: number; high: number } | null {
  if (!profile.ftpWatts || profile.ftpWatts <= 0) return null
  const powered = flattenSteps(workout.steps, profile).filter(
    (s) => s.step.target === 'power' && s.step.targetLow !== undefined && s.step.targetHigh !== undefined,
  )
  const working = powered.filter((s) => !['warmup', 'cooldown', 'recovery', 'rest'].includes(s.intensity ?? ''))
  const use = working.length ? working : powered
  if (!use.length) return null
  return {
    low: Math.round(Math.min(...use.map((s) => s.step.targetLow!))),
    high: Math.round(Math.max(...use.map((s) => s.step.targetHigh!))),
  }
}

// ---- Suggestions ----

export type SuggestionKind = 'life-event' | 'weather' | 'ftp-test' | 'ride-together'

export interface Suggestion {
  kind: SuggestionKind
  icon: string
  title: string
  text: string
}

export interface SuggestionInputs {
  lifeEvent?: Suggestion
  weather?: Suggestion
  ftpTest?: Suggestion
  rideTogether?: Suggestion
}

/** At most one, in order of how much it changes today: being away, weather,
 *  an overdue FTP test, then a ride someone else proposed. */
export function pickSuggestion(inputs: SuggestionInputs): Suggestion | null {
  return inputs.lifeEvent ?? inputs.weather ?? inputs.ftpTest ?? inputs.rideTogether ?? null
}
