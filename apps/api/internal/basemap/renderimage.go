package basemap

// Server-side raster rendering of TrackPreview.vue's card — the same
// background wash, route line and start dot the client builds as SVG from
// PreviewLayers JSON, drawn once here as a PNG instead. That JSON payload
// measured 1.5-2.6MB for a real dense route (Tempo trace inspection, see the
// commit this file was added in), re-fetched, re-parsed and re-rendered by
// every client on every card view; a rendered PNG at this size is tens of
// KB and needs no client-side work at all beyond decoding an <img>.

import (
	"bytes"
	"fmt"

	"github.com/fogleman/gg"
)

// imageScale renders at 2x the CSS-displayed previewWidth x previewHeight
// card size, so the PNG stays sharp on a retina/high-DPI screen — unlike the
// SVG it replaces, a raster image needs enough native pixels or it visibly
// blurs once the browser scales it up to fill the card.
const imageScale = 2

// cardImageColors are the exact hex values TrackPreview.vue's own MAP_COLORS
// and styles.css's --ui-primary/--ui-bg hardcode per theme, copied here for
// the same reason TrackPreview.vue's own comment already gives for copying
// them from protomaps-themes-base in the first place: there is no shared
// source of truth across the Go/TS boundary, so a palette change has to be
// made by hand in both places. road is 8-digit hex (RRGGBBAA) — the 70%
// opacity TrackPreview.vue applies via `stroke-[var(--ui-bg)]/70`.
type cardImageColors struct {
	earth, landuse, water, road, track string
}

var cardImageThemes = map[string]cardImageColors{
	"light": {earth: "#e2dfda", landuse: "#cfddd5", water: "#80deea", road: "#f6faf8B3", track: "#049483"},
	"dark":  {earth: "#1f1f1f", landuse: "#1c2421", water: "#31353f", road: "#050a09B3", track: "#14cfab"},
}

// cardRoadOrder is staticBasemap.ts's ROAD_WIDTH, plus a fixed draw order
// (highway last, so a more significant road never disappears under a minor
// one crossing it) — a plain map range over kinds would draw them in a
// random order every render, which costs nothing for correctness here (thin
// same-colored strokes) but makes output non-deterministic for no reason,
// which the round-trip test relies on not being true.
var cardRoadOrder = []struct {
	kind  string
	width float64
}{
	{"minor_road", 0.5},
	{"major_road", 0.9},
	{"highway", 1.4},
}

// RenderCardImage draws PreviewLayers plus the route's own points as a single
// PNG, in the same order TrackPreview.vue's template does: earth, landuse,
// water fills, water lines, roads, the track line, then the start dot.
// theme selects the palette; an unrecognized value (including "") falls back
// to "light", the same default TrackPreview.vue's own useColorMode resolves
// to before dark mode is known.
func RenderCardImage(points [][2]float64, layers PreviewLayers, theme string) ([]byte, error) {
	proj, ok := newCardProjection(points)
	if !ok {
		return nil, fmt.Errorf("render card image: fewer than 2 points")
	}
	colors, ok := cardImageThemes[theme]
	if !ok {
		colors = cardImageThemes["light"]
	}

	dc := gg.NewContext(int(previewWidth*imageScale), int(previewHeight*imageScale))

	project := func(pt LatLon) (float64, float64) {
		x, y := proj.project(pt[0], pt[1])
		return x * imageScale, y * imageScale
	}

	fillRings := func(rings [][]LatLon, hex string) {
		if len(rings) == 0 {
			return
		}
		for _, ring := range rings {
			drawPath(dc, ring, project)
			dc.ClosePath()
		}
		dc.SetHexColor(hex)
		dc.Fill()
	}

	strokeLines := func(lines [][]LatLon, hex string, width float64) {
		if len(lines) == 0 {
			return
		}
		for _, line := range lines {
			drawPath(dc, line, project)
		}
		dc.SetHexColor(hex)
		dc.SetLineWidth(width * imageScale)
		dc.Stroke()
	}

	fillRings(layers.Earth, colors.earth)
	fillRings(layers.Landuse, colors.landuse)
	fillRings(layers.Water, colors.water)
	strokeLines(layers.WaterLines, colors.water, 1)

	byKind := make(map[string][][]LatLon, len(cardRoadOrder))
	for _, road := range layers.Roads {
		byKind[road.Kind] = append(byKind[road.Kind], road.Points)
	}
	for _, r := range cardRoadOrder {
		strokeLines(byKind[r.kind], colors.road, r.width)
	}

	track := make([]LatLon, len(points))
	for i, p := range points {
		track[i] = LatLon{p[0], p[1]}
	}
	strokeLines([][]LatLon{track}, colors.track, 2.5)

	startX, startY := project(track[0])
	dc.DrawCircle(startX, startY, 4*imageScale)
	dc.SetHexColor(colors.track)
	dc.Fill()

	var buf bytes.Buffer
	if err := dc.EncodePNG(&buf); err != nil {
		return nil, fmt.Errorf("encode card image: %w", err)
	}
	return buf.Bytes(), nil
}

// drawPath adds one ring/line's points to dc's current path as a new
// subpath (MoveTo the first point, LineTo the rest) without filling or
// stroking — callers accumulate every ring/line they want in one fill or
// stroke call before invoking it, since SetLineWidth only takes effect on
// the *next* Stroke and Fill/Stroke both clear the path afterward.
func drawPath(dc *gg.Context, ring []LatLon, project func(LatLon) (float64, float64)) {
	for i, pt := range ring {
		x, y := project(pt)
		if i == 0 {
			dc.MoveTo(x, y)
		} else {
			dc.LineTo(x, y)
		}
	}
}
