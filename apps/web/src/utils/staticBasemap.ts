import { VectorTile } from '@mapbox/vector-tile'
import { PbfReader } from 'pbf'
import { PMTiles } from 'pmtiles'

/**
 * A static, non-interactive land/water/road wash for TrackPreview's
 * card-grid thumbnails — no maplibre-gl, no WebGL context, no Worker. A
 * Library page can show dozens of cards at once; a real interactive map per
 * card would mean dozens of concurrent WebGL contexts, which browsers cap
 * (~8-16), so older cards would start silently breaking as you scroll. This
 * instead decodes a handful of vector tiles directly and turns their
 * earth/landuse/water/roads geometry into plain [lat, lon] point lists that
 * the caller projects with whatever projection it's already using for the
 * route line itself — sharing that one projection is what keeps the
 * background and the route aligned, rather than mixing this module's own
 * Mercator math with TrackPreview's simpler equirectangular one.
 */

// One PMTiles instance (and its internal header/directory cache) shared by
// every TrackPreview on the page, not one per card — otherwise a Library
// page with dozens of cards would each independently re-fetch the same
// ~16KB header and root directory.
let shared: PMTiles | null = null
function client(): PMTiles {
  if (!shared) {
    shared = new PMTiles(`${window.location.origin}/tiles/basemap.pmtiles`)
  }
  return shared
}

export interface BBox {
  west: number
  south: number
  east: number
  north: number
}

/** A ring (closed polygon) or line (open), as [lat, lon] points. */
export type Points = [number, number][]

export interface RoadSegment {
  kind: keyof typeof ROAD_WIDTH
  points: Points
}

export interface BasemapLayers {
  earth: Points[]
  /** Only the "green" landuse kinds (parks, forest, farmland, ...) — see GREEN_LANDUSE_KINDS. */
  landuse: Points[]
  water: Points[]
  /** Rivers/canals, which this basemap represents as lines, not polygons. */
  waterLines: Points[]
  roads: RoadSegment[]
}

// The basemap's landuse layer carries plenty of urban kinds (residential,
// commercial, school, ...) that would just muddy a 320x160 thumbnail with
// near-earth-colored fills. Only the kinds that actually read as "green
// space" at a glance are drawn — everything else is left as bare earth.
const GREEN_LANDUSE_KINDS = new Set([
  'forest',
  'wood',
  'park',
  'grass',
  'grassland',
  'meadow',
  'garden',
  'allotments',
  'national_park',
  'nature_reserve',
  'scrub',
  'wetland',
  'recreation_ground',
  'playground',
  'pitch',
  'zoo',
  'dog_park',
  'cemetery',
])

// Only the road kinds that still read as meaningful context at thumbnail
// size — minor_road and below (path, track, ...) are dense enough to just
// turn into visual noise. Value is the stroke width in SVG user units.
const ROAD_WIDTH = {
  highway: 1.4,
  major_road: 0.9,
  minor_road: 0.5,
}

function lonToTileX(lon: number, z: number): number {
  return Math.floor(((lon + 180) / 360) * 2 ** z)
}

function latToTileY(lat: number, z: number): number {
  const latRad = (lat * Math.PI) / 180
  return Math.floor(
    ((1 - Math.log(Math.tan(latRad) + 1 / Math.cos(latRad)) / Math.PI) / 2) * 2 ** z,
  )
}

function tilesForBbox(bbox: BBox, z: number): { x: number; y: number }[] {
  const minX = lonToTileX(bbox.west, z)
  const maxX = lonToTileX(bbox.east, z)
  // North is a *higher* latitude, which is a *lower* tile Y — tile rows
  // count down from the top of the world, the opposite of latitude.
  const minY = latToTileY(bbox.north, z)
  const maxY = latToTileY(bbox.south, z)

  const tiles: { x: number; y: number }[] = []
  for (let x = minX; x <= maxX; x++) {
    for (let y = minY; y <= maxY; y++) {
      tiles.push({ x, y })
    }
  }
  return tiles
}

// Picks a zoom that fits the route's own bbox in roughly 2-3 tiles across —
// enough resolution to actually show real roads/landuse near the route
// (the fixed zoom 5 this replaced covered ~1200km per tile, so a 30km loop
// rendered as an undifferentiated blob), while capping how many tiles a
// long point-to-point route pulls in. MAX_TILES is the hard backstop for
// routes whose bbox is wide in one axis but not the other (e.g. a straight
// 100km ride), where the zoom-from-span estimate alone could still pick a
// zoom that yields a long thin strip of many tiles.
const MIN_ZOOM = 8
const MAX_ZOOM = 13
const MAX_TILES = 9

