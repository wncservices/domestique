package adapter

import (
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Thursday 2026-03-19. Its week runs Mon 16th to Sun 22nd.
var thursday = time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC)

var weekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

func planned(id, name, date string) workout.Workout {
	return workout.Workout{
		ID: id, GoalID: "g", Sport: model.SportCycling, Name: name, Date: date,
		Description: scheduler.GeneratedDescription,
		Steps:       []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600}},
	}
}

func hand(id, name, date string) workout.Workout {
	w := planned(id, name, date)
	w.Description = "mine"
	return w
}

func profileAvailable(days ...string) workout.RiderProfile {
	return workout.RiderProfile{AvailableDays: days}
}

func rode(date string, seconds float64) workout.CompletedSession {
	return workout.CompletedSession{Date: date, Sport: "cycling", DurationSeconds: seconds}
}

func TestAMissedKeySessionMovesToTheNextFreeDay(t *testing.T) {
	ws := []workout.Workout{
		planned("tue", "VO2max intervals", "2026-03-17"),
		planned("sat", "Long ride", "2026-03-21"),
	}
	got := AdaptSessions(ws, nil, profileAvailable("tue", "fri", "sat"), nil, thursday, nil)

	if len(got) != 1 || got[0].WorkoutID != "tue" || got[0].NewDate != "2026-03-20" {
		t.Fatalf("changes = %+v, want the missed Tuesday intervals moved to Friday (Thursday is not an available day, Saturday is taken)", got)
	}
	if !strings.Contains(got[0].Reason, "2026-03-17") {
		t.Errorf("reason %q must name the original date — scheduling reads it back", got[0].Reason)
	}
}

func TestASessionTheRiderDidIsNotMissed(t *testing.T) {
	ws := []workout.Workout{planned("tue", "VO2max intervals", "2026-03-17")}
	// Cut short but over half of the planned hour.
	got := AdaptSessions(ws, []workout.CompletedSession{rode("2026-03-17", 2400)}, profileAvailable("fri"), nil, thursday, nil)
	if len(got) != 0 {
		t.Errorf("changes = %+v, want none: 40 of 60 minutes is the session done", got)
	}

	// Ten minutes is not.
	got = AdaptSessions(ws, []workout.CompletedSession{rode("2026-03-17", 600)}, profileAvailable("fri"), nil, thursday, nil)
	if len(got) != 1 {
		t.Errorf("changes = %+v, want the session treated as missed", got)
	}
}

func TestAMissedEasyDayIsLetGo(t *testing.T) {
	ws := []workout.Workout{planned("mon", "Endurance ride", "2026-03-16")}
	if got := AdaptSessions(ws, nil, profileAvailable("fri"), nil, thursday, nil); len(got) != 0 {
		t.Errorf("changes = %+v, want none: nothing is lost by skipping an easy day", got)
	}
}

// The scheduler books every day the rider can train, so a genuinely free day
// is the exception. The missed session takes over the next easy day instead.
func TestAMissedKeySessionTakesOverTheNextEasyDay(t *testing.T) {
	ws := []workout.Workout{
		planned("tue", "Tempo ride", "2026-03-17"),
		planned("fri", "Endurance ride", "2026-03-20"),
		planned("sat", "Endurance ride", "2026-03-21"),
		planned("sun", "Long ride", "2026-03-22"),
	}
	got := AdaptSessions(ws, nil, profileAvailable("tue", "fri", "sat", "sun"), nil, thursday, nil)

	if len(got) != 1 || got[0].WorkoutID != "tue" || got[0].NewDate != "2026-03-20" || got[0].ReplaceWorkoutID != "fri" {
		t.Fatalf("changes = %+v, want Tuesday's tempo to take Friday's easy ride", got)
	}
}

func TestAMissedKeySessionWithNowhereToGoIsLetGo(t *testing.T) {
	ws := []workout.Workout{
		planned("tue", "Tempo ride", "2026-03-17"),
		planned("fri", "Long ride", "2026-03-20"),       // a key session is never given up
		hand("sat", "Endurance ride", "2026-03-21"),     // nor is a rider's own
		planned("done", "Endurance ride", "2026-03-19"), // today's easy ride, already ridden
	}
	sessions := []workout.CompletedSession{rode("2026-03-19", 3600)}
	if got := AdaptSessions(ws, sessions, profileAvailable("fri", "sat"), nil, thursday, nil); len(got) != 0 {
		t.Errorf("changes = %+v, want none: there is nowhere to put it that costs nothing that matters", got)
	}
}

