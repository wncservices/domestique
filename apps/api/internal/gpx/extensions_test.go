package gpx

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A recorded 200k-point track from a head unit carries time, power, heart
// rate, cadence and temperature on every point: about eleven elements each.
// That is a legitimate file and must not trip the element bound.
func TestParseUploadAcceptsAnExtensionHeavyTrackAtThePointCap(t *testing.T) {
	var sb strings.Builder
	sb.Grow(60 << 20)
	for i := 0; i < MaxPoints; i++ {
		fmt.Fprintf(&sb, `<trkpt lat="51.%06d" lon="4.%06d"><ele>12.5</ele><time>2026-01-01T00:00:00Z</time>`+
			`<extensions><power>210</power><gpxtpx:TrackPointExtension><gpxtpx:atemp>14</gpxtpx:atemp>`+
			`<gpxtpx:hr>141</gpxtpx:hr><gpxtpx:cad>88</gpxtpx:cad><gpxtpx:speed>8</gpxtpx:speed><gpxtpx:course>90</gpxtpx:course></gpxtpx:TrackPointExtension></extensions></trkpt>`, i%1000000, i%1000000)
	}
	points, err := ParseUpload(context.Background(), gpxWith(sb.String()))
	if err != nil {
		t.Fatalf("a legitimate extension-heavy track at the point cap was refused: %v", err)
	}
	if len(points) != MaxPoints {
		t.Errorf("%d points", len(points))
	}
}
