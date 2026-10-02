package lifeevents

import (
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func ill(start, end, option string) Event {
	return Event{ID: "ill-" + start, Rider: "r", Kind: KindIllness, Start: start, End: end, Option: option}
}

func longRide(id, date string, minutes int) workout.Workout {
	return gen(id, date, workout.ZoneEndurance, "Long ride", minutes)
}

func TestRampTable(t *testing.T) {
	cases := []struct {
		name        string
		e           Event
		easy, until int
		ok          bool
	}{
		{"mild, 2 days", ill("2026-10-08", "2026-10-09", OptionMild), 1, 3, true},
		{"mild, 20 days", ill("2026-10-08", "2026-10-27", OptionMild), 1, 3, true},
		{"proper, 1 day", ill("2026-10-08", "2026-10-08", OptionProper), 2, 7, true},
		{"proper, 4 days", ill("2026-10-08", "2026-10-11", OptionProper), 2, 7, true},
		{"proper, 5 days", ill("2026-10-08", "2026-10-12", OptionProper), 3, 7, true},
		{"proper, 9 days", ill("2026-10-08", "2026-10-16", OptionProper), 3, 7, true},
		{"proper, 10 days", ill("2026-10-08", "2026-10-17", OptionProper), 3, 14, true},
		{"proper, 14 days", ill("2026-10-08", "2026-10-21", OptionProper), 3, 14, true},
		{"the option defaults to proper", ill("2026-10-08", "2026-10-11", ""), 2, 7, true},
		{"travel without a bike, 6 days: no ramp", travel("2026-10-08", "2026-10-13"), 0, 0, false},
		{"travel without a bike, 7 days: like mild", travel("2026-10-08", "2026-10-14"), 1, 3, true},
		{"travel with a gym: no ramp", Event{Kind: KindTravel, Option: OptionGym, Start: "2026-10-08", End: "2026-10-30"}, 0, 0, false},
		{"busy: no ramp", Event{Kind: KindBusy, Start: "2026-10-08", End: "2026-10-30"}, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			easyDays, until, ok := Ramp(c.e)
			if easyDays != c.easy || until != c.until || ok != c.ok {
				t.Fatalf("Ramp = %d, %d, %v; want %d, %d, %v", easyDays, until, ok, c.easy, c.until, c.ok)
			}
		})
	}
}

func TestProperIllnessRemovesEverythingAndMakesNothingUp(t *testing.T) {
	d := preview([]Event{ill("2026-10-08", "2026-10-10", OptionProper)}, nil,
		easy("thu", "2026-10-08", 60),
		hard("fri", "2026-10-09", 60),
		longRide("sat", "2026-10-10", 150),
		easy("sun", "2026-10-11", 60),
	)
	for _, id := range []string{"remove:thu", "remove:fri", "remove:sat"} {
		if c := mustFind(t, d, id); !c.Default {
			t.Errorf("%s should be ticked", id)
		}
	}
	for _, c := range d.Changes {
		if c.Op == OpMove || c.Op == OpAdd {
			t.Errorf("sickness is not made up: %s", c.ID)
		}
	}
	if _, ok := find(d, "remove:sun"); ok {
		t.Error("a session after the illness was removed")
	}
}

func TestMildIllnessRemovesHardAndCapsEnduranceAtFortyFiveMinutes(t *testing.T) {
	d := preview([]Event{ill("2026-10-08", "2026-10-10", OptionMild)}, nil,
		hard("thu", "2026-10-08", 60),
		easy("fri", "2026-10-09", 120),
		easy("short", "2026-10-09", 30),
		longRide("sat", "2026-10-10", 150),
	)
	mustFind(t, d, "remove:thu")
	for _, id := range []string{"shorten:fri", "shorten:sat"} {
		c := mustFind(t, d, id)
		if secs := workout.PlannedSeconds(*c.Update.Steps); secs > 45*60 {
			t.Errorf("%s is %v seconds, want at most 2700", id, secs)
		}
		if !strings.Contains(*c.Update.Description, scheduler.AdjustedMarker) {
			t.Errorf("%s carries no adjusted marker", id)
		}
		if !c.ReKeepIndoor {
			t.Errorf("%s must ask apply to keep an indoor session indoor", id)
		}
	}
	if _, ok := find(d, "shorten:short"); ok {
		t.Error("a 30 minute ride is already under the cap and must be left as it is")
	}
}