func TestTwoMissedSessionsNeverLandOnTheSameDay(t *testing.T) {
	ws := []workout.Workout{
		planned("a", "Tempo ride", "2026-03-16"),
		planned("b", "Long ride", "2026-03-17"),
		planned("e1", "Endurance ride", "2026-03-20"),
		planned("e2", "Endurance ride", "2026-03-21"),
	}
	got := AdaptSessions(ws, nil, profileAvailable("fri", "sat"), nil, thursday, nil)
	if len(got) != 2 || got[0].NewDate == got[1].NewDate || got[0].ReplaceWorkoutID == got[1].ReplaceWorkoutID {
		t.Errorf("changes = %+v, want two different days", got)
	}
}

func TestATiredRiderDoesNotMakeUpAMissedSession(t *testing.T) {
	ws := []workout.Workout{planned("tue", "Long ride", "2026-03-17")}
	tired := &workout.FitnessSnapshot{Date: "2026-03-19", TSB: -35}
	if got := AdaptSessions(ws, nil, profileAvailable("fri"), tired, thursday, nil); len(got) != 0 {
		t.Errorf("changes = %+v, want none: rest is the right answer", got)
	}
}

func TestAHardSessionIsSwappedForAnEasyOneWhenVeryFatigued(t *testing.T) {
	ws := []workout.Workout{
		planned("today", "VO2max intervals", "2026-03-19"),
		planned("next-week", "VO2max intervals", "2026-03-26"),
		planned("long", "Long ride", "2026-03-20"),
	}
	tired := &workout.FitnessSnapshot{Date: "2026-03-19", TSB: -34}
	got := AdaptSessions(ws, nil, profileAvailable(weekdays...), tired, thursday, nil)

	if len(got) != 1 || got[0].WorkoutID != "today" || !got[0].Downgrade {
		t.Fatalf("changes = %+v, want only today's intervals downgraded: a long ride is volume not intensity, and next week is not yet", got)
	}
	if !strings.Contains(got[0].Reason, "-34") {
		t.Errorf("reason %q should say what the form is", got[0].Reason)
	}
}

func TestFatigueFromOldDataChangesNothing(t *testing.T) {
	ws := []workout.Workout{planned("today", "VO2max intervals", "2026-03-19")}
	stale := &workout.FitnessSnapshot{Date: "2026-03-01", TSB: -50}
	if got := AdaptSessions(ws, nil, profileAvailable(weekdays...), stale, thursday, nil); len(got) != 0 {
		t.Errorf("changes = %+v, want none: a snapshot from two and a half weeks ago says nothing about today", got)
	}
}

func TestNothingElseIsEverTouched(t *testing.T) {
	hand := planned("mine", "VO2max intervals", "2026-03-17")
	hand.Description = "my own session"
	noGoal := planned("free", "Long ride", "2026-03-17")
	noGoal.GoalID = ""
	adjusted := planned("done-once", "Long ride", "2026-03-18")
	adjusted.Description += " " + scheduler.AdjustedMarker + " moved from 2026-03-16."

	got := AdaptSessions([]workout.Workout{hand, noGoal, adjusted}, nil, profileAvailable("fri"), nil, thursday, nil)
	if len(got) != 0 {
		t.Errorf("changes = %+v, want none: a rider's own, goal-less or already-adjusted workout is never changed", got)
	}
}

func analysed(workoutID, outcome string, tss float64, steps ...workout.AnalysisStep) workout.SessionAnalysis {
	return workout.SessionAnalysis{WorkoutID: workoutID, Outcome: outcome, TSS: tss, Steps: steps}
}

func hitStep(result string) workout.AnalysisStep { return workout.AnalysisStep{Result: result} }

