package lifeevents

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

var allDays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

func profile() workout.RiderProfile {
	return workout.RiderProfile{Rider: "r", AvailableDays: allDays, FTPWatts: 250}
}

func steps(minutes int) []workout.WorkoutStep {
	return []workout.WorkoutStep{{
		Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime,
		Seconds: float64(minutes * 60), Target: workout.TargetPower, TargetLow: 140, TargetHigh: 190,
	}}
}

// gen is a session the scheduler made and nobody touched.
func gen(id, date string, zone workout.Zone, name string, minutes int) workout.Workout {
	return workout.Workout{
		ID: id, Rider: "r", Sport: model.SportCycling, Name: name, GoalID: "g", Date: date,
		Description: scheduler.GeneratedDescription, Zone: zone, Steps: steps(minutes),
	}
}

func easy(id, date string, minutes int) workout.Workout {
	return gen(id, date, workout.ZoneEndurance, "Endurance ride", minutes)
}

func hard(id, date string, minutes int) workout.Workout {
	w := gen(id, date, workout.ZoneThreshold, "Threshold 3x12", minutes)
	w.Level = 4
	return w
}

// built is a session the rider made themselves.
func built(id, date string, minutes int) workout.Workout {
	return workout.Workout{ID: id, Rider: "r", Sport: model.SportCycling, Name: "My ride", Date: date, Description: "mine", Steps: steps(minutes)}
}

func travel(start, end string) Event {
	return Event{ID: "ev-" + start, Rider: "r", Kind: KindTravel, Start: start, End: end, Option: OptionNoBike}
}

func preview(events, previous []Event, ws ...workout.Workout) Diff {
	return Preview(Input{Events: events, Previous: previous, Workouts: ws, Profile: profile(), Now: wed, Caps: Capabilities{Indoor: true}})
}

func find(d Diff, id string) (Change, bool) {
	for _, c := range d.Changes {
		if c.ID == id {
			return c, true
		}
	}
	return Change{}, false
}

func mustFind(t *testing.T, d Diff, id string) Change {
	t.Helper()
	c, ok := find(d, id)
	if !ok {
		t.Fatalf("no change %q in %+v", id, ids(d))
	}
	return c
}

func ids(d Diff) []string {
	var out []string
	for _, c := range d.Changes {
		out = append(out, c.ID)
	}
	return out
}

func TestMoveGoesToTheNearestFreeDayAndALaterDayWinsATie(t *testing.T) {
	// Thursday 8 is the trip. Wednesday (today) and Friday are both a day away
	// and both free, so the later one wins; Saturday and Sunday are further.
	d := preview([]Event{travel("2026-10-08", "2026-10-08")}, nil, easy("e1", "2026-10-08", 60))
	c := mustFind(t, d, "move:e1")
	if c.ToDate != "2026-10-09" {
		t.Fatalf("moved to %s, want 2026-10-09 (the later of two equal days)", c.ToDate)
	}
	if !c.Default {
		t.Error("a move is ticked by default")
	}
	if c.Update == nil || c.Update.Date == nil || *c.Update.Date != "2026-10-09" {
		t.Fatalf("the change carries no update to apply: %+v", c.Update)
	}
}

func TestMoveNeverLandsBeforeToday(t *testing.T) {
	// Monday and Tuesday are free but already past: today is Wednesday.
	d := preview([]Event{travel("2026-10-07", "2026-10-09")}, nil, easy("e1", "2026-10-07", 60))
	c := mustFind(t, d, "move:e1")
	if c.ToDate < "2026-10-07" {
		t.Fatalf("moved to %s, which is before today", c.ToDate)
	}
	if c.ToDate != "2026-10-10" {
		t.Fatalf("moved to %s, want the nearest free day outside the trip, 2026-10-10", c.ToDate)
	}
}

func TestKeySessionPicksItsDayFirst(t *testing.T) {
	// Wednesday is taken. Friday's threshold session is the key one, so it
	// gets Saturday, nearest to it; Thursday's endurance ride takes what is
	// left, Sunday. Picking in date order would have given Saturday to Thursday.
	d := preview([]Event{travel("2026-10-08", "2026-10-09")}, nil,
		built("wed", "2026-10-07", 30),
		easy("thu", "2026-10-08", 60),
		hard("fri", "2026-10-09", 75),
	)
	if got := mustFind(t, d, "move:fri").ToDate; got != "2026-10-10" {
		t.Errorf("the key session moved to %s, want 2026-10-10", got)
	}
	if got := mustFind(t, d, "move:thu").ToDate; got != "2026-10-11" {
		t.Errorf("the endurance ride moved to %s, want 2026-10-11", got)
	}
}