// rampWeek is next week, Monday 12 to Sunday 18 October, plus Monday the 19th.
func rampWeek() []workout.Workout {
	return []workout.Workout{
		hard("d1", "2026-10-12", 90),
		easy("d2", "2026-10-13", 120),
		hard("d3", "2026-10-14", 60),
		hard("d4", "2026-10-15", 60),
		longRide("d5", "2026-10-16", 180),
		easy("d6", "2026-10-17", 60),
		hard("d7", "2026-10-18", 60),
		hard("d8", "2026-10-19", 60),
	}
}

func TestProperRampForAShortIllnessDayByDay(t *testing.T) {
	// Four days ill (8 to 11 October): ramp from Monday the 12th, two easy
	// days, then hard sessions eased one rung until day 7, Sunday the 18th.
	d := preview([]Event{ill("2026-10-08", "2026-10-11", OptionProper)}, nil, rampWeek()...)

	// Day 1: a hard session becomes the easy variant, capped at an hour.
	c := mustFind(t, d, "ease:d1")
	if *c.Update.Zone != workout.ZoneEndurance {
		t.Errorf("day 1 zone %s, want endurance", *c.Update.Zone)
	}
	if secs := workout.PlannedSeconds(*c.Update.Steps); secs > 3600 {
		t.Errorf("day 1 is %v seconds, want at most 3600", secs)
	}
	// Day 2: an endurance ride is only capped.
	if secs := workout.PlannedSeconds(*mustFind(t, d, "shorten:d2").Update.Steps); secs > 3600 {
		t.Errorf("day 2 is %v seconds, want at most 3600", secs)
	}
	// Days 3, 4 and 7: one rung down in the same zone.
	for _, id := range []string{"ease:d3", "ease:d4", "ease:d7"} {
		u := mustFind(t, d, id).Update
		if *u.Zone != workout.ZoneThreshold || *u.Level != 3 {
			t.Errorf("%s -> zone %s level %v, want threshold level 3", id, *u.Zone, *u.Level)
		}
	}
	// Day 5: a long ride is not hard, and outside the easy window it is left alone.
	if _, ok := find(d, "shorten:d5"); ok {
		t.Error("a long ride after the easy window was capped")
	}
	// Day 6: an easy ride is untouched; day 8 is past the ramp.
	for _, id := range []string{"shorten:d6", "ease:d6", "ease:d8", "shorten:d8"} {
		if _, ok := find(d, id); ok {
			t.Errorf("%s should not exist", id)
		}
	}
	for _, c := range d.Changes {
		if c.Op == OpEase || c.Op == OpShort {
			if !strings.Contains(*c.Update.Description, scheduler.AdjustedMarker) ||
				!strings.Contains(*c.Update.Description, "Life event") {
				t.Errorf("%s description %q lacks the marker or the life-event note", c.ID, *c.Update.Description)
			}
			if !c.ReKeepIndoor {
				t.Errorf("%s must keep an indoor session indoor", c.ID)
			}
		}
	}
}

