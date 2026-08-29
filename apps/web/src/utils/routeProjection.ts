/**
 * Projects a route's lat/lon points onto a fixed-size viewbox for an inline
 * SVG preview — shared by TrackPreview.vue (a saved route's own card,
 * fetched by slug) and RouteCandidatePreview.vue (an unsaved route-builder
 * candidate, drawn straight from a points array already in hand). Pulled
 * out here after the two independently duplicated this exact math: a
 * future correction to one was at real risk of silently not reaching the
 * other.
 */

export const PREVIEW_WIDTH = 320
export const PREVIEW_HEIGHT = 160
const PADDING = 10

export interface RouteProjection {
  project(lat: number, lon: number): { x: number; y: number }
  /** The lat/lon visible at the *card's own edges*, not just the route's
   *  own tight bounding box — a route whose aspect ratio doesn't match the
   *  2:1 card gets letterboxed by the scale/offset below, and fetching only
   *  the route's own bbox would leave that letterboxed margin with no
   *  background at all (see TrackPreview.vue's own use of this). */
  bbox: { west: number; east: number; south: number; north: number }
}

/**
 * Longitude is scaled by cos(latitude) so a route does not look stretched
 * east-west — at Belgian latitudes a degree of longitude is only ~63% of a
 * degree of latitude.
 */
export function projectRoute(points: [number, number][]): RouteProjection | null {
  if (points.length < 2) return null

  const lats = points.map((p) => p[0])
  const midLat = (Math.min(...lats) + Math.max(...lats)) / 2
  const lonScale = Math.cos((midLat * Math.PI) / 180)

  const xs = points.map((p) => p[1] * lonScale)
  const minX = Math.min(...xs)
  const maxX = Math.max(...xs)
  const minY = Math.min(...lats)
  const maxY = Math.max(...lats)

  const spanX = maxX - minX || 1e-9
  const spanY = maxY - minY || 1e-9
  // One scale for both axes keeps the aspect ratio honest.
  const scale = Math.min(
    (PREVIEW_WIDTH - 2 * PADDING) / spanX,
    (PREVIEW_HEIGHT - 2 * PADDING) / spanY,
  )
  const offsetX = (PREVIEW_WIDTH - spanX * scale) / 2
  const offsetY = (PREVIEW_HEIGHT - spanY * scale) / 2

  return {
    project(lat: number, lon: number) {
      return {
        x: offsetX + (lon * lonScale - minX) * scale,
        // SVG y grows downward; latitude grows north, so flip it.
        y: PREVIEW_HEIGHT - offsetY - (lat - minY) * scale,
      }
    },
    bbox: {
      west: (-offsetX / scale + minX) / lonScale,
      east: ((PREVIEW_WIDTH - offsetX) / scale + minX) / lonScale,
      south: minY - offsetY / scale,
      north: minY + (PREVIEW_HEIGHT - offsetY) / scale,
    },
  }
}

/** An SVG path `d` string through points, via an already-built projection. */
export function routePath(points: [number, number][], proj: RouteProjection): string {
  return points
    .map(([lat, lon], index) => {
      const { x, y } = proj.project(lat, lon)
      return `${index === 0 ? 'M' : 'L'}${x.toFixed(1)} ${y.toFixed(1)}`
    })
    .join(' ')
}

/** The path's own starting point, for the small dot both preview
 *  components draw at a route's start — parsed back out of the `d` string
 *  rather than threaded through separately, since it's already exactly
 *  the first project() call routePath made. */
export function routeStart(path: string): { x: number; y: number } | null {
  const match = path.match(/^M([\d.]+) ([\d.]+)/)
  return match ? { x: Number(match[1]), y: Number(match[2]) } : null
}
