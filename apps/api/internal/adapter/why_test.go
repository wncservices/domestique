package adapter

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// inputsAs reads a Change's recorded inputs back as the typed struct its rule
// documents, so a test compares fields and not the shape of a map.
func inputsAs[T any](t *testing.T, c Change) T {
	t.Helper()
	var out T
	raw, err := json.Marshal(c.Why.Inputs)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	if err != nil {
		t.Fatalf("inputs %v do not decode: %v", c.Why.Inputs, err)
	}
	return out
}

func TestAMissedMoveIntoAFreeDayCarriesItsRule(t *testing.T) {
	ws := []workout.Workout{
		planned("tue", "VO2max intervals", "2026-03-17"),
		planned("sat", "Long ride", "2026-03-21"),
	}
	got := AdaptSessions(ws, nil, profileAvailable("tue", "fri", "sat"), thursday, nil, readiness.Assessment{})
	if len(got) != 1 || got[0].Why.Rule != why.MissedMoved {
		t.Fatalf("changes = %+v, want one missed_moved", got)
	}
	in := inputsAs[why.MissedMovedInputs](t, got[0])
	if in != (why.MissedMovedInputs{From: "2026-03-17", To: "2026-03-20"}) {
		t.Errorf("inputs = %+v", in)
	}
}

func TestAMissedMoveOntoAnEasyDayRecordsTheReplacement(t *testing.T) {
	ws := []workout.Workout{
		planned("tue", "Tempo ride", "2026-03-17"),
		planned("fri", "Endurance ride", "2026-03-20"),
		planned("sat", "Endurance ride", "2026-03-21"),
		planned("sun", "Long ride", "2026-03-22"),
	}
	got := AdaptSessions(ws, nil, profileAvailable("tue", "fri", "sat", "sun"), thursday, nil, readiness.Assessment{})
	if len(got) != 1 || got[0].Why.Rule != why.MissedMoved {
		t.Fatalf("changes = %+v", got)
	}
	in := inputsAs[why.MissedMovedInputs](t, got[0])
	if !in.ReplacedEasy || in.From != "2026-03-17" || in.To != "2026-03-20" {
		t.Errorf("inputs = %+v, want the easy day recorded as given up", in)
	}
}

func TestAFatigueSwapForTwoStruggledSessionsRecordsBoth(t *testing.T) {
	ws := []workout.Workout{
		structured("mon", "Tempo ride", "2026-03-16", workout.ZoneTempo, 3),
		structured("wed", "Interval session", "2026-03-18", workout.ZoneVO2Max, 4),
		planned("today", "VO2max intervals", "2026-03-19"),
	}
	analyses := map[string]workout.SessionAnalysis{
		"mon": analysed("mon", "struggled", 70),
		"wed": analysed("wed", "struggled", 75,
			onStep("under"), recoveryStep("hit"), onStep("hit"), recoveryStep("hit"),
			onStep("under"), recoveryStep("hit"), onStep("hit"), recoveryStep("hit")),
	}
	got := AdaptSessions(ws, nil, profileAvailable("mon", "wed", "thu", "fri"), thursday, analyses, readiness.Assessment{})
	if len(got) != 1 || got[0].Why.Rule != why.FatigueStruggles || !got[0].Downgrade {
		t.Fatalf("changes = %+v, want one fatigue_struggles downgrade", got)
	}
	in := inputsAs[why.FatigueStrugglesInputs](t, got[0])
	want := []why.StruggledSession{
		{Date: "2026-03-18", Zone: "vo2max", Outcome: "struggled", HardHit: 2, HardTotal: 4},
		{Date: "2026-03-16", Zone: "tempo", Outcome: "struggled"},
	}
	if !reflect.DeepEqual(in.Sessions, want) {
		t.Errorf("sessions = %+v, want %+v", in.Sessions, want)
	}
}

func TestAnOverloadedWeekRecordsTheLoads(t *testing.T) {
	ws := []workout.Workout{
		planned("mon", "Tempo ride", "2026-03-16"),
		planned("wed", "Endurance ride", "2026-03-18"),
		planned("today", "VO2max intervals", "2026-03-19"),
	}
	hourAtFTP := []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetPower, TargetLow: 200, TargetHigh: 200}}
	ws[0].Steps, ws[1].Steps = hourAtFTP, hourAtFTP
	profile := profileAvailable("mon", "wed", "thu", "fri")
	profile.FTPWatts = 200
	analyses := map[string]workout.SessionAnalysis{
		"mon": analysed("mon", "completed", 130),
		"wed": analysed("wed", "completed", 130),
	}
	got := AdaptSessions(ws, nil, profile, thursday, analyses, readiness.Assessment{})
	if len(got) != 1 || got[0].Why.Rule != why.FatigueOverload {
		t.Fatalf("changes = %+v, want one fatigue_overload", got)
	}
	in := inputsAs[why.FatigueOverloadInputs](t, got[0])
	if in.AnalysedTSS != 260 || in.PlannedTSS < 199 || in.PlannedTSS > 201 || in.Ratio < 1.29 || in.Ratio > 1.31 {
		t.Errorf("inputs = %+v, want 260 analysed against ~200 planned", in)
	}
}

