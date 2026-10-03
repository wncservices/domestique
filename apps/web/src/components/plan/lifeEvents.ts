// What the Plan page needs to show a life event (travel, illness, busy) and
// its preview, in one place. The rules themselves live on the server
// (internal/lifeevents): this file only words them and keeps the checkbox
// bookkeeping of a preview.
import type { LifeChange, LifeChangeOp, LifeDiff, LifeEvent, LifeEventKind, LifeEventOption } from '@/api/types'
import { localDate, weekdayDateShort } from '@/utils/planDates'

interface KindMeta {
  label: string
  icon: string
  /** Nuxt UI semantic classes only, so light and dark follow the theme. */
  band: string
  /** The same tone as a CSS variable, for the season timeline's SVG. */
  fill: string
}

export const KIND_META: Record<LifeEventKind, KindMeta> = {
  travel: { label: 'Travel', icon: 'i-lucide-plane', band: 'bg-info/10 text-info', fill: 'var(--ui-info)' },
  illness: { label: 'Illness', icon: 'i-lucide-thermometer', band: 'bg-error/10 text-error', fill: 'var(--ui-error)' },
  busy: { label: 'Busy', icon: 'i-lucide-briefcase', band: 'bg-warning/10 text-warning', fill: 'var(--ui-warning)' },
  other: { label: 'Other', icon: 'i-lucide-calendar-off', band: 'bg-elevated text-muted', fill: 'var(--ui-border-accented)' },
}

export const KIND_ITEMS: { value: LifeEventKind; label: string; icon: string }[] = (
  ['travel', 'illness', 'busy', 'other'] as LifeEventKind[]
).map((value) => ({ value, label: KIND_META[value].label, icon: KIND_META[value].icon }))

export interface OptionItem {
  value: LifeEventOption
  label: string
  description: string
}

/** The options of a kind; none for busy and other. */
export function optionsFor(kind: LifeEventKind): OptionItem[] {
  if (kind === 'travel') {
    return [
      { value: 'no_bike', label: 'No bike', description: 'Nothing can be ridden. The week’s work is kept where a day exists.' },
      { value: 'gym', label: 'Hotel gym', description: 'Short indoor rides and running stay; the key session moves home.' },
    ]
  }
  if (kind === 'illness') {
    return [
      { value: 'proper', label: 'Proper', description: 'Fever, chest symptoms, body aches, or not sure. Everything in the range goes.' },
      { value: 'mild', label: 'Mild', description: 'Symptoms above the neck only (runny nose, sore throat). Hard sessions go, easy ones are cut.' },
    ]
  }
  return []
}

export function defaultOption(kind: LifeEventKind): LifeEventOption | '' {
  if (kind === 'travel') return 'no_bike'
  if (kind === 'illness') return 'proper'
  return ''
}

/** "Travelling · no bike", "Ill · symptoms above the neck", "Busy". */
export function eventLabel(e: LifeEvent): string {
  switch (e.kind) {
    case 'travel':
      return e.option === 'gym' ? 'Travelling · hotel gym' : 'Travelling · no bike'
    case 'illness':
      return e.option === 'mild' ? 'Ill · above the neck' : 'Ill'
    case 'busy':
      return 'Busy'
    default:
      return 'Away from training'
  }
}

export function rangeLabel(e: Pick<LifeEvent, 'startDate' | 'endDate'>): string {
  return e.startDate === e.endDate
    ? weekdayDateShort(e.startDate)
    : `${weekdayDateShort(e.startDate)} – ${weekdayDateShort(e.endDate)}`
}

/** The events that cover a date (inclusive). */
export function eventsOn(events: LifeEvent[] | undefined, date: string | undefined): LifeEvent[] {
  if (!events || !date) return []
  return events.filter((e) => e.startDate <= date && date <= e.endDate)
}

export const OP_META: Record<LifeChangeOp, { label: string; icon: string }> = {
  remove: { label: 'Remove', icon: 'i-lucide-trash-2' },
  move: { label: 'Move', icon: 'i-lucide-calendar-clock' },
  ease: { label: 'Ease', icon: 'i-lucide-feather' },
  shorten: { label: 'Shorten', icon: 'i-lucide-scissors' },
  indoor: { label: 'Indoors', icon: 'i-lucide-house' },
  add: { label: 'Add back', icon: 'i-lucide-plus' },
  swap: { label: 'Swap', icon: 'i-lucide-shuffle' },
}

/** Which changes the preview ticks to begin with: the server's own default
 *  (the opt-in removal of a session the rider built is not). */
export function defaultTicks(diff: LifeDiff): Record<string, boolean> {
  const out: Record<string, boolean> = {}
  for (const c of diff.changes) out[c.id] = c.default
  return out
}

/** What the apply call is told. A default-ticked change the rider unticked is
 *  skipped; an opt-in change they ticked is included. The server recomputes the
 *  diff and honours these by id, so a change that has gone is ignored. */
export function skipAndInclude(diff: LifeDiff, ticks: Record<string, boolean>): { skip: string[]; include: string[] } {
  const skip: string[] = []
  const include: string[] = []
  for (const c of diff.changes) {
    const on = ticks[c.id] ?? c.default
    if (c.default && !on) skip.push(c.id)
    if (!c.default && on) include.push(c.id)
  }
  return { skip, include }
}

/** Changes grouped by day, in date order, for the preview. */
export function groupByDay(changes: LifeChange[]): { date: string; changes: LifeChange[] }[] {
  const days = new Map<string, LifeChange[]>()
  for (const c of changes) {
    const list = days.get(c.date) ?? []
    list.push(c)
    days.set(c.date, list)
  }
  return [...days.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([date, list]) => ({ date, changes: list }))
}

export function countTicked(diff: LifeDiff, ticks: Record<string, boolean>): number {
  return diff.changes.filter((c) => ticks[c.id] ?? c.default).length
}

/** A YYYY-MM-DD shifted by whole days, built from local date parts like the rest
 *  of the plan's date handling (toISOString would shift the day west of UTC). */
export function shiftYMD(date: string, days: number): string {
  const d = localDate(date)
  d.setDate(d.getDate() + days)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}