func TestAnIncompleteAnalysisIsTreatedAsMissedEvenIfTimeWasLogged(t *testing.T) {
	ws := []workout.Workout{planned("tue", "VO2max intervals", "2026-03-17")}
	// Would pass the ≥ 50% time rule on its own, but the analysis says the
	// session itself did not go as planned.
	sessions := []workout.CompletedSession{rode("2026-03-17", 3600)}
	analyses := map[string]workout.SessionAnalysis{"tue": analysed("tue", "incomplete", 40)}

	got := AdaptSessions(ws, sessions, profileAvailable("fri"), nil, thursday, analyses)
	if len(got) != 1 || got[0].WorkoutID != "tue" {
		t.Fatalf("changes = %+v, want the session made up: an incomplete analysis overrides the time rule", got)
	}
}

func TestAStruggledAnalysisIsDoneNotMadeUp(t *testing.T) {
	ws := []workout.Workout{planned("tue", "VO2max intervals", "2026-03-17")}
	analyses := map[string]workout.SessionAnalysis{"tue": analysed("tue", "struggled", 80)}

	got := AdaptSessions(ws, nil, profileAvailable("fri"), nil, thursday, analyses)
	if len(got) != 0 {
		t.Errorf("changes = %+v, want none: a struggled session happened, it is not missed", got)
	}
}

func TestLastTwoStruggledKeySessionsSwapTodaysHardSessionEvenWithMildTSB(t *testing.T) {
	ws := []workout.Workout{
		planned("mon", "Tempo ride", "2026-03-16"),
		planned("wed", "Interval session", "2026-03-18"),
		planned("today", "VO2max intervals", "2026-03-19"),
	}
	// TSB alone (-10) is nowhere near fatigueTSB (-30).
	mild := &workout.FitnessSnapshot{Date: "2026-03-19", TSB: -10}
	analyses := map[string]workout.SessionAnalysis{
		"mon": analysed("mon", "struggled", 70),
		"wed": analysed("wed", "struggled", 75, hitStep("under"), hitStep("hit"), hitStep("under"), hitStep("under")),
	}

	got := AdaptSessions(ws, nil, profileAvailable("mon", "wed", "thu", "fri"), mild, thursday, analyses)
	if len(got) != 1 || got[0].WorkoutID != "today" || !got[0].Downgrade {
		t.Fatalf("changes = %+v, want today's intervals swapped for easy: two struggled key sessions in a row is fatigue even without TSB", got)
	}
	if !strings.Contains(got[0].Reason, "Wednesday's interval session were under target (1 of 4)") {
		t.Errorf("reason %q must name the most recent struggled ride and its hit count", got[0].Reason)
	}
}

func TestSevenDayOverloadedTSSSwapsTodaysHardSession(t *testing.T) {
	// One planned+analysed hour a day at FTP (200W): estimatePlannedTSS ~=
	// 100 each. Analysed TSS of 260 across two rides is well past 1.3x the
	// 200 planned across those same two days.
	ws := []workout.Workout{
		planned("mon", "Tempo ride", "2026-03-16"),
		planned("wed", "Endurance ride", "2026-03-18"),
		planned("today", "VO2max intervals", "2026-03-19"),
	}
	ws[0].Steps = []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetPower, TargetLow: 200, TargetHigh: 200}}
	ws[1].Steps = []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetPower, TargetLow: 200, TargetHigh: 200}}
	profile := profileAvailable("mon", "wed", "thu", "fri")
	profile.FTPWatts = 200
	analyses := map[string]workout.SessionAnalysis{
		"mon": analysed("mon", "completed", 130),
		"wed": analysed("wed", "completed", 130),
	}

	got := AdaptSessions(ws, nil, profile, nil, thursday, analyses)
	if len(got) != 1 || got[0].WorkoutID != "today" || !got[0].Downgrade {
		t.Fatalf("changes = %+v, want today's intervals swapped for easy: the last 7 days carried far more load than planned", got)
	}
}