func TestReadinessChangesCarryTheVerdictAndTheSignals(t *testing.T) {
	signals := []why.Signal{{Kind: "hrv", Label: "HRV low two nights", Value: "38, 41 ms vs usual 52", Numbers: []float64{38, 41, 52}}}

	t.Run("rest downgrades with readiness_rest", func(t *testing.T) {
		ws := []workout.Workout{planned("today", "VO2max intervals", "2026-03-19")}
		rest := readiness.Assessment{Verdict: readiness.Rest, Reasons: []string{"HRV has been low for two nights"}, Signals: signals}
		got := AdaptSessions(ws, nil, profileAvailable(weekdays...), thursday, nil, rest)
		if len(got) != 1 || got[0].Why.Rule != why.ReadinessRest || !got[0].Downgrade {
			t.Fatalf("changes = %+v", got)
		}
		in := inputsAs[why.ReadinessInputs](t, got[0])
		if in.Verdict != "rest" || !reflect.DeepEqual(in.Signals, signals) {
			t.Errorf("inputs = %+v", in)
		}
	})

	t.Run("caution steps down with readiness_caution", func(t *testing.T) {
		ws := []workout.Workout{structured("today", "Threshold 3×8", "2026-03-19", workout.ZoneThreshold, 4)}
		caution := readiness.Assessment{Verdict: readiness.Caution, Reasons: []string{"HRV is unbalanced today"}, Signals: signals}
		got := AdaptSessions(ws, nil, workout.RiderProfile{}, thursday, nil, caution)
		if len(got) != 1 || got[0].Why.Rule != why.ReadinessCaution || !got[0].StepDown {
			t.Fatalf("changes = %+v", got)
		}
		in := inputsAs[why.ReadinessInputs](t, got[0])
		if in.Verdict != "caution" || !reflect.DeepEqual(in.Signals, signals) {
			t.Errorf("inputs = %+v", in)
		}
	})
}

func TestAStruggleStepDownRecordsTheSourceSession(t *testing.T) {
	ws := []workout.Workout{
		structured("tue-threshold", "Threshold 3×12", "2026-03-17", workout.ZoneThreshold, 5),
		structured("thu-threshold", "Threshold 3×8", "2026-03-19", workout.ZoneThreshold, 4),
	}
	analyses := map[string]workout.SessionAnalysis{"tue-threshold": struggled()}
	got := AdaptSessions(ws, nil, workout.RiderProfile{}, thursday, analyses, readiness.Assessment{})
	var step *Change
	for i := range got {
		if got[i].StepDown {
			step = &got[i]
		}
	}
	if step == nil || step.Why.Rule != why.StruggleStepDown {
		t.Fatalf("changes = %+v, want a struggle_step_down", got)
	}
	in := inputsAs[why.StruggleStepDownInputs](t, *step)
	if in.SourceDate != "2026-03-17" || in.SourceZone != "threshold" {
		t.Errorf("inputs = %+v", in)
	}
}

// TestEveryChangeCarriesARuleAndASentence is the catch-all: whichever rule
// produced a change, it says which and why. A new rule that forgets to would
// otherwise fall back silently to an old-style text-only adjustment.
func TestEveryChangeCarriesARuleAndASentence(t *testing.T) {
	ws := []workout.Workout{
		planned("tue", "Tempo ride", "2026-03-17"),
		structured("today", "Threshold 3×8", "2026-03-19", workout.ZoneThreshold, 4),
	}
	caution := readiness.Assessment{Verdict: readiness.Caution, Reasons: []string{"HRV is unbalanced today"}}
	for _, c := range AdaptSessions(ws, nil, profileAvailable("tue", "fri", "sat"), thursday, nil, caution) {
		if c.Why.Rule == "" || c.Why.Text != c.Reason {
			t.Errorf("change %+v has rule %q and text %q, want a rule and the reason as its text", c, c.Why.Rule, c.Why.Text)
		}
	}
}
