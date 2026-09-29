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

	out, err := WeekWorkouts(week, profile, nil, "wilant", "goal-1", model.SportCycling)
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

	out, err := WeekWorkouts(week, profile, nil, "wilant", "goal-1", model.SportCycling)
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

	out, err := WeekWorkouts(week, profile, nil, "wilant", "goal-1", model.SportCycling)
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

// The whole point of normalizeWeights summing to 1: total scheduled duration
// across the week should land on the periodization week's own TargetHours,
// not drift from it — whatever mix of structured and endurance sessions the
// phase calls for.
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

			out, err := WeekWorkouts(week, profile, nil, "wilant", "goal-1", model.SportCycling)
			if err != nil {
				t.Fatal(err)
			}

			var total float64
			for _, w := range out {
				total += totalSeconds(w.Steps)
			}
			wantSeconds := week.TargetHours * 3600
			// A structured session's own length is a discrete rung, not a
			// continuously tunable share of the budget, so the week's total
			// can be off by however far the picked rung sits from its slot's
			// exact share — allow up to 10%, the same tolerance the brief
			// itself sets for this.
			if diff := math.Abs(total - wantSeconds); diff > wantSeconds*0.10 {
				t.Errorf("total scheduled seconds = %.1f, want %.1f (target hours = %v)", total, wantSeconds, week.TargetHours)
			}
		})
	}
}

// A recovery week is easy across every available day, whatever phase it
// falls in — no long day, no structured session of any zone.
func TestRecoveryWeekIsAllEasy(t *testing.T) {
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBuild, Recovery: true, TargetHours: 6}
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "thu", "sat", "sun"}}

	out, err := WeekWorkouts(week, profile, nil, "wilant", "goal-1", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range out {
		if w.Name != "Endurance ride" {
			t.Errorf("recovery week workout = %q, want an easy Endurance ride", w.Name)
		}
		if w.Zone != workout.ZoneEndurance {
			t.Errorf("recovery week workout zone = %q, want endurance", w.Zone)
		}
	}
}

// Base adds exactly one sweet-spot session on top of an otherwise-easy week
// (n >= 3); Build raises that to threshold + vo2max; Peak raises it again to
// vo2max + anaerobic.
func TestZoneMixRisesWithPhase(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "wed", "thu", "sat", "sun"}} // n=5

	base, err := WeekWorkouts(periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBase, TargetHours: 8}, profile, nil, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	if zones := structuredZones(base); len(zones) != 1 || zones[0] != workout.ZoneSweetSpot {
		t.Errorf("base week zones = %v, want exactly one sweet_spot session", zones)
	}

	build, err := WeekWorkouts(periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBuild, TargetHours: 8}, profile, nil, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	if zones := structuredZones(build); !hasZone(zones, workout.ZoneThreshold) || !hasZone(zones, workout.ZoneVO2Max) {
		t.Errorf("build week zones = %v, want threshold and vo2max", zones)
	}

	peak, err := WeekWorkouts(periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhasePeak, TargetHours: 8}, profile, nil, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	if zones := structuredZones(peak); !hasZone(zones, workout.ZoneVO2Max) || !hasZone(zones, workout.ZoneAnaerobic) {
		t.Errorf("peak week zones = %v, want vo2max and anaerobic", zones)
	}
	if hasZone(structuredZones(peak), workout.ZoneThreshold) {
		t.Errorf("peak week should not also carry a threshold session")
	}
}

// The taper phase schedules a single opener at level - 2, not a productive
// session — the only phase whose target level goes down instead of up.
func TestTaperOpenerIsTwoLevelsBelowCurrent(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "thu", "sat"}}
	levels := map[string]float64{"vo2max": 6.0}
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseTaper, TargetHours: 6}

	out, err := WeekWorkouts(week, profile, levels, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range out {
		if w.Zone != workout.ZoneVO2Max {
			continue
		}
		found = true
		if w.Level > 4.5 {
			t.Errorf("taper opener level = %v, want close to current (6.0) - 2 = 4.0", w.Level)
		}
	}
	if !found {
		t.Fatal("expected a vo2max opener in the taper week")
	}
}

