package fitcourse

import (
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
)

// pinTracks are the synthetic profiles DeriveClimbs's output is pinned on.
// Device cues must not move when climb detection is shared with the training
// features, so these record what the algorithm returned before it moved.
func pinTracks() map[string][]gpx.Point {
	return map[string][]gpx.Point{
		"two-climbs-and-a-dip": elevationProfile(25, 100,
			elevSegment{300, 0}, elevSegment{2000, 5}, elevSegment{150, -4}, elevSegment{2000, 5},
			elevSegment{600, -6}, elevSegment{1500, 7}, elevSegment{400, 0}),
		"rolling": elevationProfile(20, 40,
			elevSegment{500, 2}, elevSegment{800, 4}, elevSegment{300, -3}, elevSegment{1200, 3.5},
			elevSegment{700, 0}, elevSegment{2500, 8}, elevSegment{500, -8}),
		"gentle-and-short": elevationProfile(25, 200,
			elevSegment{400, 0}, elevSegment{5000, 2.9}, elevSegment{300, 0}, elevSegment{450, 9},
			elevSegment{300, 0}, elevSegment{1000, 5}, elevSegment{300, 0}),
	}
}

type pinned struct {
	StartIndex, SummitIndex int
	Name                    string
	LengthM, GainM, AvgGrad float64
}

func TestDeriveClimbsOutputIsPinned(t *testing.T) {
	want := map[string][]pinned{
		"two-climbs-and-a-dip": {
			{11, 178, "Cat 3 climb", 4170.309187337926, 192.10000000000002, 4.60637308579571},
			{202, 264, "Cat 4 climb", 1548.2585006890085, 103.05000000000001, 6.655865280516175},
		},
		"rolling": {
			{173, 300, "Cat 3 climb", 2537.1461882656704, 198.08000000000004, 7.807196956806125},
		},
		"gentle-and-short": nil,
	}
	for name, pts := range pinTracks() {
		got := DeriveClimbs(pts, cumulativeDistances(pts))
		if len(got) != len(want[name]) {
			t.Errorf("%s: %d climbs, want %d: %+v", name, len(got), len(want[name]), got)
			continue
		}
		for i, c := range got {
			w := want[name][i]
			if c.StartIndex != w.StartIndex || c.SummitIndex != w.SummitIndex || c.Name != w.Name ||
				math.Abs(c.LengthM-w.LengthM) > 1e-6 || math.Abs(c.GainM-w.GainM) > 1e-6 ||
				math.Abs(c.AvgGradient-w.AvgGrad) > 1e-6 {
				t.Errorf("%s climb %d = %+v, want %+v", name, i, c, w)
			}
		}
	}
}
