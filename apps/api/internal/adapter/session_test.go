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

// onStep and recoveryStep build the two kinds of scored step a real
// generated interval session produces: an "on" rep, which is Hard, and a
// "Recovery" rep, which is scored too (scheduler.intervalOffTarget gives it
// a real power target, not TargetOpen) but is never Hard — the case that
// motivated AnalysisStep carrying Hard at all, since counting every scored
// step would double a 4-rep session's denominator to 8.
func onStep(result string) workout.AnalysisStep {
	return workout.AnalysisStep{Name: "On", Result: result, Hard: true}
}

func recoveryStep(result string) workout.AnalysisStep {
	return workout.AnalysisStep{Name: "Recovery", Result: result, Hard: false}
}

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
		// A realistic 4-on/4-recovery interval session: 4 hard "On" steps
		// (2 hit, 2 under) interleaved with 4 non-hard "Recovery" steps
		// (all hit, since an easy recovery target is rarely missed) — only
		// the On steps should ever appear in the reason's "of 4".
		"wed": analysed("wed", "struggled", 75,
			onStep("under"), recoveryStep("hit"),
			onStep("hit"), recoveryStep("hit"),
			onStep("under"), recoveryStep("hit"),
			onStep("hit"), recoveryStep("hit"),
		),
	}

	got := AdaptSessions(ws, nil, profileAvailable("mon", "wed", "thu", "fri"), mild, thursday, analyses)
	if len(got) != 1 || got[0].WorkoutID != "today" || !got[0].Downgrade {
		t.Fatalf("changes = %+v, want today's intervals swapped for easy: two struggled key sessions in a row is fatigue even without TSB", got)
	}
	if !strings.Contains(got[0].Reason, "Wednesday's interval session were under target (2 of 4)") {
		t.Errorf("reason %q must name the most recent struggled ride and its hit count", got[0].Reason)
	}
}