// With no zone recorded for the rider yet, the productive-level picking
// rule falls back to progression.Initial's starting value for the profile's
// experience level — an intermediate rider (4.0) targets a rung nearer 4.5
// than a beginner (2.0) targeting 2.5.
func TestMissingLevelFallsBackToProgressionInitial(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "wed", "thu", "sat"}, ExperienceLevel: "advanced"}
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBuild, TargetHours: 10}

	out, err := WeekWorkouts(week, profile, nil, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range out {
		if w.Zone != workout.ZoneThreshold {
			continue
		}
		found = true
		// advanced starts at 6.0, productive target 6.5 — an unset level
		// must not silently target something near 1 or 0.
		if w.Level < 4 {
			t.Errorf("threshold level = %v, want something reasonably close to an advanced rider's 6.0 start", w.Level)
		}
	}
	if !found {
		t.Fatal("expected a threshold session")
	}
}

// A short week — two or fewer available days — never gets a structured
// session forced in, whatever the phase: only the long session and
// endurance. This is the design's own short-week carve-out, and the review
// focus item on it.
func TestShortWeekHasNoStructuredSession(t *testing.T) {
	for _, phase := range []periodization.Phase{periodization.PhaseBase, periodization.PhaseBuild, periodization.PhasePeak, periodization.PhaseTaper} {
		profile := workout.RiderProfile{AvailableDays: []string{"tue", "sat"}}
		week := periodization.Week{StartDate: "2026-01-05", Phase: phase, TargetHours: 4}

		out, err := WeekWorkouts(week, profile, nil, "w", "g", model.SportCycling)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range out {
			if w.Zone != "" && w.Zone != workout.ZoneEndurance {
				t.Errorf("%s: n=2 week produced a structured session %q/%q, want none", phase, w.Name, w.Zone)
			}
		}
	}
}

// If a structured rung's own length doesn't fit even at the ladder's
// easiest rung — a time budget too small for anything — the slot falls back
// to endurance instead of forcing a session in.
func TestStructuredSlotWithNoRungThatFitsFallsBackToEndurance(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "wed", "thu", "sat"}} // n=4, build => threshold+vo2max
	// A tiny week: even the easiest rung (threshold's 3x5/5, ~35 minutes incl.
	// warmup/cooldown) cannot fit a slot's own tiny share of one hour spread
	// across four days.
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBuild, TargetHours: 1}

	out, err := WeekWorkouts(week, profile, nil, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range out {
		if w.Zone == workout.ZoneThreshold || w.Zone == workout.ZoneVO2Max {
			t.Errorf("a 1-hour week across 4 days should not fit a structured rung, got %q/%q", w.Name, w.Zone)
		}
	}
}

// Every week, whatever the phase, has exactly one Long session — the
// rider's last available day of the week.
func TestLastAvailableDayIsAlwaysTheLongSession(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"wed", "fri", "sat"}}
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBase, TargetHours: 6}

	out, err := WeekWorkouts(week, profile, nil, "w", "g", model.SportRunning)
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

// No two structured (hard) sessions ever land on calendar-adjacent days —
// checked here end to end through WeekWorkouts (placeStructuredDays has its
// own more exhaustive table test below).
func TestNoTwoStructuredSessionsOnConsecutiveDays(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}}
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBuild, TargetHours: 12}

	out, err := WeekWorkouts(week, profile, nil, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	var structuredDates []string
	for _, w := range out {
		if w.Zone != "" && w.Zone != workout.ZoneEndurance {
			structuredDates = append(structuredDates, w.Date)
		}
	}
	for i := 0; i < len(structuredDates); i++ {
		for j := i + 1; j < len(structuredDates); j++ {
			d1, _ := time.Parse("2006-01-02", structuredDates[i])
			d2, _ := time.Parse("2006-01-02", structuredDates[j])
			diff := d2.Sub(d1).Hours() / 24
			if diff < 0 {
				diff = -diff
			}
			if diff == 1 {
				t.Errorf("structured sessions on consecutive days: %v and %v", structuredDates[i], structuredDates[j])
			}
		}
	}
}

func structuredZones(out []workout.CreateWorkoutRequest) []workout.Zone {
	var zones []workout.Zone
	for _, w := range out {
		if w.Zone != "" && w.Zone != workout.ZoneEndurance {
			zones = append(zones, w.Zone)
		}
	}
	return zones
}

func hasZone(zones []workout.Zone, z workout.Zone) bool {
	for _, zone := range zones {
		if zone == z {
			return true
		}
	}
	return false
}

// mainStepOf returns the step this package always places right before the
// (always-last) cooldown — the plain work step for an endurance/long
// session, or a structured rung's own top-level work step (itself a repeat
// block for a multi-rep rung, whose own Target fields are unset — see
// flatten for reading into one of those).
func mainStepOf(steps []workout.WorkoutStep) workout.WorkoutStep {
	return steps[len(steps)-2]
}

