package scheduler

import (
	"math"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func totalSeconds(steps []workout.WorkoutStep) float64 {
	total := 0.0
	for _, s := range steps {
		if s.Repeat > 1 {
			var repSeconds float64
			for _, sub := range s.Steps {
				repSeconds += sub.Seconds
			}
			total += repSeconds * float64(s.Repeat)
			continue
		}
		total += s.Seconds
	}
	return total
}

func TestWeekWorkoutsWithNoAvailableDaysProducesNothing(t *testing.T) {
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBase, TargetHours: 5}
	profile := workout.RiderProfile{}

	out, err := WeekWorkouts(week, profile, "wilant", "goal-1", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("got %d workouts with no available days, want 0", len(out))
	}
}

func TestWeekWorkoutsWithZeroTargetHoursProducesNothing(t *testing.T) {
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBase, TargetHours: 0}
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "thu", "sat", "sun"}}

	out, err := WeekWorkouts(week, profile, "wilant", "goal-1", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("got %d workouts with zero target hours, want 0", len(out))
	}
}

// One workout per available day, on the right calendar dates, and every
// field the caller needs to persist it is filled in — the shape a
// handler wiring this to workout.DB.CreateWorkout depends on.
func TestWeekWorkoutsOneWorkoutPerAvailableDayOnTheRightDates(t *testing.T) {
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBase, TargetHours: 8} // Monday
	profile := workout.RiderProfile{AvailableDays: []string{"sun", "tue", "thu"}}                       // deliberately out of order

	out, err := WeekWorkouts(week, profile, "wilant", "goal-1", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d workouts, want 3", len(out))
	}

	wantDates := []string{"2026-01-06", "2026-01-08", "2026-01-11"} // tue, thu, sun — calendar order
	for i, w := range out {
		if w.Rider != "wilant" {
			t.Errorf("workout %d: rider = %q", i, w.Rider)
		}
		if w.GoalID != "goal-1" {
			t.Errorf("workout %d: goalId = %q", i, w.GoalID)
		}
		if w.Sport != model.SportCycling {
			t.Errorf("workout %d: sport = %q", i, w.Sport)
		}
		if w.Date != wantDates[i] {
			t.Errorf("workout %d: date = %q, want %q", i, w.Date, wantDates[i])
		}
		if w.Name == "" || len(w.Steps) == 0 {
			t.Errorf("workout %d: incomplete %+v", i, w)
		}
	}
}

// The whole point of sessionShares summing to 1: total scheduled duration
// across the week should land on the periodization week's own TargetHours,
// not drift from it.
func TestWeekWorkoutsTotalDurationMatchesTargetHours(t *testing.T) {
	for _, tc := range []struct {
		name  string
		phase periodization.Phase
	}{
		{"base", periodization.PhaseBase},
		{"build", periodization.PhaseBuild},
		{"peak", periodization.PhasePeak},
		{"taper", periodization.PhaseTaper},
	} {
		t.Run(tc.name, func(t *testing.T) {
			week := periodization.Week{StartDate: "2026-01-05", Phase: tc.phase, TargetHours: 10}
			profile := workout.RiderProfile{AvailableDays: []string{"tue", "thu", "sat", "sun"}}

			out, err := WeekWorkouts(week, profile, "wilant", "goal-1", model.SportCycling)
			if err != nil {
				t.Fatal(err)
			}

			var total float64
			for _, w := range out {
				total += totalSeconds(w.Steps)
			}
			wantSeconds := week.TargetHours * 3600
			// An interval session rounds to a whole number of fixed-length
			// reps, so it can be off from its own share of the budget by up
			// to half a rep (150s) — tolerate that, not exact equality.
			const intervalRoundingTolerance = (intervalOnSeconds + intervalOffSeconds) / 2
			if diff := math.Abs(total - wantSeconds); diff > intervalRoundingTolerance {
				t.Errorf("total scheduled seconds = %.1f, want %.1f (target hours = %v)", total, wantSeconds, week.TargetHours)
			}
		})
	}
}

// A recovery week is easy across every available day, whatever phase it
// falls in — no long day, no tempo, no intervals.
func TestRecoveryWeekIsAllEasy(t *testing.T) {
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBuild, Recovery: true, TargetHours: 6}
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "thu", "sat", "sun"}}

	out, err := WeekWorkouts(week, profile, "wilant", "goal-1", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range out {
		if w.Name != "Endurance ride" {
			t.Errorf("recovery week workout = %q, want an easy Endurance ride", w.Name)
		}
	}
}

