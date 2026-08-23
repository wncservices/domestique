import { VectorTile } from '@mapbox/vector-tile'
import { PbfReader } from 'pbf'
import { PMTiles } from 'pmtiles'

/**
 * A static, non-interactive land/water wash for TrackPreview's card-grid
 * thumbnails — no maplibre-gl, no WebGL context, no Worker. A Library page
 * can show dozens of cards at once; a real interactive map per card would
 * mean dozens of concurrent WebGL contexts, which browsers cap (~8-16), so
 * older cards would start silently breaking as you scroll. This instead
 * decodes a couple of vector tiles directly and turns their earth/water
 * polygons into plain [lat, lon] rings that the caller projects with
 * whatever projection it's already using for the route line itself —
 * sharing that one projection is what keeps the background and the route
 * aligned, rather than mixing this module's own Mercator math with
 * TrackPreview's simpler equirectangular one.
 */

// Deliberately low and fixed, not "tightest zoom that fits": land/water
// shapes don't need more detail than this for a 320x160 thumbnail, and it
// keeps almost every route within a single tile fetch — each tile at this
// zoom covers roughly 1200km, far more than any point-to-point route
// spans. A higher, per-route zoom would just mean heavier, more detailed
// coastline geometry for no visible benefit at this size.
const ZOOM = 5

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

export interface BasemapLayers {
  /** Each entry is one polygon ring (outer or hole) as [lat, lon] points. */
  earth: [number, number][][]
  water: [number, number][][]
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

/**
 * Fetches whichever tiles cover `bbox` at the fixed preview zoom and
 * returns their earth/water polygon rings as plain [lat, lon] points.
 * Best-effort: a missing tile (outside the basemap's own coverage) is
 * just skipped, not an error — a route near the edge of what an admin
 * configured still gets whatever land/water does exist for it.
 */
export async function fetchBasemapLayers(bbox: BBox): Promise<BasemapLayers> {
  const earth: [number, number][][] = []
  const water: [number, number][][] = []
  const pm = client()

  await Promise.all(
    tilesForBbox(bbox, ZOOM).map(async ({ x, y }) => {
      const resp = await pm.getZxy(ZOOM, x, y)
      if (!resp) return
      const tile = new VectorTile(new PbfReader(resp.data))

      for (const [name, target] of [
        ['earth', earth],
        ['water', water],
      ] as const) {
        const layer = tile.layers[name]
        if (!layer) continue
        for (let i = 0; i < layer.length; i++) {
          const feature = layer.feature(i)
          if (feature.type !== 3) continue // Polygon only — no roads/labels/points.
          for (const ring of feature.loadGeometry()) {
            target.push(ring.map((pt) => tileLocalToLatLon(x, y, ZOOM, layer.extent, pt.x, pt.y)))
          }
        }
      }
    }),
  )

  return { earth, water }
}
