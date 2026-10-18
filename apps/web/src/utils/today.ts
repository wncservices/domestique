// The words and numbers on the Today page, kept apart from the page so the
// choices (what to say, which suggestion wins, when a tile is "behind") are
// testable without a DOM. Everything here reads the API shapes already used
// by the Plan and Fitness pages; none of it invents a new one.
import type {
  FitnessSnapshot,
  FtpTestSuggestion,
  ProjectionResponse,
  ReadinessResponse,
  RideTogether,
  RiderProfile,
  WeatherSuggestion,
  WeekDay,
  WeekFocus,
  Workout,
} from '@/api/types'
import { phaseLabel } from '../components/plan/phaseStyle.ts'
import { formatSigned, formStatus, latestWithDelta } from './fitnessMath.ts'
import { formWords, weekDelta } from './fitnessSummary.ts'
import { weekdayLong, weekdayShort } from './planDates.ts'
import { flattenSteps, formatDuration, isPlanMadeSession } from './workoutMath.ts'
import { zoneColorVar } from './zones.ts'

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
export function formTile(snapshots: FitnessSnapshot[], failed = false): Tile {
  // A failed read is not an empty history: no prompt, just a dash.
  if (failed) return { label: 'Form', value: '—', tone: 'default' }
  const tsb = latestWithDelta(snapshots, 'tsb', 7)
  if (!tsb) return { label: 'Form', value: '—', caption: 'Appears after your first synced ride', tone: 'default' }
  const status = formStatus(tsb.value)
  const ctl = latestWithDelta(snapshots, 'ctl', 7)
  const parts = [status.label]
  if (ctl) {
    const change = weekDelta(ctl.delta)
    const delta = change ? `, ${change}` : ''
    parts.push(`fitness ${Math.round(ctl.value)}${delta}`)
  }
  return { label: 'Form', value: formatSigned(tsb.value), caption: parts.join(' · '), tone: formWords(tsb.value).tone }
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
    value: days === 1 ? '1 day' : `${days} days`,
    caption: own?.message,
    tone: own?.key === 'undertrained' ? 'behind' : 'default',
  }
}

// ---- Week ----

/** "2.6 / 3.4 h", the Plan page's arithmetic: the target is the larger of what
 *  is planned and the focus week's target hours. */
export function weekHoursLabel(completedSeconds: number, plannedSeconds: number, focusTargetHours = 0): string {
  const target = Math.max(plannedSeconds, focusTargetHours * 3600)
  if (target <= 0 && completedSeconds <= 0) return 'Nothing planned'
  return `${(completedSeconds / 3600).toFixed(1)} / ${(target / 3600).toFixed(1)} h`
}

/** The week's load line. With a TSS target (the "ctl" load basis) it reads
 *  "420 TSS · 2.6 / 7.5 h": the target in TSS, then the hours done against the
 *  hours target; without one it is `weekHoursLabel`, unchanged. */
export function weekLoadLabel(
  completedSeconds: number,
  plannedSeconds: number,
  focus?: { targetHours?: number; targetTss?: number },
): string {
  const tss = focus?.targetTss ?? 0
  if (tss <= 0) return weekHoursLabel(completedSeconds, plannedSeconds, focus?.targetHours)
  const hours = (Math.max(plannedSeconds, (focus?.targetHours ?? 0) * 3600) / 3600).toFixed(1)
  return `${Math.round(tss)} TSS \u00b7 ${(completedSeconds / 3600).toFixed(1)} / ${hours} h`
}

export interface WeekTile {
  date: string
  label: string
  isToday: boolean
  /** A zone colour variable; absent for rest days and rides with no plan. */
  color?: string
  done: boolean
  /** A planned day that went by unridden. */
  missed: boolean
  /** Visible under the colour, so a tile never relies on colour alone. */
  text: string
  /** The same, short enough for a seventh of a phone screen. */
  short: string
  aria: string
}

