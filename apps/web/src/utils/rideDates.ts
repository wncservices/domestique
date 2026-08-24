import type { Ride } from '@/api/types'

/** Today, as a plain YYYY-MM-DD string in the *browser's* local timezone —
 *  Date#toISOString gives the UTC date instead, which drifts a day off from
 *  "today" for part of the day at Belgian longitudes (this deployment's own
 *  timezone). A plain native <input type="date"> already speaks and returns
 *  exactly this form, so no date library is needed just to compute it — see
 *  the git history here for why that's deliberate: a UCalendar/
 *  @internationalized/date picker was tried first and reverted after it
 *  alone added ~31kB gzipped to a page's own bundle chunk.
 *
 *  Also what api.upcomingRides() sends as its own `from` cutoff — the server
 *  has no reliable notion of the rider's local day (it may not even run in
 *  the same timezone), so the browser computes "today" and the server just
 *  compares it as an opaque string against each ride's own YYYY-MM-DD date,
 *  the same way every other date comparison in this feature already works. */
export function todayISO(): string {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

// Plain string slicing on the already-known YYYY-MM-DD shape, not a date
// library — same reasoning todayISO's own comment gives, and
// Intl.DateTimeFormat would need a real Date first, which for a date-only
// string means either constructing one at UTC midnight (correct value,
// wrong-looking if formatted with a local timezone) or reintroducing
// exactly the parsing this file has already deliberately avoided once.
const MONTH_ABBR = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

export function rideMonth(date: string): string {
  return MONTH_ABBR[Number(date.slice(5, 7)) - 1] ?? ''
}

export function rideDay(date: string): string {
  return date.slice(8, 10)
}

/** "Aug 24" or, when a time of day was named, "Aug 24, 09:30" — shared by
 *  the Library page's upcoming-ride banner and the Crews page's own "Next
 *  ride" line so the two surfaces can't drift out of formatting sync. */
export function formatRideWhen(ride: Pick<Ride, 'date' | 'time'>): string {
  const when = `${rideMonth(ride.date)} ${rideDay(ride.date)}`
  return ride.time ? `${when}, ${ride.time}` : when
}
