package projection

import (
	"math"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func mustDate(s string) time.Time {
	d, _ := time.Parse(dateLayout, s)
	return d
}

func snap(date string, ctl, atl float64) workout.FitnessSnapshot {
	return workout.FitnessSnapshot{Date: date, CTL: ctl, ATL: atl, TSB: ctl - atl}
}

func TestRollThreeDaysMatchesRollFitnessByHand(t *testing.T) {
	pts := Roll(Input{
		Start:   snap("2026-10-05", 50, 60),
		Planned: map[string]float64{"2026-10-05": 100, "2026-10-06": 0, "2026-10-07": 40},
		Today:   "2026-10-05", Event: "2026-10-08",
	})
	if len(pts) != 4 {
		t.Fatalf("points = %d, want 4 (today..event)", len(pts))
	}
	ctl, atl := 50.0, 60.0
	for i, load := range []float64{100, 0, 40} {
		if !near(pts[i].CTL, ctl) || !near(pts[i].ATL, atl) || !near(pts[i].TSB, ctl-atl) || pts[i].Load != load {
			t.Errorf("day %d = %+v, want ctl %v atl %v load %v", i, pts[i], ctl, atl, load)
		}
		ctl, atl = workout.RollFitness(ctl, atl, load)
	}
	if !near(pts[3].CTL, ctl) || !near(pts[3].ATL, atl) {
		t.Errorf("event day = %+v, want ctl %v atl %v", pts[3], ctl, atl)
	}
}

func TestRollEventDayExcludesTheEventsOwnLoad(t *testing.T) {
	with := Roll(Input{Start: snap("2026-10-05", 50, 50), Planned: map[string]float64{"2026-10-07": 500}, Today: "2026-10-05", Event: "2026-10-07"})
	without := Roll(Input{Start: snap("2026-10-05", 50, 50), Today: "2026-10-05", Event: "2026-10-07"})
	if len(with) != 3 || len(without) != 3 {
		t.Fatalf("points = %d / %d, want 3", len(with), len(without))
	}
	if with[2] != without[2] {
		t.Errorf("event-day point %+v differs from %+v: the event's own load must not count", with[2], without[2])
	}
	if with[2].Load != 0 {
		t.Errorf("event-day load = %v, want 0", with[2].Load)
	}
}

func TestRollGapBetweenSnapshotAndTodayUsesActualLoadAndZeroForEmptyDays(t *testing.T) {
	pts := Roll(Input{
		Start:   snap("2026-10-01", 40, 40),
		Actual:  map[string]float64{"2026-10-02": 80},
		Planned: map[string]float64{"2026-10-01": 999, "2026-10-02": 999, "2026-10-03": 999},
		Today:   "2026-10-04", Event: "2026-10-05",
	})
	ctl, atl := 40.0, 40.0
	for _, load := range []float64{0, 80, 0} { // 1, 2, 3 Oct: planned ignored in the past
		ctl, atl = workout.RollFitness(ctl, atl, load)
	}
	if len(pts) != 2 || pts[0].Date != "2026-10-04" || !near(pts[0].CTL, ctl) || !near(pts[0].ATL, atl) {
		t.Errorf("points = %+v, want first = today with ctl %v atl %v", pts, ctl, atl)
	}
}

func TestRollACompletedSessionReplacesThatDaysPlannedWorkout(t *testing.T) {
	pts := Roll(Input{
		Start:   snap("2026-10-05", 50, 50),
		Actual:  map[string]float64{"2026-10-05": 30, "2026-10-06": 0},
		Planned: map[string]float64{"2026-10-05": 100, "2026-10-06": 100, "2026-10-07": 100},
		Today:   "2026-10-05", Event: "2026-10-08",
	})
	for i, w := range []float64{30, 0, 100} {
		if pts[i].Load != w {
			t.Errorf("day %d load = %v, want %v (actual wins, even at 0; planned otherwise)", i, pts[i].Load, w)
		}
	}
}

func TestRollTodayUsesActualElsePlanned(t *testing.T) {
	planned := map[string]float64{"2026-10-05": 70}
	a := Roll(Input{Start: snap("2026-10-05", 50, 50), Planned: planned, Today: "2026-10-05", Event: "2026-10-06"})
	b := Roll(Input{Start: snap("2026-10-05", 50, 50), Planned: planned, Actual: map[string]float64{"2026-10-05": 20}, Today: "2026-10-05", Event: "2026-10-06"})
	if a[0].Load != 70 || b[0].Load != 20 {
		t.Errorf("today loads = %v / %v, want 70 planned / 20 actual", a[0].Load, b[0].Load)
	}
}

func TestRollAFlatPlanConvergesTowardItsDailyLoad(t *testing.T) {
	planned := map[string]float64{}
	start := mustDate("2026-10-01")
	for i := 0; i < 300; i++ {
		planned[start.AddDate(0, 0, i).Format(dateLayout)] = 60
	}
	pts := Roll(Input{Start: snap("2026-10-01", 20, 20), Planned: planned, Today: "2026-10-01", Event: "2027-07-01"})
	last := pts[len(pts)-1]
	if math.Abs(last.CTL-60) > 0.5 || math.Abs(last.ATL-60) > 0.01 {
		t.Errorf("after 273 flat days ctl %v atl %v, want both near 60", last.CTL, last.ATL)
	}
}

func TestRollRefusesAnUnusableEvent(t *testing.T) {
	base := Input{Start: snap("2026-10-05", 50, 50), Today: "2026-10-05"}
	for name, event := range map[string]string{
		"today": "2026-10-05", "past": "2026-10-01", "over 400 days": "2027-11-10", "garbage": "soon",
	} {
		in := base
		in.Event = event
		if got := Roll(in); got != nil {
			t.Errorf("%s: %d points, want none", name, len(got))
		}
	}
	in := base
	in.Event = "2027-11-09" // exactly 400 days
	if got := Roll(in); len(got) != 401 {
		t.Errorf("400 days out: %d points, want 401", len(got))
	}
}

func TestRollASnapshotAfterTodayIsTreatedAsToday(t *testing.T) {
	pts := Roll(Input{Start: snap("2026-10-09", 50, 55), Today: "2026-10-05", Event: "2026-10-07"})
	if len(pts) != 3 || !near(pts[0].CTL, 50) || !near(pts[0].ATL, 55) || pts[0].Date != "2026-10-05" {
		t.Errorf("points = %+v, want to start at today with the snapshot's values", pts)
	}
}