func TestOneSessionPerDayAndTheLowestPriorityIsLost(t *testing.T) {
	// A trip Thursday to Saturday leaves one free day, Sunday. The long ride is
	// the week's key session and takes it; the threshold session and the easy
	// ride have no day and are removed with that reason.
	d := preview([]Event{travel("2026-10-08", "2026-10-10")}, nil,
		built("wed", "2026-10-07", 30),
		easy("thu", "2026-10-08", 60),
		hard("fri", "2026-10-09", 60),
		gen("sat", "2026-10-10", workout.ZoneEndurance, "Long ride", 150),
	)
	if got := mustFind(t, d, "move:sat").ToDate; got != "2026-10-11" {
		t.Errorf("the long ride moved to %s, want 2026-10-11", got)
	}
	for _, id := range []string{"remove:thu", "remove:fri"} {
		c := mustFind(t, d, id)
		if !strings.Contains(c.Reason, "no free day this week") {
			t.Errorf("%s reason %q does not say there was no free day", id, c.Reason)
		}
	}
}

func TestNeverTwoHardDaysInARow(t *testing.T) {
	// A hard session the day before Wednesday (Tuesday, already past) makes
	// Wednesday unusable for a hard one; Friday is occupied. So the threshold
	// session goes to Saturday, though Wednesday is nearer.
	d := preview([]Event{travel("2026-10-08", "2026-10-08")}, nil,
		hard("tue", "2026-10-06", 60),
		built("fri", "2026-10-09", 30),
		hard("thu", "2026-10-08", 60),
	)
	if got := mustFind(t, d, "move:thu").ToDate; got != "2026-10-10" {
		t.Fatalf("moved to %s, want 2026-10-10 (Wednesday follows a hard day)", got)
	}
}

func TestALongRideMayFollowAHardDay(t *testing.T) {
	d := preview([]Event{travel("2026-10-08", "2026-10-08")}, nil,
		hard("tue", "2026-10-06", 60),
		built("fri", "2026-10-09", 30),
		gen("thu", "2026-10-08", workout.ZoneEndurance, "Long ride", 150),
	)
	if got := mustFind(t, d, "move:thu").ToDate; got != "2026-10-07" {
		t.Fatalf("a long ride is not hard and may follow one: moved to %s, want 2026-10-07", got)
	}
}

func TestTwoMovedHardSessionsAreNotPlacedBackToBack(t *testing.T) {
	// Two threshold sessions leave a Thursday-Friday trip; Saturday and Sunday
	// are free. Placed on both, they would be back to back, so the second has
	// no legal day and is removed.
	d := preview([]Event{travel("2026-10-08", "2026-10-09")}, nil,
		built("wed", "2026-10-07", 30),
		hard("thu", "2026-10-08", 60),
		hard("fri", "2026-10-09", 60),
	)
	moved, removed := 0, 0
	for _, c := range d.Changes {
		switch c.Op {
		case OpMove:
			moved++
		case OpRemove:
			removed++
			if !strings.Contains(c.Reason, "two hard days in a row") {
				t.Errorf("reason %q should name the back-to-back rule", c.Reason)
			}
		}
	}
	if moved != 1 || removed != 1 {
		t.Fatalf("moved %d removed %d, want 1 and 1: %v", moved, removed, ids(d))
	}
}

func TestAMoveStaysInItsWeek(t *testing.T) {
	// A trip Thursday to Sunday: next Monday is free, but it is next week's.
	d := preview([]Event{travel("2026-10-08", "2026-10-11")}, nil,
		built("wed", "2026-10-07", 30),
		hard("thu", "2026-10-08", 60),
	)
	c := mustFind(t, d, "remove:thu")
	if !strings.Contains(c.Reason, "no free day this week") {
		t.Errorf("reason %q", c.Reason)
	}
	if _, moved := find(d, "move:thu"); moved {
		t.Fatal("the session was pushed into next week")
	}
}

