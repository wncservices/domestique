package adapter

import (
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func felt(a workout.SessionAnalysis, feel int) workout.SessionAnalysis {
	a.Feel = feel
	return a
}

func stepDowns(changes []Change) []Change {
	var out []Change
	for _, c := range changes {
		if c.StepDown {
			out = append(out, c)
		}
	}
	return out
}

func TestAnAllOutNailedRideStepsTheNextSameZoneSessionDown(t *testing.T) {
	ws := []workout.Workout{
		structured("tue-threshold", "Threshold 3×12", "2026-03-17", workout.ZoneThreshold, 5),
		structured("thu-threshold", "Threshold 3×8", "2026-03-19", workout.ZoneThreshold, 4),
	}
	nailed := workout.SessionAnalysis{WorkoutID: "tue-threshold", Outcome: "nailed"}

	t.Run("effort 5", func(t *testing.T) {
		got := stepDowns(AdaptSessions(ws, nil, workout.RiderProfile{}, thursday,
			map[string]workout.SessionAnalysis{"tue-threshold": felt(nailed, 5)}, readiness.Assessment{}))
		if len(got) != 1 || got[0].WorkoutID != "thu-threshold" || got[0].Why.Rule != why.StruggleStepDown {
			t.Fatalf("changes = %+v, want the next threshold session stepped down", got)
		}
		if !strings.Contains(got[0].Reason, "Tuesday's threshold session felt all-out to you (5 of 5)") {
			t.Errorf("reason = %q, want it to say whose word it is", got[0].Reason)
		}
		in := inputsAs[why.StruggleStepDownInputs](t, got[0])
		if !in.FeltAllOut || in.SourceDate != "2026-03-17" {
			t.Errorf("inputs = %+v", in)
		}
	})

	for _, feel := range []int{0, 1, 2, 3, 4} {
		got := stepDowns(AdaptSessions(ws, nil, workout.RiderProfile{}, thursday,
			map[string]workout.SessionAnalysis{"tue-threshold": felt(nailed, feel)}, readiness.Assessment{}))
		if len(got) != 0 {
			t.Errorf("effort %d: changes = %+v, want none", feel, got)
		}
	}
}

func TestAnAllOutRideCountsTowardTheTwoStruggleSwap(t *testing.T) {
	ws := []workout.Workout{
		planned("mon", "Tempo ride", "2026-03-16"),
		planned("wed", "Interval session", "2026-03-18"),
		planned("today", "VO2max intervals", "2026-03-19"),
	}
	analyses := map[string]workout.SessionAnalysis{
		"mon": analysed("mon", "struggled", 70),
		"wed": felt(analysed("wed", "nailed", 75, onStep("hit"), onStep("hit")), 5),
	}
	got := AdaptSessions(ws, nil, profileAvailable("mon", "wed", "thu", "fri"), thursday, analyses, readiness.Assessment{})
	if len(got) != 1 || got[0].WorkoutID != "today" || !got[0].Downgrade || got[0].Why.Rule != why.FatigueStruggles {
		t.Fatalf("changes = %+v, want today's intervals swapped: one struggle plus one all-out ride", got)
	}
	if !strings.Contains(got[0].Reason, "Wednesday's interval session felt all-out to you (5 of 5)") {
		t.Errorf("reason = %q", got[0].Reason)
	}
	if strings.Contains(got[0].Reason, "under target") {
		t.Errorf("reason = %q: the power file said this ride went to plan", got[0].Reason)
	}
	in := inputsAs[why.FatigueStrugglesInputs](t, got[0])
	if len(in.Sessions) != 2 || !in.Sessions[0].FeltAllOut || in.Sessions[1].FeltAllOut {
		t.Errorf("sessions = %+v, want only the all-out ride flagged", in.Sessions)
	}

	// The same rides at effort 4 are not a pattern.
	analyses["wed"] = felt(analyses["wed"], 4)
	if got := AdaptSessions(ws, nil, profileAvailable("mon", "wed", "thu", "fri"), thursday, analyses, readiness.Assessment{}); len(got) != 0 {
		t.Errorf("effort 4: changes = %+v, want none", got)
	}
}

func TestAnAllOutRideThatWasNotPlannedChangesNothing(t *testing.T) {
	ws := []workout.Workout{
		structured("tue-threshold", "Threshold 3×12", "2026-03-17", workout.ZoneThreshold, 5),
		structured("thu-threshold", "Threshold 3×8", "2026-03-19", workout.ZoneThreshold, 4),
	}
	// An analysis with no planned workout is never keyed into the map the
	// adapter reads, but EffectiveOutcome must also refuse it on its own.
	a := workout.SessionAnalysis{Outcome: "nailed", Feel: 5}
	if a.EffectiveOutcome() != "nailed" {
		t.Fatal("an unplanned all-out ride became a struggle")
	}
	if got := AdaptSessions(ws, nil, workout.RiderProfile{}, thursday, nil, readiness.Assessment{}); len(got) != 0 {
		t.Errorf("changes = %+v", got)
	}
}

func TestTheSurveyStillYieldsOneChangePerWorkout(t *testing.T) {
	ws := []workout.Workout{
		structured("tue-threshold", "Threshold 3×12", "2026-03-17", workout.ZoneThreshold, 5),
		structured("today", "Threshold 3×8", "2026-03-19", workout.ZoneThreshold, 4),
	}
	analyses := map[string]workout.SessionAnalysis{
		"tue-threshold": felt(workout.SessionAnalysis{WorkoutID: "tue-threshold", Outcome: "nailed"}, 5),
	}
	caution := readiness.Assessment{Verdict: readiness.Caution, Reasons: []string{"HRV is unbalanced today"}}
	got := AdaptSessions(ws, nil, workout.RiderProfile{}, thursday, analyses, caution)
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c.WorkoutID] {
			t.Errorf("workout %s has two changes: %+v", c.WorkoutID, got)
		}
		seen[c.WorkoutID] = true
	}
	if len(got) != 1 || got[0].Why.Rule != why.ReadinessCaution {
		t.Errorf("changes = %+v, want readiness to have claimed today's session first", got)
	}
}
