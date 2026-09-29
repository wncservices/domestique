package testschedule

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// brussels is an explicit zone so no test depends on the process's TZ.
var brussels = time.FixedZone("CET", 3600)

// wed is Wednesday 2026-03-18, in the week that starts Monday 2026-03-16.
var wed = time.Date(2026, 3, 18, 10, 0, 0, 0, brussels)

type wk struct {
	phase    periodization.Phase
	recovery bool
}

// planFrom builds a plan whose first week starts Monday 2026-03-16.
func planFrom(weeks ...wk) *periodization.Plan {
	start := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	p := &periodization.Plan{}
	for i, w := range weeks {
		p.Weeks = append(p.Weeks, periodization.Week{
			Number: i + 1, StartDate: start.AddDate(0, 0, 7*i).Format("2006-01-02"),
			Phase: w.phase, Recovery: w.recovery,
		})
	}
	return p
}

func base() wk  { return wk{phase: periodization.PhaseBase} }
func build() wk { return wk{phase: periodization.PhaseBuild} }
func rec() wk   { return wk{phase: periodization.PhaseBase, recovery: true} }
func taper() wk { return wk{phase: periodization.PhaseTaper} }
func peak() wk  { return wk{phase: periodization.PhasePeak} }

// input is a cycling rider who trains Tue/Thu/Sat with a fresh, known FTP, so
// only the plan can make a test worth suggesting.
func input(plan *periodization.Plan) Input {
	return Input{
		Plan: plan,
		Profile: workout.RiderProfile{
			Rider: "wilant", FTPWatts: 250, AvailableDays: []string{"tue", "thu", "sat"},
			FTPVerifiedAt: "2026-03-10",
		},
		HasPower: true,
		Now:      wed,
	}
}

func mustSuggest(t *testing.T, in Input) *Suggestion {
	t.Helper()
	s := Suggest(in)
	if s == nil {
		t.Fatal("want a suggestion, got nil")
	}
	return s
}

func TestNoFTPSuggestsATestThisWeek(t *testing.T) {
	in := input(planFrom(base(), base(), base()))
	in.Profile.FTPWatts = 0
	in.Profile.FTPVerifiedAt = ""
	s := mustSuggest(t, in)
	if s.Reason != ReasonNoFTP || s.Message != "Set your FTP with a test" {
		t.Errorf("got %+v", s)
	}
	if s.Date != "2026-03-19" { // Thursday: the first available day from tomorrow
		t.Errorf("date = %s, want 2026-03-19", s.Date)
	}
	if s.Recommended != "twenty_minute" {
		t.Errorf("recommended = %q, want twenty_minute when FTP is unknown", s.Recommended)
	}
}

func TestNoPowerEvidenceMeansNoSuggestion(t *testing.T) {
	in := input(planFrom(base(), base()))
	in.Profile.FTPWatts = 0
	in.HasPower = false
	if s := Suggest(in); s != nil {
		t.Errorf("got %+v for a rider with no power meter and no FTP", s)
	}
	in.Profile.FTPWatts = 250 // an FTP on file is evidence enough
	in.Profile.FTPVerifiedAt = "2026-01-01"
	if Suggest(in) == nil {
		t.Error("an FTP on file should count as power evidence")
	}
}

func TestAfterRecoveryWeekSuggestsTheFollowingWeek(t *testing.T) {
	// Now is in the recovery week (index 3); the week after is index 4.
	plan := planFrom(base(), base(), base(), rec(), base(), base())
	in := input(plan)
	in.Now = time.Date(2026, 4, 8, 10, 0, 0, 0, brussels) // Wed in the recovery week
	s := mustSuggest(t, in)
	if s.Reason != ReasonAfterRecovery {
		t.Errorf("reason = %s, want after_recovery", s.Reason)
	}
	if s.Date != "2026-04-14" { // the Tuesday after the recovery week
		t.Errorf("date = %s, want 2026-04-14", s.Date)
	}
	if want := "Time for an FTP test. Tuesday after your recovery week would be ideal"; s.Message != want {
		t.Errorf("message = %q, want %q", s.Message, want)
	}
	if s.Recommended != "ramp" {
		t.Errorf("recommended = %q, want ramp when FTP is known", s.Recommended)
	}
}