func TestAMoveNeverAddsLoad(t *testing.T) {
	ws := []workout.Workout{
		built("wed", "2026-10-07", 30),
		easy("thu", "2026-10-08", 60),
		hard("fri", "2026-10-09", 75),
		gen("sat", "2026-10-10", workout.ZoneEndurance, "Long ride", 150),
	}
	d := preview([]Event{travel("2026-10-08", "2026-10-09")}, nil, ws...)
	var before, after float64
	removed := map[string]bool{}
	for _, c := range d.Changes {
		if c.Op == OpRemove {
			removed[c.WorkoutID] = true
		}
		if c.Op == OpAdd {
			t.Fatalf("a travel move must never add a session: %v", ids(d))
		}
	}
	for _, w := range ws {
		before += workout.PlannedSeconds(w.Steps)
		if !removed[w.ID] {
			after += workout.PlannedSeconds(w.Steps)
		}
	}
	if after > before {
		t.Fatalf("the week got heavier: %v -> %v seconds", before, after)
	}
}

func TestVacatedAndEventDaysAreNotFree(t *testing.T) {
	// Saturday was vacated by an earlier move (that session sits on Monday of
	// next week) and Sunday is inside another event, so a Thursday-Friday trip
	// has no free day at all this week.
	moved := easy("old", "2026-10-12", 60)
	moved.Description += " Rescheduled by you: moved from 2026-10-10."
	busy := Event{ID: "b", Kind: KindBusy, Start: "2026-10-11", End: "2026-10-11"}
	d := preview(
		[]Event{travel("2026-10-08", "2026-10-09"), busy},
		[]Event{busy},
		built("wed", "2026-10-07", 30),
		moved,
		easy("thu", "2026-10-08", 60),
	)
	c := mustFind(t, d, "remove:thu")
	if !strings.Contains(c.Reason, "no free day this week") {
		t.Errorf("reason %q", c.Reason)
	}
}

func TestAnFTPTestMovesLikeASession(t *testing.T) {
	test := workout.Workout{
		ID: "test", Rider: "r", Sport: model.SportCycling, Name: "FTP test: ramp", GoalID: "g", Date: "2026-10-08",
		Description: "FTP ramp test.", TestProtocol: "ramp", Steps: steps(40),
	}
	d := preview([]Event{travel("2026-10-08", "2026-10-08")}, nil, test)
	c := mustFind(t, d, "move:test")
	if c.ToDate != "2026-10-09" {
		t.Fatalf("the test moved to %s, want 2026-10-09", c.ToDate)
	}
	if strings.Contains(*c.Update.Description, scheduler.AdjustedMarker) {
		t.Error("an FTP test is not a generated session and carries no adjusted marker")
	}
	if _, ok := scheduler.MovedFrom(*c.Update.Description); !ok {
		t.Error("the move is not recorded in the form the scheduler reads")
	}
}

func TestTheMoveIsRecordedWhereTheSchedulerReadsIt(t *testing.T) {
	d := preview([]Event{travel("2026-10-08", "2026-10-08")}, nil, easy("e1", "2026-10-08", 60))
	desc := *mustFind(t, d, "move:e1").Update.Description
	from, ok := scheduler.MovedFrom(desc)
	if !ok || from != "2026-10-08" {
		t.Fatalf("MovedFrom(%q) = %q, %v", desc, from, ok)
	}
	if !strings.Contains(desc, "Rescheduled by a life event: moved from 2026-10-08.") {
		t.Errorf("description %q lacks the life-event note", desc)
	}
	moved := easy("e1", "2026-10-09", 60)
	moved.Description = desc
	if scheduler.IsGenerated(moved) {
		t.Error("a moved session must no longer be adaptable: it is the one automatic change")
	}
}