func TestRampWindowsOfEachRow(t *testing.T) {
	cases := []struct {
		name        string
		e           Event
		easyDayOne  string // the first date that is an easy day, ramp day 1
		lastRung    string // the last date hard sessions are eased one rung
		afterRamp   string // the first date left alone
		easyThrough string // the last easy day
	}{
		{"mild", ill("2026-10-08", "2026-10-09", OptionMild), "2026-10-10", "2026-10-12", "2026-10-13", "2026-10-10"},
		{"proper d=5", ill("2026-10-08", "2026-10-12", OptionProper), "2026-10-13", "2026-10-19", "2026-10-20", "2026-10-15"},
		{"proper d=9", ill("2026-10-08", "2026-10-16", OptionProper), "2026-10-17", "2026-10-23", "2026-10-24", "2026-10-19"},
		{"proper d=10", ill("2026-10-08", "2026-10-17", OptionProper), "2026-10-18", "2026-10-31", "2026-11-01", "2026-10-20"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := preview([]Event{c.e}, nil,
				hard("first", c.easyDayOne, 60),
				hard("lastEasy", c.easyThrough, 60),
				hard("lastRung", c.lastRung, 60),
				hard("after", c.afterRamp, 60),
			)
			if u := mustFind(t, d, "ease:first").Update; *u.Zone != workout.ZoneEndurance {
				t.Errorf("ramp day 1 was not made easy: %s", *u.Zone)
			}
			if u := mustFind(t, d, "ease:lastEasy").Update; *u.Zone != workout.ZoneEndurance {
				t.Errorf("the last easy day was not made easy: %s", *u.Zone)
			}
			if u := mustFind(t, d, "ease:lastRung").Update; *u.Zone != workout.ZoneThreshold || *u.Level != 3 {
				t.Errorf("the last ramp day should be one rung down, got %s level %v", *u.Zone, *u.Level)
			}
			if _, ok := find(d, "ease:after"); ok {
				t.Error("a session after the ramp was changed")
			}
		})
	}
}

func TestRampCapsOnEasyDays(t *testing.T) {
	d := preview([]Event{ill("2026-10-08", "2026-10-11", OptionProper)}, nil,
		longRide("long", "2026-10-12", 240),
		easy("alreadyEasy", "2026-10-13", 40),
	)
	c := mustFind(t, d, "shorten:long")
	if secs := workout.PlannedSeconds(*c.Update.Steps); secs > 90*60 || secs < 85*60 {
		t.Errorf("a long ride on an easy day should be capped at 90 minutes, got %v seconds", secs)
	}
	if *c.Update.Name != "Long ride" {
		t.Errorf("it is still the long ride, named %q", *c.Update.Name)
	}
	if _, ok := find(d, "shorten:alreadyEasy"); ok {
		t.Error("a 40 minute ride is under the cap and must be left alone")
	}
}

func TestRampNeverRewritesWhatTheRiderOrAnEarlierRuleTouched(t *testing.T) {
	adjusted := hard("adj", "2026-10-12", 60)
	adjusted.Description += " Rescheduled by a life event: moved from 2026-10-09. " + scheduler.AdjustedMarker + " moved."
	swapped := hard("swp", "2026-10-13", 60)
	swapped.Description += " " + scheduler.SwappedMarker + " harder."
	mine := built("mine", "2026-10-14", 60)
	ridden := hard("ridden", "2026-10-15", 60)
	d := Preview(Input{
		Events:   []Event{ill("2026-10-08", "2026-10-11", OptionProper)},
		Workouts: []workout.Workout{adjusted, swapped, mine, ridden},
		Ridden:   map[string]bool{"ridden": true},
		Profile:  profile(), Now: wed,
	})
	if len(d.Changes) != 0 {
		t.Fatalf("a touched session was changed: %v", ids(d))
	}
}

func TestASessionTheRiderKeptThroughTheReturnIsNotRampedAgain(t *testing.T) {
	kept := hard("kept", "2026-10-12", 90)
	kept.Description += " " + KeptMarker
	d := preview([]Event{ill("2026-10-08", "2026-10-11", OptionProper)}, nil, kept, hard("other", "2026-10-13", 90))
	if _, ok := find(d, "ease:kept"); ok {
		t.Fatal("the ramp eased a session the rider unticked")
	}
	if _, ok := find(d, "ease:other"); !ok {
		t.Fatal("the control session was not eased")
	}
}

func TestRampChangesSayWhichRampTheyBelongTo(t *testing.T) {
	d := preview([]Event{ill("2026-10-08", "2026-10-11", OptionProper)}, nil,
		hard("d1", "2026-10-12", 60), hard("d3", "2026-10-14", 60))
	c := mustFind(t, d, "ease:d3")
	want := RampInfo{Kind: KindIllness, Option: OptionProper, End: "2026-10-11", Day: 3, EasyDays: 2, UntilDay: 7}
	if c.Ramp == nil || *c.Ramp != want {
		t.Fatalf("ramp = %+v, want %+v", c.Ramp, want)
	}
	if c := mustFind(t, d, "ease:d1"); c.Ramp == nil || c.Ramp.Day != 1 {
		t.Errorf("day one ramp = %+v", c.Ramp)
	}
}

