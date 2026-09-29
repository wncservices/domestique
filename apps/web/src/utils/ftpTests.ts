// Display copy for FTP tests. The protocol menu itself (durations, formulas,
// trainer mode) comes from GET /api/training/tests; these are the few places
// that only have a protocol id in hand — a workout chip, a sync toast — and
// need a name for it. Mirrors internal/fitnesstest's protocol ids.
import type { FtpTestProtocolId, FtpTestResult } from '@/api/types'
import { weekdayLong } from '@/utils/planDates'

const LABELS: Record<FtpTestProtocolId, string> = {
  ramp: 'ramp test',
  twenty_minute: '20-minute test',
  two_by_eight: '2 x 8-minute test',
}

/** "ramp test" — lowercase, for use inside a sentence. */
export function ftpTestLabel(id: FtpTestProtocolId): string {
  return LABELS[id] ?? 'FTP test'
}

/** What a smart-trainer rider should do with this protocol. Only the ramp is
 *  meant for ERG: an all-out effort ridden in ERG is capped at the target, so
 *  the test would measure the target and not the rider. */
export function ftpTestTrainerNote(id: FtpTestProtocolId): string {
  return id === 'ramp'
    ? 'On a smart trainer, use ERG mode: each step is a power the trainer holds for you.'
    : 'On a smart trainer, use resistance (level or slope) mode, not ERG: ERG would cap your effort at its target.'
}

export interface FtpTestToastCopy {
  title: string
  icon: string
  color: 'success' | 'neutral' | 'warning'
}

/** The toast for one test a sync just read. */
export function ftpTestToast(r: FtpTestResult): FtpTestToastCopy {
  const watts = Math.round(r.ftpWatts)
  switch (r.outcome) {
    case 'applied':
      return {
        title: `FTP updated to ${watts} W, from ${weekdayLong(r.date)}'s ${ftpTestLabel(r.protocol)}`,
        icon: 'i-lucide-gauge',
        color: 'success',
      }
    case 'suggested':
      return {
        title: `Your ${ftpTestLabel(r.protocol)} suggests ${watts} W, review it on the Fitness page`,
        icon: 'i-lucide-gauge',
        color: 'neutral',
      }
    case 'confirmed':
      return { title: `Your FTP test confirms ${watts} W`, icon: 'i-lucide-circle-check', color: 'success' }
    default:
      return { title: "Couldn't read a result from that ride", icon: 'i-lucide-triangle-alert', color: 'warning' }
  }
}