func TestAfterRecoveryAlsoFiresWhenTheRecoveryWeekIsBehindUs(t *testing.T) {
	plan := planFrom(base(), base(), rec(), base(), base())
	in := input(plan)
	in.Now = time.Date(2026, 4, 6, 10, 0, 0, 0, brussels) // Monday of the week after recovery (index 3)
	s := mustSuggest(t, in)
	if s.Reason != ReasonAfterRecovery || s.Date != "2026-04-07" {
		t.Errorf("got %+v", s)
	}
}

func TestBlockStartIsTheFirstBuildWeekOnly(t *testing.T) {
	plan := planFrom(base(), base(), build(), build(), build())
	// In the last Base week the next week starts the block.
	in := input(plan)
	in.Now = time.Date(2026, 3, 25, 10, 0, 0, 0, brussels)
	s := mustSuggest(t, in)
	if s.Reason != ReasonBlockStart || s.Date != "2026-03-31" {
		t.Errorf("got %+v, want block_start on 2026-03-31", s)
	}
	if want := "A new block starts. Test now so its targets are right"; s.Message != want {
		t.Errorf("message = %q", s.Message)
	}
	// Inside the first Build week itself: still block_start, this week.
	in.Now = time.Date(2026, 4, 1, 10, 0, 0, 0, brussels)
	s = mustSuggest(t, in)
	if s.Reason != ReasonBlockStart || s.Date != "2026-04-02" {
		t.Errorf("got %+v, want block_start on 2026-04-02", s)
	}
	// The second Build week is not a block start.
	in.Now = time.Date(2026, 4, 8, 10, 0, 0, 0, brussels)
	if s := Suggest(in); s != nil {
		t.Errorf("second Build week suggested %+v", s)
	}
}

func TestStaleFTPBoundaryIsMoreThanFortyTwoDays(t *testing.T) {
	in := input(planFrom(base(), base(), base()))
	today := "2026-03-18"
	_ = today
	in.Profile.FTPVerifiedAt = "2026-02-04" // exactly 42 days before
	if s := Suggest(in); s != nil {
		t.Errorf("42 days is not stale yet, got %+v", s)
	}
	in.Profile.FTPVerifiedAt = "2026-02-03" // 43 days
	s := mustSuggest(t, in)
	if s.Reason != ReasonStale {
		t.Errorf("reason = %s, want stale", s.Reason)
	}
	if want := "It's been 6 weeks since your FTP was checked"; s.Message != want {
		t.Errorf("message = %q, want %q", s.Message, want)
	}
}

func TestUnknownVerifiedDateWithAnFTPIsNotStale(t *testing.T) {
	in := input(planFrom(base(), base()))
	in.Profile.FTPVerifiedAt = ""
	if s := Suggest(in); s != nil {
		t.Errorf("got %+v: an unknown verified date must not nag", s)
	}
}

func TestStaleRiderInARecoveryWeekIsPointedAtTheNextWeek(t *testing.T) {
	in := input(planFrom(rec(), base(), base()))
	in.Profile.FTPVerifiedAt = "2026-01-01"
	s := mustSuggest(t, in)
	if s.Date < "2026-03-23" || s.Date > "2026-03-29" {
		t.Errorf("date = %s, want a day in the week of 2026-03-23, not the tired recovery week", s.Date)
	}
}

func TestNeverInTaperPeakOrARecoveryWeek(t *testing.T) {
	for name, plan := range map[string]*periodization.Plan{
		"taper":    planFrom(taper(), taper(), taper()),
		"peak":     planFrom(peak(), peak(), peak()),
		"recovery": planFrom(rec(), rec()),
	} {
		in := input(plan)
		in.Profile.FTPWatts = 0 // even the strongest reason
		in.Profile.FTPVerifiedAt = ""
		if s := Suggest(in); s != nil {
			t.Errorf("%s: got %+v", name, s)
		}
		in.Profile.FTPWatts = 250
		in.Profile.FTPVerifiedAt = "2026-01-01" // stale
		if s := Suggest(in); s != nil {
			t.Errorf("%s (stale): got %+v", name, s)
		}
	}
}

