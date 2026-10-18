package adapter

import (
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

// Thursday 2026-03-19. Its week runs Mon 16th to Sun 22nd.
var thursday = time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC)

var weekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

func planned(id, name, date string) workout.Workout {
	return workout.Workout{
		ID: id, GoalID: "g", Sport: model.SportCycling, Name: name, Date: date,
		Description: scheduler.GeneratedDescription,
		Flags:       workout.Flags{Origin: workout.OriginPlan},
		Steps:       []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600}},
	}
}

func hand(id, name, date string) workout.Workout {
	w := planned(id, name, date)
	w.Description = "mine"
	w.Flags = workout.Flags{Origin: workout.OriginRider}
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
	got := AdaptSessions(ws, nil, profileAvailable("tue", "fri", "sat"), thursday, nil, readiness.Assessment{})

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
	got := AdaptSessions(ws, []workout.CompletedSession{rode("2026-03-17", 2400)}, profileAvailable("fri"), thursday, nil, readiness.Assessment{})
	if len(got) != 0 {
		t.Errorf("changes = %+v, want none: 40 of 60 minutes is the session done", got)
	}

	// Ten minutes is not.
	got = AdaptSessions(ws, []workout.CompletedSession{rode("2026-03-17", 600)}, profileAvailable("fri"), thursday, nil, readiness.Assessment{})
	if len(got) != 1 {
		t.Errorf("changes = %+v, want the session treated as missed", got)
	}
}