// "1h 6m" is five characters, too wide for a day tile at 375px; "1:06" fits.
function compactDuration(seconds: number): string {
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`
  const m = Math.round(seconds / 60)
  return `${Math.floor(m / 60)}:${String(m % 60).padStart(2, '0')}`
}

const sum = (xs: number[]) => xs.reduce((a, b) => a + b, 0)

/** One tile for every day of the week: planned sessions, rides already done
 *  (a done day may have nothing left planned) or rest. */
export function weekTiles(days: WeekDay[], today: string): WeekTile[] {
  return days.map((d) => {
    const plannedSeconds = sum(d.planned.map((w) => w.plannedSeconds))
    const completedSeconds = sum(d.completed.map((c) => c.durationSeconds))
    const done = d.status === 'done' || d.status === 'partial'
    const seconds = done || !d.planned.length ? completedSeconds : plannedSeconds
    const text = seconds > 0 ? formatDuration(seconds) : d.planned.length || d.completed.length ? 'Ride' : 'Rest'
    const missed = d.status === 'missed'
    return {
      date: d.date,
      label: weekdayShort(d.date),
      isToday: d.date === today,
      color: d.planned.length ? zoneColorVar(d.planned[0]!.zone ?? 'endurance') : undefined,
      done,
      missed,
      text,
      short: seconds > 0 ? compactDuration(seconds) : text,
      aria: `${weekdayLong(d.date)}: ${text}${done ? ', done' : missed ? ', missed' : ''}`,
    }
  })
}

export type CardState =
  | { kind: 'rest'; title: string; text: string }
  | { kind: 'ridden'; title: string; text: string }
  | { kind: 'planned' }

/** What the primary card is about: a rest day, a ride with nothing planned, or
 *  the planned session. */
export function cardState(day: WeekDay | undefined): CardState {
  if (day && !day.planned.length && !day.completed.length) return { kind: 'rest', title: REST_HEADLINE, text: REST_TIP }
  if (day && !day.planned.length) {
    const secs = sum(day.completed.map((c) => c.durationSeconds))
    return { kind: 'ridden', title: 'You rode today', text: `${formatDuration(secs)} logged. Nothing else is planned.` }
  }
  return { kind: 'planned' }
}

export function eyebrowFor(
  focus: Pick<WeekFocus, 'phase' | 'recovery' | 'weekNumber'> | undefined,
  restDay = false,
): string {
  const lead = restDay ? 'Today' : "Today's ride"
  if (!focus?.phase) return lead
  return `${lead} · ${phaseLabel(focus.phase, focus.recovery)}${focus.weekNumber ? ` week ${focus.weekNumber}` : ''}`
}

/** The session the Swap menu belongs to: plan-made, unridden, not past, with no
 *  route (a swap would change what the route was chosen for). */
export function swappableWorkout(day: WeekDay | undefined, todayDate: string): Workout | undefined {
  const w = day?.planned.find(isPlanMadeSession)
  if (!w || w.route || !day || day.completed.length > 0 || day.date < todayDate) return undefined
  return w
}

// ---- Session details (behind the Details toggle) ----

/** An estimate of TSS and intensity factor from the power steps against the
 *  rider's FTP (not a server figure). Null without an FTP or a power step: the steps then carry no honest watts, and a
 *  guessed load would read as fact. */
export function workoutLoad(workout: Pick<Workout, 'steps'>, profile: RiderProfile): { tss: number; if: number } | null {
  if (!profile.ftpWatts || profile.ftpWatts <= 0) return null
  // Only power steps carry watts; heart-rate or open steps would be guesses.
  const steps = flattenSteps(workout.steps, profile).filter((s) => s.step.target === 'power')
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

export interface SuggestionSources {
  lifeEvent?: { label: string; range: string }
  weather?: Pick<WeatherSuggestion, 'reasons'>
  ftpTest?: Pick<FtpTestSuggestion, 'message'>
  rideTogether?: Pick<RideTogether, 'crewName' | 'routeName' | 'day'>
}

/** The words and icon for each candidate; pickSuggestion then keeps one. */
export function buildSuggestionInputs(src: SuggestionSources): SuggestionInputs {
  const day = (d: string) => d.charAt(0).toUpperCase() + d.slice(1)
  return {
    lifeEvent: src.lifeEvent && {
      kind: 'life-event',
      icon: 'i-lucide-calendar-off',
      title: src.lifeEvent.label,
      text: `${src.lifeEvent.range}. Change or end it on the Plan page.`,
    },
    weather: src.weather && {
      kind: 'weather',
      icon: 'i-lucide-cloud-rain',
      title: "Weather may spoil today's ride.",
      text: src.weather.reasons.join(', ') + '.',
    },
    ftpTest: src.ftpTest && {
      kind: 'ftp-test',
      icon: 'i-lucide-gauge',
      title: 'An FTP test would help.',
      text: src.ftpTest.message,
    },
    rideTogether: src.rideTogether && {
      kind: 'ride-together',
      icon: 'i-lucide-users-round',
      title: `${src.rideTogether.crewName} proposes a ride together.`,
      text: `${src.rideTogether.routeName}, ${day(src.rideTogether.day)}.`,
    },
  }
}