// Cycling targets power as a fraction of FTP when the profile states one —
// never open when a real threshold is on file. n=2 keeps every session
// endurance/long, so mainStepOf is always the plain (non-repeat) work step.
func TestCyclingTargetsPowerWhenFTPIsSet(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "sat"}, FTPWatts: 250}
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBase, TargetHours: 4}

	out, err := WeekWorkouts(week, profile, nil, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range out {
		main := mainStepOf(w.Steps)
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
// and a structured threshold session must reach a *higher* fraction of
// threshold speed than an easy/long day, the same "harder session, faster
// pace" relationship the old tempo/easy split checked, now expressed
// through the library's threshold zone instead.
func TestRunningTargetsPaceAsFractionOfThresholdSpeed(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "thu", "sat"}, ThresholdPaceSecPerKM: 240} // 4:00/km
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBuild, TargetHours: 4}

	out, err := WeekWorkouts(week, profile, nil, "w", "g", model.SportRunning)
	if err != nil {
		t.Fatal(err)
	}
	thresholdSpeed := 1000.0 / profile.ThresholdPaceSecPerKM

	var easyHigh, structuredHigh float64
	for _, w := range out {
		high := workEffortHigh(w.Steps)
		if high <= 0 {
			t.Fatalf("%s: no pace target found, want one", w.Name)
		}
		if w.Zone == workout.ZoneThreshold {
			structuredHigh = high
		} else if high > easyHigh {
			easyHigh = high
		}
	}
	if structuredHigh <= 0 || easyHigh <= 0 {
		t.Fatalf("expected both a structured threshold session and an easy/long one, got %+v", names(out))
	}
	if structuredHigh <= easyHigh {
		t.Errorf("threshold pace ceiling (%v) should exceed easy/long's (%v) — threshold is faster", structuredHigh, easyHigh)
	}
	if structuredHigh > thresholdSpeed*1.10 {
		t.Errorf("threshold pace ceiling (%v m/s) should not exceed threshold speed (%v m/s) by much", structuredHigh, thresholdSpeed)
	}
}

// workEffortHigh is the highest TargetHigh among a workout's actual work
// steps (active or interval intensity — never warmup, cooldown or the rest
// between reps), found by flattening any repeat block so a structured
// session's real work target is comparable to an endurance session's plain
// one.
func workEffortHigh(steps []workout.WorkoutStep) float64 {
	var high float64
	for _, s := range flatten(steps) {
		if s.Intensity != workout.IntensityActive && s.Intensity != workout.IntensityInterval {
			continue
		}
		if s.TargetHigh > high {
			high = s.TargetHigh
		}
	}
	return high
}

func names(out []workout.CreateWorkoutRequest) []string {
	n := make([]string, len(out))
	for i, w := range out {
		n[i] = w.Name
	}
	return n
}

// With no FTP, no threshold pace and no max HR on file, every session
// targets open — never a fabricated number a profile never actually stated.
func TestWithNoProfileNumbersEverySessionIsOpen(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "thu", "sat"}}
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhasePeak, TargetHours: 5}

	out, err := WeekWorkouts(week, profile, nil, "w", "g", model.SportCycling)
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

