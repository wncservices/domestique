// The Plan page's toolbar: one primary action and an overflow. Which of Replan
// and "Short on time?" leads depends on whether the week has gaps; the
// handlers stay on the page, this only decides order and labels.
import type { TrainingWeek } from '@/api/types'

export type PlanActionId = 'replan' | 'train-now' | 'life-event' | 'ftp-test' | 'export'

export interface PlanAction {
  id: PlanActionId
  label: string
  icon: string
}

export interface PlanActionsState {
  /** Replan and "Short on time?" both act on today, so only the current week offers them. */
  isCurrentWeek: boolean
  hasGaps: boolean
}

const ACTIONS: Record<PlanActionId, PlanAction> = {
  replan: { id: 'replan', label: 'Replan', icon: 'i-lucide-refresh-ccw' },
  'train-now': { id: 'train-now', label: 'Short on time?', icon: 'i-lucide-timer' },
  'life-event': { id: 'life-event', label: 'Life event', icon: 'i-lucide-calendar-off' },
  'ftp-test': { id: 'ftp-test', label: 'FTP test', icon: 'i-lucide-gauge' },
  export: { id: 'export', label: 'Export', icon: 'i-lucide-download' },
}

export function planActions(state: PlanActionsState): { primary: PlanAction; overflow: PlanAction[] } {
  const always = [ACTIONS['life-event'], ACTIONS['ftp-test'], ACTIONS.export]
  if (!state.isCurrentWeek) return { primary: ACTIONS['life-event'], overflow: [ACTIONS['ftp-test'], ACTIONS.export] }
  const primary = state.hasGaps ? ACTIONS.replan : ACTIONS['train-now']
  const other = state.hasGaps ? ACTIONS['train-now'] : ACTIONS.replan
  return { primary, overflow: [other, ...always] }
}

/** "The week has gaps" is the signal the page already uses to offer Fill: a
 *  week that has not ended, has a plan focus and no planned session at all,
 *  for a rider whose profile says which days they train. */
export function hasGaps(
  week: Pick<TrainingWeek, 'end' | 'today' | 'focus'> & { days: { planned: unknown[] }[] },
  canFill: boolean,
): boolean {
  return canFill && week.end >= week.today && !!week.focus && !week.days.some((d) => d.planned.length > 0)
}
