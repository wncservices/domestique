package crewplan

import (
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The week is Monday 2026-10-05 to Sunday 2026-10-11 and "now" is Wednesday
// morning, in a fixed non-UTC zone so no test depends on the machine's TZ.
var now = time.Date(2026, 10, 7, 9, 0, 0, 0, time.FixedZone("CEST", 2*3600))

const (
	mon = "2026-10-05"
	tue = "2026-10-06"
	wed = "2026-10-07"
	thu = "2026-10-08"
	fri = "2026-10-09"
	sat = "2026-10-10"
	sun = "2026-10-11"
)

func step(seconds float64) []workout.WorkoutStep {
	return []workout.WorkoutStep{{Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: seconds, Target: workout.TargetOpen}}
}

// gen is a plan-made, untouched session.
func gen(id, name, date string, zone workout.Zone, seconds float64) workout.Workout {
	return workout.Workout{
		ID: id, Rider: "wilant", Sport: model.SportCycling, Name: name, GoalID: "goal", Date: date,
		Zone: zone, Description: scheduler.GeneratedDescription, Steps: step(seconds),
	}
}

func hard(id, date string) workout.Workout {
	return gen(id, "Threshold", date, workout.ZoneThreshold, 5400)
}

func riderBuilt(id, date string) workout.Workout {
	return workout.Workout{ID: id, Rider: "wilant", Sport: model.SportCycling, Name: "My ride", Date: date, Description: "mine", Steps: step(3600)}
}

func input(ride Ride, ws ...workout.Workout) Input {
	return Input{Ride: ride, Workouts: ws, Now: now, WeekTarget: 8}
}

var longRide = Ride{ID: "ride-1", Date: sat, RouteName: "Hill Loop", Seconds: 4 * 3600}

func ops(d Diff) map[string]Change {
	out := map[string]Change{}
	for _, c := range d.Changes {
		out[c.ID] = c
	}
	return out
}

func TestJoiningAnEmptyWeekChangesNothing(t *testing.T) {
	d := Preview(input(longRide))
	if len(d.Changes) != 0 || len(d.LeftAlone) != 0 {
		t.Errorf("diff = %+v, want nothing", d)
	}
	if d.Warnings == nil {
		t.Error("warnings must be an empty list, not nil")
	}
}

func TestJoiningRemovesTheGeneratedSessionOnTheRidesDay(t *testing.T) {
	d := Preview(input(longRide, gen("on-day", "Endurance ride", sat, workout.ZoneEndurance, 5400)))
	c, ok := ops(d)["remove:on-day"]
	if !ok || c.Op != OpRemove || c.Reason == "" || c.Date != sat {
		t.Fatalf("changes = %+v, want remove:on-day with a reason", d.Changes)
	}
}

func TestALongRideRemovesTheWeeksGeneratedLongRide(t *testing.T) {
	d := Preview(input(longRide, gen("long", "Long ride", sun, workout.ZoneEndurance, 3*3600)))
	if _, ok := ops(d)["remove:long"]; !ok {
		t.Errorf("changes = %+v, want the week's generated long ride removed", d.Changes)
	}
}

func TestAnEnduranceRideReplacesOneEnduranceSlotAndKeepsTheLongRide(t *testing.T) {
	ride := Ride{ID: "r", Date: sat, Seconds: 90 * 60}
	d := Preview(input(ride,
		gen("long", "Long ride", sun, workout.ZoneEndurance, 3*3600),
		gen("e1", "Endurance ride", tue, workout.ZoneEndurance, 3600),
		gen("e2", "Endurance ride", thu, workout.ZoneEndurance, 3600),
	))
	got := ops(d)
	if _, ok := got["remove:long"]; ok {
		t.Error("an endurance crew ride must not remove the long ride")
	}
	removed := 0
	for id := range got {
		if strings.HasPrefix(id, "remove:e") {
			removed++
		}
	}
	if removed != 1 {
		t.Errorf("removed %d endurance sessions, want exactly one: %+v", removed, d.Changes)
	}
}

func TestAShortRideTakesItsDayAndNothingElseMoves(t *testing.T) {
	ride := Ride{ID: "r", Date: sat, Seconds: 30 * 60}
	d := Preview(input(ride,
		hard("fri-hard", fri),
		gen("long", "Long ride", sun, workout.ZoneEndurance, 3*3600),
		gen("on-day", "Endurance ride", sat, workout.ZoneEndurance, 3600),
	))
	if len(d.Changes) != 1 || d.Changes[0].ID != "remove:on-day" {
		t.Errorf("changes = %+v, want only the day's own session removed", d.Changes)
	}
}

func TestJoiningNeverRemovesARoutedSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		ride Ride
		w    workout.Workout
	}{
		{"same day", Ride{ID: "r", Date: sat, Seconds: 30 * 60}, gen("routed", "Endurance ride", sat, workout.ZoneEndurance, 3600)},
		{"long ride", longRide, gen("routed", "Long ride", thu, workout.ZoneEndurance, 3*3600)},
		{"endurance replacement", Ride{ID: "r", Date: sat, Seconds: 90 * 60}, gen("routed", "Endurance ride", thu, workout.ZoneEndurance, 3600)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.w.RouteSlug = "chosen-loop"
			d := Preview(input(tc.ride, tc.w))
			if _, ok := ops(d)["remove:routed"]; ok {
				t.Errorf("changes = %+v: a chosen route must keep its session", d.Changes)
			}
		})
	}

	routed := gen("routed", "Endurance ride", thu, workout.ZoneEndurance, 3600)
	routed.RouteSlug = "chosen-loop"
	d := Preview(input(Ride{ID: "r", Date: sat, Seconds: 90 * 60},
		gen("unrouted", "Endurance ride", wed, workout.ZoneEndurance, 3600), routed))
	if _, ok := ops(d)["remove:unrouted"]; !ok {
		t.Errorf("changes = %+v, want the unrouted endurance slot replaced", d.Changes)
	}
}

