// Small screen-space geometry helpers shared by roadSnap.ts (nearest point
// on a rendered road) and RouteBuilderMap.vue (nearest point on the drawn
// route itself, for inserting a waypoint mid-route) — both are "closest
// point on the nearest of several line segments to a pixel," just against
// different sources of segments.

export interface ScreenPoint {
  x: number
  y: number
}

export interface SegmentHit {
  point: ScreenPoint
  distSq: number
}

/**
 * The closest point to `p` lying on the segment from `a` to `b`, clamped to
 * the segment itself (never an extrapolation past either end) — the
 * standard point-to-segment projection, in screen pixels rather than
 * lng/lat so ordinary Euclidean distance is exactly the right measure with
 * no map-projection distortion to account for.
 */
export function closestPointOnSegment(p: ScreenPoint, a: ScreenPoint, b: ScreenPoint): SegmentHit {
  const dx = b.x - a.x
  const dy = b.y - a.y
  const lengthSq = dx * dx + dy * dy
  const t = lengthSq === 0 ? 0 : Math.max(0, Math.min(1, ((p.x - a.x) * dx + (p.y - a.y) * dy) / lengthSq))
  const point = { x: a.x + t * dx, y: a.y + t * dy }
  const ddx = p.x - point.x
  const ddy = p.y - point.y
  return { point, distSq: ddx * ddx + ddy * ddy }
}
