package fitcourse

import (
	"math"
	"testing"
	"time"

	"github.com/muktihari/fit/profile/typedef"
)

var fixedStamp = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func TestCoursePointsAreWrittenAtTheNearestTrackPointWithTheirNameAndType(t *testing.T) {
	points := elevationProfile(25, 100, elevSegment{300, 0}, elevSegment{3000, 6}, elevSegment{300, 0})
	dist := cumulativeDistances(points)

	want := []CoursePoint{
		{DistanceM: 1000, Name: "C1 250-265W", Type: typedef.CoursePointThirdCategory},
		{DistanceM: 3300, Name: "Top C1", Type: typedef.CoursePointSummit},
	}
	raw, err := Encode(points, Options{Name: "Pacing", CoursePoints: want, CreatedAt: fixedStamp})
	if err != nil {
		t.Fatal(err)
	}
	course := decode(t, raw)
	if len(course.CoursePoints) != 2 {
		t.Fatalf("got %d course points, want 2", len(course.CoursePoints))
	}
	for i, cp := range course.CoursePoints {
		if cp.Name != want[i].Name || cp.Type != want[i].Type {
			t.Errorf("point %d = %q / %v, want %q / %v", i, cp.Name, cp.Type, want[i].Name, want[i].Type)
		}
		// At the nearest track point: within half the 25 m spacing of where it
		// was asked for, with that point's own position and distance.
		if d := cp.DistanceScaled(); math.Abs(d-want[i].DistanceM) > 13 {
			t.Errorf("point %d is at %.1f m, want %.0f within a point spacing", i, d, want[i].DistanceM)
		}
		idx := nearestIndex(dist, cp.DistanceScaled())
		if math.Abs(cp.PositionLatDegrees()-points[idx].Lat) > 1e-6 || math.Abs(cp.PositionLongDegrees()-points[idx].Lon) > 1e-6 {
			t.Errorf("point %d is not at track point %d", i, idx)
		}
		if !cp.Timestamp.Equal(fixedStamp.Add(time.Duration(idx) * time.Second)) {
			t.Errorf("point %d timestamp %v does not follow the track's timeline", i, cp.Timestamp)
		}
	}
}

func TestCoursePointsAddToTurnAndClimbCuesWithoutChangingThem(t *testing.T) {
	points := elevationProfile(25, 100, elevSegment{300, 0}, elevSegment{3000, 6}, elevSegment{300, 0})

	base, err := Encode(points, Options{Name: "Climb", ClimbCues: true, CreatedAt: fixedStamp})
	if err != nil {
		t.Fatal(err)
	}
	with, err := Encode(points, Options{
		Name: "Climb", ClimbCues: true, CreatedAt: fixedStamp,
		CoursePoints: []CoursePoint{{DistanceM: 500, Name: "Extra", Type: typedef.CoursePointGeneric}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, w := decode(t, base).CoursePoints, decode(t, with).CoursePoints
	if len(b) != 2 || len(w) != 3 {
		t.Fatalf("cue counts %d and %d, want 2 and 3", len(b), len(w))
	}
	// The writer orders points along the track, so look them up by name.
	byName := map[string]int{}
	for i := range w {
		byName[w[i].Name] = i
	}
	for _, c := range b {
		j, ok := byName[c.Name]
		if !ok || c.Type != w[j].Type || c.DistanceScaled() != w[j].DistanceScaled() {
			t.Errorf("the climb cue %q moved or vanished", c.Name)
		}
	}
	if j, ok := byName["Extra"]; !ok || w[j].Type != typedef.CoursePointGeneric {
		t.Error("the added point is missing")
	}

	// And with none given, Encode is byte-for-byte what it was.
	again, _ := Encode(points, Options{Name: "Climb", ClimbCues: true, CreatedAt: fixedStamp, CoursePoints: []CoursePoint{}})
	if string(again) != string(base) {
		t.Error("an empty CoursePoints changed the file")
	}
}

func TestCoursePointsAreWrittenInDistanceOrderAndClamped(t *testing.T) {
	points := straightNorth(80, 25) // about 1975 m
	raw, err := Encode(points, Options{CreatedAt: fixedStamp, CoursePoints: []CoursePoint{
		{DistanceM: 1500, Name: "B", Type: typedef.CoursePointGeneric},
		{DistanceM: -50, Name: "A", Type: typedef.CoursePointGeneric},
		{DistanceM: 99_999, Name: "C", Type: typedef.CoursePointGeneric},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cps := decode(t, raw).CoursePoints
	if len(cps) != 3 || cps[0].Name != "A" || cps[1].Name != "B" || cps[2].Name != "C" {
		t.Fatalf("order = %v", cps)
	}
	if cps[0].DistanceScaled() != 0 {
		t.Errorf("a point before the start is at %.1f, want 0", cps[0].DistanceScaled())
	}
	if last := cumulativeDistances(points); math.Abs(cps[2].DistanceScaled()-last[len(last)-1]) > 0.5 {
		t.Errorf("a point past the end is at %.1f, want the end", cps[2].DistanceScaled())
	}
}
