package basemap

import (
	"bytes"
	"image/png"
	"testing"
)

// samplePoints is a small real-shaped route (a short loop, not a straight
// line) — long enough to give newCardProjection a real span on both axes.
func samplePoints() [][2]float64 {
	return [][2]float64{
		{50.880, 4.700}, {50.882, 4.703}, {50.884, 4.706},
		{50.883, 4.710}, {50.879, 4.708}, {50.880, 4.700},
	}
}

func sampleLayers() PreviewLayers {
	return PreviewLayers{
		Earth:      [][]LatLon{{{50.870, 4.690}, {50.870, 4.720}, {50.895, 4.720}, {50.895, 4.690}}},
		Landuse:    [][]LatLon{{{50.878, 4.698}, {50.878, 4.705}, {50.885, 4.705}, {50.885, 4.698}}},
		Water:      [][]LatLon{{{50.881, 4.712}, {50.881, 4.716}, {50.884, 4.716}, {50.884, 4.712}}},
		WaterLines: [][]LatLon{{{50.875, 4.695}, {50.877, 4.699}}},
		Roads: []RoadSegment{
			{Kind: "highway", Points: []LatLon{{50.879, 4.699}, {50.885, 4.709}}},
			{Kind: "minor_road", Points: []LatLon{{50.880, 4.701}, {50.882, 4.702}}},
		},
	}
}

// TestRenderCardImageProducesAValidPNGAtTheExpectedSize pins the whole point
// of this file: replacing a 1.5-2.6MB JSON payload (measured on a real
// route — see this file's own doc comment) with a rendered image the
// browser needs no client-side parsing or path construction for. A tiny
// PNG that's the wrong size defeats that just as much as one that fails to
// decode at all.
func TestRenderCardImageProducesAValidPNGAtTheExpectedSize(t *testing.T) {
	for _, theme := range []string{"light", "dark", "unrecognized-falls-back-to-light"} {
		data, err := RenderCardImage(samplePoints(), sampleLayers(), theme)
		if err != nil {
			t.Fatalf("theme %q: RenderCardImage: %v", theme, err)
		}

		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("theme %q: decode PNG: %v", theme, err)
		}
		bounds := img.Bounds()
		wantW, wantH := int(previewWidth*imageScale), int(previewHeight*imageScale)
		if bounds.Dx() != wantW || bounds.Dy() != wantH {
			t.Errorf("theme %q: image size = %dx%d, want %dx%d", theme, bounds.Dx(), bounds.Dy(), wantW, wantH)
		}

		// Not a tight bound — just enough to catch a gross regression (an
		// accidentally-uncompressed dump, or a canvas far larger than
		// intended) without pinning an exact byte count a minor gg/png
		// version bump could shift.
		const sanityCeilingBytes = 200_000
		if len(data) > sanityCeilingBytes {
			t.Errorf("theme %q: image is %d bytes, want well under %d — the whole point of this "+
				"endpoint is replacing a multi-megabyte JSON payload with a small image",
				theme, len(data), sanityCeilingBytes)
		}
	}
}

// TestRenderCardImageRejectsTooFewPoints mirrors newCardProjection's own
// threshold (and TrackPreview.vue's client-side one) — fewer than 2 points
// means no route shape to fit a canvas to at all.
func TestRenderCardImageRejectsTooFewPoints(t *testing.T) {
	_, err := RenderCardImage([][2]float64{{50.88, 4.70}}, sampleLayers(), "light")
	if err == nil {
		t.Fatal("RenderCardImage with 1 point: want an error, got nil")
	}
}
