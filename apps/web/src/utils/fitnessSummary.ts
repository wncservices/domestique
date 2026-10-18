import type { FitnessResponse } from '@/api/types'
import { formatSigned, formStatus, latestWithDelta, weeklyHours } from './fitnessMath.ts'

export type Tone = 'default' | 'good' | 'behind'

/** Form in plain words. The bands are fitnessMath.formStatus's; only the two
 *  clearly good bands and the risk band carry a tone, so Today and Fitness
 *  cannot disagree about how a rider is doing. */
export function formWords(tsb: number): { label: string; tone: Tone } {
  const status = formStatus(tsb)
  const tone: Tone = status.key === 'high-risk' ? 'behind' : status.key === 'fresh' || status.key === 'productive' ? 'good' : 'default'
  return { label: status.label, tone }
}

export interface SummaryTile {
  label: string
  value: string
  caption?: string
  tone: Tone
}

export function weekDelta(delta: number | undefined): string | undefined {
  if (delta === undefined) return undefined
  const n = Math.round(delta)
  return `${n < 0 ? '−' : '+'}${Math.abs(n)} in 7 days`
}

/** Form, fitness, fatigue and hours this week, from the same data the status
 *  card has always used. Empty history gives dashes, not zeros. */
export function summaryTiles(fitness: FitnessResponse | null, now = new Date()): SummaryTile[] {
  const snaps = fitness?.snapshots ?? []
  const tsb = latestWithDelta(snaps, 'tsb', 7)
  const ctl = latestWithDelta(snaps, 'ctl', 7)
  const atl = latestWithDelta(snaps, 'atl', 7)
  const hours = weeklyHours(fitness?.sessions ?? [], now)
  const words = tsb ? formWords(tsb.value) : null
  return [
    { label: 'Form', value: tsb ? formatSigned(tsb.value) : '—', caption: words?.label, tone: words?.tone ?? 'default' },
    { label: 'Fitness', value: ctl ? String(Math.round(ctl.value)) : '—', caption: ctl ? weekDelta(ctl.delta) : undefined, tone: 'default' },
    { label: 'Fatigue', value: atl ? String(Math.round(atl.value)) : '—', caption: atl ? weekDelta(atl.delta) : undefined, tone: 'default' },
    { label: 'Hours this week', value: `${hours.thisWeek.toFixed(1)} h`, caption: `last week ${hours.lastWeek.toFixed(1)} h`, tone: 'default' },
  ]
}

export type StatusCardState = 'ready' | 'failed' | 'loading' | 'empty'

/** What the status card shows. History in hand wins, so a failed re-read after
 *  a sync does not hide numbers the rider can still use; with none, a failed
 *  read is a failure, never "no data yet", which would send a rider with
 *  history off to connect a device. */
export function statusCardState(input: { hasHistory: boolean; loading: boolean; failed: boolean }): StatusCardState {
  if (input.hasHistory) return 'ready'
  if (input.failed) return 'failed'
  return input.loading ? 'loading' : 'empty'
}