func TestWhatItLeavesAloneIsListedWithAnUntickedRemoval(t *testing.T) {
	adjusted := easy("adj", "2026-10-08", 60)
	adjusted.Description += " " + scheduler.AdjustedMarker + " eased."
	swapped := easy("swp", "2026-10-08", 60)
	swapped.Description += " " + scheduler.SwappedMarker + " easier."
	// ridden is dated Friday; the API says which sessions are ridden.
	d := Preview(Input{
		Events: []Event{travel("2026-10-08", "2026-10-09")},
		Workouts: []workout.Workout{
			built("mine", "2026-10-08", 45), adjusted, swapped,
			easy("ridden", "2026-10-09", 60), easy("past", "2026-10-06", 60),
		},
		Ridden:  map[string]bool{"ridden": true},
		Profile: profile(), Now: wed,
	})
	for _, id := range []string{"mine", "adj", "swp"} {
		c := mustFind(t, d, "remove:"+id)
		if c.Default {
			t.Errorf("remove:%s is ticked by default; a session the rider touched is theirs", id)
		}
	}
	left := map[string]bool{}
	for _, n := range d.LeftAlone {
		left[n.WorkoutID] = true
		if n.Reason == "" {
			t.Errorf("%s is left alone with no reason", n.WorkoutID)
		}
	}
	for _, id := range []string{"mine", "adj", "swp", "ridden"} {
		if !left[id] {
			t.Errorf("%s is not listed as left alone", id)
		}
	}
	if _, ok := find(d, "remove:ridden"); ok {
		t.Error("a ridden session offers no removal")
	}
	for _, id := range ids(d) {
		if strings.HasSuffix(id, ":past") {
			t.Errorf("a past session was touched: %s", id)
		}
	}
}

func TestGymKeepsCyclingAsShortIndoorEnduranceAndLeavesRunning(t *testing.T) {
	run := easy("run", "2026-10-09", 50)
	run.Sport = model.SportRunning
	run.Name = "Easy run"
	gym := travel("2026-10-08", "2026-10-09")
	gym.Option = OptionGym
	// Wednesday is occupied, so the key session (threshold) has a free day only
	// on the weekend. The endurance ride on the trip stays, indoors.
	d := preview([]Event{gym}, nil,
		built("wed", "2026-10-07", 30),
		hard("thu", "2026-10-08", 90),
		easy("fri", "2026-10-09", 180),
		run,
	)
	if got := mustFind(t, d, "move:thu").ToDate; got != "2026-10-10" {
		t.Errorf("the week's key session should move to a free home day, got %s", got)
	}
	c := mustFind(t, d, "indoor:fri")
	if c.Update == nil || c.Update.Indoor == nil || !*c.Update.Indoor {
		t.Fatalf("not converted to indoor: %+v", c.Update)
	}
	if *c.Update.Zone != workout.ZoneEndurance {
		t.Errorf("zone %s, want endurance", *c.Update.Zone)
	}
	if secs := workout.PlannedSeconds(*c.Update.Steps); secs > 3600 {
		t.Errorf("the indoor ride is %v seconds, want at most 3600", secs)
	}
	if c.Update.OutdoorSteps == nil {
		t.Error("the outdoor original is not kept for the revert")
	}
	if _, ok := find(d, "indoor:run"); ok {
		t.Error("a running session was converted; the rider can run")
	}
	if _, ok := find(d, "remove:run"); ok {
		t.Error("a running session was removed")
	}
	left := false
	for _, n := range d.LeftAlone {
		if n.WorkoutID == "run" {
			left = true
		}
	}
	if !left {
		t.Error("the running session is not listed as left alone")
	}
}

func TestGymFallsBackToIndoorWhenTheKeySessionHasNowhereToGo(t *testing.T) {
	gym := travel("2026-10-08", "2026-10-11")
	gym.Option = OptionGym
	d := preview([]Event{gym}, nil, built("wed", "2026-10-07", 30), hard("thu", "2026-10-08", 90))
	if _, ok := find(d, "move:thu"); ok {
		t.Fatal("there is no free day, so nothing can move")
	}
	if _, ok := find(d, "indoor:thu"); !ok {
		t.Fatalf("the key session should stay on its day as an indoor ride: %v", ids(d))
	}
}

