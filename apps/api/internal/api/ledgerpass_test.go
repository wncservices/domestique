package api_test

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/indoor"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// ledgerHarness is a CTL-basis rider (FTP, a month of riding) with a goal, on a
// clock the test places in the Brussels morning. 2026-10-05 is a Monday.
type ledgerHarness struct {
	*seasonHarness
	goal workout.Goal
}

var ledgerDays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// at puts the clock at hh:mm Brussels time on the given October 2026 day.
func (h *ledgerHarness) at(day, hour, min int) {
	loc, _ := time.LoadLocation("Europe/Brussels")
	h.now = time.Date(2026, time.October, day, hour, min, 0, 0, loc).UTC()
}

func newLedgerHarness(t *testing.T, ftp float64) *ledgerHarness {
	t.Helper()
	// The week is built on Monday afternoon, after the morning slot, so no
	// repayment has happened yet; backdating makes every session look
	// untouched, as one made days ago does.
	return newLedgerHarnessBuiltOn(t, ftp, 5)
}

// newLedgerHarnessBuiltOn saves the goal, and so builds the plan, at 13:00 on
// the given October day (the 5th is the week's Monday).
func newLedgerHarnessBuiltOn(t *testing.T, ftp float64, day int) *ledgerHarness {
	t.Helper()
	h := &ledgerHarness{seasonHarness: newSeasonHarness(t)}
	h.profile(t, ledgerDays, ftp)
	h.now = utcNoon(2026, time.October, 5)
	h.ride(t, h.now, 40, 1, 60)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	h.at(day, 13, 0)
	g, err := h.store.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: "2027-04-14"})
	if err != nil {
		t.Fatal(err)
	}
	h.goal = g
	h.srv.AutoScheduleTick(context.Background())
	h.backdate(t)
	return h
}

// tick runs one scheduling pass and waits for nothing else.
func (h *ledgerHarness) tick() { h.srv.AutoScheduleTick(context.Background()) }

func (h *ledgerHarness) thisWeek(t *testing.T) []workout.Workout {
	t.Helper()
	mon, _ := weekBounds(h.now)
	return h.week(t, mon)
}

func byID(ws []workout.Workout) map[string]workout.Workout {
	m := make(map[string]workout.Workout, len(ws))
	for _, w := range ws {
		m[w.ID] = w
	}
	return m
}

func repaid(w workout.Workout) bool {
	for _, r := range w.Flags.AdaptedRules {
		if r == why.LoadRepaid {
			return true
		}
	}
	return false
}

func secs(w workout.Workout) float64 { return workout.PlannedSeconds(w.Steps) }

// repaidSince lists the sessions that changed by repayment between two snapshots.
func repaidSince(before, after []workout.Workout) []workout.Workout {
	was := byID(before)
	var out []workout.Workout
	for _, w := range after {
		if repaid(w) && !repaid(was[w.ID]) {
			out = append(out, w)
		}
	}
	return out
}

func TestMissedSessionIsRepaidOnTheNextMorning(t *testing.T) {
	h := newLedgerHarness(t, 250)
	before := h.thisWeek(t)
	for _, w := range before {
		t.Logf("%s %s %s %.0fs rules=%v", w.Date, w.Name, w.Zone, secs(w), w.Flags.AdaptedRules)
	}

	h.at(7, 7, 0) // Wednesday morning; Monday's and Tuesday's sessions were not ridden
	h.tick()

	after := h.thisWeek(t)
	changed := repaidSince(before, after)
	if len(changed) == 0 {
		t.Fatal("nothing was repaid on the morning after a missed session")
	}
	was := byID(before)
	for _, w := range changed {
		if w.Date <= "2026-10-07" {
			t.Errorf("%s dated %s was repaid, only tomorrow onwards may be", w.Name, w.Date)
		}
		if w.Zone == workout.ZoneEndurance && !(secs(w) > secs(was[w.ID])) {
			t.Errorf("%s was not made longer: %.0fs -> %.0fs", w.Name, secs(was[w.ID]), secs(w))
		}
		if !strings.Contains(w.Description, "Load made up") || !strings.Contains(w.Description, scheduler.AdjustedMarker) {
			t.Errorf("description %q carries no repayment note", w.Description)
		}
		adj, err := h.store.LatestAdjustments(context.Background(), "wilant", workout.SubjectWorkout, []string{w.ID})
		if err != nil || adj[w.ID].Rule != why.LoadRepaid {
			t.Errorf("no load_repaid reason stored for %s: %v %+v", w.Name, err, adj[w.ID])
		}
	}
}

