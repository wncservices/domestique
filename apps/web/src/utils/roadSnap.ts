import type { Map as MapLibreMap, MapGeoJSONFeature, PointLike } from 'maplibre-gl'
import type { Geometry, Position } from 'geojson'
import { closestPointOnSegment, type ScreenPoint, type SegmentHit } from './geometry'

// The Protomaps/OpenMapTiles vector schema's own source-layer for anything
// a bike can ride — protomaps-themes-base's base_layers.ts draws every
// road kind (highway, major/minor road, path, "other") from this one
// layer, keyed by a "kind" property, alongside rail on the same layer.
// "rail" is the one kind excluded here: the route builder is cycling-only
// (AGENTS.md), and snapping a waypoint onto a train track would be
// actively wrong, not just imprecise.
const ROAD_SOURCE_LAYER = 'roads'
const EXCLUDED_KINDS = new Set(['rail'])

// Screen-pixel search radii to try, nearest first — a tight box first so a
// click right on a road snaps to that road rather than a busier one two
// streets over, widened only if nothing at all rendered nearby, so an
// isolated click still lands somewhere instead of refusing outright.
const SEARCH_RADII_PX = [24, 64, 160]

function isRoadFeature(feature: MapGeoJSONFeature): boolean {
  return feature.sourceLayer === ROAD_SOURCE_LAYER && !EXCLUDED_KINDS.has(String(feature.properties?.kind))
}

function* linesOf(geometry: Geometry): Generator<Position[]> {
  if (geometry.type === 'LineString') yield geometry.coordinates
  else if (geometry.type === 'MultiLineString') yield* geometry.coordinates
}

/**
 * Finds the nearest point on a rideable road/path to a screen-space pixel —
 * snapping a rider's click (or hover) onto the basemap's own road network
 * instead of wherever they happened to land, since a point the routing
 * engine cannot connect to a road is a point it cannot route through at
 * all. Purely client-side (`queryRenderedFeatures` reads whatever the
 * basemap has already rendered, no network round trip), which is what
 * makes it cheap enough to run on every mouse move, not just on click.
 *
 * Returns null if no road renders anywhere near screenPoint even after
 * widening the search — a genuinely empty area (open water, a tile that
 * hasn't loaded yet) is left for the caller to fall back to the raw
 * position rather than snapping to a road that might be kilometres away.
 */
export function nearestRoadPoint(
  map: MapLibreMap,
  screenPoint: ScreenPoint,
): { lng: number; lat: number } | null {
  for (const radius of SEARCH_RADII_PX) {
    const box: [PointLike, PointLike] = [
      [screenPoint.x - radius, screenPoint.y - radius],
      [screenPoint.x + radius, screenPoint.y + radius],
    ]
    const features = map.queryRenderedFeatures(box)

    let best: SegmentHit | null = null
    for (const feature of features) {
      if (!isRoadFeature(feature) || !feature.geometry) continue
      for (const line of linesOf(feature.geometry)) {
        for (let i = 0; i < line.length - 1; i++) {
          const a = map.project(line[i] as [number, number])
          const b = map.project(line[i + 1] as [number, number])
          const candidate = closestPointOnSegment(screenPoint, a, b)
          if (!best || candidate.distSq < best.distSq) best = candidate
        }
      }
    }

    if (best) {
      const { lng, lat } = map.unproject([best.point.x, best.point.y])
      return { lng, lat }
    }
  }
  return null
}
