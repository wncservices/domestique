package basemap

// Server-side precompute for TrackPreview's card-grid background wash —
// the same earth/landuse/water/roads geometry apps/web's own
// utils/staticBasemap.ts decodes client-side, computed once per route here
// instead, cached, and served as small JSON. A client re-decoding vector
// tiles on every page load never benefits from a previous visitor's work;
// this does, and small JSON (unlike the raw .pmtiles file, which needed a
// Cloudflare cache-bypass rule for Range requests — see
// lab/cloudflare/rules_domestique.tf) is normal-cacheable too.
//
// previewWidth/previewHeight/previewPadding and the zoom/tile-count
// constants below MUST match apps/web/src/components/TrackPreview.vue and
// utils/staticBasemap.ts exactly — there is no shared source of truth
// across the Go/TS boundary, so a change to the card's own size or the
// zoom-selection formula on one side needs the same change made here by
// hand, or the server starts computing a bbox that no longer actually
// covers what the client renders.

import (
	"context"
	"math"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/mvt"
	"github.com/paulmach/orb/maptile"
)

const (
	previewWidth   = 320.0
	previewHeight  = 160.0
	previewPadding = 10.0
)

const (
	previewMinZoom  = 8
	previewMaxZoom  = 13
	previewMaxTiles = 9
)

// LatLon mirrors this app's existing [lat, lon] point convention (see
// handleTrack), not orb's own [lon, lat] Point order.
type LatLon [2]float64

// RoadSegment is one road's geometry plus the basemap "kind" it carries —
// the client picks its own stroke width per kind (ROAD_WIDTH in
// staticBasemap.ts), so only the kind travels over the wire, not a width.
type RoadSegment struct {
	Kind   string   `json:"kind"`
	Points []LatLon `json:"points"`
}

// PreviewLayers is the wire format for GET /api/tracks/{slug}/preview,
// shaped to match staticBasemap.ts's BasemapLayers exactly so the client
// can render it with no translation step.
type PreviewLayers struct {
	Earth      [][]LatLon    `json:"earth"`
	Landuse    [][]LatLon    `json:"landuse"`
	Water      [][]LatLon    `json:"water"`
	WaterLines [][]LatLon    `json:"waterLines"`
	Roads      []RoadSegment `json:"roads"`
}

// greenLanduseKinds mirrors GREEN_LANDUSE_KINDS in staticBasemap.ts.
var greenLanduseKinds = map[string]bool{
	"forest": true, "wood": true, "park": true, "grass": true, "grassland": true,
	"meadow": true, "garden": true, "allotments": true, "national_park": true,
	"nature_reserve": true, "scrub": true, "wetland": true, "recreation_ground": true,
	"playground": true, "pitch": true, "zoo": true, "dog_park": true, "cemetery": true,
}

// allowedRoadKinds mirrors ROAD_WIDTH's key set in staticBasemap.ts —
// rail/path and anything else is dropped as noise at thumbnail size.
var allowedRoadKinds = map[string]bool{
	"highway": true, "major_road": true, "minor_road": true,
}

// RouteBBox computes exactly the bbox TrackPreview.vue's own `projection`
// computed property does: not the route's raw min/max lat/lon, but what's
// visible at the *card's own edges* once the route is centered and scaled
// to fit (aspect-locked, so whichever axis has slack gets letterboxed).
// Fetching only the route's tight bbox left that letterboxed margin with
// no background at all — see the "Fill the whole preview card" fix this
// mirrors. ok is false for a route with fewer than 2 points, same
// threshold as the client's own projection computed.
func RouteBBox(points [][2]float64) (west, south, east, north float64, ok bool) {
	if len(points) < 2 {
		return 0, 0, 0, 0, false
	}

	minLat, maxLat := points[0][0], points[0][0]
	for _, p := range points {
		minLat = math.Min(minLat, p[0])
		maxLat = math.Max(maxLat, p[0])
	}
	midLat := (minLat + maxLat) / 2
	lonScale := math.Cos(midLat * math.Pi / 180)

	minX, maxX := points[0][1]*lonScale, points[0][1]*lonScale
	for _, p := range points {
		x := p[1] * lonScale
		minX = math.Min(minX, x)
		maxX = math.Max(maxX, x)
	}
	minY, maxY := minLat, maxLat

	spanX := maxX - minX
	if spanX == 0 {
		spanX = 1e-9
	}
	spanY := maxY - minY
	if spanY == 0 {
		spanY = 1e-9
	}
	scale := math.Min((previewWidth-2*previewPadding)/spanX, (previewHeight-2*previewPadding)/spanY)
	offsetX := (previewWidth - spanX*scale) / 2
	offsetY := (previewHeight - spanY*scale) / 2

	west = (-offsetX/scale + minX) / lonScale
	east = ((previewWidth-offsetX)/scale + minX) / lonScale
	south = minY - offsetY/scale
	north = minY + (previewHeight-offsetY)/scale
	return west, south, east, north, true
}

func tilesForBBox(west, south, east, north float64, zoom maptile.Zoom) []maptile.Tile {
	nw := maptile.At(orb.Point{west, north}, zoom)
	se := maptile.At(orb.Point{east, south}, zoom)
	var tiles []maptile.Tile
	for x := nw.X; x <= se.X; x++ {
		for y := nw.Y; y <= se.Y; y++ {
			tiles = append(tiles, maptile.New(x, y, zoom))
		}
	}
	return tiles
}