func TestLedgerPassRunsOncePerDay(t *testing.T) {
	h := newLedgerHarness(t, 250)
	h.at(7, 7, 0)
	h.tick()
	first := h.thisWeek(t)

	h.at(7, 7, 30)
	h.tick()
	second := h.thisWeek(t)

	if len(first) != len(second) {
		t.Fatalf("the week went from %d to %d sessions", len(first), len(second))
	}
	was := byID(first)
	for _, w := range second {
		if secs(w) != secs(was[w.ID]) || len(w.Flags.AdaptedRules) != len(was[w.ID].Flags.AdaptedRules) {
			t.Errorf("%s changed on the second tick of the day", w.Name)
		}
	}
}

func TestLedgerPassOutsideTheMorningSlotDoesNothing(t *testing.T) {
	h := newLedgerHarness(t, 250)
	before := h.thisWeek(t)
	h.at(7, 13, 0)
	h.tick()
	if got := repaidSince(before, h.thisWeek(t)); len(got) != 0 {
		t.Errorf("%d sessions repaid at 13:00", len(got))
	}
	h.at(7, 22, 0)
	h.tick()
	if got := repaidSince(before, h.thisWeek(t)); len(got) != 0 {
		t.Errorf("%d sessions repaid at 22:00", len(got))
	}
}

func TestHoursBasisRiderIsUntouched(t *testing.T) {
	h := newLedgerHarness(t, 0)
	before := h.thisWeek(t)
	h.at(7, 7, 0)
	h.tick()
	if got := repaidSince(before, h.thisWeek(t)); len(got) != 0 {
		t.Errorf("%d sessions repaid for a rider with no FTP", len(got))
	}
	if _, ok, _ := h.store.GetLedgerWeek(context.Background(), "wilant", "2026-10-05"); ok {
		t.Error("a ledger row exists for a rider on the hours basis")
	}
}

func TestLedgerLogsCarryNoLoadValues(t *testing.T) {
	h := newLedgerHarness(t, 250)
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	// Two mornings, so the Monday-style settle line is exercised too.
	h.at(7, 7, 0)
	h.tick()
	h.at(12, 7, 0)
	h.tick()
	re := regexp.MustCompile(`(?i)(debt|gain|tss|load|target)\w*[=: ]+\S*\d`)
	for _, line := range strings.Split(logs.String(), "\n") {
		if m := re.FindString(line); m != "" {
			t.Errorf("a log line carries a load value (%q): %s", m, line)
		}
	}
	if !strings.Contains(logs.String(), "load repaid") {
		t.Errorf("no repayment log line:\n%s", logs.String())
	}
}

// The plan's TSS target against what the scheduler built is not owed load: a
// fresh, fully built week owes nothing on its first morning.
func TestUntouchedWeekOwesNothing(t *testing.T) {
	h := newLedgerHarness(t, 250)
	before := h.thisWeek(t)
	h.at(5, 7, 0)
	h.tick()
	if got := repaidSince(before, h.thisWeek(t)); len(got) != 0 {
		t.Errorf("%d sessions repaid on the first morning of an untouched week", len(got))
	}
	row, ok, err := h.store.GetLedgerWeek(context.Background(), "wilant", "2026-10-05")
	if err != nil || !ok || row.TargetTSS <= 0 || row.OpenDays != 7 {
		t.Errorf("row = %+v ok=%v err=%v, want opened with a target and 7 open days", row, ok, err)
	}
}