func TestARoutedSessionCanStillBeEasedBeforeACrewRide(t *testing.T) {
	w := hard("routed", fri)
	w.RouteSlug = "chosen-loop"
	d := Preview(input(longRide, w))
	if _, ok := ops(d)["ease:routed"]; !ok {
		t.Errorf("changes = %+v, want the routed session eased, not removed", d.Changes)
	}
}

func TestTheDayBeforeAHardSessionIsEasedForLongAndEnduranceRides(t *testing.T) {
	for _, secs := range []float64{4 * 3600, 90 * 60} {
		d := Preview(input(Ride{ID: "r", Date: sat, Seconds: secs}, hard("fri-hard", fri)))
		c, ok := ops(d)["ease:fri-hard"]
		if !ok || c.Update == nil || c.Update.Description == nil {
			t.Fatalf("%v s: changes = %+v, want the hard session eased", secs, d.Changes)
		}
		if !strings.Contains(*c.Update.Description, scheduler.AdjustedMarker) || !strings.Contains(c.Reason, "the day before your crew ride") {
			t.Errorf("ease = %q / %q, want the marker and the reason", *c.Update.Description, c.Reason)
		}
		if *c.Update.Zone != workout.ZoneEndurance {
			t.Errorf("eased zone = %q, want endurance", *c.Update.Zone)
		}
	}
}

func TestOnlyALongRideEasesTheDayAfterAndCapsItAtAnHour(t *testing.T) {
	sunday := gen("sun-ss", "Sweet spot", sun, workout.ZoneSweetSpot, 2*3600)
	d := Preview(input(longRide, sunday))
	c, ok := ops(d)["ease:sun-ss"]
	if !ok || c.Update == nil || c.Update.Steps == nil {
		t.Fatalf("changes = %+v, want the day after eased", d.Changes)
	}
	if got := workout.PlannedSeconds(*c.Update.Steps); got > 3600 {
		t.Errorf("eased day after = %v s, want at most an hour", got)
	}
	if !strings.Contains(c.Reason, "the day after your crew ride") {
		t.Errorf("reason = %q", c.Reason)
	}

	d = Preview(input(Ride{ID: "r", Date: sat, Seconds: 90 * 60}, sunday))
	if _, ok := ops(d)["ease:sun-ss"]; ok {
		t.Error("an endurance crew ride must not ease the day after")
	}
}

