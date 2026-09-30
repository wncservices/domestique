// How the hard efforts of a ride went, in words a rider reads at a glance.
// Replaces "0 of 1 efforts on target", which said neither which effort nor by
// how much: a main set 3% short and one 30% short both read as a flat "0".
//
// Each hard effort gets one of five grades, from its own numbers:
// - a lap-scored effort (average over the lap) by how far that average sits
//   outside the target range, past the 5% tolerance the server already
//   allows before it calls a step under or over (rideanalysis.classifyStep);
// - a time-scored effort (no laps: the whole main set judged by time in
//   range) by that share of time, where 70% is the server's own hit line.
import type { AnalysisStep } from '@/api/types'
import { formatPace } from '@/utils/fitnessMath'

export type EffortGrade = 'on' | 'justUnder' | 'justOver' | 'under' | 'over'

export interface EffortLine {
  name: string
  grade: EffortGrade
  /** "212 W vs 230–250 W, 8% under", or "55% of the time in 230–250 W". */
  detail: string
}

export interface EffortSummary {
  /** One of the five headline phrases below. */
  label: string
  tone: 'success' | 'warning' | 'error'
  icon: string
  efforts: EffortLine[]
}

/** How far past the range an average may be and still read as "just". */
const JUST_MARGIN = 0.1
/** Share of the time in range below which a time-scored effort is "under". */
const TIME_JUST = 50

function unitFor(target: string): string {
  if (target === 'power') return 'W'
  if (target === 'heart_rate') return 'bpm'
  if (target === 'cadence') return 'rpm'
  return ''
}

function fmt(target: string, v: number): string {
  if (target === 'pace') return formatPace(v)
  const unit = unitFor(target)
  return unit ? `${Math.round(v)} ${unit}` : `${Math.round(v)}`
}

function range(s: AnalysisStep): string {
  if (s.target === 'pace') return s.low === s.high ? formatPace(s.low) : `${formatPace(s.high)}–${formatPace(s.low)}`
  const unit = unitFor(s.target)
  const r = s.low === s.high ? `${Math.round(s.low)}` : `${Math.round(s.low)}–${Math.round(s.high)}`
  return unit ? `${r} ${unit}` : r
}

// A faster pace is a higher speed, so "under" a pace target is slower: the
// comparison on speed already reads the right way round.
function gradeByAverage(s: AnalysisStep): { grade: EffortGrade; pct: number } {
  if (s.result === 'hit') return { grade: 'on', pct: 0 }
  if (s.result === 'under') {
    const pct = s.low > 0 ? (s.low - s.actual) / s.low : 1
    return { grade: pct <= JUST_MARGIN ? 'justUnder' : 'under', pct }
  }
  const pct = s.high > 0 ? (s.actual - s.high) / s.high : 1
  return { grade: pct <= JUST_MARGIN ? 'justOver' : 'over', pct }
}

export function effortLine(s: AnalysisStep): EffortLine {
  if (s.inTargetPct !== undefined && s.inTargetPct > 0) {
    const pct = Math.round(s.inTargetPct)
    const grade: EffortGrade = s.result === 'hit' ? 'on' : pct >= TIME_JUST ? 'justUnder' : 'under'
    return { name: s.name, grade, detail: `${pct}% of the time in ${range(s)}` }
  }
  const { grade, pct } = gradeByAverage(s)
  const off = grade === 'on' ? 'on target' : `${Math.round(pct * 100)}% ${grade === 'justOver' || grade === 'over' ? 'over' : 'under'}`
  return { name: s.name, grade, detail: `${fmt(s.target, s.actual)} vs ${range(s)}, ${off}` }
}

/** undefined when the ride has no hard effort to judge (an endurance ride,
 *  an FTP test). */
export function summariseEfforts(steps: AnalysisStep[] | undefined): EffortSummary | undefined {
  const efforts = (steps ?? []).filter((s) => s.hard).map(effortLine)
  if (efforts.length === 0) return undefined

  const n = efforts.length
  const count = (g: EffortGrade[]) => efforts.filter((e) => g.includes(e.grade)).length
  const on = count(['on'])
  const near = count(['on', 'justUnder', 'justOver'])
  const low = count(['justUnder', 'under'])
  const high = count(['justOver', 'over'])

  // Five steps, best first. "Close" means every effort was on target or
  // within 10% of it; past that, the side most efforts missed on names it.
  if (on === n) return { label: 'On target', tone: 'success', icon: 'i-lucide-target', efforts }
  if (near === n) {
    return on * 2 >= n
      ? { label: 'Mostly on target', tone: 'success', icon: 'i-lucide-target', efforts }
      : { label: low >= high ? 'Just under target' : 'Just over target', tone: 'warning', icon: low >= high ? 'i-lucide-arrow-down-right' : 'i-lucide-arrow-up-right', efforts }
  }
  return low >= high
    ? { label: 'Below target', tone: 'error', icon: 'i-lucide-arrow-down', efforts }
    : { label: 'Above target', tone: 'warning', icon: 'i-lucide-arrow-up', efforts }
}

export const GRADE_LABEL: Record<EffortGrade, string> = {
  on: 'On target',
  justUnder: 'Just under',
  justOver: 'Just over',
  under: 'Under',
  over: 'Over',
}