func TestNotWithinFourteenDaysBeforeAnAEvent(t *testing.T) {
	plan := planFrom(base(), base(), base(), base())
	mk := func(event string, priority workout.Priority) Input {
		in := input(plan)
		in.Profile.FTPWatts = 0
		in.Profile.FTPVerifiedAt = ""
		in.Goals = []workout.Goal{{ID: "g", Priority: priority, EventDate: event}}
		return in
	}
	// Event 2026-04-01: 14 days before is 2026-03-18, so this week (ending
	// Sunday 03-22) and the next are both inside it.
	if s := Suggest(mk("2026-04-01", workout.PriorityA)); s != nil {
		t.Errorf("A event in 14 days: got %+v", s)
	}
	// A B event does not exclude anything.
	if Suggest(mk("2026-04-01", workout.PriorityB)) == nil {
		t.Error("a B event must not exclude a test")
	}
	// Event 2026-04-06: this week ends 03-22, the day before the 14-day
	// window opens (03-23), so this week stays a candidate and next does not.
	s := mustSuggest(t, mk("2026-04-06", workout.PriorityA))
	if s.Date > "2026-03-22" {
		t.Errorf("date = %s, want this week", s.Date)
	}
	// An A event far away excludes nothing.
	if Suggest(mk("2026-06-01", workout.PriorityA)) == nil {
		t.Error("a distant A event must not exclude a test")
	}
}

func TestDayChoice(t *testing.T) {
	plan := planFrom(base(), base())
	noFTP := func() Input {
		in := input(plan)
		in.Profile.FTPWatts = 0
		in.Profile.FTPVerifiedAt = ""
		return in
	}
	generated := func(id, date string, zone workout.Zone) workout.Workout {
		return workout.Workout{ID: id, GoalID: "g", Date: date, Zone: zone, Name: "Session", Description: scheduler.GeneratedDescription}
	}

	t.Run("first available day from tomorrow", func(t *testing.T) {
		if s := mustSuggest(t, noFTP()); s.Date != "2026-03-19" {
			t.Errorf("date = %s", s.Date)
		}
	})
	t.Run("not the day after a key session", func(t *testing.T) {
		in := noFTP()
		in.Upcoming = []workout.Workout{generated("w-key", "2026-03-18", workout.ZoneVO2Max)}
		if s := mustSuggest(t, in); s.Date != "2026-03-21" {
			t.Errorf("date = %s, want Saturday 2026-03-21 (Thursday follows a key session)", s.Date)
		}
	})
	t.Run("a generated session on the day is replaced", func(t *testing.T) {
		in := noFTP()
		in.Upcoming = []workout.Workout{generated("w-easy", "2026-03-19", workout.ZoneEndurance)}
		s := mustSuggest(t, in)
		if s.Date != "2026-03-19" || s.ReplacesWorkoutID != "w-easy" {
			t.Errorf("got %+v, want to replace w-easy on 2026-03-19", s)
		}
	})
	t.Run("a rider-built workout is never replaced", func(t *testing.T) {
		in := noFTP()
		in.Upcoming = []workout.Workout{{ID: "mine", Date: "2026-03-19", Name: "My ride", Description: "my own"}}
		s := mustSuggest(t, in)
		if s.Date != "2026-03-21" || s.ReplacesWorkoutID != "" {
			t.Errorf("got %+v, want Saturday with nothing replaced", s)
		}
	})
	t.Run("a test-linked workout with the goal id is not plan-made either", func(t *testing.T) {
		in := noFTP()
		in.Upcoming = []workout.Workout{{ID: "t", GoalID: "g", Date: "2026-03-19", Name: "FTP Test", Description: "The ramp test"}}
		s := mustSuggest(t, in)
		if s.Date == "2026-03-19" {
			t.Errorf("date = %s: a hand-built day must not be taken over", s.Date)
		}
	})
	t.Run("falls to next week when this one has no day left", func(t *testing.T) {
		in := noFTP()
		in.Now = time.Date(2026, 3, 21, 10, 0, 0, 0, brussels) // Saturday
		if s := mustSuggest(t, in); s.Date != "2026-03-24" {
			t.Errorf("date = %s, want next Tuesday 2026-03-24", s.Date)
		}
	})
	t.Run("no stated days means any day", func(t *testing.T) {
		in := noFTP()
		in.Profile.AvailableDays = nil
		if s := mustSuggest(t, in); s.Date != "2026-03-19" {
			t.Errorf("date = %s", s.Date)
		}
	})
}