func TestAnEasyDayAfterThatIsAlreadyShortIsLeftAsItIs(t *testing.T) {
	d := Preview(input(longRide, gen("sun-e", "Endurance ride", sun, workout.ZoneEndurance, 3000)))
	if _, ok := ops(d)["ease:sun-e"]; ok {
		t.Error("an endurance session under an hour needs no easing")
	}
}

func TestNothingThePlanDidNotMakeIsEverChanged(t *testing.T) {
	adjusted := hard("fri-adjusted", fri)
	adjusted.Description += " " + scheduler.AdjustedMarker + " eased."
	built := riderBuilt("built-on-day", sat)
	ridden := gen("ridden", "Endurance ride", sat, workout.ZoneEndurance, 3600)
	in := input(longRide, adjusted, built, ridden)
	in.Ridden = map[string]bool{"ridden": true}
	d := Preview(in)
	if len(d.Changes) != 0 {
		t.Errorf("changes = %+v, want none: rider-built, adjusted and ridden sessions are left alone", d.Changes)
	}
	left := map[string]string{}
	for _, n := range d.LeftAlone {
		left[n.WorkoutID] = n.Reason
	}
	for _, id := range []string{"fri-adjusted", "built-on-day", "ridden"} {
		if left[id] == "" {
			t.Errorf("%s is not listed as left alone with a reason: %+v", id, d.LeftAlone)
		}
	}
}

func TestASwappedSessionIsLeftAlone(t *testing.T) {
	swapped := hard("swapped", fri)
	swapped.Description += " " + scheduler.SwappedMarker + " alternate."
	d := Preview(input(longRide, swapped))
	if len(d.Changes) != 0 || len(d.LeftAlone) != 1 {
		t.Errorf("diff = %+v, want the swapped session left alone", d)
	}
}

func TestAKeptSessionIsNotEasedAgain(t *testing.T) {
	kept := hard("fri-kept", fri)
	kept.Description += " " + KeptMarker
	if d := Preview(input(longRide, kept)); len(d.Changes) != 0 {
		t.Errorf("changes = %+v, want none for a session the rider chose to keep", d.Changes)
	}
}

func TestAPastSessionIsNeverChanged(t *testing.T) {
	ride := Ride{ID: "r", Date: wed, Seconds: 4 * 3600}
	d := Preview(input(ride, hard("tue-hard", tue)))
	if len(d.Changes) != 0 {
		t.Errorf("changes = %+v, want none: yesterday has gone", d.Changes)
	}
}

func TestShorteningFitsTheWeekToTheReducedTargetWithAFloor(t *testing.T) {
	// Target 8 h, a 4 h ride: generated work may be 4 h. Three generated 2 h
	// endurance sessions (6 h) must shrink by a third, none below 45 minutes.
	in := input(longRide,
		gen("a", "Endurance ride", mon, workout.ZoneEndurance, 2*3600),
		gen("b", "Endurance ride", tue, workout.ZoneEndurance, 2*3600),
		gen("c", "Endurance ride", thu, workout.ZoneEndurance, 2*3600),
	)
	in.Now = time.Date(2026, 10, 5, 8, 0, 0, 0, now.Location()) // Monday: nothing has gone
	d := Preview(in)
	total := 0.0
	n := 0
	for _, id := range []string{"a", "b", "c"} {
		c, ok := ops(d)["shorten:"+id]
		if !ok || c.Update == nil || c.Update.Steps == nil {
			t.Fatalf("changes = %+v, want %s shortened", d.Changes, id)
		}
		secs := workout.PlannedSeconds(*c.Update.Steps)
		if secs < 45*60 {
			t.Errorf("%s shortened to %v s, below the 45 minute floor", id, secs)
		}
		total += secs
		n++
	}
	if total/3600 > 4.01 || total/3600 < 3.9 {
		t.Errorf("shortened total = %v h, want about 4", total/3600)
	}
}