func TestOverloadTriggerIsSkippedWithoutFTP(t *testing.T) {
	ws := []workout.Workout{
		planned("mon", "Tempo ride", "2026-03-16"),
		planned("today", "VO2max intervals", "2026-03-19"),
	}
	ws[0].Steps = []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetPower, TargetLow: 200, TargetHigh: 200}}
	// No FTP on the profile — there is no way to estimate planned TSS at all.
	analyses := map[string]workout.SessionAnalysis{"mon": analysed("mon", "completed", 400)}

	got := AdaptSessions(ws, nil, profileAvailable("mon", "thu"), nil, thursday, analyses)
	if len(got) != 0 {
		t.Errorf("changes = %+v, want none: without an FTP the overload trigger cannot fire", got)
	}
}

func TestWithoutAnalysesTheTimeRuleStillApplies(t *testing.T) {
	ws := []workout.Workout{planned("tue", "VO2max intervals", "2026-03-17")}
	got := AdaptSessions(ws, []workout.CompletedSession{rode("2026-03-17", 600)}, profileAvailable("fri"), nil, thursday, nil)
	if len(got) != 1 {
		t.Errorf("changes = %+v, want the ≥ 50%% time rule to still catch a missed session when there is no analysis at all", got)
	}
}

func TestAnAnalysedRiderOwnedWorkoutIsNeverChanged(t *testing.T) {
	w := hand("mine", "VO2max intervals", "2026-03-17")
	analyses := map[string]workout.SessionAnalysis{"mine": analysed("mine", "incomplete", 0)}
	got := AdaptSessions([]workout.Workout{w}, nil, profileAvailable("fri"), nil, thursday, analyses)
	if len(got) != 0 {
		t.Errorf("changes = %+v, want none: a rider's own workout is never touched, whatever ride-analysis says", got)
	}
}

func TestEstimatePlannedTSS(t *testing.T) {
	hourAt := func(low, high float64) []workout.WorkoutStep {
		return []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetPower, TargetLow: low, TargetHigh: high}}
	}

	tests := []struct {
		name  string
		steps []workout.WorkoutStep
		ftp   float64
		want  float64
	}{
		{
			name:  "an hour exactly at FTP scores ~100",
			steps: hourAt(200, 200),
			ftp:   200,
			want:  100,
		},
		{
			name:  "a power target uses its mid-point",
			steps: hourAt(150, 250), // mid 200, same IF as above
			ftp:   200,
			want:  100,
		},
		{
			name:  "no power target falls back to the easy default IF",
			steps: []workout.WorkoutStep{{Name: "Endurance", Duration: workout.DurationTime, Seconds: 3600}},
			ftp:   200,
			want:  defaultPlannedIF * defaultPlannedIF * 100,
		},
		{
			name: "a power target nested in a repeat block still counts",
			steps: []workout.WorkoutStep{
				{Name: "Warmup", Duration: workout.DurationTime, Seconds: 600},
				{Name: "Intervals", Repeat: 4, Steps: hourAt(300, 300)},
			},
			// PlannedSeconds: 600 + 4*3600 = 15000s = 4.1666h; IF = 300/200 = 1.5
			ftp:  200,
			want: (600.0/3600 + 4*3600.0/3600) * 1.5 * 1.5 * 100,
		},
		{
			name:  "no FTP means no estimate at all",
			steps: hourAt(200, 200),
			ftp:   0,
			want:  0,
		},
		{
			name:  "no time-based steps means no estimate",
			steps: []workout.WorkoutStep{{Name: "Open", Duration: workout.DurationOpen}},
			ftp:   200,
			want:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := workout.Workout{Steps: tt.steps}
			got := estimatePlannedTSS(w, tt.ftp)
			if diff := got - tt.want; diff > 0.01 || diff < -0.01 {
				t.Errorf("estimatePlannedTSS() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNoteCarriesTheMarkerThatPreventsASecondAdjustment(t *testing.T) {
	c := Change{Reason: "moved from 2026-03-17 — missed."}
	w := workout.Workout{GoalID: "g", Description: scheduler.GeneratedDescription + " " + Note(c)}
	if scheduler.IsGenerated(w) {
		t.Error("a workout carrying its own adjustment note must not be adjustable again")
	}
	if d, ok := scheduler.MovedFrom(w.Description); !ok || d != "2026-03-17" {
		t.Errorf("MovedFrom = %q, %v — scheduling relies on this to not recreate the vacated day", d, ok)
	}
}
