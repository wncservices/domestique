package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestAClimbIsCoveredAtExactlyEightyPercent(t *testing.T) {
	cases := []struct {
		longest, climb float64
		want           bool
	}{
		{400, 500, true},    // exactly 80 %
		{399.9, 500, false}, // just under
		{600, 500, true},
		{0, 500, false},
		{0, 0, true}, // a zero-length climb needs nothing
	}
	for _, c := range cases {
		if got := climbCovered(c.longest, c.climb); got != c.want {
			t.Errorf("climbCovered(%v, %v) = %v, want %v", c.longest, c.climb, got, c.want)
		}
	}
}

func TestCoverageMessages(t *testing.T) {
	mk := func(secs ...float64) []demandClimbDTO {
		out := make([]demandClimbDTO, len(secs))
		for i, s := range secs {
			out[i] = demandClimbDTO{Index: i, DurationSec: s}
		}
		return out
	}
	cases := []struct {
		name    string
		climbs  []demandClimbDTO
		longest float64
		zone    workout.Zone
		want    string
	}{
		{"none", nil, 600, workout.ZoneThreshold, ""},
		{"all covered", mk(300, 420), 600, workout.ZoneThreshold, "Your plan trains for your route's climbs."},
		{"the spec's example", mk(540, 600, 900), 360, workout.ZoneThreshold,
			"Your route has 3 climbs over 9 minutes; your plan's longest threshold effort is 6 minutes."},
		{"one climb, singular", mk(600), 360, workout.ZoneSweetSpot,
			"Your route has 1 climb over 10 minutes; your plan's longest sweet-spot effort is 6 minutes."},
		{"only the uncovered ones count", mk(200, 900, 1000), 600, workout.ZoneVO2Max,
			"Your route has 2 climbs over 15 minutes; your plan's longest VO2max effort is 10 minutes."},
		{"nothing planned", mk(600), 0, "",
			"Your route has 1 climb over 10 minutes; your plan has no sweet-spot or harder effort scheduled yet."},
	}
	for _, c := range cases {
		covered := 0
		for i := range c.climbs {
			c.climbs[i].Covered = climbCovered(c.longest, c.climbs[i].DurationSec)
			if c.climbs[i].Covered {
				covered++
			}
		}
		cov := coverageFor(c.climbs, c.longest, c.zone)
		if cov.Message != c.want {
			t.Errorf("%s: message = %q, want %q", c.name, cov.Message, c.want)
		}
		if cov.Uncovered != len(c.climbs)-covered {
			t.Errorf("%s: uncovered = %d, want %d", c.name, cov.Uncovered, len(c.climbs)-covered)
		}
	}
}