func TestShorteningNeverGoesBelowFortyFiveMinutes(t *testing.T) {
	in := input(longRide,
		gen("a", "Endurance ride", mon, workout.ZoneEndurance, 3000), // already 50 min
	)
	in.WeekTarget = 4 // target 4 h, ride 4 h: floor of half the target is 2 h
	in.Now = time.Date(2026, 10, 5, 8, 0, 0, 0, now.Location())
	d := Preview(in)
	if c, ok := ops(d)["shorten:a"]; ok && workout.PlannedSeconds(*c.Update.Steps) < 45*60 {
		t.Errorf("shortened below the floor: %+v", c)
	}
}

func TestAWeekThatAlreadyFitsIsNotShortened(t *testing.T) {
	in := input(longRide, gen("a", "Endurance ride", tue, workout.ZoneEndurance, 3600))
	d := Preview(in)
	for _, c := range d.Changes {
		if c.Op == OpShorten {
			t.Errorf("shortened a week that fits: %+v", c)
		}
	}
}

func TestAFiftyPercentFloorLimitsHowMuchTheRideReducesTheWeek(t *testing.T) {
	// Target 6 h and an 8 h ride: reduced target is the 3 h floor, not -2 h.
	ride := Ride{ID: "r", Date: sat, Seconds: 8 * 3600}
	in := input(ride,
		gen("a", "Endurance ride", mon, workout.ZoneEndurance, 2*3600),
		gen("b", "Endurance ride", tue, workout.ZoneEndurance, 2*3600),
		gen("c", "Endurance ride", thu, workout.ZoneEndurance, 2*3600),
	)
	in.WeekTarget = 6
	in.Now = time.Date(2026, 10, 5, 8, 0, 0, 0, now.Location())
	d := Preview(in)
	total := 0.0
	for _, id := range []string{"a", "b", "c"} {
		if c, ok := ops(d)["shorten:"+id]; ok {
			total += workout.PlannedSeconds(*c.Update.Steps) / 3600
		} else {
			total += 2
		}
	}
	if total < 2.99 {
		t.Errorf("generated work = %v h, want at least the 3 h floor (half of 6 h)", total)
	}
}

func TestARecoveryWeekKeepsItsOwnTargetAndTheRideWarns(t *testing.T) {
	in := input(longRide, gen("a", "Endurance ride", tue, workout.ZoneEndurance, 3600))
	in.WeekTarget, in.Recovery = 3, true
	d := Preview(in)
	for _, c := range d.Changes {
		if c.Op == OpShorten {
			t.Errorf("a recovery week must not be shortened further: %+v", c)
		}
	}
	if !hasWarning(d, "recovery week") {
		t.Errorf("warnings = %v, want one that the ride exceeds a recovery week", d.Warnings)
	}
}

func TestABlackoutDayWarnsButNeverBlocks(t *testing.T) {
	in := input(longRide)
	in.Blackout = map[string]bool{sat: true}
	d := Preview(in)
	if !hasWarning(d, "away") {
		t.Errorf("warnings = %v, want a life event warning", d.Warnings)
	}
}

func TestLeavingAddsWhatThePlanWouldHaveMadeOnTheFreedDay(t *testing.T) {
	refill := []workout.CreateWorkoutRequest{
		{Rider: "wilant", Name: "Long ride", Date: sat, GoalID: "goal", Description: scheduler.GeneratedDescription, Steps: step(3 * 3600)},
		{Rider: "wilant", Name: "Endurance ride", Date: fri, GoalID: "goal", Description: scheduler.GeneratedDescription, Steps: step(3600)},
	}
	row := workout.Workout{ID: "fixed", Rider: "wilant", Name: "Crew ride: Hill Loop", GoalID: "goal", Date: sat, CrewRideID: "ride-1", Steps: step(4 * 3600)}
	in := input(longRide, row)
	in.Leaving, in.Refill = true, refill
	d := Preview(in)
	if len(d.Changes) != 1 || d.Changes[0].Op != OpAdd || d.Changes[0].Create == nil || d.Changes[0].Create.Date != sat {
		t.Fatalf("changes = %+v, want one add on the freed Saturday", d.Changes)
	}
	if !hasWarning(d, "not restored") {
		t.Errorf("warnings = %v, want the not-restored note", d.Warnings)
	}
}