func TestAMissedEasyDayIsLetGo(t *testing.T) {
	ws := []workout.Workout{planned("mon", "Endurance ride", "2026-03-16")}
	if got := AdaptSessions(ws, nil, profileAvailable("fri"), thursday, nil, readiness.Assessment{}); len(got) != 0 {
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
	got := AdaptSessions(ws, nil, profileAvailable("tue", "fri", "sat", "sun"), thursday, nil, readiness.Assessment{})

	if len(got) != 1 || got[0].WorkoutID != "tue" || got[0].NewDate != "2026-03-20" || got[0].ReplaceWorkoutID != "fri" {
		t.Fatalf("changes = %+v, want Tuesday's tempo to take Friday's easy ride", got)
	}
}

// A routed ride is the rider's own choice for its day: a missed key session
// never takes its slot, because taking it deletes the ride and strands the
// loop made for it.
func TestAMissedKeySessionNeverTakesARoutedEasyDay(t *testing.T) {
	routed := planned("thu", "Endurance ride", "2026-03-19") // Thursday, today
	routed.RouteSlug = "thursday-loop"
	ws := []workout.Workout{planned("tue", "VO2max intervals", "2026-03-17"), routed}
	got := AdaptSessions(ws, nil, profileAvailable("tue"), thursday, nil, readiness.Assessment{})
	for _, c := range got {
		if c.ReplaceWorkoutID == "thu" {
			t.Fatalf("changes = %+v: the routed Thursday ride was taken over", got)
		}
	}

	// Control: the same week without the route does give the slot up.
	plain := planned("thu", "Endurance ride", "2026-03-19")
	got = AdaptSessions([]workout.Workout{planned("tue", "VO2max intervals", "2026-03-17"), plain}, nil, profileAvailable("tue"), thursday, nil, readiness.Assessment{})
	if len(got) != 1 || got[0].ReplaceWorkoutID != "thu" {
		t.Fatalf("control changes = %+v, want the plain easy day taken", got)
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
	if got := AdaptSessions(ws, sessions, profileAvailable("fri", "sat"), thursday, nil, readiness.Assessment{}); len(got) != 0 {
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
	got := AdaptSessions(ws, nil, profileAvailable("fri", "sat"), thursday, nil, readiness.Assessment{})
	if len(got) != 2 || got[0].NewDate == got[1].NewDate || got[0].ReplaceWorkoutID == got[1].ReplaceWorkoutID {
		t.Errorf("changes = %+v, want two different days", got)
	}
}

// TestATiredRiderDoesNotMakeUpAMissedSession used to drive "tired" from a
// very negative TSB passed directly to AdaptSessions; that rule now lives in
// internal/readiness, so the equivalent trigger here is a Rest verdict
// (which is what readiness.Assess would have produced from that same form) —
// intent preserved: whatever produces "rest", a missed session is not made
// up.
func TestATiredRiderDoesNotMakeUpAMissedSession(t *testing.T) {
	ws := []workout.Workout{planned("tue", "Long ride", "2026-03-17")}
	rest := readiness.Assessment{Verdict: readiness.Rest, Reasons: []string{"your form is −35"}}
	if got := AdaptSessions(ws, nil, profileAvailable("fri"), thursday, nil, rest); len(got) != 0 {
		t.Errorf("changes = %+v, want none: rest is the right answer", got)
	}
}

// TestAHardSessionIsSwappedForAnEasyOneWhenVeryFatigued used to drive the
// swap from a fresh TSB of -34 passed directly to AdaptSessions; that rule
// now lives in internal/readiness (see the readiness.Rest case in
// AdaptSessions), so the equivalent input here is the Rest Assessment
// readiness.Assess would have produced from that same form — intent
// preserved: today's hard session, and only today's, is downgraded.
func TestAHardSessionIsSwappedForAnEasyOneWhenVeryFatigued(t *testing.T) {
	ws := []workout.Workout{
		planned("today", "VO2max intervals", "2026-03-19"),
		planned("next-week", "VO2max intervals", "2026-03-26"),
		planned("long", "Long ride", "2026-03-20"),
	}
	rest := readiness.Assessment{Verdict: readiness.Rest, Reasons: []string{"your form is −34"}}
	got := AdaptSessions(ws, nil, profileAvailable(weekdays...), thursday, nil, rest)

	if len(got) != 1 || got[0].WorkoutID != "today" || !got[0].Downgrade {
		t.Fatalf("changes = %+v, want only today's intervals downgraded: a long ride is volume not intensity, and next week is not yet", got)
	}
	// The description carries no figure (it reaches the calendar feed); the
	// Why record keeps what the form is.
	if strings.Contains(got[0].Reason, "−34") || !strings.Contains(got[0].Why.Text, "−34") {
		t.Errorf("reason %q / why text %q: the figure belongs in the Why record only", got[0].Reason, got[0].Why.Text)
	}
}

// TestReadyAssessmentChangesNothing used to be TestFatigueFromOldDataChangesNothing,
// which drove a stale TSB snapshot straight into AdaptSessions to prove a
// two-and-a-half-week-old form reading is ignored. That freshness check now
// lives entirely in internal/readiness (readiness.Assess's own tsbFresh, see
// its table tests) — a stale TSB simply never produces anything but a Ready
// Assessment. What is left for the adapter to guarantee is the other half:
// a Ready verdict changes nothing here, whatever produced it.
func TestReadyAssessmentChangesNothing(t *testing.T) {
	ws := []workout.Workout{planned("today", "VO2max intervals", "2026-03-19")}
	if got := AdaptSessions(ws, nil, profileAvailable(weekdays...), thursday, nil, readiness.Assessment{Verdict: readiness.Ready}); len(got) != 0 {
		t.Errorf("changes = %+v, want none: a ready verdict never changes anything", got)
	}
}

// TestCautionStepsDownTodaysStructuredHardWorkout is the caution half of the
// readiness.Verdict switch: today's structured hard workout, still
// untouched, gets stepped down one rung — not downgraded to easy — with the
// readiness:<date> source marker and the "Eased one level" reason.
func TestCautionStepsDownTodaysStructuredHardWorkout(t *testing.T) {
	ws := []workout.Workout{
		structured("today", "Threshold 3×8", "2026-03-19", workout.ZoneThreshold, 4),
		structured("next-week", "Threshold 3×8", "2026-03-26", workout.ZoneThreshold, 4),
	}
	caution := readiness.Assessment{Verdict: readiness.Caution, Reasons: []string{"HRV is unbalanced today"}}

	got := AdaptSessions(ws, nil, workout.RiderProfile{}, thursday, nil, caution)

	if len(got) != 1 {
		t.Fatalf("changes = %+v, want exactly one", got)
	}
	c := got[0]
	if c.WorkoutID != "today" || !c.StepDown || c.Downgrade {
		t.Fatalf("change = %+v, want today's workout stepped down, not downgraded", c)
	}
	if c.StepDownSourceID != "readiness:2026-03-19" {
		t.Errorf("StepDownSourceID = %q, want readiness:2026-03-19", c.StepDownSourceID)
	}
	if c.Reason != "Eased one level for readiness." || strings.Contains(c.Reason, "HRV") {
		t.Errorf("reason = %q, want plain wording with no health text (it is written into the description)", c.Reason)
	}
	if !strings.HasPrefix(c.Why.Text, "Eased one level — ") || !strings.Contains(c.Why.Text, "HRV is unbalanced today") {
		t.Errorf("why text = %q, want the assessment's reasons kept for the popover", c.Why.Text)
	}
}

// TestCautionChangesNothingOnASecondSamedayPass mirrors the rest of the
// package's own "once is enough" guarantee: a workout readiness has already
// stepped down (marked exactly the way applyStepDown leaves it — the
// adjustment marker in its description) is no longer scheduler.IsGenerated,
// so a second pass the same day — same caution Assessment, nothing else
// changed — produces nothing at all for it.
func TestCautionChangesNothingOnASecondSamedayPass(t *testing.T) {
	already := structured("today", "Threshold 3×6", "2026-03-19", workout.ZoneThreshold, 3)
	already.Description += " " + scheduler.AdjustedMarker + " Eased one level — HRV is unbalanced today."
	already.Flags = workout.LegacyFlags(already)
	caution := readiness.Assessment{Verdict: readiness.Caution, Reasons: []string{"HRV is unbalanced today"}}

	got := AdaptSessions([]workout.Workout{already}, nil, workout.RiderProfile{}, thursday, nil, caution)
	if len(got) != 0 {
		t.Errorf("changes = %+v, want none: this workout already carries today's readiness step-down", got)
	}
}

// TestCautionSkipsALegacyZonelessHardWorkout is the fix for the review
// finding: a caution step-down needs a rung on its own zone's ladder
// (workoutlib), so a legacy hard workout with no zone — which
// scheduler.IsHardSession still recognises by name — must not be claimed
// and then silently produce nothing further down the line. Unlike caution,
// rest's downgrade replaces the workout wholesale and has no such
// restriction — see TestAHardSessionIsSwappedForAnEasyOneWhenVeryFatigued.
func TestCautionSkipsALegacyZonelessHardWorkout(t *testing.T) {
	ws := []workout.Workout{planned("today", "Interval session", "2026-03-19")}
	caution := readiness.Assessment{Verdict: readiness.Caution, Reasons: []string{"HRV is unbalanced today"}}

	got := AdaptSessions(ws, nil, workout.RiderProfile{}, thursday, nil, caution)
	if len(got) != 0 {
		t.Errorf("changes = %+v, want none: a zone-less legacy hard workout has no ladder to step down on", got)
	}
}

func TestNothingElseIsEverTouched(t *testing.T) {
	hand := planned("mine", "VO2max intervals", "2026-03-17")
	hand.Description = "my own session"
	hand.Flags = workout.Flags{Origin: workout.OriginRider}
	noGoal := planned("free", "Long ride", "2026-03-17")
	noGoal.GoalID = ""
	adjusted := planned("done-once", "Long ride", "2026-03-18")
	adjusted.Description += " " + scheduler.AdjustedMarker + " moved from 2026-03-16."
	adjusted.Flags = workout.LegacyFlags(adjusted)

	got := AdaptSessions([]workout.Workout{hand, noGoal, adjusted}, nil, profileAvailable("fri"), thursday, nil, readiness.Assessment{})
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

	got := AdaptSessions(ws, sessions, profileAvailable("fri"), thursday, analyses, readiness.Assessment{})
	if len(got) != 1 || got[0].WorkoutID != "tue" {
		t.Fatalf("changes = %+v, want the session made up: an incomplete analysis overrides the time rule", got)
	}
}

func TestAStruggledAnalysisIsDoneNotMadeUp(t *testing.T) {
	ws := []workout.Workout{planned("tue", "VO2max intervals", "2026-03-17")}
	analyses := map[string]workout.SessionAnalysis{"tue": analysed("tue", "struggled", 80)}

	got := AdaptSessions(ws, nil, profileAvailable("fri"), thursday, analyses, readiness.Assessment{})
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
	// No readiness signal at all here — struggled sessions alone are fatigue,
	// independent of form.
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

	got := AdaptSessions(ws, nil, profileAvailable("mon", "wed", "thu", "fri"), thursday, analyses, readiness.Assessment{})
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
	analyses := map[string]workout.SessionAnalysis{
		"mon": analysed("mon", "struggled", 70),
		"wed": analysed("wed", "struggled", 75), // no steps at all: no hard steps to score
	}

	got := AdaptSessions(ws, nil, profileAvailable("mon", "wed", "thu", "fri"), thursday, analyses, readiness.Assessment{})
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
	// One planned+analysed hour a day at FTP (200W): the planned TSS ~=
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

	got := AdaptSessions(ws, nil, profile, thursday, analyses, readiness.Assessment{})
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

	got := AdaptSessions(ws, nil, profileAvailable("mon", "thu"), thursday, analyses, readiness.Assessment{})
	if len(got) != 0 {
		t.Errorf("changes = %+v, want none: without an FTP the overload trigger cannot fire", got)
	}
}

func TestWithoutAnalysesTheTimeRuleStillApplies(t *testing.T) {
	ws := []workout.Workout{planned("tue", "VO2max intervals", "2026-03-17")}
	got := AdaptSessions(ws, []workout.CompletedSession{rode("2026-03-17", 600)}, profileAvailable("fri"), thursday, nil, readiness.Assessment{})
	if len(got) != 1 {
		t.Errorf("changes = %+v, want the ≥ 50%% time rule to still catch a missed session when there is no analysis at all", got)
	}
}

func TestAnAnalysedRiderOwnedWorkoutIsNeverChanged(t *testing.T) {
	w := hand("mine", "VO2max intervals", "2026-03-17")
	analyses := map[string]workout.SessionAnalysis{"mine": analysed("mine", "incomplete", 0)}
	got := AdaptSessions([]workout.Workout{w}, nil, profileAvailable("fri"), thursday, analyses, readiness.Assessment{})
	if len(got) != 0 {
		t.Errorf("changes = %+v, want none: a rider's own workout is never touched, whatever ride-analysis says", got)
	}
}

func TestOverloadUsesStepByStepPlannedTSS(t *testing.T) {
	const ftp = 250
	l, ok := workoutlib.LadderFor(model.SportCycling, "threshold")
	if !ok {
		t.Fatal("no threshold ladder")
	}
	req := workoutlib.Instantiate(l, l.Rungs[3], workout.RiderProfile{FTPWatts: ftp})
	w := workout.Workout{ID: "t", Date: "2026-03-18", Sport: model.SportCycling, Steps: req.Steps}
	want, sized := workout.PlannedTSS(w, ftp)
	if !sized {
		t.Fatal("structured threshold session not sized")
	}
	today := time.Date(2026, 3, 19, 0, 0, 0, 0, time.UTC)
	analyses := map[string]workout.SessionAnalysis{"t": {TSS: 1000}}
	_, planned, _ := overloadedWeek([]workout.Workout{w}, analyses, ftp, today)
	if planned != want {
		t.Errorf("planned TSS = %v, want the step-by-step %v", planned, want)
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

	got := AdaptSessions(ws, nil, workout.RiderProfile{}, thursday, analyses, readiness.Assessment{})

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

	got := AdaptSessions(ws, nil, workout.RiderProfile{}, thursday, analyses, readiness.Assessment{})

	for _, c := range got {
		if c.StepDown {
			t.Fatalf("changes = %+v, want no step-down — the only later workout is a different zone", got)
		}
	}
}

func TestAnAlreadyAdjustedWorkoutIsNeverStepDownTargeted(t *testing.T) {
	touched := structured("thu-threshold", "Threshold 3×8", "2026-03-19", workout.ZoneThreshold, 4)
	touched.Description += " " + scheduler.AdjustedMarker + " moved already."
	touched.Flags = workout.LegacyFlags(touched)
	ws := []workout.Workout{
		structured("tue-threshold", "Threshold 3×12", "2026-03-17", workout.ZoneThreshold, 5),
		touched,
	}
	analyses := map[string]workout.SessionAnalysis{"tue-threshold": struggled()}

	got := AdaptSessions(ws, nil, workout.RiderProfile{}, thursday, analyses, readiness.Assessment{})

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

			got := AdaptSessions(ws, nil, workout.RiderProfile{}, thursday, analyses, readiness.Assessment{})
			for _, c := range got {
				if c.StepDown {
					t.Fatalf("changes = %+v, want no step-down for %s", got, tt.name)
				}
			}
		})
	}
}

// TestAFatigueSwapTargetIsNeverAlsoStepDownTargeted is the final review's
// fix for the bug the brief describes: a workout can be both readiness's own
// target for today (Downgrade) and stepDownTarget's own candidate (the next
// untouched same-zone workout after a struggled session) — before the fix,
// AdaptSessions handed that one workout two Changes in the same pass, and
// adaptRider applied them against a snapshot taken before the loop, so the
// later write silently clobbered the earlier one. Readiness's own rest/
// caution check now runs, and claims its target, before stepDownTarget.
//
// This used to drive the conflict from a fresh, very negative TSB passed
// directly to AdaptSessions; that rule now lives in internal/readiness, so
// the equivalent input is the Rest Assessment readiness.Assess would have
// produced from that same form — intent preserved.
func TestAFatigueSwapTargetIsNeverAlsoStepDownTargeted(t *testing.T) {
	ws := []workout.Workout{
		structured("tue-threshold", "Threshold 3×12", "2026-03-17", workout.ZoneThreshold, 5),
		structured("thu-threshold", "Threshold 3×8", "2026-03-19", workout.ZoneThreshold, 4),
	}
	analyses := map[string]workout.SessionAnalysis{"tue-threshold": struggled()}
	// Deep fatigue (a Rest verdict, standing in for the fresh, very negative
	// TSB that used to drive this directly) makes thu-threshold — dated
	// today and a hard/structured-zone session — readiness's own Downgrade
	// target too, on top of already being stepDownTarget's own candidate for
	// the Tuesday struggle.
	rest := readiness.Assessment{Verdict: readiness.Rest, Reasons: []string{"your form is −34"}}

	got := AdaptSessions(ws, nil, workout.RiderProfile{}, thursday, analyses, rest)

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
	got := AdaptSessions(ws, nil, profileAvailable("fri"), thursday, analyses, readiness.Assessment{})

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
	w.Flags = workout.LegacyFlags(w)
	if scheduler.IsGenerated(w) {
		t.Error("a workout carrying its own adjustment note must not be adjustable again")
	}
	if d, ok := scheduler.MovedFrom(w); !ok || d != "2026-03-17" {
		t.Errorf("MovedFrom = %q, %v — scheduling relies on this to not recreate the vacated day", d, ok)
	}
}

func enduranceToday(name string, seconds float64) workout.Workout {
	w := planned("today", name, "2026-03-19")
	w.Zone = workout.ZoneEndurance
	w.Steps = []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: seconds}}
	return w
}

