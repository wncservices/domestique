// Display copy for FTP tests. The protocol menu itself (durations, formulas,
// trainer mode) comes from GET /api/training/tests; these are the few places
// that only have a protocol id in hand — a workout chip, a sync toast — and
// need a name for it. Mirrors internal/fitnesstest's protocol ids.
import type { FtpTestProtocol, FtpTestProtocolId, FtpTestResult } from '@/api/types'
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

/** The menu to fall back to when GET /api/training/tests failed, so the manual
 *  "FTP test" action still works. Mirrors internal/fitnesstest.Protocols. */
export const FALLBACK_FTP_PROTOCOLS: FtpTestProtocol[] = [
  {
    id: 'ramp',
    name: 'Ramp test',
    durationMinutes: 35,
    difficulty: 'moderate',
    trainerMode: 'erg',
    formula: '0.75 x best 1-minute power',
    forWhom: 'The default. Best for a first test or if you pace badly: it is short and there is nothing to pace.',
    prerequisites: 'A power meter or smart trainer, and a rough FTP to start from.',
  },
  {
    id: 'twenty_minute',
    name: '20-minute test',
    durationMinutes: 60,
    difficulty: 'hard',
    trainerMode: 'resistance',
    formula: '0.95 x best 20-minute power',
    forWhom: 'Experienced riders who can pace an all-out effort. Needs no FTP guess.',
    prerequisites: 'A power meter or smart trainer.',
  },
  {
    id: 'two_by_eight',
    name: '2 x 8-minute test',
    durationMinutes: 65,
    difficulty: 'hard',
    trainerMode: 'resistance',
    formula: '0.90 x the higher 8-minute power',
    forWhom: 'Riders who fade in a 20-minute effort. The second effort checks the first.',
    prerequisites: 'A power meter or smart trainer.',
  },
]

export interface FtpTestToastCopy {
  title: string
  icon: string
  color: 'success' | 'neutral' | 'warning'
}

/** The toast for one test a sync just read. */
export function ftpTestToast(r: FtpTestResult): FtpTestToastCopy {
  const watts = Math.round(r.ftpWatts)
  // A breakthrough in the ordinary rides outranked the test: say so, and name
  // the number that was actually applied or suggested, with the test's beside it.
  if (r.source === 'rides' && (r.outcome === 'applied' || r.outcome === 'suggested')) {
    const beaten = `higher than ${weekdayLong(r.date)}'s ${ftpTestLabel(r.protocol)} (${Math.round(r.testWatts ?? 0)} W)`
    return r.outcome === 'applied'
      ? { title: `FTP updated to ${watts} W from your recent rides, ${beaten}`, icon: 'i-lucide-gauge', color: 'success' }
      : { title: `Your recent rides suggest ${watts} W, ${beaten}. Review it on the Fitness page`, icon: 'i-lucide-gauge', color: 'neutral' }
  }
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