// A goal saved on Thursday plans Thursday to Sunday; the days before it were
// never planned, so they owe nothing.
func TestMidWeekGoalOwesNothingForPastDays(t *testing.T) {
	h := newLedgerHarnessBuiltOn(t, 250, 8)
	ctx := context.Background()
	for _, w := range h.thisWeek(t) {
		if w.Date == "2026-10-08" {
			if _, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
				Rider: "wilant", Provider: "garmin", ExternalID: "thu", Sport: "cycling", Date: w.Date,
				DurationSeconds: workout.PlannedSeconds(w.Steps), TrainingLoad: workout.PlannedTSSOrHours(w, 250),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := h.thisWeek(t)
	if len(before) == 0 {
		t.Fatal("setup: no sessions this week")
	}
	h.at(9, 7, 0)
	h.tick()
	if got := repaidSince(before, h.thisWeek(t)); len(got) != 0 {
		t.Errorf("%d sessions repaid for days that were never planned", len(got))
	}
}

// A life event added after the week opened lowers what it owes: the removed
// session is not debt.
func TestLifeEventAfterOpenLowersTheTarget(t *testing.T) {
	h := newLedgerHarness(t, 250)
	ctx := context.Background()
	h.at(5, 7, 0)
	h.tick() // opens the week with all seven days
	var mon, thu workout.Workout
	for _, w := range h.thisWeek(t) {
		switch w.Date {
		case "2026-10-05":
			mon = w
		case "2026-10-08":
			thu = w
		}
	}
	// Monday is ridden as planned, so only the event can make Tuesday owe.
	if _, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "mon", Sport: "cycling", Date: mon.Date,
		DurationSeconds: workout.PlannedSeconds(mon.Steps), TrainingLoad: workout.PlannedTSSOrHours(mon, 250),
	}); err != nil {
		t.Fatal(err)
	}
	// Thursday is taken out by a trip: the event, and the session it removed.
	if _, err := h.store.CreateLifeEvent(ctx, workout.LifeEvent{Rider: "wilant", Kind: "travel", Option: "no_bike", Start: thu.Date, End: thu.Date}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.DeleteWorkout(ctx, thu.ID); err != nil {
		t.Fatal(err)
	}
	before := h.thisWeek(t)

	h.at(6, 7, 0)
	h.tick()
	if got := repaidSince(before, h.thisWeek(t)); len(got) != 0 {
		t.Errorf("%d sessions repaid for a day a life event took out", len(got))
	}
}

// A repayment is not an easing: the session stays the plan's, so readiness can
// still ease it, and a later morning never makes it bigger a second time.
func TestReadinessCanStillEaseARepaidSession(t *testing.T) {
	h := newLedgerHarness(t, 250)
	ctx := context.Background()
	// Open the week with a large carried-in debt, so repayment goes on to
	// upgrade an endurance day to a structured session after lengthening.
	if err := h.store.OpenLedgerWeek(ctx, "wilant", "2026-10-05", 100, 2000, 7); err != nil {
		t.Fatal(err)
	}
	// A level above the bottom rung, so an easing has a rung to step down to.
	for _, z := range []workout.Zone{workout.ZoneSweetSpot, workout.ZoneTempo, workout.ZoneThreshold, workout.ZoneVO2Max} {
		if err := h.store.SaveLevel(ctx, workout.ProgressionLevel{Rider: "wilant", Sport: "cycling", Zone: z, Level: 4, Reason: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	// Every ride is already as long as a day may be, so lengthening has nowhere
	// to go and the debt can only be paid by upgrading a day. The week has no
	// structured session left, so one is owed.
	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{Rider: "wilant", HoursPerAvailableDay: 0.5, AvailableDays: ledgerDays, FTPWatts: 250}); err != nil {
		t.Fatal(err)
	}
	for _, w := range h.thisWeek(t) {
		if workout.IsStructuredZone(w.Zone) {
			if err := h.store.DeleteWorkout(ctx, w.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := h.thisWeek(t)
	h.at(6, 7, 0)
	h.tick()
	var upgraded workout.Workout
	for _, w := range repaidSince(before, h.thisWeek(t)) {
		if workout.IsStructuredZone(w.Zone) {
			upgraded = w
		}
	}
	if upgraded.ID == "" {
		t.Fatal("setup: no session was upgraded to a structured one")
	}
	if !scheduler.IsGenerated(upgraded) || len(upgraded.Flags.EasedBy()) != 0 {
		t.Fatalf("a repaid session reads as touched: %+v", upgraded.Flags)
	}

	// The morning of that session the rider is run down.
	loc, _ := time.LoadLocation("Europe/Brussels")
	day, _ := time.ParseInLocation("2006-01-02", upgraded.Date, loc)
	h.now = time.Date(day.Year(), day.Month(), day.Day(), 7, 0, 0, 0, loc).UTC()
	if err := h.store.SaveWellness(ctx, workout.DailyWellness{
		Rider: "wilant", Date: upgraded.Date, HRVStatus: "UNBALANCED", HRVLastNight: 41, HRVWeeklyAvg: 52,
		SleepScore: 53, SleepSeconds: 5*3600 + 20*60, ReadinessScore: 44, ReadinessLevel: "LOW", RestingHR: 61,
	}); err != nil {
		t.Fatal(err)
	}
	// Form of -60: a rest verdict, which swaps a hard session for an easy one.
	if _, err := h.conn.Exec(`INSERT INTO fitness_snapshots (rider, date, ctl, atl, tsb) VALUES ('wilant', ?, 40, 100, -60)`, upgraded.Date); err != nil {
		t.Fatal(err)
	}
	h.srv.AdaptWorkouts(ctx)

	got, err := h.store.GetWorkout(ctx, upgraded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Flags.EasedBy()) == 0 || !repaid(got) {
		t.Errorf("rules = %v, want load_repaid followed by a readiness easing", got.Flags.AdaptedRules)
	}
	if got.Name == upgraded.Name || len(got.Steps) == len(upgraded.Steps) && secs(got) == secs(upgraded) {
		t.Errorf("the session was not swapped for an easy ride: %q", got.Name)
	}
	if !strings.Contains(got.Description, scheduler.AdjustedMarker) || len(got.Flags.AdaptedRules) != 2 {
		t.Errorf("flags %v and description %q disagree", got.Flags.AdaptedRules, got.Description)
	}
	if adj, err := h.store.LatestAdjustments(ctx, "wilant", workout.SubjectWorkout, []string{upgraded.ID}); err != nil || adj[upgraded.ID].Rule != why.ReadinessRest {
		t.Errorf("the latest reason is %+v (%v), want the readiness easing", adj[upgraded.ID], err)
	}

	// A later morning's pass leaves the eased session as the easing made it.
	eased := byID(h.thisWeek(t))
	h.at(int(day.Day())+1, 7, 0)
	h.tick()
	for _, w := range h.thisWeek(t) {
		if w.ID == upgraded.ID && (secs(w) != secs(eased[w.ID]) || len(w.Flags.AdaptedRules) != len(eased[w.ID].Flags.AdaptedRules)) {
			t.Errorf("the eased session was changed again by a later morning pass")
		}
	}
}

func TestRecoveryWeekIsNotRepaid(t *testing.T) {
	h := newLedgerHarness(t, 250)
	profile, _, _ := h.store.GetProfile(context.Background(), "wilant")
	plan, err := periodization.Build(h.goal, profile, h.now)
	if err != nil {
		t.Fatal(err)
	}
	var recovery string
	for _, w := range plan.Weeks {
		if w.Recovery && w.StartDate > "2026-10-05" {
			recovery = w.StartDate
			break
		}
	}
	if recovery == "" {
		t.Fatal("setup: no recovery week in the plan")
	}
	monday, _ := time.Parse("2006-01-02", recovery)
	loc, _ := time.LoadLocation("Europe/Brussels")
	h.now = time.Date(monday.Year(), monday.Month(), monday.Day()+2, 7, 0, 0, 0, loc).UTC() // the Wednesday morning
	before := h.week(t, monday)
	h.tick()

	if got := repaidSince(before, h.week(t, monday)); len(got) != 0 {
		t.Errorf("%d sessions repaid in a recovery week", len(got))
	}
	row, ok, err := h.store.GetLedgerWeek(context.Background(), "wilant", recovery)
	if err != nil || !ok || row.CarriedInTSS != 0 {
		t.Errorf("recovery week row: %+v ok=%v err=%v, want opened with nothing carried in", row, ok, err)
	}
}

// On a Monday, last week's own debt rolls in up to a tenth of the new target
// and the rest is forgiven.
func TestMondayPassRollsAndForgives(t *testing.T) {
	h := newLedgerHarness(t, 250)
	ctx := context.Background()
	if err := h.store.OpenLedgerWeek(ctx, "wilant", "2026-10-05", 400, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "last-week", Sport: "cycling",
		Date: "2026-10-07", DurationSeconds: 3600, TrainingLoad: 100,
	}); err != nil {
		t.Fatal(err)
	}

	h.at(12, 7, 0)
	h.tick()

	row, ok, err := h.store.GetLedgerWeek(ctx, "wilant", "2026-10-12")
	if err != nil || !ok || row.TargetTSS <= 0 {
		t.Fatalf("this week was not opened: %+v ok=%v err=%v", row, ok, err)
	}
	last, _, _ := h.store.GetLedgerWeek(ctx, "wilant", "2026-10-05")
	const lastDebt = 300.0 // 400 target, 100 ridden
	if want := 0.10 * row.TargetTSS; math.Abs(row.CarriedInTSS-want) > 1e-6 {
		t.Errorf("carried in %.2f, want a tenth of the target %.2f", row.CarriedInTSS, want)
	}
	if math.Abs(last.ForgivenTSS-(lastDebt-row.CarriedInTSS)) > 1e-6 {
		t.Errorf("forgiven %.2f, want %.2f", last.ForgivenTSS, lastDebt-row.CarriedInTSS)
	}
}

// An indoor ride that is lengthened is converted for the trainer again, not
// turned back into a road ride.
func TestRepaidIndoorRideStaysIndoor(t *testing.T) {
	h := newLedgerHarness(t, 250)
	ctx := context.Background()
	profile, _, _ := h.store.GetProfile(ctx, "wilant")
	yes := true
	for _, w := range h.thisWeek(t) {
		if w.Date < "2026-10-08" || w.Zone != workout.ZoneEndurance {
			continue
		}
		outdoor := append([]workout.WorkoutStep{}, w.Steps...)
		res, err := indoor.Convert(w, profile)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.UpdateWorkout(ctx, w.ID, workout.UpdateWorkoutRequest{Indoor: &yes, Steps: &res.Steps, OutdoorSteps: &outdoor}); err != nil {
			t.Fatal(err)
		}
	}
	h.backdate(t)
	before := h.thisWeek(t)

	h.at(7, 7, 0)
	h.tick()

	changed := repaidSince(before, h.thisWeek(t))
	if len(changed) == 0 {
		t.Fatal("nothing was repaid")
	}
	was := byID(before)
	for _, w := range changed {
		if !w.Indoor || w.OutdoorSteps == nil {
			t.Errorf("%s was turned back into a road ride", w.Name)
			continue
		}
		if !(workout.PlannedSeconds(*w.OutdoorSteps) > workout.PlannedSeconds(*was[w.ID].OutdoorSteps)) {
			t.Errorf("%s: the outdoor original was not lengthened", w.Name)
		}
	}
}