function chooseZoomAndTiles(bbox: BBox): { zoom: number; tiles: { x: number; y: number }[] } {
  const span = Math.max(bbox.east - bbox.west, bbox.north - bbox.south, 1e-6)
  let zoom = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, Math.round(Math.log2(900 / span))))
  let tiles = tilesForBbox(bbox, zoom)
  while (tiles.length > MAX_TILES && zoom > MIN_ZOOM) {
    zoom -= 1
    tiles = tilesForBbox(bbox, zoom)
  }
  return { zoom, tiles }
}

/**
 * The inverse of the standard slippy-map (Web Mercator) tile projection: a
 * tile-local point — MVT's own coordinate space, [0, extent), y already
 * growing downward like screen/SVG space — back to real [lat, lon].
 */
function tileLocalToLatLon(
  tileX: number,
  tileY: number,
  z: number,
  extent: number,
  px: number,
  py: number,
): [number, number] {
  const scale = 2 ** z
  const worldX = (tileX + px / extent) / scale
  const worldY = (tileY + py / extent) / scale
  const lon = worldX * 360 - 180
  const latRad = Math.atan(Math.sinh(Math.PI * (1 - 2 * worldY)))
  return [(latRad * 180) / Math.PI, lon]
}

export { ROAD_WIDTH }

/**
 * Fetches whichever tiles cover `bbox` at a zoom chosen to fit it, and
 * returns earth/landuse/water/roads geometry as plain [lat, lon] points.
 * Best-effort: a missing tile (outside the basemap's own coverage) is just
 * skipped, not an error — a route near the edge of what an admin
 * configured still gets whatever land/water does exist for it.
 */
export async function fetchBasemapLayers(bbox: BBox): Promise<BasemapLayers> {
  const earth: Points[] = []
  const landuse: Points[] = []
  const water: Points[] = []
  const waterLines: Points[] = []
  const roads: RoadSegment[] = []
  const pm = client()
  const { zoom, tiles } = chooseZoomAndTiles(bbox)

  await Promise.all(
    tiles.map(async ({ x, y }) => {
      const resp = await pm.getZxy(zoom, x, y)
      if (!resp) return
      const tile = new VectorTile(new PbfReader(resp.data))
      const project = (px: number, py: number, extent: number) =>
        tileLocalToLatLon(x, y, zoom, extent, px, py)

      const earthLayer = tile.layers.earth
      for (let i = 0; earthLayer && i < earthLayer.length; i++) {
        const feature = earthLayer.feature(i)
        if (feature.type !== 3) continue // Polygon only.
        for (const ring of feature.loadGeometry()) {
          earth.push(ring.map((pt) => project(pt.x, pt.y, earthLayer.extent)))
        }
      }

      const landuseLayer = tile.layers.landuse
      for (let i = 0; landuseLayer && i < landuseLayer.length; i++) {
        const feature = landuseLayer.feature(i)
        if (feature.type !== 3) continue
        if (!GREEN_LANDUSE_KINDS.has(String(feature.properties.kind))) continue
        for (const ring of feature.loadGeometry()) {
          landuse.push(ring.map((pt) => project(pt.x, pt.y, landuseLayer.extent)))
        }
      }

      const waterLayer = tile.layers.water
      for (let i = 0; waterLayer && i < waterLayer.length; i++) {
        const feature = waterLayer.feature(i)
        // Lakes/sea are polygons (type 3); rivers/canals are lines (type 2)
        // in this basemap — both read as "water" but need different fills.
        const target = feature.type === 3 ? water : feature.type === 2 ? waterLines : null
        if (!target) continue
        for (const line of feature.loadGeometry()) {
          target.push(line.map((pt) => project(pt.x, pt.y, waterLayer.extent)))
        }
      }

      const roadsLayer = tile.layers.roads
      for (let i = 0; roadsLayer && i < roadsLayer.length; i++) {
        const feature = roadsLayer.feature(i)
        if (feature.type !== 2) continue // LineString only.
        const kind = String(feature.properties.kind)
        if (!(kind in ROAD_WIDTH)) continue
        for (const line of feature.loadGeometry()) {
          roads.push({
            kind: kind as RoadSegment['kind'],
            points: line.map((pt) => project(pt.x, pt.y, roadsLayer.extent)),
          })
        }
      }
    }),
  )

  return { earth, landuse, water, waterLines, roads }
}