// Build adds exactly one Tempo session on top of Base's shape; Peak
// replaces that slot with an Interval session instead.
func TestBuildAddsTempoAndPeakAddsIntervals(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "thu", "sat", "sun"}}

	base, err := WeekWorkouts(periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBase, TargetHours: 8}, profile, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range base {
		if w.Name == "Tempo ride" || w.Name == "VO2max intervals" {
			t.Errorf("base week should have no hard session, got %q", w.Name)
		}
	}

	build, err := WeekWorkouts(periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBuild, TargetHours: 8}, profile, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSession(build, "Tempo ride") {
		t.Errorf("build week = %+v, want exactly one Tempo ride", names(build))
	}
	if hasSession(build, "VO2max intervals") {
		t.Errorf("build week should have no intervals yet")
	}

	peak, err := WeekWorkouts(periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhasePeak, TargetHours: 8}, profile, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSession(peak, "VO2max intervals") {
		t.Errorf("peak week = %+v, want exactly one VO2max session", names(peak))
	}
	if hasSession(peak, "Tempo ride") {
		t.Errorf("peak week should not also carry a separate tempo session")
	}
}

// Every week, whatever the phase, has exactly one Long session — the
// rider's last available day of the week.
func TestLastAvailableDayIsAlwaysTheLongSession(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"wed", "fri", "sat"}}
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBase, TargetHours: 6}

	out, err := WeekWorkouts(week, profile, "w", "g", model.SportRunning)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d workouts, want 3", len(out))
	}
	last := out[len(out)-1]
	if last.Name != "Long run" {
		t.Errorf("last day = %q, want Long run", last.Name)
	}
	if last.Date != "2026-01-10" { // the Saturday
		t.Errorf("last day date = %q, want the Saturday", last.Date)
	}
}

func hasSession(out []workout.CreateWorkoutRequest, name string) bool {
	for _, w := range out {
		if w.Name == name {
			return true
		}
	}
	return false
}

func names(out []workout.CreateWorkoutRequest) []string {
	n := make([]string, len(out))
	for i, w := range out {
		n[i] = w.Name
	}
	return n
}

// Cycling targets power as a fraction of FTP when the profile states one —
// never open when a real threshold is on file.
func TestCyclingTargetsPowerWhenFTPIsSet(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "sat"}, FTPWatts: 250}
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBase, TargetHours: 4}

	out, err := WeekWorkouts(week, profile, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range out {
		main := w.Steps[1]
		if main.Target != workout.TargetPower {
			t.Fatalf("main step target = %q, want power", main.Target)
		}
		if main.TargetLow <= 0 || main.TargetHigh <= main.TargetLow {
			t.Errorf("power range = [%v, %v], want a real ascending range", main.TargetLow, main.TargetHigh)
		}
		if main.TargetHigh > profile.FTPWatts {
			t.Errorf("an easy/long day's target high (%v) should stay under FTP (%v)", main.TargetHigh, profile.FTPWatts)
		}
	}
}

// Running targets pace (metres/second) as a fraction of threshold speed —
// and a slower zone (Easy/Long) must be a *smaller* fraction of threshold
// speed than Tempo, the opposite direction from the power case, which is
// exactly the subtlety paceZone's own comment calls out.
func TestRunningTargetsPaceAsFractionOfThresholdSpeed(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "thu", "sat"}, ThresholdPaceSecPerKM: 240} // 4:00/km
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBuild, TargetHours: 4}

	out, err := WeekWorkouts(week, profile, "w", "g", model.SportRunning)
	if err != nil {
		t.Fatal(err)
	}
	thresholdSpeed := 1000.0 / profile.ThresholdPaceSecPerKM

	var easyHigh, tempoHigh float64
	for _, w := range out {
		main := w.Steps[1]
		if main.Target != workout.TargetPace {
			t.Fatalf("%s: target = %q, want pace", w.Name, main.Target)
		}
		switch w.Name {
		case "Tempo run":
			tempoHigh = main.TargetHigh
		case "Easy run", "Long run":
			if main.TargetHigh > easyHigh {
				easyHigh = main.TargetHigh
			}
		}
	}
	if tempoHigh <= 0 || easyHigh <= 0 {
		t.Fatalf("expected both a tempo and an easy/long session, got %+v", names(out))
	}
	if tempoHigh <= easyHigh {
		t.Errorf("tempo pace ceiling (%v) should exceed easy/long's (%v) — tempo is faster", tempoHigh, easyHigh)
	}
	if tempoHigh > thresholdSpeed*1.01 {
		t.Errorf("tempo pace ceiling (%v m/s) should not exceed threshold speed (%v m/s) by more than rounding", tempoHigh, thresholdSpeed)
	}
}