func TestScheduledTestAndSnoozeSilenceTheSuggestion(t *testing.T) {
	in := input(planFrom(base(), base()))
	in.Profile.FTPWatts = 0
	in.Profile.FTPVerifiedAt = ""

	scheduled := func(date string) []workout.Workout {
		return []workout.Workout{{ID: "t", Date: date, TestProtocol: "ramp"}}
	}
	in.Upcoming = scheduled("2026-03-18") // today counts
	if s := Suggest(in); s != nil {
		t.Errorf("a test today should silence it, got %+v", s)
	}
	in.Upcoming = scheduled("2026-04-20")
	if s := Suggest(in); s != nil {
		t.Errorf("a test later should silence it, got %+v", s)
	}
	in.Upcoming = scheduled("2026-03-17") // yesterday: done, not scheduled
	if Suggest(in) == nil {
		t.Error("yesterday's test must not silence it")
	}

	in.Upcoming = nil
	in.Profile.FTPTestSnoozedUntil = "2026-03-19"
	if s := Suggest(in); s != nil {
		t.Errorf("an active snooze should silence it, got %+v", s)
	}
	in.Profile.FTPTestSnoozedUntil = "2026-03-17"
	if Suggest(in) == nil {
		t.Error("an expired snooze must not silence it")
	}
}

func TestRecommendedProtocolIsTheLastTestsWhenThereIsOne(t *testing.T) {
	in := input(planFrom(base(), base(), base()))
	in.Profile.FTPVerifiedAt = "2026-01-01"
	in.LastTestProtocol = "two_by_eight"
	if s := mustSuggest(t, in); s.Recommended != "two_by_eight" {
		t.Errorf("recommended = %q", s.Recommended)
	}
	in.LastTestProtocol = "bogus"
	if s := mustSuggest(t, in); s.Recommended != "ramp" {
		t.Errorf("an unknown last protocol must fall back, got %q", s.Recommended)
	}
}

func TestNoPlanOnlyNoFTPAndStaleApplyOnTheNextAvailableDay(t *testing.T) {
	in := input(nil)
	if s := Suggest(in); s != nil {
		t.Errorf("fresh FTP and no plan: got %+v", s)
	}
	in.Profile.FTPVerifiedAt = "2026-01-01"
	s := mustSuggest(t, in)
	if s.Reason != ReasonStale || s.Date != "2026-03-19" {
		t.Errorf("stale, no plan: got %+v", s)
	}
	in.Profile.FTPWatts = 0
	in.Profile.FTPVerifiedAt = ""
	s = mustSuggest(t, in)
	if s.Reason != ReasonNoFTP || s.Date != "2026-03-19" {
		t.Errorf("no ftp, no plan: got %+v", s)
	}
}

func TestPlanThatDoesNotCoverTodayIsTreatedAsNoPlan(t *testing.T) {
	in := input(planFrom(base(), base()))
	in.Now = time.Date(2026, 9, 1, 10, 0, 0, 0, brussels) // long after the plan
	in.Profile.FTPVerifiedAt = "2026-01-01"
	s := mustSuggest(t, in)
	if s.Reason != ReasonStale {
		t.Errorf("got %+v", s)
	}
}

func TestTheSameInstantGivesTheSameAnswerInAnyZone(t *testing.T) {
	// 23:30 Wednesday in UTC is already Thursday in Brussels; both must read
	// their own calendar day, and neither may panic or mix the two.
	utc := time.Date(2026, 3, 18, 23, 30, 0, 0, time.UTC)
	in := input(planFrom(base(), base()))
	in.Profile.FTPWatts = 0
	in.Profile.FTPVerifiedAt = ""
	in.Now = utc
	a := mustSuggest(t, in)
	in.Now = utc.In(brussels)
	b := mustSuggest(t, in)
	if a.Date != "2026-03-19" { // Wednesday in UTC: tomorrow is Thursday
		t.Errorf("UTC date = %s", a.Date)
	}
	if b.Date != "2026-03-21" { // Thursday in Brussels: tomorrow is Friday, first available is Saturday
		t.Errorf("Brussels date = %s", b.Date)
	}
}