func TestEveryLifeEventNoteIsRecognisedAsOne(t *testing.T) {
	// Mild illness shortens, a trip moves, the gym converts: each leaves a
	// description Touched knows, so Replan and a later edit can tell them from
	// a readiness easing.
	cases := map[string]Diff{
		"shorten": preview([]Event{ill("2026-10-08", "2026-10-09", OptionMild)}, nil, easy("a", "2026-10-08", 120)),
		"move":    preview([]Event{travel("2026-10-08", "2026-10-08")}, nil, easy("a", "2026-10-08", 60)),
	}
	gym := travel("2026-10-08", "2026-10-09")
	gym.Option = OptionGym
	cases["indoor"] = preview([]Event{gym}, nil, built("w", "2026-10-07", 30),
		gen("long", "2026-10-08", workout.ZoneEndurance, "Long ride", 180), easy("a", "2026-10-09", 90))
	for name, d := range cases {
		var got bool
		for _, c := range d.Changes {
			if c.Update != nil && c.Update.Description != nil && c.Op != OpRemove {
				got = true
				if !Touched(*c.Update.Description) {
					t.Errorf("%s: %q is not recognised as a life event's", name, *c.Update.Description)
				}
			}
		}
		if !got {
			t.Errorf("%s: no change to check", name)
		}
	}
	if Touched(scheduler.GeneratedDescription + " " + scheduler.AdjustedMarker + " Eased one level — HRV low") {
		t.Error("a readiness easing was taken for a life event's")
	}
	if !Touched("x " + KeptMarker) {
		t.Error("a kept session is not Touched")
	}
}

