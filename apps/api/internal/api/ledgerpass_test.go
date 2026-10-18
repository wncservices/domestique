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
	h := &ledgerHarness{seasonHarness: newSeasonHarness(t)}
	h.profile(t, ledgerDays, ftp)
	h.now = utcNoon(2026, time.October, 5)
	h.ride(t, h.now, 40, 1, 60)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	g, err := h.store.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: "2027-04-14"})
	if err != nil {
		t.Fatal(err)
	}
	h.goal = g
	// The week is built on Monday afternoon, after the morning slot, so no
	// repayment has happened yet; backdating makes every session look
	// untouched, as one made days ago does.
	h.at(5, 13, 0)
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
	h.at(7, 7, 0)
	h.tick()
	re := regexp.MustCompile(`(?i)tss\D{0,4}\d`)
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "rider=wilant") && re.MatchString(line) {
			t.Errorf("a log line carries a load value next to the rider: %s", line)
		}
	}
	if !strings.Contains(logs.String(), "load repaid") {
		t.Errorf("no repayment log line:\n%s", logs.String())
	}
}

// A repayment is not an easing: the session stays the plan's, so readiness can
// still reach it, and a later morning never makes it bigger a second time.
func TestReadinessCanStillEaseARepaidSession(t *testing.T) {
	h := newLedgerHarness(t, 250)
	h.at(7, 7, 0)
	h.tick()
	repaidNow := repaidSince(nil, h.thisWeek(t))
	if len(repaidNow) == 0 {
		t.Fatal("setup: nothing was repaid")
	}
	for _, w := range repaidNow {
		if !scheduler.IsGenerated(w) {
			t.Errorf("%s no longer counts as an untouched plan session after being repaid", w.Name)
		}
		if len(w.Flags.EasedBy()) != 0 {
			t.Errorf("%s reads as eased: %v", w.Name, w.Flags.EasedBy())
		}
	}

	h.at(8, 7, 0) // the next morning: the same sessions are not lengthened again
	h.tick()
	was := byID(repaidNow)
	for _, w := range h.thisWeek(t) {
		if prev, ok := was[w.ID]; ok && secs(w) != secs(prev) {
			t.Errorf("%s was lengthened a second time: %.0fs -> %.0fs", w.Name, secs(prev), secs(w))
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
	if err := h.store.OpenLedgerWeek(ctx, "wilant", "2026-10-05", 400, 0); err != nil {
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
