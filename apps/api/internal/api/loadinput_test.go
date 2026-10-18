package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type periodizationBody struct {
	LoadBasis string  `json:"loadBasis"`
	TargetCTL float64 `json:"targetCtl"`
	Shortfall float64 `json:"shortfall"`
	Weeks     []struct {
		StartDate   string  `json:"startDate"`
		TargetHours float64 `json:"targetHours"`
		TargetTSS   float64 `json:"targetTss"`
		PlannedCTL  float64 `json:"plannedCtl"`
		LoadCapped  bool    `json:"loadCapped"`
	} `json:"weeks"`
}

func (h *seasonHarness) periodization(t *testing.T, goalID string) periodizationBody {
	t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/goals/"+goalID+"/periodization", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("periodization: status %d", resp.StatusCode)
	}
	var out periodizationBody
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// ride seeds one cycling session per day for the n days ending the day before
// from, each carrying load, then rebuilds the fitness history at the clock.
func (h *seasonHarness) ride(t *testing.T, from time.Time, n int, hours, load float64) {
	t.Helper()
	ctx := context.Background()
	for d := 1; d <= n; d++ {
		date := from.AddDate(0, 0, -d).Format("2006-01-02")
		if _, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
			Rider: "wilant", Provider: "garmin", ExternalID: fmt.Sprintf("r-%s", date), Sport: "cycling",
			Date: date, DurationSeconds: hours * 3600, TrainingLoad: load,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.RecomputeFitnessSnapshots(ctx, "wilant"); err != nil {
		t.Fatal(err)
	}
}

func (h *seasonHarness) latestSnapshot(t *testing.T) workout.FitnessSnapshot {
	t.Helper()
	snaps, err := h.store.ListFitnessSnapshots(context.Background(), "wilant")
	if err != nil || len(snaps) == 0 {
		t.Fatalf("snapshots: %v %v", len(snaps), err)
	}
	return snaps[len(snaps)-1]
}

func TestPeriodizationIsSizedFromCTLForARiderWithFitness(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 250)
	h.ride(t, h.now, 40, 1, 60)
	g, err := h.store.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: "2027-04-14"})
	if err != nil {
		t.Fatal(err)
	}

	got := h.periodization(t, g.ID)
	snap := h.latestSnapshot(t)
	if snap.Date != "2026-10-07" {
		t.Fatalf("latest snapshot is %s, want today", snap.Date)
	}
	if got.LoadBasis != "ctl" {
		t.Fatalf("loadBasis = %q, want ctl", got.LoadBasis)
	}
	if got.Weeks[0].TargetTSS <= 0 {
		t.Errorf("first week targetTss = %v, want > 0", got.Weeks[0].TargetTSS)
	}
	if math.Abs(got.Weeks[0].PlannedCTL-snap.CTL) > 0.5 {
		t.Errorf("first week plannedCtl = %v, want the seeded CTL %v", got.Weeks[0].PlannedCTL, snap.CTL)
	}
}

func TestPeriodizationWithoutFTPIsTheHoursPlan(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	h.ride(t, h.now, 40, 1, 60)
	g, err := h.store.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: "2027-04-14"})
	if err != nil {
		t.Fatal(err)
	}

	got := h.periodization(t, g.ID)
	profile, _, _ := h.store.GetProfile(context.Background(), "wilant")
	built, err := periodization.Build(g, profile, h.now)
	if err != nil {
		t.Fatal(err)
	}
	sessions, _ := h.store.ListSessions(context.Background(), "wilant")
	want := adapter.Reconcile(g, profile, built, sessions, h.now).Weeks
	if got.LoadBasis != "hours" {
		t.Errorf("loadBasis = %q, want hours", got.LoadBasis)
	}
	if len(got.Weeks) != len(want) {
		t.Fatalf("%d weeks, want %d", len(got.Weeks), len(want))
	}
	for i, wk := range got.Weeks {
		if wk.TargetTSS != 0 || wk.PlannedCTL != 0 || wk.LoadCapped {
			t.Errorf("week %d carries load fields: %+v", i, wk)
		}
		if wk.TargetHours != want[i].TargetHours {
			t.Errorf("week %d targetHours = %v, want %v", i, wk.TargetHours, want[i].TargetHours)
		}
	}
}

func TestStaleSnapshotRollsForwardToToday(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 250)
	h.now = utcNoon(2026, time.September, 7)
	h.ride(t, h.now, 40, 1, 60)
	snap := h.latestSnapshot(t)
	h.now = utcNoon(2026, time.October, 7) // 30 days on, nothing ridden since
	g, err := h.store.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: "2027-04-14"})
	if err != nil {
		t.Fatal(err)
	}

	got := h.periodization(t, g.ID)
	want := snap.CTL
	for range 30 {
		want, _ = workout.RollFitness(want, 0, 0)
	}
	if got.LoadBasis != "ctl" {
		t.Fatalf("loadBasis = %q, want ctl", got.LoadBasis)
	}
	if !(got.Weeks[0].PlannedCTL < snap.CTL) || math.Abs(got.Weeks[0].PlannedCTL-want) > 0.5 {
		t.Errorf("first week plannedCtl = %v, want the snapshot's %v rolled 30 days to %v", got.Weeks[0].PlannedCTL, snap.CTL, want)
	}
}

func TestDatedGoalReportsTargetCTL(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 250)
	h.ride(t, h.now, 40, 1, 60)
	g, err := h.store.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: "wilant", Name: "Keep riding"})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.periodization(t, g.ID); got.TargetCTL != 0 {
		t.Errorf("an undated goal reports targetCtl %v, want none", got.TargetCTL)
	}

	resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+g.ID, `{"eventDate":"2027-04-14","targetDistanceM":120000,"targetElevationM":1500}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edit goal: status %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()
	if got := h.periodization(t, g.ID); got.TargetCTL <= 0 || got.LoadBasis != "ctl" {
		t.Errorf("a dated goal reports targetCtl %v basis %q, want a target on the ctl basis", got.TargetCTL, got.LoadBasis)
	}
}

func TestRefreshResizesNextWeekFromNewCTL(t *testing.T) {
	h := newSeasonHarness(t)
	ctx := context.Background()
	days := []string{"mon", "tue", "wed", "thu", "fri", "sat"}
	h.profile(t, days, 250)
	h.ride(t, h.now, 30, 1, 49)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: "2027-04-14"}); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(ctx)
	h.backdate(t)

	thisMon, _ := weekBounds(h.now)
	target := thisMon.AddDate(0, 0, 7*10)
	sum := func() float64 {
		var secs float64
		for _, w := range h.week(t, target) {
			secs += workout.PlannedSeconds(w.Steps)
		}
		return secs
	}
	before := sum()
	if before == 0 {
		t.Fatal("the target week was not filled")
	}

	h.now = utcNoon(2026, time.December, 9) // the Wednesday before target
	h.ride(t, h.now, 14, 8, 400)            // a big block just behind the rider
	h.srv.AutoScheduleTick(ctx)

	if after := sum(); !(after > before) {
		t.Errorf("next week plans %v s after a big block of riding, was %v s: it was not re-sized from the new CTL", after, before)
	}
}
