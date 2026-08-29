package fitcourse

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/typedef"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
)

// decode reads a FIT course back. The decoder verifies the file header and the
// CRC, so a successful decode is also proof the bytes are structurally valid
// FIT and not just something that looked right on the way out.
func decode(t *testing.T, raw []byte) *filedef.Course {
	t.Helper()

	fit, err := decoder.New(bytes.NewReader(raw)).Decode()
	if err != nil {
		t.Fatalf("the FIT we produced does not decode: %v", err)
	}
	return filedef.NewCourse(fit.Messages...)
}

// straightNorth builds a track heading due north, `count` points `spacingM` apart.
func straightNorth(count int, spacingM float64) []gpx.Point {
	// 1 degree of latitude is ~111_320 m.
	step := spacingM / 111_320.0
	points := make([]gpx.Point, count)
	for i := range points {
		points[i] = gpx.Point{Lat: 50.0 + float64(i)*step, Lon: 3.0, Ele: 40, HasEle: true}
	}
	return points
}

func TestEncodeRoundTrip(t *testing.T) {
	points := []gpx.Point{
		{Lat: 50.7920, Lon: 2.8180, Ele: 42, HasEle: true},
		{Lat: 50.7982, Lon: 2.8344, Ele: 128, HasEle: true},
		{Lat: 50.8007, Lon: 2.8437, Ele: 139, HasEle: true},
	}

	raw, err := Encode(points, Options{Name: "Kemmelberg Loop", CreatedAt: time.Unix(1_800_000_000, 0).UTC()})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	course := decode(t, raw)

	// The device files a course by its file_id type. Get this wrong and the
	// file imports as something else, or not at all.
	if course.FileId.Type != typedef.FileCourse {
		t.Errorf("file type = %v, want course", course.FileId.Type)
	}
	if course.Course == nil {
		t.Fatal("no course message — the device would have no name to show")
	}
	if course.Course.Name != "Kemmelberg Loop" {
		t.Errorf("name = %q", course.Course.Name)
	}
	if course.Course.Sport != typedef.SportCycling {
		t.Errorf("sport = %v, want cycling", course.Course.Sport)
	}

	if len(course.Records) != len(points) {
		t.Fatalf("got %d records, want %d", len(course.Records), len(points))
	}

	// Coordinates survive the trip through semicircles. The unit is
	// 180/2^31 degrees, so ~1e-7 of a degree; allow a shade more.
	for i, record := range course.Records {
		gotLat := record.PositionLatDegrees()
		gotLon := record.PositionLongDegrees()
		if math.Abs(gotLat-points[i].Lat) > 1e-6 {
			t.Errorf("record %d lat = %.7f, want %.7f", i, gotLat, points[i].Lat)
		}
		if math.Abs(gotLon-points[i].Lon) > 1e-6 {
			t.Errorf("record %d lon = %.7f, want %.7f", i, gotLon, points[i].Lon)
		}
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	points := []gpx.Point{
		{Lat: 50.7920, Lon: 2.8180, Ele: 42, HasEle: true},
		{Lat: 50.7982, Lon: 2.8344, Ele: 128, HasEle: true},
		{Lat: 50.8007, Lon: 2.8437, Ele: 139, HasEle: true},
	}

	raw, err := Encode(points, Options{Name: "Kemmelberg Loop"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(got) != len(points) {
		t.Fatalf("got %d points, want %d", len(got), len(points))
	}
	for i, p := range got {
		if math.Abs(p.Lat-points[i].Lat) > 1e-6 {
			t.Errorf("point %d lat = %.7f, want %.7f", i, p.Lat, points[i].Lat)
		}
		if math.Abs(p.Lon-points[i].Lon) > 1e-6 {
			t.Errorf("point %d lon = %.7f, want %.7f", i, p.Lon, points[i].Lon)
		}
		if !p.HasEle {
			t.Errorf("point %d lost its elevation", i)
		}
		// Altitude's scale is 1/5 m — a shade more than 0.2 tolerates rounding.
		if math.Abs(p.Ele-points[i].Ele) > 0.25 {
			t.Errorf("point %d ele = %.2f, want %.2f", i, p.Ele, points[i].Ele)
		}
	}
}

func TestDecodeWithoutElevationLeavesHasEleFalse(t *testing.T) {
	points := []gpx.Point{{Lat: 50.79, Lon: 2.81}, {Lat: 50.80, Lon: 2.84}}

	raw, err := Encode(points, Options{Name: "No elevation"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range got {
		if p.HasEle {
			t.Errorf("point %d reported an elevation Encode never wrote: %v", i, p)
		}
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := Decode([]byte("not a fit file")); err == nil {
		t.Fatal("garbage bytes decoded without error")
	}
}

func TestEncodeCarriesLapSummary(t *testing.T) {
	points := straightNorth(5, 100)

	raw, err := Encode(points, Options{Name: "Straight"})
	if err != nil {
		t.Fatal(err)
	}
	course := decode(t, raw)

	if course.Lap == nil {
		t.Fatal("no lap message — some devices show no summary without it")
	}
	// 4 gaps of 100 m.
	if got := course.Lap.TotalDistanceScaled(); math.Abs(got-400) > 5 {
		t.Errorf("lap distance = %.1f m, want ~400", got)
	}
	if math.Abs(course.Lap.StartPositionLatDegrees()-points[0].Lat) > 1e-6 {
		t.Errorf("lap start does not match the first point")
	}
	if math.Abs(course.Lap.EndPositionLatDegrees()-points[len(points)-1].Lat) > 1e-6 {
		t.Errorf("lap end does not match the last point")
	}
}

// Distance along the track is what course points and the device's "distance to
// next turn" are expressed in, so it has to increase monotonically.
func TestRecordDistancesAreMonotonic(t *testing.T) {
	raw, err := Encode(straightNorth(10, 50), Options{})
	if err != nil {
		t.Fatal(err)
	}
	course := decode(t, raw)

	previous := -1.0
	for i, record := range course.Records {
		got := record.DistanceScaled()
		if got < previous {
			t.Fatalf("record %d distance %.1f went backwards from %.1f", i, got, previous)
		}
		previous = got
	}
	if previous < 400 {
		t.Errorf("final distance = %.1f m, want ~450", previous)
	}
}

func TestEncodeWithoutElevation(t *testing.T) {
	points := []gpx.Point{
		{Lat: 50.0, Lon: 3.0},
		{Lat: 50.01, Lon: 3.0},
	}

	raw, err := Encode(points, Options{})
	if err != nil {
		t.Fatalf("a track with no elevation should still encode: %v", err)
	}
	if course := decode(t, raw); len(course.Records) != 2 {
		t.Errorf("got %d records, want 2", len(course.Records))
	}
}

func TestEncodeRejectsTooFewPoints(t *testing.T) {
	for name, points := range map[string][]gpx.Point{
		"empty": {},
		"one":   {{Lat: 50, Lon: 3}},
	} {
		if _, err := Encode(points, Options{}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestEncodeDefaultsTheName(t *testing.T) {
	raw, err := Encode(straightNorth(3, 100), Options{})
	if err != nil {
		t.Fatal(err)
	}
	// An unnamed course shows as blank in the device menu; anything beats that.
	if got := decode(t, raw).Course.Name; got == "" {
		t.Error("name is empty")
	}
}

// ---------- turn cues ----------

func TestNoTurnCuesByDefault(t *testing.T) {
	// A right-angle corner, but cues are off.
	points := append(straightNorth(6, 40), gpx.Point{Lat: 50.0018, Lon: 3.0030})

	raw, err := Encode(points, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(decode(t, raw).CoursePoints); got != 0 {
		t.Errorf("got %d course points with TurnCues off, want 0", got)
	}
}

func TestStraightTrackProducesNoTurns(t *testing.T) {
	points := straightNorth(40, 20)
	turns := DeriveTurns(points, cumulativeDistances(points))
	if len(turns) != 0 {
		t.Errorf("a straight line produced %d turns: %+v", len(turns), turns)
	}
}

// GPS wobble must not read as a turn, or a straight road gets a cue every
// 50 metres and the rider stops trusting them.
func TestJitterProducesNoTurns(t *testing.T) {
	points := straightNorth(60, 20)
	for i := range points {
		// ±3 m of lateral noise, alternating.
		if i%2 == 0 {
			points[i].Lon += 3.0 / 71_000.0
		} else {
			points[i].Lon -= 3.0 / 71_000.0
		}
	}

	turns := DeriveTurns(points, cumulativeDistances(points))
	if len(turns) != 0 {
		t.Errorf("jitter produced %d spurious turns: %+v", len(turns), turns)
	}
}

func TestRightAngleTurnIsDetected(t *testing.T) {
	// North for 400 m, then due east for 400 m.
	var points []gpx.Point
	latStep := 20.0 / 111_320.0
	lonStep := 20.0 / 71_000.0 // ~1 degree lon at 50°N is ~71.7 km

	for i := 0; i < 20; i++ {
		points = append(points, gpx.Point{Lat: 50.0 + float64(i)*latStep, Lon: 3.0})
	}
	corner := points[len(points)-1]
	for i := 1; i <= 20; i++ {
		points = append(points, gpx.Point{Lat: corner.Lat, Lon: corner.Lon + float64(i)*lonStep})
	}

	turns := DeriveTurns(points, cumulativeDistances(points))
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want exactly 1: %+v", len(turns), turns)
	}

	turn := turns[0]
	// North then east is a right turn.
	if turn.Type != typedef.CoursePointRight {
		t.Errorf("type = %v, want right", turn.Type)
	}
	if turn.Degrees < 60 || turn.Degrees > 120 {
		t.Errorf("heading change = %.0f°, want ~90", turn.Degrees)
	}
}

func TestLeftTurnIsDetected(t *testing.T) {
	// North, then due west.
	var points []gpx.Point
	latStep := 20.0 / 111_320.0
	lonStep := 20.0 / 71_000.0

	for i := 0; i < 20; i++ {
		points = append(points, gpx.Point{Lat: 50.0 + float64(i)*latStep, Lon: 3.0})
	}
	corner := points[len(points)-1]
	for i := 1; i <= 20; i++ {
		points = append(points, gpx.Point{Lat: corner.Lat, Lon: corner.Lon - float64(i)*lonStep})
	}

	turns := DeriveTurns(points, cumulativeDistances(points))
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1: %+v", len(turns), turns)
	}
	if turns[0].Type != typedef.CoursePointLeft {
		t.Errorf("type = %v, want left", turns[0].Type)
	}
	if turns[0].Degrees > -60 {
		t.Errorf("heading change = %.0f°, want ~-90", turns[0].Degrees)
	}
}

func TestHairpinIsSharp(t *testing.T) {
	// North, then straight back south alongside.
	var points []gpx.Point
	latStep := 20.0 / 111_320.0
	for i := 0; i < 20; i++ {
		points = append(points, gpx.Point{Lat: 50.0 + float64(i)*latStep, Lon: 3.0})
	}
	corner := points[len(points)-1]
	offset := 15.0 / 71_000.0
	for i := 1; i <= 20; i++ {
		points = append(points, gpx.Point{Lat: corner.Lat - float64(i)*latStep, Lon: corner.Lon + offset})
	}

	turns := DeriveTurns(points, cumulativeDistances(points))
	if len(turns) == 0 {
		t.Fatal("a hairpin produced no turn")
	}
	got := turns[0].Type
	if got != typedef.CoursePointSharpLeft && got != typedef.CoursePointSharpRight {
		t.Errorf("type = %v, want a sharp turn", got)
	}
}

func TestTurnCuesReachTheFile(t *testing.T) {
	var points []gpx.Point
	latStep := 20.0 / 111_320.0
	lonStep := 20.0 / 71_000.0
	for i := 0; i < 20; i++ {
		points = append(points, gpx.Point{Lat: 50.0 + float64(i)*latStep, Lon: 3.0})
	}
	corner := points[len(points)-1]
	for i := 1; i <= 20; i++ {
		points = append(points, gpx.Point{Lat: corner.Lat, Lon: corner.Lon + float64(i)*lonStep})
	}

	raw, err := Encode(points, Options{Name: "Corner", TurnCues: true})
	if err != nil {
		t.Fatal(err)
	}

	course := decode(t, raw)
	if len(course.CoursePoints) != 1 {
		t.Fatalf("got %d course points in the file, want 1", len(course.CoursePoints))
	}

	cue := course.CoursePoints[0]
	if cue.Type != typedef.CoursePointRight {
		t.Errorf("cue type = %v, want right", cue.Type)
	}
	if cue.Name == "" {
		t.Error("cue has no name; devices display this")
	}
	// The cue has to sit at a sensible distance along the route, or the device
	// announces it in the wrong place.
	if d := cue.DistanceScaled(); d < 300 || d > 500 {
		t.Errorf("cue distance = %.0f m, want ~400 (the corner)", d)
	}
}

// A single junction must not produce a burst of cues as the track bends
// through it.
func TestClosePointsCollapseToOneCue(t *testing.T) {
	// A tight 90° bend sampled every 5 m, so several points are "turning".
	var points []gpx.Point
	latStep := 5.0 / 111_320.0
	lonStep := 5.0 / 71_000.0
	for i := 0; i < 40; i++ {
		points = append(points, gpx.Point{Lat: 50.0 + float64(i)*latStep, Lon: 3.0})
	}
	corner := points[len(points)-1]
	for i := 1; i <= 40; i++ {
		points = append(points, gpx.Point{Lat: corner.Lat, Lon: corner.Lon + float64(i)*lonStep})
	}

	turns := DeriveTurns(points, cumulativeDistances(points))
	if len(turns) > 1 {
		t.Errorf("one junction produced %d cues: %+v", len(turns), turns)
	}
}

func TestNormaliseDegrees(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{0, 0}, {90, 90}, {-90, -90}, {180, 180},
		{270, -90}, // a 270° right turn is a 90° left
		{-270, 90}, // and the reverse
		{450, 90},  // more than a full circle
	} {
		if got := normaliseDegrees(tc.in); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("normaliseDegrees(%.0f) = %.0f, want %.0f", tc.in, got, tc.want)
		}
	}
}

func TestBearingCardinalDirections(t *testing.T) {
	origin := gpx.Point{Lat: 50, Lon: 3}
	for name, tc := range map[string]struct {
		to   gpx.Point
		want float64
	}{
		"north": {gpx.Point{Lat: 50.01, Lon: 3}, 0},
		"east":  {gpx.Point{Lat: 50, Lon: 3.01}, 90},
		"south": {gpx.Point{Lat: 49.99, Lon: 3}, 180},
		"west":  {gpx.Point{Lat: 50, Lon: 2.99}, -90},
	} {
		got := bearing(origin, tc.to)
		if math.Abs(normaliseDegrees(got-tc.want)) > 1 {
			t.Errorf("%s: bearing = %.1f°, want %.0f°", name, got, tc.want)
		}
	}
}

// ---------- native turn cues ----------

// rightAngleTurn builds a track heading north for 400 m then due east for
// 400 m — the same corner TestRightAngleTurnIsDetected uses, so the derived
// cue and a native one can be compared directly.
func rightAngleTurn() []gpx.Point {
	var points []gpx.Point
	latStep := 20.0 / 111_320.0
	lonStep := 20.0 / 71_000.0
	for i := 0; i < 20; i++ {
		points = append(points, gpx.Point{Lat: 50.0 + float64(i)*latStep, Lon: 3.0})
	}
	corner := points[len(points)-1]
	for i := 1; i <= 20; i++ {
		points = append(points, gpx.Point{Lat: corner.Lat, Lon: corner.Lon + float64(i)*lonStep})
	}
	return points
}

func TestClassifyCueRecognisesCommonWording(t *testing.T) {
	cases := []struct {
		cue  gpx.Cue
		want typedef.CoursePoint
	}{
		{gpx.Cue{Sym: "Right"}, typedef.CoursePointRight},
		{gpx.Cue{Name: "Turn left onto Main St"}, typedef.CoursePointLeft},
		{gpx.Cue{Desc: "Sharp right bend"}, typedef.CoursePointSharpRight},
		{gpx.Cue{Cmt: "Sharp left"}, typedef.CoursePointSharpLeft},
		{gpx.Cue{Name: "Bear right"}, typedef.CoursePointSlightRight},
		{gpx.Cue{Name: "Slight left"}, typedef.CoursePointSlightLeft},
		{gpx.Cue{Name: "U-turn"}, typedef.CoursePointUTurn},
		{gpx.Cue{Name: "Turn around"}, typedef.CoursePointUTurn},
		{gpx.Cue{Name: "Keep left at the fork"}, typedef.CoursePointLeftFork},
		{gpx.Cue{Name: "Bear right at the fork"}, typedef.CoursePointRightFork},
		{gpx.Cue{Name: "Fork"}, typedef.CoursePointMiddleFork},
		{gpx.Cue{Name: "Continue straight"}, typedef.CoursePointStraight},
	}
	for _, tc := range cases {
		got, ok := classifyCue(tc.cue)
		if !ok {
			t.Errorf("%+v: not recognised as a turn", tc.cue)
			continue
		}
		if got != tc.want {
			t.Errorf("%+v: type = %v, want %v", tc.cue, got, tc.want)
		}
	}
}

func TestClassifyCueIgnoresNonTurnWaypoints(t *testing.T) {
	for _, cue := range []gpx.Cue{
		{Name: "Water stop"},
		{Name: "Photo point"},
		{Desc: "Café"},
		{},
	} {
		if _, ok := classifyCue(cue); ok {
			t.Errorf("%+v classified as a turn", cue)
		}
	}
}

func TestNativeTurnsSnapsToNearestPoint(t *testing.T) {
	points := rightAngleTurn()
	corner := points[19] // the point at the corner itself

	turns := NativeTurns(points, []gpx.Cue{
		{Lat: corner.Lat, Lon: corner.Lon, Name: "Turn right onto Market St", Sym: "Right"},
	})
	if len(turns) != 1 {
		t.Fatalf("got %d native turns, want 1: %+v", len(turns), turns)
	}
	if turns[0].Index != 19 {
		t.Errorf("index = %d, want 19 (the corner)", turns[0].Index)
	}
	if turns[0].Type != typedef.CoursePointRight {
		t.Errorf("type = %v, want Right", turns[0].Type)
	}
	// The planner's own wording survives verbatim — that is the entire
	// point of preferring it over a derived cue.
	if turns[0].Name != "Turn right onto Market St" {
		t.Errorf("name = %q, want the cue's own text", turns[0].Name)
	}
}

func TestNativeTurnsDropsCuesTooFarFromTheTrack(t *testing.T) {
	points := rightAngleTurn()
	far := gpx.Point{Lat: points[19].Lat + 1.0, Lon: points[19].Lon + 1.0} // ~100+ km away

	turns := NativeTurns(points, []gpx.Cue{{Lat: far.Lat, Lon: far.Lon, Sym: "Right"}})
	if turns != nil {
		t.Errorf("a cue far from the track should be dropped, got %+v", turns)
	}
}

func TestNativeTurnsDropsUnrecognisedCues(t *testing.T) {
	points := rightAngleTurn()
	corner := points[19]

	turns := NativeTurns(points, []gpx.Cue{{Lat: corner.Lat, Lon: corner.Lon, Name: "Water stop"}})
	if turns != nil {
		t.Errorf("a non-turn waypoint should be dropped, got %+v", turns)
	}
}

func TestNativeTurnsSortsByIndex(t *testing.T) {
	points := rightAngleTurn()
	first, second := points[5], points[25]

	turns := NativeTurns(points, []gpx.Cue{
		{Lat: second.Lat, Lon: second.Lon, Sym: "Right"}, // given out of order
		{Lat: first.Lat, Lon: first.Lon, Sym: "Left"},
	})
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2", len(turns))
	}
	if turns[0].Index >= turns[1].Index {
		t.Errorf("turns are not sorted by index: %+v", turns)
	}
}

func TestNativeTurnsEmptyWhenNoCuesClassify(t *testing.T) {
	points := rightAngleTurn()
	if got := NativeTurns(points, nil); got != nil {
		t.Errorf("expected nil with no cues at all, got %+v", got)
	}
	if got := NativeTurns(points, []gpx.Cue{{Lat: points[19].Lat, Lon: points[19].Lon, Name: "Photo point"}}); got != nil {
		t.Errorf("expected nil when nothing classifies, got %+v", got)
	}
}

func TestEncodePrefersNativeCuesOverDerived(t *testing.T) {
	points := rightAngleTurn()
	corner := points[19]

	raw, err := Encode(points, Options{
		Name:     "Corner",
		TurnCues: true,
		NativeCues: []gpx.Cue{
			{Lat: corner.Lat, Lon: corner.Lon, Name: "Turn right onto Market St", Sym: "Right"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	course := decode(t, raw)
	if len(course.CoursePoints) != 1 {
		t.Fatalf("got %d course points, want 1 (the native cue, not a derived one too)", len(course.CoursePoints))
	}
	if got := course.CoursePoints[0].Name; got != "Turn right onto Market St" {
		t.Errorf("course point name = %q, want the native cue's own wording", got)
	}
}

func TestEncodeFallsBackToDerivedWhenNativeCuesDoNotClassify(t *testing.T) {
	points := rightAngleTurn()

	raw, err := Encode(points, Options{
		Name:       "Corner",
		TurnCues:   true,
		NativeCues: []gpx.Cue{{Lat: points[0].Lat, Lon: points[0].Lon, Name: "Water stop"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	course := decode(t, raw)
	if len(course.CoursePoints) != 1 {
		t.Fatalf("got %d course points, want 1 (DeriveTurns's own corner cue)", len(course.CoursePoints))
	}
	if got := course.CoursePoints[0].Name; got != "Right" {
		t.Errorf("course point name = %q, want DeriveTurns's generic label", got)
	}
}

// ---------- climb cues ----------

type elevSegment struct{ LengthM, GradePercent float64 }

// elevationProfile builds a track heading due north at spacingM intervals,
// its elevation following a sequence of segments back to back from baseEle.
// Tests bracket the segment under test with flat margins well wider than
// climbSmoothRadiusM, so smoothing at a segment boundary can't bias the
// measurement taken from the middle of the interesting one.
func elevationProfile(spacingM, baseEle float64, segments ...elevSegment) []gpx.Point {
	latStep := spacingM / 111_320.0
	points := []gpx.Point{{Lat: 50.0, Lon: 3.0, Ele: baseEle, HasEle: true}}
	lat, ele := 50.0, baseEle
	for _, seg := range segments {
		rise := spacingM * seg.GradePercent / 100
		for d := 0.0; d < seg.LengthM; d += spacingM {
			lat += latStep
			ele += rise
			points = append(points, gpx.Point{Lat: lat, Lon: 3.0, Ele: ele, HasEle: true})
		}
	}
	return points
}

func TestFlatTrackProducesNoClimbs(t *testing.T) {
	points := straightNorth(80, 25)
	if got := DeriveClimbs(points, cumulativeDistances(points)); got != nil {
		t.Errorf("a flat track produced %d climbs: %+v", len(got), got)
	}
}

func TestNoClimbCuesByDefault(t *testing.T) {
	points := elevationProfile(25, 100, elevSegment{300, 0}, elevSegment{3000, 6}, elevSegment{300, 0})

	raw, err := Encode(points, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(decode(t, raw).CoursePoints); got != 0 {
		t.Errorf("got %d course points with ClimbCues off, want 0", got)
	}
}

func TestSteadyClimbIsCategorised(t *testing.T) {
	// 3000 m at 6% averages a Strava score of 18,000 — solidly inside the
	// category 3 band (16,000-32,000), clear of either edge so smoothing at
	// the climb's own boundaries can't tip it into a neighbouring category.
	points := elevationProfile(25, 100, elevSegment{300, 0}, elevSegment{3000, 6}, elevSegment{300, 0})

	climbs := DeriveClimbs(points, cumulativeDistances(points))
	if len(climbs) != 1 {
		t.Fatalf("got %d climbs, want 1: %+v", len(climbs), climbs)
	}

	c := climbs[0]
	if c.Category != typedef.CoursePointThirdCategory {
		t.Errorf("category = %v, want ThirdCategory", c.Category)
	}
	if c.Name == "" {
		t.Error("climb has no name; devices display this")
	}
	if math.Abs(c.LengthM-3000) > 200 {
		t.Errorf("length = %.0f, want ~3000", c.LengthM)
	}
	if math.Abs(c.AvgGradient-6) > 0.5 {
		t.Errorf("avg gradient = %.1f%%, want ~6%%", c.AvgGradient)
	}
	if c.SummitIndex <= c.StartIndex {
		t.Errorf("summit index %d must come after start index %d", c.SummitIndex, c.StartIndex)
	}
}

func TestShortClimbBelowLengthThresholdIsIgnored(t *testing.T) {
	// 300 m at 10% is steep, but shorter than climbMinLengthM and its score
	// (3,000) is well under even a category 4 climb.
	points := elevationProfile(25, 100, elevSegment{300, 0}, elevSegment{300, 10}, elevSegment{300, 0})
	if got := DeriveClimbs(points, cumulativeDistances(points)); got != nil {
		t.Errorf("a short climb produced %d climbs: %+v", len(got), got)
	}
}

func TestClimbBelowAvgGradientThresholdIsIgnored(t *testing.T) {
	// 5000 m at 2.9% is long enough, and its score (14,500) alone would
	// clear category 4 — but the average gradient sits just under
	// climbMinAvgGradient, which ClimbPro itself also won't show without.
	points := elevationProfile(25, 100, elevSegment{300, 0}, elevSegment{5000, 2.9}, elevSegment{300, 0})
	if got := DeriveClimbs(points, cumulativeDistances(points)); got != nil {
		t.Errorf("a shallow climb produced %d climbs: %+v", len(got), got)
	}
}

func TestClimbBelowScoreThresholdIsIgnored(t *testing.T) {
	// 1000 m at 5% clears both the length and gradient minimums on their
	// own, but its score (5,000) is still short of a category 4 climb.
	points := elevationProfile(25, 100, elevSegment{300, 0}, elevSegment{1000, 5}, elevSegment{300, 0})
	if got := DeriveClimbs(points, cumulativeDistances(points)); got != nil {
		t.Errorf("a climb below the score threshold produced %d climbs: %+v", len(got), got)
	}
}

func TestShortDipInsideAClimbIsMerged(t *testing.T) {
	// A short false-flat dip mid-climb — well inside climbMergeGapM — must
	// not split one climb into two, or hide it entirely: on its own, either
	// half is far too short to categorise.
	points := elevationProfile(25, 100,
		elevSegment{300, 0}, elevSegment{2000, 5}, elevSegment{150, -4}, elevSegment{2000, 5}, elevSegment{300, 0})

	climbs := DeriveClimbs(points, cumulativeDistances(points))
	if len(climbs) != 1 {
		t.Fatalf("got %d climbs, want 1 (the dip should merge): %+v", len(climbs), climbs)
	}
	// 2000+150+2000 m, net of the dip's own 4% loss over 150 m.
	if got := climbs[0].LengthM; math.Abs(got-4150) > 250 {
		t.Errorf("length = %.0f, want ~4150 (the dip should not have split it)", got)
	}
}

func TestLongDescentSplitsIntoTwoClimbs(t *testing.T) {
	// A 300 m descent is past climbMergeGapM, so this is genuinely two
	// climbs with a valley between them, not one climb with a dip.
	points := elevationProfile(25, 100,
		elevSegment{300, 0}, elevSegment{1500, 6}, elevSegment{300, -5}, elevSegment{1500, 6}, elevSegment{300, 0})

	climbs := DeriveClimbs(points, cumulativeDistances(points))
	if len(climbs) != 2 {
		t.Fatalf("got %d climbs, want 2 (the descent should have split them): %+v", len(climbs), climbs)
	}
	for i, c := range climbs {
		if math.Abs(c.LengthM-1500) > 200 {
			t.Errorf("climb %d length = %.0f, want ~1500", i, c.LengthM)
		}
	}
	if climbs[0].SummitIndex >= climbs[1].StartIndex {
		t.Error("the two climbs overlap; the descent between them should separate their indices")
	}
}

func TestDeriveClimbsRequiresElevation(t *testing.T) {
	points := elevationProfile(25, 100, elevSegment{300, 0}, elevSegment{3000, 6}, elevSegment{300, 0})
	points[len(points)/2].HasEle = false

	if got := DeriveClimbs(points, cumulativeDistances(points)); got != nil {
		t.Errorf("a track missing elevation on one point produced %d climbs: %+v", len(got), got)
	}
}

// DeriveClimbs indexes the distances it is handed, the same as DeriveTurns.
// A caller passing a nil or mismatched slice used to panic; it now returns
// nothing instead, and Climbs is the safe entry point.
func TestDeriveClimbsIsSafeWithBadDistances(t *testing.T) {
	points := elevationProfile(25, 100, elevSegment{300, 0}, elevSegment{3000, 6}, elevSegment{300, 0})

	for name, distances := range map[string][]float64{
		"nil":   nil,
		"short": {0, 1, 2},
		"long":  make([]float64, len(points)+5),
	} {
		if got := DeriveClimbs(points, distances); got != nil {
			t.Errorf("%s: expected no climbs, got %d", name, len(got))
		}
	}
}

func TestClimbsComputesItsOwnDistances(t *testing.T) {
	points := elevationProfile(25, 100, elevSegment{300, 0}, elevSegment{3000, 6}, elevSegment{300, 0})

	if got := len(Climbs(points)); got != 1 {
		t.Errorf("Climbs found %d climbs, want 1", got)
	}
	// And it must not panic on a track too short to have any.
	if got := Climbs(points[:2]); got != nil {
		t.Errorf("expected no climbs from 2 points, got %v", got)
	}
}

func TestClimbCuesReachTheFile(t *testing.T) {
	points := elevationProfile(25, 100, elevSegment{300, 0}, elevSegment{3000, 6}, elevSegment{300, 0})

	raw, err := Encode(points, Options{Name: "Climb", ClimbCues: true})
	if err != nil {
		t.Fatal(err)
	}

	course := decode(t, raw)
	if len(course.CoursePoints) != 2 {
		t.Fatalf("got %d course points, want 2 (category marker + summit)", len(course.CoursePoints))
	}

	start, summit := course.CoursePoints[0], course.CoursePoints[1]
	if start.Type != typedef.CoursePointThirdCategory {
		t.Errorf("start marker type = %v, want ThirdCategory", start.Type)
	}
	if start.Name == "" {
		t.Error("start marker has no name; devices display this")
	}
	if summit.Type != typedef.CoursePointSummit {
		t.Errorf("summit marker type = %v, want Summit", summit.Type)
	}
	if summit.Name != "Summit" {
		t.Errorf("summit marker name = %q, want %q", summit.Name, "Summit")
	}
	if summit.DistanceScaled() <= start.DistanceScaled() {
		t.Error("summit must sit further along the course than the climb's start")
	}
}

// ---------- independent format checks ----------

// The round-trip tests prove the library agrees with itself. These check the
// bytes against the FIT specification directly, so a bug in the library — or a
// wrong assumption about how to drive it — cannot pass unnoticed.
func TestFileHeaderMatchesTheSpec(t *testing.T) {
	raw, err := Encode(straightNorth(5, 100), Options{Name: "Header check"})
	if err != nil {
		t.Fatal(err)
	}

	if len(raw) < 14 {
		t.Fatalf("file is %d bytes; a header alone is 12 or 14", len(raw))
	}

	headerSize := int(raw[0])
	if headerSize != 12 && headerSize != 14 {
		t.Errorf("header size = %d, want 12 or 14", headerSize)
	}

	// Bytes 8..11 are the ASCII signature ".FIT".
	if got := string(raw[8:12]); got != ".FIT" {
		t.Errorf("signature = %q, want .FIT", got)
	}

	// Bytes 4..7 are the data size: everything after the header except the
	// trailing 2-byte CRC.
	dataSize := int(raw[4]) | int(raw[5])<<8 | int(raw[6])<<16 | int(raw[7])<<24
	if want := len(raw) - headerSize - 2; dataSize != want {
		t.Errorf("header data size = %d, want %d", dataSize, want)
	}
}

// crc16FIT is the CRC the FIT spec defines, implemented here from the spec's
// table so it is not the library's implementation checking its own output.
func crc16FIT(data []byte) uint16 {
	table := [16]uint16{
		0x0000, 0xCC01, 0xD801, 0x1400, 0xF001, 0x3C00, 0x2800, 0xE401,
		0xA001, 0x6C00, 0x7800, 0xB401, 0x5000, 0x9C01, 0x8801, 0x4400,
	}

	var crc uint16
	for _, b := range data {
		check := crc & 0xF
		crc >>= 4
		crc = crc ^ table[check] ^ table[b&0xF]

		check = crc & 0xF
		crc >>= 4
		crc = crc ^ table[check] ^ table[(b>>4)&0xF]
	}
	return crc
}

func TestTrailingCRCIsCorrect(t *testing.T) {
	raw, err := Encode(straightNorth(8, 75), Options{Name: "CRC check", TurnCues: true})
	if err != nil {
		t.Fatal(err)
	}

	body := raw[:len(raw)-2]
	want := crc16FIT(body)
	got := uint16(raw[len(raw)-2]) | uint16(raw[len(raw)-1])<<8

	if got != want {
		t.Errorf("trailing CRC = %#04x, computed %#04x — a device would reject this file", got, want)
	}
}

// DeriveTurns indexes the distances it is handed. A caller passing a nil or
// mismatched slice used to panic; it now returns nothing instead, and Turns is
// the safe entry point.
func TestDeriveTurnsIsSafeWithBadDistances(t *testing.T) {
	points := straightNorth(10, 30)

	for name, distances := range map[string][]float64{
		"nil":   nil,
		"short": {0, 1, 2},
		"long":  make([]float64, len(points)+5),
	} {
		if got := DeriveTurns(points, distances); got != nil {
			t.Errorf("%s: expected no turns, got %d", name, len(got))
		}
	}
}

func TestTurnsComputesItsOwnDistances(t *testing.T) {
	var points []gpx.Point
	latStep := 20.0 / 111_320.0
	lonStep := 20.0 / 71_000.0
	for i := 0; i < 20; i++ {
		points = append(points, gpx.Point{Lat: 50.0 + float64(i)*latStep, Lon: 3.0})
	}
	corner := points[len(points)-1]
	for i := 1; i <= 20; i++ {
		points = append(points, gpx.Point{Lat: corner.Lat, Lon: corner.Lon + float64(i)*lonStep})
	}

	if got := len(Turns(points)); got != 1 {
		t.Errorf("Turns found %d turns, want 1", got)
	}
	// And it must not panic on a track too short to have any.
	if got := Turns(points[:2]); got != nil {
		t.Errorf("expected no turns from 2 points, got %v", got)
	}
}

func TestSportFromString(t *testing.T) {
	cases := map[string]typedef.Sport{
		"running": typedef.SportRunning,
		"cycling": typedef.SportCycling,
		"":        typedef.SportCycling,
		"hiking":  typedef.SportCycling, // unrecognised falls back to cycling
	}
	for in, want := range cases {
		if got := SportFromString(in); got != want {
			t.Errorf("SportFromString(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestEncodeSetsTheCourseSport(t *testing.T) {
	points := []gpx.Point{{Lat: 50.0, Lon: 3.0}, {Lat: 50.001, Lon: 3.001}}

	raw, err := Encode(points, Options{Name: "Test", Sport: SportFromString("running")})
	if err != nil {
		t.Fatal(err)
	}
	course := decode(t, raw)
	if course.Course.Sport != typedef.SportRunning {
		t.Errorf("course sport = %v, want SportRunning", course.Course.Sport)
	}
}