func TestLeavingAddsNothingOnADayThatHasPassedIsTakenOrIsABlackout(t *testing.T) {
	refill := []workout.CreateWorkoutRequest{{Rider: "wilant", Name: "Long ride", Date: sat, Description: scheduler.GeneratedDescription, Steps: step(3600)}}
	row := workout.Workout{ID: "fixed", Rider: "wilant", Name: "Crew ride: Hill Loop", Date: sat, CrewRideID: "ride-1", Steps: step(4 * 3600)}

	taken := input(longRide, row, riderBuilt("other", sat))
	taken.Leaving, taken.Refill = true, refill
	if d := Preview(taken); len(d.Changes) != 0 {
		t.Errorf("taken day: changes = %+v, want none", d.Changes)
	}

	away := input(longRide, row)
	away.Leaving, away.Refill, away.Blackout = true, refill, map[string]bool{sat: true}
	if d := Preview(away); len(d.Changes) != 0 {
		t.Errorf("blackout day: changes = %+v, want none", d.Changes)
	}

	past := input(Ride{ID: "r", Date: tue, Seconds: 3600}, workout.Workout{ID: "f", CrewRideID: "r", Date: tue, Steps: step(3600)})
	past.Leaving = true
	past.Refill = []workout.CreateWorkoutRequest{{Rider: "wilant", Name: "Endurance ride", Date: tue, Description: scheduler.GeneratedDescription, Steps: step(3600)}}
	if d := Preview(past); len(d.Changes) != 0 {
		t.Errorf("past day: changes = %+v, want none", d.Changes)
	}
}

func TestPreviewIsDeterministicAndHasStableIDs(t *testing.T) {
	ws := []workout.Workout{hard("fri-hard", fri), gen("long", "Long ride", sun, workout.ZoneEndurance, 3*3600)}
	a, b := Preview(input(longRide, ws...)), Preview(input(longRide, ws...))
	if len(a.Changes) != len(b.Changes) {
		t.Fatal("two previews of the same inputs differ")
	}
	for i := range a.Changes {
		if a.Changes[i].ID != b.Changes[i].ID || a.Changes[i].ID != a.Changes[i].Op+":"+a.Changes[i].WorkoutID {
			t.Errorf("change %d id = %q / %q", i, a.Changes[i].ID, b.Changes[i].ID)
		}
	}
}

func TestEasesFindsTheSessionsAroundEveryCrewRideRow(t *testing.T) {
	row := workout.Workout{ID: "fixed", Rider: "wilant", Name: "Crew ride: Hill Loop", GoalID: "goal", Date: sat, CrewRideID: "ride-1", Steps: step(4 * 3600)}
	ws := []workout.Workout{row, hard("fri-hard", fri), gen("sun-ss", "Sweet spot", sun, workout.ZoneSweetSpot, 2*3600)}
	got := Eases(ws, workout.RiderProfile{}, nil, "2026-10-07", "")
	if len(got) != 2 {
		t.Fatalf("eases = %+v, want the day before and the day after", got)
	}
	byID := ops(Diff{Changes: got})
	if !byID["ease:fri-hard"].Eve || !byID["ease:sun-ss"].After {
		t.Errorf("eases = %+v, want eve on Friday and after on Sunday", got)
	}
	// Restricted to another ride, nothing is found.
	if got := Eases(ws, workout.RiderProfile{}, nil, "2026-10-07", "other"); len(got) != 0 {
		t.Errorf("eases for another ride = %+v", got)
	}
}

func hasWarning(d Diff, sub string) bool {
	for _, w := range d.Warnings {
		if strings.Contains(strings.ToLower(w), sub) {
			return true
		}
	}
	return false
}