// A structured VO2max session is a repeat block, never a single continuous
// block the way an endurance/long session is — and its on-effort exceeds
// its own between-reps recovery, the same relationship the old fixed
// interval block checked, now produced by the library's vo2max ladder
// instead of a hand-built 3-on/2-off shape.
func TestStructuredSessionIsARepeatBlockWithOnExceedingRecovery(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "wed", "thu", "sat"}, MaxHR: 180} // n=4: peak => vo2max+anaerobic
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhasePeak, TargetHours: 8}

	out, err := WeekWorkouts(week, profile, nil, "w", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range out {
		if w.Zone != workout.ZoneVO2Max {
			continue
		}
		found = true
		main := mainStepOf(w.Steps)
		if main.Repeat < 2 {
			t.Fatalf("vo2max session repeat = %d, want a repeat block", main.Repeat)
		}
		if len(main.Steps) != 2 {
			t.Fatalf("repeat block steps = %d, want 2 (work/recovery)", len(main.Steps))
		}
		on, off := main.Steps[0], main.Steps[1]
		if on.TargetLow <= off.TargetHigh {
			t.Errorf("work effort (low %v) should exceed recovery's own ceiling (%v)", on.TargetLow, off.TargetHigh)
		}
	}
	if !found {
		t.Fatal("expected a vo2max session in a peak week with 4 available days")
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
	out, err := NextWorkouts(plan, profile, nil, "w", "goal-1", model.SportCycling, today)
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

	out, err := NextWorkouts(plan, profile, nil, "w", "goal-1", model.SportCycling, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("got %d workouts after the plan's last week, want 0", len(out))
	}
}

// placeStructuredDays' own table test: the hard constraint (no two
// structured slots calendar-adjacent) and the placement heuristic (spread
// away from each other) for every k this package ever actually asks for.
func TestPlaceStructuredDays(t *testing.T) {
	for _, tc := range []struct {
		name    string
		days    []string
		k       int
		wantLen int
	}{
		{"no structured sessions", []string{"tue", "thu", "sat"}, 0, 0},
		{"too few days for any structured session", []string{"tue", "sat"}, 1, 0},
		{"one session, three days", []string{"tue", "thu", "sat"}, 1, 1},
		{"one session, seven days", []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, 1, 1},
		{"two sessions, four days", []string{"tue", "wed", "thu", "sat"}, 2, 2},
		{"two sessions, seven days", []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			positions := placeStructuredDays(tc.days, tc.k)

			seen := map[int]bool{}
			for _, pos := range positions {
				if pos == len(tc.days)-1 {
					t.Errorf("placed a structured session on the long day (slot %d)", pos)
				}
				if seen[pos] {
					t.Errorf("placed two structured sessions on the same slot %d", pos)
				}
				seen[pos] = true
			}
			for i := 0; i < len(positions); i++ {
				for j := i + 1; j < len(positions); j++ {
					if abs(weekdayOffset(tc.days[positions[i]])-weekdayOffset(tc.days[positions[j]])) == 1 {
						t.Errorf("positions %v and %v are calendar-adjacent", positions[i], positions[j])
					}
				}
			}
			if len(positions) != tc.wantLen {
				t.Errorf("placed %d structured sessions, want %d", len(positions), tc.wantLen)
			}
		})
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func TestEnduranceZoneTargetUsesFrielZone2WhenThresholdHRKnown(t *testing.T) {
	cases := []struct {
		name              string
		sport             model.Sport
		profile           workout.RiderProfile
		wantLow, wantHigh float64
	}{
		{"cycling LTHR", model.SportCycling, workout.RiderProfile{ThresholdHR: 160}, 160 * 0.81, 160 * 0.89},
		{"running LTHR", model.SportRunning, workout.RiderProfile{ThresholdHR: 160}, 160 * 0.85, 160 * 0.89},
		{"LTHR wins over max HR", model.SportCycling, workout.RiderProfile{ThresholdHR: 160, MaxHR: 190}, 160 * 0.81, 160 * 0.89},
		{"max HR below LTHR is not a ceiling", model.SportCycling, workout.RiderProfile{ThresholdHR: 170, MaxHR: 165}, 170 * 0.81, 170 * 0.89},
	}
	for _, c := range cases {
		tt, low, high := enduranceZoneTarget(c.sport, c.profile)
		if tt != workout.TargetHeartRate || math.Abs(low-c.wantLow) > 1e-9 || math.Abs(high-c.wantHigh) > 1e-9 {
			t.Errorf("%s: got %v [%v, %v], want heart_rate [%v, %v]", c.name, tt, low, high, c.wantLow, c.wantHigh)
		}
	}
}

func TestEnduranceZoneTargetWithOnlyMaxHRIsUnchanged(t *testing.T) {
	tt, low, high := enduranceZoneTarget(model.SportCycling, workout.RiderProfile{MaxHR: 180})
	if tt != workout.TargetHeartRate || math.Abs(low-180*0.60) > 1e-9 || math.Abs(high-180*0.75) > 1e-9 {
		t.Errorf("got %v [%v, %v], want heart_rate 60-75%% of max HR", tt, low, high)
	}
	if tt, low, high := enduranceZoneTarget(model.SportCycling, workout.RiderProfile{FTPWatts: 200, ThresholdHR: 160}); tt != workout.TargetPower || math.Abs(low-110) > 1e-9 || math.Abs(high-150) > 1e-9 {
		t.Errorf("FTP still wins: got %v [%v, %v]", tt, low, high)
	}
}