func TestCautionShortensTodaysEnduranceRide(t *testing.T) {
	caution := readiness.Assessment{Verdict: readiness.Caution, Reasons: []string{"two things are off"}}
	for _, name := range []string{"Endurance ride", "Long ride", scheduler.ClimbingLongRideName} {
		got := AdaptSessions([]workout.Workout{enduranceToday(name, 10000)}, nil, workout.RiderProfile{}, thursday, nil, caution)
		if len(got) != 1 {
			t.Fatalf("%s: changes = %+v, want exactly one", name, got)
		}
		c := got[0]
		if c.ShortenSeconds != 8000 || c.StepDown || c.Downgrade || c.RecoveryRide {
			t.Errorf("%s: change = %+v, want only ShortenSeconds 8000", name, c)
		}
		if c.Why.Rule != why.ReadinessCaution {
			t.Errorf("%s: rule = %q, want readiness_caution", name, c.Why.Rule)
		}
	}
	// The floor: 80 % of 2000 s is 1600 s, so the ride keeps 1800 s.
	got := AdaptSessions([]workout.Workout{enduranceToday("Endurance ride", 2000)}, nil, workout.RiderProfile{}, thursday, nil, caution)
	if len(got) != 1 || got[0].ShortenSeconds != 1800 {
		t.Errorf("changes = %+v, want ShortenSeconds 1800 (the floor)", got)
	}
	// At or under the floor already: nothing to take away.
	for _, secs := range []float64{1800, 1200} {
		if got := AdaptSessions([]workout.Workout{enduranceToday("Endurance ride", secs)}, nil, workout.RiderProfile{}, thursday, nil, caution); len(got) != 0 {
			t.Errorf("%v s: changes = %+v, want none", secs, got)
		}
	}
	// An indoor session is sized from the outdoor steps it was made from.
	in := enduranceToday("Endurance ride", 3000)
	out := []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 10000}}
	in.Indoor, in.OutdoorSteps = true, &out
	if got := AdaptSessions([]workout.Workout{in}, nil, workout.RiderProfile{}, thursday, nil, caution); len(got) != 1 || got[0].ShortenSeconds != 8000 {
		t.Errorf("indoor: changes = %+v, want ShortenSeconds 8000 from the outdoor steps", got)
	}
	// Tomorrow's, a done one, a blackout day and an already-eased one are left.
	tomorrow := enduranceToday("Endurance ride", 10000)
	tomorrow.Date = "2026-03-20"
	if got := AdaptSessions([]workout.Workout{tomorrow}, nil, workout.RiderProfile{}, thursday, nil, caution); len(got) != 0 {
		t.Errorf("tomorrow: changes = %+v, want none", got)
	}
	if got := AdaptSessions([]workout.Workout{enduranceToday("Endurance ride", 10000)}, []workout.CompletedSession{rode("2026-03-19", 9000)}, workout.RiderProfile{}, thursday, nil, caution); len(got) != 0 {
		t.Errorf("done: changes = %+v, want none", got)
	}
	if got := AdaptSessionsAround([]workout.Workout{enduranceToday("Endurance ride", 10000)}, nil, workout.RiderProfile{}, thursday, nil, caution, map[string]bool{"2026-03-19": true}); len(got) != 0 {
		t.Errorf("blackout: changes = %+v, want none", got)
	}
	eased := enduranceToday("Endurance ride", 10000)
	eased.Description += " " + scheduler.AdjustedMarker + " Eased."
	eased.Flags = eased.Flags.WithAdaptation(why.ReadinessCaution)
	if got := AdaptSessions([]workout.Workout{eased}, nil, workout.RiderProfile{}, thursday, nil, caution); len(got) != 0 {
		t.Errorf("already eased: changes = %+v, want none", got)
	}
}

