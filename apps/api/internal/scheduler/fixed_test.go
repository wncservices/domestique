package scheduler

import (
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A base week, Monday 2026-10-05, 8 hours, four available days: Tuesday is the
// structured day, Thursday and Saturday are endurance and Sunday is the long ride.
func fixedWeek() (periodization.Week, workout.RiderProfile) {
	return periodization.Week{StartDate: "2026-10-05", Phase: periodization.PhaseBase, TargetHours: 8},
		workout.RiderProfile{AvailableDays: []string{"tue", "thu", "sat", "sun"}}
}

func build(t *testing.T, week periodization.Week, profile workout.RiderProfile, opts ...Option) []workout.CreateWorkoutRequest {
	t.Helper()
	out, err := WeekWorkouts(week, profile, nil, "wilant", "goal", model.SportCycling, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func hoursOf(reqs []workout.CreateWorkoutRequest) float64 {
	total := 0.0
	for _, r := range reqs {
		total += workout.PlannedSeconds(r.Steps) / 3600
	}
	return total
}

func datesOf(reqs []workout.CreateWorkoutRequest) map[string]string {
	out := map[string]string{}
	for _, r := range reqs {
		out[r.Date] = r.Name
	}
	return out
}

func TestWithoutAFixedRideTheWeekIsUnchanged(t *testing.T) {
	week, profile := fixedWeek()
	plain := build(t, week, profile)
	same := build(t, week, profile, WithFixed(nil))
	if len(plain) != len(same) || math.Abs(hoursOf(plain)-hoursOf(same)) > 1e-9 {
		t.Errorf("an empty WithFixed changed the week: %d vs %d sessions", len(plain), len(same))
	}
	if math.Abs(hoursOf(plain)-8) > 0.05 {
		t.Fatalf("the unadjusted week holds %v h, want 8", hoursOf(plain))
	}
}

func TestALongFixedRideRemovesTheLongSlotAndItsDayAndReducesTheVolume(t *testing.T) {
	week, profile := fixedWeek()
	out := build(t, week, profile, WithFixed([]Fixed{{Date: "2026-10-10", Seconds: 4 * 3600}})) // Saturday
	got := datesOf(out)
	if _, ok := got["2026-10-10"]; ok {
		t.Error("the ride's own day still holds a generated session")
	}
	for _, name := range got {
		if IsLongRideName(name) {
			t.Errorf("a generated long ride remains (%q): the crew ride is the week's long ride", name)
		}
	}
	if h := hoursOf(out); math.Abs(h-4) > 0.05 {
		t.Errorf("generated volume = %v h, want 8 - 4 = 4", h)
	}
}

func TestAnEnduranceFixedRideReplacesOneEnduranceSlotAndKeepsTheLongRide(t *testing.T) {
	week, profile := fixedWeek()
	out := build(t, week, profile, WithFixed([]Fixed{{Date: "2026-10-10", Seconds: 90 * 60}}))
	got := datesOf(out)
	if _, ok := got["2026-10-10"]; ok {
		t.Error("the ride's own day still holds a session")
	}
	if got["2026-10-11"] == "" || !IsLongRideName(got["2026-10-11"]) {
		t.Errorf("Sunday = %q, want the long ride kept", got["2026-10-11"])
	}
	if h := hoursOf(out); math.Abs(h-6.5) > 0.05 {
		t.Errorf("generated volume = %v h, want 8 - 1.5", h)
	}
}

func TestAnEnduranceFixedRideOnANonSlotDayStillDropsOneEnduranceSlot(t *testing.T) {
	week, profile := fixedWeek()
	plain := build(t, week, profile)
	out := build(t, week, profile, WithFixed([]Fixed{{Date: "2026-10-09", Seconds: 90 * 60}})) // Friday: no slot
	if len(out) != len(plain)-1 {
		t.Errorf("sessions = %d, want one fewer than %d", len(out), len(plain))
	}
}

func TestAShortFixedRideTakesItsDayAndReducesTheVolumeByItsHours(t *testing.T) {
	week, profile := fixedWeek()
	plain := build(t, week, profile)
	out := build(t, week, profile, WithFixed([]Fixed{{Date: "2026-10-10", Seconds: 30 * 60}}))
	if _, ok := datesOf(out)["2026-10-10"]; ok {
		t.Error("the ride's own day still holds a session")
	}
	if len(out) != len(plain)-1 {
		t.Errorf("sessions = %d, want only that day's slot gone (%d)", len(out), len(plain)-1)
	}
	if h := hoursOf(out); math.Abs(h-7.5) > 0.05 {
		t.Errorf("generated volume = %v h, want 7.5", h)
	}
}

func TestTheVolumeNeverFallsBelowHalfTheTarget(t *testing.T) {
	week, profile := fixedWeek()
	out := build(t, week, profile, WithFixed([]Fixed{{Date: "2026-10-10", Seconds: 8 * 3600}}))
	if h := hoursOf(out); h < 3.95 {
		t.Errorf("generated volume = %v h, want at least the 4 h floor", h)
	}
}

func TestARecoveryWeekKeepsItsOwnTarget(t *testing.T) {
	week, profile := fixedWeek()
	week.Recovery, week.TargetHours = true, 4
	plain := build(t, week, profile)
	out := build(t, week, profile, WithFixed([]Fixed{{Date: "2026-10-10", Seconds: 4 * 3600}}))
	if _, ok := datesOf(out)["2026-10-10"]; ok {
		t.Error("the ride's own day still holds a session in a recovery week")
	}
	// The day's session is gone, but the remaining ones are not squeezed further.
	perSession := hoursOf(plain) / float64(len(plain))
	if got := hoursOf(out) / float64(len(out)); got < perSession-0.01 {
		t.Errorf("sessions shrank to %v h each from %v: a recovery week is not reduced again", got, perSession)
	}
}

func TestAFixedRideOutsideTheWeekIsIgnored(t *testing.T) {
	week, profile := fixedWeek()
	plain := build(t, week, profile)
	out := build(t, week, profile, WithFixed([]Fixed{{Date: "2026-10-17", Seconds: 4 * 3600}}))
	if len(out) != len(plain) || math.Abs(hoursOf(out)-hoursOf(plain)) > 1e-9 {
		t.Error("a ride in another week changed this one")
	}
}