// TestStruggleReasonOmitsHitCountWithNoHardSteps covers a session that
// struggled on duration alone (an endurance ride cut short, no hard interval
// steps to score) — struggleReason must not print the nonsensical "(0 of 0)".
func TestStruggleReasonOmitsHitCountWithNoHardSteps(t *testing.T) {
	ws := []workout.Workout{
		planned("mon", "Tempo ride", "2026-03-16"),
		planned("wed", "Long ride", "2026-03-18"),
		planned("today", "VO2max intervals", "2026-03-19"),
	}
	mild := &workout.FitnessSnapshot{Date: "2026-03-19", TSB: -10}
	analyses := map[string]workout.SessionAnalysis{
		"mon": analysed("mon", "struggled", 70),
		"wed": analysed("wed", "struggled", 75), // no steps at all: no hard steps to score
	}

	got := AdaptSessions(ws, nil, profileAvailable("mon", "wed", "thu", "fri"), mild, thursday, analyses)
	if len(got) != 1 || got[0].WorkoutID != "today" || !got[0].Downgrade {
		t.Fatalf("changes = %+v, want today's intervals swapped for easy", got)
	}
	if !strings.Contains(got[0].Reason, "Wednesday's long ride was cut short") {
		t.Errorf("reason %q must name the most recent struggled ride without a bogus hit count", got[0].Reason)
	}
	if strings.Contains(got[0].Reason, "of 0") {
		t.Errorf("reason %q must never print a (0 of 0) clause", got[0].Reason)
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

// structured builds a generated, structured-zone workout — the shape
// stepDownTarget needs to recognise both the struggled source session and
// the candidate it might step down (workout.IsStructuredZone is what makes
// IsKeySession true regardless of name; see scheduler.IsKeySession).
func structured(id, name, date string, zone workout.Zone, level float64) workout.Workout {
	w := planned(id, name, date)
	w.Zone, w.Level = zone, level
	return w
}

func struggled(steps ...bool) workout.SessionAnalysis {
	// steps is unused today (struggleReason's own "N of M" logic is not part
	// of the step-down reason, which names only the weekday and zone) — kept
	// as a no-op parameter only so a caller reads naturally; ignore it.
	return workout.SessionAnalysis{Outcome: "struggled"}
}

func TestAStruggledKeySessionStepsDownTheNextSameZoneWorkout(t *testing.T) {
	ws := []workout.Workout{
		structured("tue-threshold", "Threshold 3×12", "2026-03-17", workout.ZoneThreshold, 5),
		structured("thu-threshold", "Threshold 3×8", "2026-03-19", workout.ZoneThreshold, 4),
	}
	analyses := map[string]workout.SessionAnalysis{"tue-threshold": struggled()}

	got := AdaptSessions(ws, nil, workout.RiderProfile{}, nil, thursday, analyses)

	var stepDowns []Change
	for _, c := range got {
		if c.StepDown {
			stepDowns = append(stepDowns, c)
		}
	}
	if len(stepDowns) != 1 || stepDowns[0].WorkoutID != "thu-threshold" {
		t.Fatalf("changes = %+v, want exactly one step-down of thu-threshold", got)
	}
	if !strings.Contains(stepDowns[0].Reason, "Tuesday") || !strings.Contains(stepDowns[0].Reason, "threshold") {
		t.Errorf("reason = %q, want it to name Tuesday and threshold", stepDowns[0].Reason)
	}
}

func TestAStruggledSessionDoesNotStepDownADifferentZone(t *testing.T) {
	ws := []workout.Workout{
		structured("tue-threshold", "Threshold 3×12", "2026-03-17", workout.ZoneThreshold, 5),
		structured("thu-vo2", "VO2max 5×4", "2026-03-19", workout.ZoneVO2Max, 5),
	}
	analyses := map[string]workout.SessionAnalysis{"tue-threshold": struggled()}

	got := AdaptSessions(ws, nil, workout.RiderProfile{}, nil, thursday, analyses)

	for _, c := range got {
		if c.StepDown {
			t.Fatalf("changes = %+v, want no step-down — the only later workout is a different zone", got)
		}
	}
}

func TestAnAlreadyAdjustedWorkoutIsNeverStepDownTargeted(t *testing.T) {
	touched := structured("thu-threshold", "Threshold 3×8", "2026-03-19", workout.ZoneThreshold, 4)
	touched.Description += " " + scheduler.AdjustedMarker + " moved already."
	ws := []workout.Workout{
		structured("tue-threshold", "Threshold 3×12", "2026-03-17", workout.ZoneThreshold, 5),
		touched,
	}
	analyses := map[string]workout.SessionAnalysis{"tue-threshold": struggled()}

	got := AdaptSessions(ws, nil, workout.RiderProfile{}, nil, thursday, analyses)

	for _, c := range got {
		if c.StepDown {
			t.Fatalf("changes = %+v, want no step-down — the only candidate has already been adjusted", got)
		}
	}
}

func TestNailedOrOldStruggleTriggersNoStepDown(t *testing.T) {
	cases := []struct {
		name     string
		outcome  string
		sourceID string
	}{
		{"nailed", "nailed", "tue-threshold"},
		{"struggled but 9 days ago", "struggled", "old-threshold"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ws := []workout.Workout{
				structured(tt.sourceID, "Threshold 3×12", "2026-03-10", workout.ZoneThreshold, 5),
				structured("thu-threshold", "Threshold 3×8", "2026-03-19", workout.ZoneThreshold, 4),
			}
			analyses := map[string]workout.SessionAnalysis{tt.sourceID: {Outcome: tt.outcome}}
			// "nailed" case uses a recent date; override to match the table.
			if tt.name == "nailed" {
				ws[0].Date = "2026-03-17"
			}

			got := AdaptSessions(ws, nil, workout.RiderProfile{}, nil, thursday, analyses)
			for _, c := range got {
				if c.StepDown {
					t.Fatalf("changes = %+v, want no step-down for %s", got, tt.name)
				}
			}
		})
	}
}

// TestAFatigueSwapTargetIsNeverAlsoStepDownTargeted is the final review's
// fix for the bug the brief describes: a workout can be both the fatigue
// swap's target (today's hard session, Downgrade) and stepDownTarget's own
// candidate (the next untouched same-zone workout after a struggled
// session) — before the fix, AdaptSessions handed that one workout two
// Changes in the same pass, and adaptRider applied them against a snapshot
// taken before the loop, so the later write silently clobbered the earlier
// one. Now stepDownTarget must skip any workout id the per-session loop
// already claimed this pass.
func TestAFatigueSwapTargetIsNeverAlsoStepDownTargeted(t *testing.T) {
	ws := []workout.Workout{
		structured("tue-threshold", "Threshold 3×12", "2026-03-17", workout.ZoneThreshold, 5),
		structured("thu-threshold", "Threshold 3×8", "2026-03-19", workout.ZoneThreshold, 4),
	}
	analyses := map[string]workout.SessionAnalysis{"tue-threshold": struggled()}
	// Deep fatigue (fresh, very negative TSB) makes thu-threshold — dated
	// today and a hard/structured-zone session — a fatigue-swap Downgrade
	// target too, on top of already being stepDownTarget's own candidate for
	// the Tuesday struggle.
	tired := &workout.FitnessSnapshot{Date: "2026-03-19", TSB: -34}

	got := AdaptSessions(ws, nil, workout.RiderProfile{}, tired, thursday, analyses)

	var forThursday []Change
	for _, c := range got {
		if c.WorkoutID == "thu-threshold" {
			forThursday = append(forThursday, c)
		}
	}
	if len(forThursday) != 1 {
		t.Fatalf("changes for thu-threshold = %+v, want exactly one", forThursday)
	}
	if !forThursday[0].Downgrade || forThursday[0].StepDown {
		t.Errorf("change = %+v, want the fatigue Downgrade, not also a StepDown", forThursday[0])
	}
}

// TestAMissedSessionRescheduledOntoASameZoneSlotIsNotAlsoSteppedDown is the
// other half of the same fix: a missed key session gets rescheduled within
// this same pass, landing (by its *original*, still-missed date, which is
// what stepDownTarget's own candidate search still sees) as the next
// untouched same-zone workout after a struggled session — it must keep its
// single reschedule Change, not pick up a second StepDown Change too.
func TestAMissedSessionRescheduledOntoASameZoneSlotIsNotAlsoSteppedDown(t *testing.T) {
	source := structured("mon-threshold", "Threshold 3×12", "2026-03-16", workout.ZoneThreshold, 5)
	missed := structured("tue-threshold", "Threshold 3×8", "2026-03-17", workout.ZoneThreshold, 4)
	ws := []workout.Workout{source, missed}
	analyses := map[string]workout.SessionAnalysis{"mon-threshold": struggled()}

	// today is Thursday the 19th; Tuesday's threshold session is this week,
	// already over, and nothing else is planned, so Friday is free to make
	// it up on.
	got := AdaptSessions(ws, nil, profileAvailable("fri"), nil, thursday, analyses)

	var forTuesday []Change
	for _, c := range got {
		if c.WorkoutID == "tue-threshold" {
			forTuesday = append(forTuesday, c)
		}
	}
	if len(forTuesday) != 1 {
		t.Fatalf("changes for tue-threshold = %+v, want exactly one", forTuesday)
	}
	if forTuesday[0].NewDate == "" || forTuesday[0].StepDown {
		t.Errorf("change = %+v, want only the missed-session reschedule, not also a StepDown", forTuesday[0])
	}
	for _, c := range got {
		if c.StepDown {
			t.Errorf("changes = %+v, want no StepDown at all: the only same-zone candidate was already claimed by the reschedule", got)
		}
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
