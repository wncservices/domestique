// Display helpers for the Season tab. Pure, so node:test pins the copy.
import type { SeasonFtp, SeasonVolume } from '@/api/types'

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

// "4 May", or "4 May 2025" outside the current year; read from the string, no timezone.
function formatEffortDate(ymd: string, now: Date): string {
  const [y, m, d] = ymd.split('-').map(Number)
  if (!y || !m || !d) return ymd
  const day = `${d} ${MONTHS[m - 1] ?? ''}`
  return y === now.getFullYear() ? day : `${day} ${y}`
}

/** "142 h 30 min", "2 h", "25 min". */
export function hoursText(seconds: number): string {
  const minutes = Math.round(seconds / 60)
  const h = Math.floor(minutes / 60)
  const m = minutes % 60
  if (h === 0) return `${m} min`
  return m === 0 ? `${h} h` : `${h} h ${m} min`
}

/** Elevation is n/a when no ride reported any (0 means not reported, never
 *  flat) and says how many rides it covers when only some did. */
export function ascentText(v: Pick<SeasonVolume, 'rides' | 'ascentM' | 'ascentRides'>): string {
  if (v.ascentRides === 0) return 'n/a'
  const total = `${Math.round(v.ascentM).toLocaleString('en-GB')} m`
  return v.ascentRides < v.rides ? `${total} from ${v.ascentRides} of ${v.rides} rides` : total
}

/** The FTP at the start of the period, or where recording began when there is
 *  no earlier value. Never a made-up number. */
export function ftpStartText(ftp: Pick<SeasonFtp, 'startWatts' | 'historyStarts' | 'changes'>, now: Date = new Date()): string {
  if (ftp.startWatts !== undefined) return `${Math.round(ftp.startWatts)} W`
  if (ftp.historyStarts) return `FTP history starts ${formatEffortDate(ftp.historyStarts, now)}`
  return 'n/a'
}

export type PeriodChoice = { year: string } | { goal: string }

/** The picker's option value for a period. */
export function periodValue(p: PeriodChoice): string {
  return 'year' in p ? `year:${p.year}` : `goal:${p.goal}`
}

export function parsePeriodValue(value: string): PeriodChoice | undefined {
  if (value.startsWith('year:')) return { year: value.slice(5) }
  if (value.startsWith('goal:')) return { goal: value.slice(5) }
  return undefined
}