// chooseZoomAndTiles mirrors chooseZoomAndTiles in staticBasemap.ts:
// target ~2-3 tiles across the bbox, clamped to previewMinZoom/MaxZoom,
// with previewMaxTiles as the hard backstop for a long thin route whose
// bbox is wide on one axis but not the other. The zoom arithmetic works in
// a plain int (previewMinZoom/MaxZoom are small int constants, easier to
// read than juggling maptile.Zoom's own uint32 through comparisons and
// decrements) and converts to maptile.Zoom only once it is already
// clamped into [previewMinZoom, previewMaxZoom] — see the #nosec on that
// single conversion below.
func chooseZoomAndTiles(west, south, east, north float64) (maptile.Zoom, []maptile.Tile) {
	span := math.Max(east-west, math.Max(north-south, 1e-6))
	zoomLevel := int(math.Round(math.Log2(900 / span)))
	if zoomLevel < previewMinZoom {
		zoomLevel = previewMinZoom
	}
	if zoomLevel > previewMaxZoom {
		zoomLevel = previewMaxZoom
	}
	zoom := maptile.Zoom(zoomLevel) // #nosec G115 -- zoomLevel is already clamped to [previewMinZoom, previewMaxZoom] = [8, 13] above.
	tiles := tilesForBBox(west, south, east, north, zoom)
	for len(tiles) > previewMaxTiles && zoomLevel > previewMinZoom {
		zoomLevel--
		zoom = maptile.Zoom(zoomLevel) // #nosec G115 -- see above; zoomLevel only ever decreases toward previewMinZoom here.
		tiles = tilesForBBox(west, south, east, north, zoom)
	}
	return zoom, tiles
}

func pointsToLatLon(pts []orb.Point) []LatLon {
	out := make([]LatLon, len(pts))
	for i, pt := range pts {
		// orb.Point is [lon, lat]; flip to this app's [lat, lon] convention.
		out[i] = LatLon{pt[1], pt[0]}
	}
	return out
}

func polygonRings(geom orb.Geometry) [][]LatLon {
	switch g := geom.(type) {
	case orb.Polygon:
		out := make([][]LatLon, len(g))
		for i, ring := range g {
			out[i] = pointsToLatLon(ring)
		}
		return out
	case orb.MultiPolygon:
		var out [][]LatLon
		for _, poly := range g {
			for _, ring := range poly {
				out = append(out, pointsToLatLon(ring))
			}
		}
		return out
	}
	return nil
}

func lineStrings(geom orb.Geometry) [][]LatLon {
	switch g := geom.(type) {
	case orb.LineString:
		return [][]LatLon{pointsToLatLon(g)}
	case orb.MultiLineString:
		out := make([][]LatLon, len(g))
		for i, ls := range g {
			out[i] = pointsToLatLon(ls)
		}
		return out
	}
	return nil
}

// PreviewTiles fetches the four preview layers from one PMTiles archive.
type PreviewTiles struct {
	client *pmtilesClient
}

// NewPreviewTiles points at the tiles component's basemap.pmtiles over
// HTTP — see BasemapConfig.TilesServiceURL.
func NewPreviewTiles(url string) *PreviewTiles {
	return &PreviewTiles{client: newPMTilesClient(url)}
}

// FetchLayers fetches whichever tiles cover the bbox at a zoom chosen to
// fit it, decodes each one, and merges the four layers this preview cares
// about. Best-effort throughout, matching staticBasemap.ts: a tile outside
// the archive's own coverage, or one that fails to decode, is skipped
// rather than failing the whole request — a route near the edge of
// whatever an admin extracted still gets whatever land/water does exist.
func (p *PreviewTiles) FetchLayers(ctx context.Context, west, south, east, north float64) (PreviewLayers, error) {
	var out PreviewLayers

	header, err := p.client.getHeader(ctx)
	if err != nil {
		return out, err
	}

	_, tiles := chooseZoomAndTiles(west, south, east, north)
	for _, t := range tiles {
		data, err := p.client.GetTile(ctx, header, uint32(t.Z), uint64(t.X), uint64(t.Y))
		if err != nil || data == nil {
			continue
		}
		layers, err := mvt.Unmarshal(data)
		if err != nil {
			continue
		}
		layers.ProjectToWGS84(t)

		for _, layer := range layers {
			switch layer.Name {
			case "earth":
				for _, f := range layer.Features {
					out.Earth = append(out.Earth, polygonRings(f.Geometry)...)
				}
			case "landuse":
				for _, f := range layer.Features {
					if greenLanduseKinds[f.Properties.MustString("kind")] {
						out.Landuse = append(out.Landuse, polygonRings(f.Geometry)...)
					}
				}
			case "water":
				for _, f := range layer.Features {
					switch f.Geometry.(type) {
					case orb.Polygon, orb.MultiPolygon:
						out.Water = append(out.Water, polygonRings(f.Geometry)...)
					case orb.LineString, orb.MultiLineString:
						out.WaterLines = append(out.WaterLines, lineStrings(f.Geometry)...)
					}
				}
			case "roads":
				for _, f := range layer.Features {
					kind := f.Properties.MustString("kind")
					if !allowedRoadKinds[kind] {
						continue
					}
					for _, pts := range lineStrings(f.Geometry) {
						out.Roads = append(out.Roads, RoadSegment{Kind: kind, Points: pts})
					}
				}
			}
		}
	}

	return out, nil
}
