// Ramer–Douglas–Peucker line simplification, over this app's own [lat, lon]
// point tuples. Plain Euclidean distance rather than a real geodesic one —
// close enough at the scale a single route spans, and simplification only
// has to agree with a rider's own eye about which points shape the route,
// not survive a precision review.

function perpendicularDistance(
  point: [number, number],
  a: [number, number],
  b: [number, number],
): number {
  const dx = b[0] - a[0]
  const dy = b[1] - a[1]
  if (dx === 0 && dy === 0) return Math.hypot(point[0] - a[0], point[1] - a[1])
  const t = ((point[0] - a[0]) * dx + (point[1] - a[1]) * dy) / (dx * dx + dy * dy)
  const cx = a[0] + t * dx
  const cy = a[1] + t * dy
  return Math.hypot(point[0] - cx, point[1] - cy)
}

function douglasPeucker(points: [number, number][], toleranceDeg: number): [number, number][] {
  if (points.length < 3) return points
  const first = points[0]
  const last = points[points.length - 1]
  let maxDist = 0
  let splitAt = 0
  for (let i = 1; i < points.length - 1; i++) {
    const dist = perpendicularDistance(points[i], first, last)
    if (dist > maxDist) {
      maxDist = dist
      splitAt = i
    }
  }
  if (maxDist <= toleranceDeg) return [first, last]
  const left = douglasPeucker(points.slice(0, splitAt + 1), toleranceDeg)
  const right = douglasPeucker(points.slice(splitAt), toleranceDeg)
  // left's own last point and right's own first point are both `points[splitAt]`
  // — drop one copy rather than duplicate the join.
  return [...left.slice(0, -1), ...right]
}

// ~15m — fine enough to keep a route's real turns rather than rounding them
// off, coarse enough that a generated loop's own routing noise (a snapped
// path rarely runs in a perfectly straight line between real turns) doesn't
// get treated as shape worth a waypoint of its own.
const INITIAL_TOLERANCE_DEG = 0.00015
// ~1.1km at the equator — a ceiling on how far doubling the tolerance is
// allowed to go, so a pathological shape can't spin the loop below forever
// chasing a point count it may never reach through simplification alone.
const MAX_TOLERANCE_DEG = 0.01

/**
 * Reduces a routing engine's full snapped path (every coordinate along the
 * road, potentially hundreds for one loop) to a small set of via-points a
 * rider can actually see and drag individually — the same shape a rider
 * would get by clicking the Draw tab's own handful of waypoints by hand,
 * not the raw output. Always returns at most maxPoints points, widening the
 * simplification tolerance until it fits and, in the pathological case that
 * still doesn't, falling back to an even stride subsample as a last resort.
 */
export function simplifyPath(points: [number, number][], maxPoints: number): [number, number][] {
  if (points.length <= maxPoints) return points

  let tolerance = INITIAL_TOLERANCE_DEG
  let simplified = douglasPeucker(points, tolerance)
  while (simplified.length > maxPoints && tolerance < MAX_TOLERANCE_DEG) {
    tolerance *= 2
    simplified = douglasPeucker(points, tolerance)
  }

  if (simplified.length > maxPoints) {
    const stride = Math.ceil(simplified.length / maxPoints)
    simplified = simplified.filter((_, i) => i % stride === 0 || i === simplified.length - 1)
  }

  return simplified
}