func TestNoRideReason(t *testing.T) {
	proper := ill("2026-10-07", "2026-10-09", OptionProper)
	mild := ill("2026-10-07", "2026-10-09", OptionMild)
	gym := travel("2026-10-07", "2026-10-09")
	gym.Option = OptionGym
	cases := []struct {
		name   string
		events []Event
		date   string
		want   bool
	}{
		{"proper illness", []Event{proper}, "2026-10-08", true},
		{"no bike", []Event{travel("2026-10-07", "2026-10-09")}, "2026-10-08", true},
		{"mild illness", []Event{mild}, "2026-10-08", false},
		{"hotel gym", []Event{gym}, "2026-10-08", false},
		{"busy", []Event{{Kind: KindBusy, Start: "2026-10-07", End: "2026-10-09"}}, "2026-10-08", false},
		{"outside the event", []Event{proper}, "2026-10-10", false},
	}
	for _, c := range cases {
		if got := NoRideReason(c.events, c.date) != ""; got != c.want {
			t.Errorf("%s: refused = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestASessionOnlyAnEarlierRampTouchedIsStillThePlansToRemove(t *testing.T) {
	// An illness over Thursday eased Saturday's session (ramp day 2). Making the
	// illness longer so it covers Saturday must still remove that session, not
	// treat it as one the rider touched.
	ramped := easy("sat", "2026-10-10", 60)
	ramped.Description += " " + scheduler.AdjustedMarker + " Life event: eased after illness (return to training)."
	prev := []Event{ill("2026-10-08", "2026-10-08", OptionProper)}
	now := []Event{ill("2026-10-08", "2026-10-10", OptionProper)}
	d := preview(now, prev, ramped)
	c := mustFind(t, d, "remove:sat")
	if !c.Default {
		t.Error("removing a ramp-touched session should be ticked")
	}
	if len(d.LeftAlone) != 0 {
		t.Errorf("left alone: %+v", d.LeftAlone)
	}
}

func TestLongIllnessAddsTheClinicianLine(t *testing.T) {
	short := preview([]Event{ill("2026-10-08", "2026-10-20", OptionProper)}, nil)
	if len(short.Advice) != 0 {
		t.Errorf("13 days ill gave advice %v", short.Advice)
	}
	long := preview([]Event{ill("2026-10-08", "2026-10-21", OptionProper)}, nil)
	if len(long.Advice) != 1 || !strings.Contains(strings.ToLower(long.Advice[0]), "clinician") {
		t.Fatalf("14 days ill should advise seeing a clinician: %v", long.Advice)
	}
}

func testWorkout(id, date string) workout.Workout {
	return workout.Workout{
		ID: id, Rider: "r", Sport: model.SportCycling, Name: "FTP test: ramp", GoalID: "g", Date: date,
		Description: "FTP ramp test.", TestProtocol: "ramp", Steps: steps(40),
	}
}

func TestAnFTPTestInTheRampWindowMovesToTheDayAfterIt(t *testing.T) {
	// Ill 8 to 11 October: the ramp window is the 12th to the 18th, so the
	// test moves to the first eligible day on or after the 19th.
	d := preview([]Event{ill("2026-10-08", "2026-10-11", OptionProper)}, nil, testWorkout("test", "2026-10-14"))
	c := mustFind(t, d, "move:test")
	if c.ToDate != "2026-10-19" {
		t.Fatalf("the test moved to %s, want 2026-10-19", c.ToDate)
	}
}

func TestAnFTPTestSkipsADayAfterAKeySession(t *testing.T) {
	d := preview([]Event{ill("2026-10-08", "2026-10-11", OptionProper)}, nil,
		testWorkout("test", "2026-10-14"),
		hard("key", "2026-10-19", 60),
	)
	if got := mustFind(t, d, "move:test").ToDate; got != "2026-10-21" {
		// The 19th holds the key session itself, the 20th follows it.
		t.Fatalf("the test moved to %s, want 2026-10-21", got)
	}
}

func TestAnFTPTestInTheIllnessItselfMovesAfterTheRamp(t *testing.T) {
	d := preview([]Event{ill("2026-10-08", "2026-10-11", OptionProper)}, nil, testWorkout("test", "2026-10-09"))
	if got := mustFind(t, d, "move:test").ToDate; got != "2026-10-19" {
		t.Fatalf("the test moved to %s, want 2026-10-19", got)
	}
}

func TestAnFTPTestWithNoEligibleDayIsRemoved(t *testing.T) {
	p := profile()
	p.AvailableDays = []string{"mon"}
	d := Preview(Input{
		Events:   []Event{ill("2026-10-08", "2026-10-11", OptionProper)},
		Workouts: []workout.Workout{testWorkout("test", "2026-10-14"), built("b1", "2026-10-19", 30), built("b2", "2026-10-26", 30), built("b3", "2026-11-02", 30), built("b4", "2026-11-09", 30), built("b5", "2026-11-16", 30)},
		Profile:  p, Now: wed,
	})
	c := mustFind(t, d, "remove:test")
	if !strings.Contains(strings.ToLower(c.Reason), "suggest") {
		t.Errorf("reason %q should say another test is suggested", c.Reason)
	}
}

func TestARetroactiveIllnessYieldsOnlyTheRamp(t *testing.T) {
	// Ill 3 to 5 October (it is Wednesday the 7th): the days are past, so
	// nothing is removed, and the ramp (days 1 to 7 are 6 to 12 October) still
	// applies to what is left of it.
	d := preview([]Event{ill("2026-10-03", "2026-10-05", OptionProper)}, nil,
		hard("past", "2026-10-06", 60),
		hard("today", "2026-10-07", 60),
		hard("rung", "2026-10-09", 60),
	)
	for _, c := range d.Changes {
		if c.Op == OpRemove {
			t.Errorf("a retroactive event removed %s", c.ID)
		}
	}
	if _, ok := find(d, "ease:past"); ok {
		t.Error("a past session was changed")
	}
	if u := mustFind(t, d, "ease:today").Update; *u.Zone != workout.ZoneEndurance {
		t.Errorf("ramp day 2 (today) should be easy, got %s", *u.Zone)
	}
	if u := mustFind(t, d, "ease:rung").Update; *u.Level != 3 {
		t.Errorf("ramp day 4 should be one rung down, got level %v", *u.Level)
	}
}

func TestTravelOfAWeekGetsTheMildRamp(t *testing.T) {
	// No bike from 8 to 14 October (7 days): ramp from the 15th, one easy day,
	// then one rung until day 3, the 17th.
	d := preview([]Event{travel("2026-10-08", "2026-10-14")}, nil,
		hard("d1", "2026-10-15", 60),
		hard("d2", "2026-10-16", 60),
		hard("d3", "2026-10-17", 60),
		hard("d4", "2026-10-18", 60),
	)
	if u := mustFind(t, d, "ease:d1").Update; *u.Zone != workout.ZoneEndurance {
		t.Errorf("day 1 should be easy, got %s", *u.Zone)
	}
	for _, id := range []string{"ease:d2", "ease:d3"} {
		if u := mustFind(t, d, id).Update; *u.Zone != workout.ZoneThreshold || *u.Level != 3 {
			t.Errorf("%s should be one rung down, got %s level %v", id, *u.Zone, *u.Level)
		}
	}
	if _, ok := find(d, "ease:d4"); ok {
		t.Error("day 4 is past the ramp")
	}

	shortTrip := preview([]Event{travel("2026-10-08", "2026-10-13")}, nil, hard("d1", "2026-10-14", 60))
	if _, ok := find(shortTrip, "ease:d1"); ok {
		t.Error("a six day trip must not get a ramp")
	}
}

func TestAnUnchangedEventIsRampedOnlyWhenAskedTo(t *testing.T) {
	ev := []Event{ill("2026-10-08", "2026-10-11", OptionProper)}
	ws := []workout.Workout{hard("d1", "2026-10-12", 60)}
	quiet := Preview(Input{Events: ev, Previous: ev, Workouts: ws, Profile: profile(), Now: wed})
	if len(quiet.Changes) != 0 {
		t.Fatalf("an unrelated edit re-ramped an old illness: %v", ids(quiet))
	}
	all := Preview(Input{Events: ev, Previous: ev, Workouts: ws, Profile: profile(), Now: wed, RampAll: true})
	mustFind(t, all, "ease:d1")
	for _, c := range all.Changes {
		if c.Op != OpEase && c.Op != OpShort {
			t.Errorf("RampAll produced %s, which is not a ramp change", c.ID)
		}
	}
}

func TestMovedSessionsAvoidTheRampWindow(t *testing.T) {
	// Ill until 6 October: the ramp window is the 7th to 13th. A trip on
	// Wednesday the 14th moves its session; Tuesday the 13th is nearer than
	// Friday the 16th (Thursday is taken) but is a ramp day, so it is not used.
	ev := ill("2026-10-01", "2026-10-06", OptionProper)
	d := preview([]Event{ev, travel("2026-10-14", "2026-10-14")}, []Event{ev},
		easy("wed", "2026-10-14", 60),
		built("thu", "2026-10-15", 30),
	)
	if got := mustFind(t, d, "move:wed").ToDate; got != "2026-10-16" {
		t.Fatalf("moved to %s, want 2026-10-16 (the 13th is in the return ramp)", got)
	}
}

func TestNoTestDaysCoverTheEventAndItsReturnWindow(t *testing.T) {
	got := NoTestDays([]Event{ill("2026-10-08", "2026-10-09", OptionMild)})
	for _, d := range []string{"2026-10-08", "2026-10-09", "2026-10-10", "2026-10-12"} {
		if !got[d] {
			t.Errorf("%s should not be offered for a test", d)
		}
	}
	if got["2026-10-13"] || got["2026-10-07"] {
		t.Error("a day outside the event and its ramp was excluded")
	}
}

func TestOneRungEasier(t *testing.T) {
	w := hard("w", "2026-10-12", 60)
	req, ok := scheduler.OneRungEasier(w, profile())
	if !ok {
		t.Fatal("a level 4 threshold session has a rung below it")
	}
	if req.Zone != workout.ZoneThreshold || req.Level != 3 {
		t.Fatalf("got %s level %v, want threshold level 3", req.Zone, req.Level)
	}
	w.Level = 1
	if _, ok := scheduler.OneRungEasier(w, profile()); ok {
		t.Error("level 1 has no rung below it")
	}
	w.Zone, w.Level = workout.ZoneEndurance, 0
	if _, ok := scheduler.OneRungEasier(w, profile()); ok {
		t.Error("an endurance session has no ladder")
	}
}