// With no FTP, no threshold pace and no max HR on file, every session
// targets open — never a fabricated number a profile never actually stated.
func TestWithNoProfileNumbersEverySessionIsOpen(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "thu", "sat"}}
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhasePeak, TargetHours: 5}

	out, err := WeekWorkouts(week, profile, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range out {
		for _, step := range flatten(w.Steps) {
			if step.Target != workout.TargetOpen {
				t.Errorf("%s / %s: target = %q, want open with no profile numbers set", w.Name, step.Name, step.Target)
			}
		}
	}
}

func flatten(steps []workout.WorkoutStep) []workout.WorkoutStep {
	var out []workout.WorkoutStep
	for _, s := range steps {
		if s.Repeat > 1 {
			out = append(out, flatten(s.Steps)...)
			continue
		}
		out = append(out, s)
	}
	return out
}

// An interval session is a repeat block of at least the floor rep count,
// never a single continuous block the way Easy/Long/Tempo sessions are.
func TestIntervalSessionIsARepeatBlockWithAFloorRepCount(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "thu", "sat"}, MaxHR: 180}
	// A short peak week: total hours divided among 3 days leaves little for
	// the interval slot, which is exactly what should trigger the floor.
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhasePeak, TargetHours: 1.5}

	out, err := WeekWorkouts(week, profile, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range out {
		if w.Name != "VO2max intervals" {
			continue
		}
		found = true
		main := w.Steps[1]
		if main.Repeat < minIntervalReps {
			t.Errorf("interval reps = %d, want at least %d", main.Repeat, minIntervalReps)
		}
		if len(main.Steps) != 2 {
			t.Fatalf("interval block steps = %d, want 2 (on/recovery)", len(main.Steps))
		}
		on, off := main.Steps[0], main.Steps[1]
		if on.TargetLow <= off.TargetHigh {
			t.Errorf("interval on-effort (low %v) should exceed recovery's own ceiling (%v)", on.TargetLow, off.TargetHigh)
		}
	}
	if !found {
		t.Fatal("expected a VO2max intervals session in a peak week with 3+ days")
	}
}

func TestNextWorkoutsPicksThePlanWeekContainingToday(t *testing.T) {
	plan := periodization.Plan{
		GoalID: "goal-1",
		Weeks: []periodization.Week{
			{Number: 1, StartDate: "2026-01-05", Phase: periodization.PhaseBase, TargetHours: 4},
			{Number: 2, StartDate: "2026-01-12", Phase: periodization.PhaseBuild, TargetHours: 6},
		},
	}
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "sat"}}

	// A Wednesday inside week 2's span.
	today := time.Date(2026, 1, 14, 0, 0, 0, 0, time.UTC)
	out, err := NextWorkouts(plan, profile, "w", "goal-1", model.SportCycling, today)
	if err != nil {
		t.Fatal(err)
	}
	var total float64
	for _, w := range out {
		total += totalSeconds(w.Steps)
	}
	if diff := math.Abs(total - 6*3600); diff > 1 {
		t.Errorf("total seconds = %v, want week 2's 6 hours (21600s)", total)
	}
}

func TestNextWorkoutsAfterThePlanEndsProducesNothing(t *testing.T) {
	plan := periodization.Plan{
		GoalID: "goal-1",
		Weeks: []periodization.Week{
			{Number: 1, StartDate: "2026-01-05", Phase: periodization.PhaseTaper, TargetHours: 3},
		},
	}
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "sat"}}
	today := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	out, err := NextWorkouts(plan, profile, "w", "goal-1", model.SportCycling, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("got %d workouts after the plan's last week, want 0", len(out))
	}
}