func TestRestSwapsTodaysLongRideForARecoveryRide(t *testing.T) {
	rest := readiness.Assessment{Verdict: readiness.Rest, Reasons: []string{"form is very low"}}
	for _, name := range []string{"Long ride", "Endurance ride"} {
		got := AdaptSessions([]workout.Workout{enduranceToday(name, 10000)}, nil, workout.RiderProfile{}, thursday, nil, rest)
		if len(got) != 1 || !got[0].RecoveryRide || got[0].Downgrade || got[0].StepDown || got[0].ShortenSeconds != 0 {
			t.Fatalf("%s: changes = %+v, want one RecoveryRide", name, got)
		}
		if got[0].Why.Rule != why.ReadinessRest {
			t.Errorf("%s: rule = %q, want readiness_rest", name, got[0].Why.Rule)
		}
	}
}

func TestHardSessionRulesUnchanged(t *testing.T) {
	hard := structured("today", "Threshold 3×8", "2026-03-19", workout.ZoneThreshold, 4)
	if got := AdaptSessions([]workout.Workout{hard}, nil, workout.RiderProfile{}, thursday, nil, readiness.Assessment{Verdict: readiness.Caution}); len(got) != 1 || !got[0].StepDown || got[0].ShortenSeconds != 0 || got[0].RecoveryRide {
		t.Errorf("caution: changes = %+v, want the step-down", got)
	}
	if got := AdaptSessions([]workout.Workout{hard}, nil, workout.RiderProfile{}, thursday, nil, readiness.Assessment{Verdict: readiness.Rest}); len(got) != 1 || !got[0].Downgrade || got[0].RecoveryRide {
		t.Errorf("rest: changes = %+v, want the downgrade", got)
	}
	// Push eases nothing, whatever is planned.
	for _, w := range []workout.Workout{hard, enduranceToday("Long ride", 10000)} {
		if got := AdaptSessions([]workout.Workout{w}, nil, workout.RiderProfile{}, thursday, nil, readiness.Assessment{Verdict: readiness.Push}); len(got) != 0 {
			t.Errorf("push: changes = %+v, want none", got)
		}
	}
}
