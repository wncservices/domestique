package crewplan

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestCrewEaseRequestsCarryFlags(t *testing.T) {
	check := func(t *testing.T, d Diff, id string, before workout.Workout, rule why.Rule) {
		t.Helper()
		c, ok := ops(d)[id]
		if !ok || c.Update == nil || c.Update.Flags == nil || c.Update.Description == nil {
			t.Fatalf("%s: change %+v must carry Flags", id, c)
		}
		final := workout.Workout{GoalID: before.GoalID, Description: *c.Update.Description}
		if legacy := workout.LegacyFlags(final); !c.Update.Flags.Equivalent(legacy) {
			t.Fatalf("%s: flags %+v do not match the text's %+v", id, *c.Update.Flags, legacy)
		}
		rules := c.Update.Flags.AdaptedRules
		if len(rules) == 0 || rules[len(rules)-1] != rule {
			t.Errorf("%s: rules = %v, want last %s", id, rules, rule)
		}
	}

	fri1 := hard("fri-hard", fri)
	check(t, Preview(input(Ride{ID: "r", Date: sat, Seconds: 90 * 60}, fri1)), "ease:fri-hard", fri1, why.CrewRideEve)

	sunday := gen("sun-ss", "Sweet spot", sun, workout.ZoneSweetSpot, 2*3600)
	check(t, Preview(input(longRide, sunday)), "ease:sun-ss", sunday, why.CrewRideAfter)
}

func TestCrewShortenRequestsCarryFlags(t *testing.T) {
	in := input(longRide,
		gen("a", "Endurance ride", mon, workout.ZoneEndurance, 2*3600),
		gen("b", "Endurance ride", tue, workout.ZoneEndurance, 2*3600),
		gen("c", "Endurance ride", thu, workout.ZoneEndurance, 2*3600),
	)
	in.Now = time.Date(2026, 10, 5, 8, 0, 0, 0, now.Location())
	d := Preview(in)
	n := 0
	for _, c := range d.Changes {
		if c.Op != OpShorten {
			continue
		}
		n++
		final := workout.Workout{GoalID: in.Workouts[0].GoalID, Description: *c.Update.Description}
		if c.Update.Flags == nil || !c.Update.Flags.Equivalent(workout.LegacyFlags(final)) {
			t.Errorf("%s: flags %+v do not match the text", c.ID, c.Update.Flags)
		}
	}
	if n == 0 {
		t.Fatal("scenario produced no shortening")
	}
}

// An adapted session is never eased again, so there are no stored rules to
// extend; what an ease must carry over is the rest of the stored flags.
func TestCrewEaseKeepsTheStoredMove(t *testing.T) {
	w := hard("fri-hard", fri)
	w.Flags = workout.Flags{Origin: workout.OriginPlan, MovedFrom: "2026-10-01", MovedBy: workout.MovedByRider}
	c := ops(Preview(input(Ride{ID: "r", Date: sat, Seconds: 90 * 60}, w)))["ease:fri-hard"]
	if c.Update == nil || c.Update.Flags == nil {
		t.Fatalf("no ease: %+v", c)
	}
	f := c.Update.Flags
	if len(f.AdaptedRules) != 1 || f.AdaptedRules[0] != why.CrewRideEve || f.MovedFrom != "2026-10-01" || f.MovedBy != workout.MovedByRider {
		t.Errorf("flags = %+v", f)
	}
}
