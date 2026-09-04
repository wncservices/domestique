package gpx

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

const twoPointGPX = `<?xml version="1.0"?>
<gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1">
  <trk><trkseg>
    <trkpt lat="50.0000" lon="3.0000"><ele>10.0</ele></trkpt>
    <trkpt lat="50.0100" lon="3.0000"><ele>30.0</ele></trkpt>
  </trkseg></trk>
</gpx>`

func writeGPX(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "route.gpx")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadPointsAcceptsRouteElement(t *testing.T) {
	path := writeGPX(t, `<?xml version="1.0"?>
<gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1">
  <rte>
    <rtept lat="50.0" lon="3.0"/>
    <rtept lat="50.1" lon="3.0"/>
  </rte>
</gpx>`)

	points, err := ReadPoints(path)
	if err != nil {
		t.Fatalf("ReadPoints: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("got %d points, want 2", len(points))
	}
}

func TestParseCuesReadsRteptAndWpt(t *testing.T) {
	raw := []byte(`<?xml version="1.0"?>
<gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1">
  <wpt lat="50.0050" lon="3.0000">
    <name>Water stop</name>
  </wpt>
  <trk><trkseg>
    <trkpt lat="50.0000" lon="3.0000"/>
    <trkpt lat="50.0100" lon="3.0000"/>
  </trkseg></trk>
  <rte>
    <rtept lat="50.0" lon="3.0">
      <name>Turn right onto Main St</name>
      <sym>Right</sym>
    </rtept>
  </rte>
</gpx>`)

	cues, err := ParseCues(raw)
	if err != nil {
		t.Fatalf("ParseCues: %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2: %+v", len(cues), cues)
	}

	wpt := cues[0]
	if wpt.Name != "Water stop" {
		t.Errorf("wpt name = %q, want %q", wpt.Name, "Water stop")
	}

	rtept := cues[1]
	if rtept.Name != "Turn right onto Main St" || rtept.Sym != "Right" {
		t.Errorf("rtept = %+v, want name %q and sym %q", rtept, "Turn right onto Main St", "Right")
	}
}

// A <rte> alongside a <trk> is exactly the shape a cue-sheet export takes: a
// dense track for the geometry, a sparse annotated route for the cues.
// ParsePoints uses the track for geometry and never looks at the route at
// all (see its own doc comment); ParseCues must still read the route's cue
// regardless.
func TestParseCuesReadsRteEvenWithATrkPresent(t *testing.T) {
	raw := []byte(`<?xml version="1.0"?>
<gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1">
  <trk><trkseg>
    <trkpt lat="50.0000" lon="3.0000"/>
    <trkpt lat="50.0100" lon="3.0000"/>
  </trkseg></trk>
  <rte><rtept lat="50.0" lon="3.0"><name>Cue</name></rtept></rte>
</gpx>`)

	points, err := ParsePoints(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("ParsePoints should have used the track, got %d points", len(points))
	}

	cues, err := ParseCues(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 1 || cues[0].Name != "Cue" {
		t.Errorf("ParseCues should still have read the route's cue, got %+v", cues)
	}
}

func TestParseCuesReturnsNilForOrdinaryGPX(t *testing.T) {
	cues, err := ParseCues([]byte(twoPointGPX))
	if err != nil {
		t.Fatal(err)
	}
	if cues != nil {
		t.Errorf("expected no cues from a plain track, got %+v", cues)
	}
}

func TestParseCuesRejectsGarbage(t *testing.T) {
	if _, err := ParseCues([]byte("not xml")); err == nil {
		t.Fatal("garbage bytes parsed without error")
	}
}

func TestReadPointsRejectsTooFewPoints(t *testing.T) {
	path := writeGPX(t, `<?xml version="1.0"?>
<gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1">
  <trk><trkseg><trkpt lat="50.0" lon="3.0"/></trkseg></trk>
</gpx>`)

	if _, err := ReadPoints(path); err == nil {
		t.Fatal("expected an error for a single-point GPX")
	}
}

func TestRenderRoundTripsThroughParsePoints(t *testing.T) {
	points := []Point{
		{Lat: 50.7920, Lon: 2.8180, Ele: 42, HasEle: true},
		{Lat: 50.7982, Lon: 2.8344},
		{Lat: 50.8007, Lon: 2.8437, Ele: 139, HasEle: true},
	}

	raw, err := Render("Kemmelberg Loop", points)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got, err := ParsePoints(raw)
	if err != nil {
		t.Fatalf("our own parser rejected Render's output: %v\n%s", err, raw)
	}
	if len(got) != len(points) {
		t.Fatalf("got %d points, want %d", len(got), len(points))
	}
	for i, p := range got {
		if p.Lat != points[i].Lat || p.Lon != points[i].Lon {
			t.Errorf("point %d = %+v, want %+v", i, p, points[i])
		}
		if p.HasEle != points[i].HasEle || (p.HasEle && p.Ele != points[i].Ele) {
			t.Errorf("point %d elevation = %+v, want %+v", i, p, points[i])
		}
	}
}

func TestRenderRejectsTooFewPoints(t *testing.T) {
	if _, err := Render("Too short", []Point{{Lat: 50, Lon: 3}}); err == nil {
		t.Fatal("a single-point track rendered without error")
	}
}

func TestComputeStats(t *testing.T) {
	points, err := ReadPoints(writeGPX(t, twoPointGPX))
	if err != nil {
		t.Fatal(err)
	}
	stats := ComputeStats(points)

	// 0.01 degrees of latitude is ~1112 m.
	if math.Abs(stats.DistanceM-1112) > 15 {
		t.Errorf("distance = %.1f m, want ~1112 m", stats.DistanceM)
	}
	if math.Abs(stats.AscentM-20) > 0.01 {
		t.Errorf("ascent = %.1f m, want 20 m", stats.AscentM)
	}
	if stats.StartLat != 50.0 || stats.StartLng != 3.0 {
		t.Errorf("start = %v,%v, want 50,3", stats.StartLat, stats.StartLng)
	}
}

func TestComputeStatsIgnoresElevationNoise(t *testing.T) {
	// A dead-flat track with sub-threshold jitter must report no ascent.
	points := []Point{
		{Lat: 50, Lon: 3, Ele: 10, HasEle: true},
		{Lat: 50.001, Lon: 3, Ele: 11.5, HasEle: true},
		{Lat: 50.002, Lon: 3, Ele: 10.2, HasEle: true},
		{Lat: 50.003, Lon: 3, Ele: 11.8, HasEle: true},
	}
	if got := ComputeStats(points).AscentM; got != 0 {
		t.Errorf("ascent = %.2f m, want 0 from noise alone", got)
	}
}

func TestSmoothElevationHoldsThroughSubThresholdJitter(t *testing.T) {
	// Same track TestComputeStatsIgnoresElevationNoise uses — a dead-flat
	// road whose DEM samples jitter by a metre or two. ComputeStats already
	// reports 0 m ascent for this; the chart fed from SmoothElevation must
	// agree, not plot the raw 10/11.5/10.2/11.8 zigzag as real climbing.
	points := []Point{
		{Lat: 50, Lon: 3, Ele: 10, HasEle: true},
		{Lat: 50.001, Lon: 3, Ele: 11.5, HasEle: true},
		{Lat: 50.002, Lon: 3, Ele: 10.2, HasEle: true},
		{Lat: 50.003, Lon: 3, Ele: 11.8, HasEle: true},
	}
	got := SmoothElevation(points)
	for i, ele := range got {
		if ele != 10 {
			t.Errorf("smoothed[%d] = %.2f m, want 10 m (held at the first confirmed value)", i, ele)
		}
	}
}

func TestSmoothElevationTracksARealClimb(t *testing.T) {
	// A genuine climb — each step past the threshold — must still show up,
	// not get smoothed away along with the noise.
	points := []Point{
		{Lat: 50, Lon: 3, Ele: 100, HasEle: true},
		{Lat: 50.001, Lon: 3, Ele: 110, HasEle: true},
		{Lat: 50.002, Lon: 3, Ele: 120, HasEle: true},
	}
	got := SmoothElevation(points)
	want := []float64{100, 110, 120}
	for i, ele := range got {
		if ele != want[i] {
			t.Errorf("smoothed[%d] = %.2f m, want %.2f m", i, ele, want[i])
		}
	}
}

func TestSmoothElevationHoldsLastValueForPointsWithoutTheirOwn(t *testing.T) {
	points := []Point{
		{Lat: 50, Lon: 3, Ele: 50, HasEle: true},
		{Lat: 50.001, Lon: 3}, // no elevation of its own
		{Lat: 50.002, Lon: 3, Ele: 60, HasEle: true},
	}
	got := SmoothElevation(points)
	if got[1] != 50 {
		t.Errorf("smoothed[1] = %.2f m, want 50 m (last confirmed value carried forward)", got[1])
	}
}

func TestNeedsElevation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		points []Point
		want   bool
	}{
		{
			name:   "no point has any elevation at all",
			points: []Point{{Lat: 50, Lon: 3}, {Lat: 50.001, Lon: 3}},
			want:   true,
		},
		{
			// The real-world shape this function exists for: a route
			// planner (afstandmeten.nl, found live) stamps every point
			// with a literal 0.00000 rather than omitting <ele> or
			// querying real terrain.
			name: "every point reports exactly zero",
			points: []Point{
				{Lat: 50, Lon: 3, Ele: 0, HasEle: true},
				{Lat: 50.001, Lon: 3, Ele: 0, HasEle: true},
				{Lat: 50.002, Lon: 3, Ele: 0, HasEle: true},
			},
			want: true,
		},
		{
			name: "real, nonzero elevation throughout",
			points: []Point{
				{Lat: 50, Lon: 3, Ele: 12.4, HasEle: true},
				{Lat: 50.001, Lon: 3, Ele: 15.8, HasEle: true},
			},
			want: false,
		},
		{
			name: "sparse but genuine elevation is left alone",
			points: []Point{
				{Lat: 50, Lon: 3},
				{Lat: 50.001, Lon: 3, Ele: 42, HasEle: true},
				{Lat: 50.002, Lon: 3},
			},
			want: false,
		},
		{
			// A single point genuinely at sea level does not, on its
			// own, prove the whole file is a placeholder — only every
			// point being exactly zero does.
			name: "one real point happens to be exactly zero, others are not",
			points: []Point{
				{Lat: 50, Lon: 3, Ele: 0, HasEle: true},
				{Lat: 50.001, Lon: 3, Ele: 8.2, HasEle: true},
			},
			want: false,
		},
		{
			name:   "no points at all",
			points: nil,
			want:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsElevation(tc.points); got != tc.want {
				t.Errorf("NeedsElevation() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestContentHashIgnoresPrecisionBelowOneMetre(t *testing.T) {
	base := []Point{{Lat: 50.123456789, Lon: 3.1234567}, {Lat: 50.2, Lon: 3.2}}
	jittered := []Point{{Lat: 50.1234561, Lon: 3.1234569}, {Lat: 50.2, Lon: 3.2}}

	if ContentHash(base, "Loop", "") != ContentHash(jittered, "Loop", "") {
		t.Error("hash changed on sub-metre coordinate jitter; re-exports would churn")
	}
	if ContentHash(base, "Loop", "") == ContentHash(base, "Other Loop", "") {
		t.Error("hash ignored the route name; renames would not propagate")
	}
}