func TestShorteningAnEventRefillsFreedDaysFromThePlan(t *testing.T) {
	prev := []Event{travel("2026-10-08", "2026-10-10")}
	now := []Event{travel("2026-10-08", "2026-10-08")}
	refill := []workout.CreateWorkoutRequest{
		{Date: "2026-10-06", Name: "past", Description: scheduler.GeneratedDescription},
		{Date: "2026-10-08", Name: "still away", Description: scheduler.GeneratedDescription},
		{Date: "2026-10-09", Name: "freed", Description: scheduler.GeneratedDescription},
		{Date: "2026-10-10", Name: "taken", Description: scheduler.GeneratedDescription},
		{Date: "2026-10-12", Name: "never covered", Description: scheduler.GeneratedDescription},
	}
	// Saturday already has a session of another goal.
	other := easy("other", "2026-10-10", 60)
	other.GoalID = "other-goal"
	d := Preview(Input{Events: now, Previous: prev, Workouts: []workout.Workout{other}, Refill: refill, Profile: profile(), Now: wed})
	if len(d.Changes) != 1 || d.Changes[0].ID != "add:2026-10-09" {
		t.Fatalf("changes %v, want only add:2026-10-09", ids(d))
	}
	c := d.Changes[0]
	if c.Create == nil || c.Create.Name != "freed" {
		t.Fatalf("the add carries no session to create: %+v", c.Create)
	}
}

func TestADeletedEventRefillsButNeverOnAVacatedDay(t *testing.T) {
	prev := []Event{travel("2026-10-08", "2026-10-09")}
	moved := easy("moved", "2026-10-11", 60)
	moved.Description += " Rescheduled by a life event: moved from 2026-10-09."
	refill := []workout.CreateWorkoutRequest{
		{Date: "2026-10-08", Name: "thu", Description: scheduler.GeneratedDescription},
		{Date: "2026-10-09", Name: "fri", Description: scheduler.GeneratedDescription},
	}
	d := Preview(Input{Events: nil, Previous: prev, Workouts: []workout.Workout{moved}, Refill: refill, Profile: profile(), Now: wed})
	if len(d.Changes) != 1 || d.Changes[0].ID != "add:2026-10-08" {
		t.Fatalf("changes %v, want only add:2026-10-08 (Friday was vacated by a move)", ids(d))
	}
}

func TestLengtheningAnEventRunsTheRulesOnTheNewDaysOnly(t *testing.T) {
	prev := []Event{travel("2026-10-08", "2026-10-08")}
	now := []Event{travel("2026-10-08", "2026-10-09")}
	// Thursday's session was dealt with when the trip was created (it is not
	// listed again); Friday's is new.
	d := preview(now, prev, easy("thu", "2026-10-08", 60), easy("fri", "2026-10-09", 60), built("wed", "2026-10-07", 30))
	if _, ok := find(d, "move:thu"); ok {
		t.Error("Thursday's session was handled again")
	}
	if _, ok := find(d, "move:fri"); !ok {
		t.Errorf("Friday's session was not handled: %v", ids(d))
	}
}

func TestPreviewIsDeterministicAndOrdered(t *testing.T) {
	ws := []workout.Workout{
		built("wed", "2026-10-07", 30),
		easy("thu", "2026-10-08", 60),
		hard("fri", "2026-10-09", 75),
		built("mine", "2026-10-09", 45),
	}
	ev := []Event{travel("2026-10-08", "2026-10-09")}
	a, b := preview(ev, nil, ws...), preview(ev, nil, ws...)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("the same inputs gave different diffs")
	}
	for i := 1; i < len(a.Changes); i++ {
		if a.Changes[i-1].Date > a.Changes[i].Date {
			t.Fatalf("changes are not in date order: %v", ids(a))
		}
	}
}

func TestPreviewReadsTodayInTheClocksOwnZone(t *testing.T) {
	// 01:00 on Wednesday 7 October in Brussels is 23:00 on Tuesday the 6th in
	// UTC. Today follows the clock's own zone, never the machine's: Tuesday's
	// session is already past in Brussels and still ahead in UTC.
	ws := []workout.Workout{easy("tue", "2026-10-06", 60), built("fri", "2026-10-09", 30)}
	ev := []Event{travel("2026-10-06", "2026-10-06")}
	early := time.Date(2026, 10, 7, 1, 0, 0, 0, brussels)
	inBrussels := Preview(Input{Events: ev, Workouts: ws, Profile: profile(), Now: early})
	if _, ok := find(inBrussels, "move:tue"); ok {
		t.Fatalf("Tuesday's session is past on Wednesday in Brussels but was handled: %v", ids(inBrussels))
	}
	inUTC := Preview(Input{Events: ev, Workouts: ws, Profile: profile(), Now: early.UTC()})
	if _, ok := find(inUTC, "move:tue"); !ok {
		t.Fatalf("Tuesday's session is still today in UTC but was not handled: %v", ids(inUTC))
	}
}
