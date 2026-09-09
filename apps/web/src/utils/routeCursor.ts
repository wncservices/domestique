/**
 * Converts between "distance along the route" and lat/lon position — the
 * shared math behind syncing the elevation profile's scrub position with
 * the interactive builder map's own hover, in both directions. Distinct
 * from routeProjection.ts (a static inline-SVG preview, screen-space only)
 * and geometry.ts's closestPointOnSegment (also screen-space, no notion of
 * distance-along-the-route) — this is the one place that knows how far
 * along a lat/lon polyline any given point sits.
 */

const EARTH_RADIUS_M = 6371000

function toRad(deg: number): number {
  return (deg * Math.PI) / 180
}

/** Great-circle distance between two [lat, lon] points, in metres. */
export function haversineM(a: [number, number], b: [number, number]): number {
  const dLat = toRad(b[0] - a[0])
  const dLon = toRad(b[1] - a[1])
  const lat1 = toRad(a[0])
  const lat2 = toRad(b[0])
  const h = Math.sin(dLat / 2) ** 2 + Math.cos(lat1) * Math.cos(lat2) * Math.sin(dLon / 2) ** 2
  return 2 * EARTH_RADIUS_M * Math.asin(Math.min(1, Math.sqrt(h)))
}

/** Cumulative distance in metres at each point, index-aligned with
 *  `points`, starting at 0 — the same "cumulative from the start" distance
 *  RouteBuilderElevationPoint.distanceM already uses server-side, computed
 *  here client-side against the builder's own live-snapped path. */
export function cumulativeDistancesM(points: [number, number][]): number[] {
  const out: number[] = points.length ? [0] : []
  for (let i = 1; i < points.length; i++) {
    out.push(out[i - 1] + haversineM(points[i - 1], points[i]))
  }
  return out
}

/** The lat/lon at a given distance along the route, linearly interpolated
 *  between the two straddling points. Clamped to the route's own extent —
 *  a distance from the elevation chart's own domain (0..totalDistanceKm)
 *  should never land outside it, but clamping keeps this safe even if the
 *  two sides' own point sets drift by a few metres of routing noise. */
export function positionAtDistanceM(
  points: [number, number][],
  distances: number[],
  targetM: number,
): [number, number] | null {
  if (points.length === 0) return null
  if (points.length === 1) return points[0]
  const total = distances[distances.length - 1]
  const clamped = Math.max(0, Math.min(total, targetM))
  // distances is non-decreasing — a linear scan is fine at route-builder
  // scale (typically well under a thousand points).
  let i = 0
  while (i < distances.length - 2 && distances[i + 1] < clamped) i++
  const span = distances[i + 1] - distances[i] || 1e-9
  const t = (clamped - distances[i]) / span
  const [lat1, lon1] = points[i]
  const [lat2, lon2] = points[i + 1]
  return [lat1 + (lat2 - lat1) * t, lon1 + (lon2 - lon1) * t]
}
