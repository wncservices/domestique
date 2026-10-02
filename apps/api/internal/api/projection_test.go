package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Every clock here is a fixed UTC instant (Wednesday 2026-10-07), so the
// tests read the same under any TZ.

type projectionBody struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	Goal      *struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		EventDate string `json:"eventDate"`
		Priority  string `json:"priority"`
	} `json:"goal"`
	Points []struct {
		Date string  `json:"date"`
		CTL  float64 `json:"ctl"`
		ATL  float64 `json:"atl"`
		TSB  float64 `json:"tsb"`
		Load float64 `json:"load"`
	} `json:"points"`
	RaceDay *struct {
		Date string  `json:"date"`
		CTL  float64 `json:"ctl"`
		ATL  float64 `json:"atl"`
		TSB  float64 `json:"tsb"`
	} `json:"raceDay"`
	Band      *struct{ Low, High float64 } `json:"band"`
	TargetCTL float64                      `json:"targetCtl"`
	Verdict   *struct {
		Key     string `json:"key"`
		Message string `json:"message"`
		Tone    string `json:"tone"`
	} `json:"verdict"`
	Ramp *struct {
		MaxPerWeek float64 `json:"maxPerWeek"`
	} `json:"ramp"`
	Suggestion *struct {
		Text string `json:"text"`
	} `json:"suggestion"`
	Events []struct {
		GoalID   string  `json:"goalId"`
		Name     string  `json:"name"`
		Date     string  `json:"date"`
		Priority string  `json:"priority"`
		CTL      float64 `json:"ctl"`
		TSB      float64 `json:"tsb"`
		Verdict  *struct {
			Key string `json:"key"`
		} `json:"verdict"`
	} `json:"events"`
	Assumptions []string `json:"assumptions"`
}

type projectionHarness struct{ *seasonHarness }

// newProjectionHarness is a rider with FTP 250, a four-day week and a
// snapshot from the day before the harness clock.
func newProjectionHarness(t *testing.T) *projectionHarness {
	t.Helper()
	h := &projectionHarness{newSeasonHarness(t)}
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 250)
	h.snapshot(t, "2026-10-06", 50, 55)
	return h
}

func (h *projectionHarness) snapshot(t *testing.T, date string, ctl, atl float64) {
	t.Helper()
	if _, err := h.conn.Exec(`DELETE FROM fitness_snapshots WHERE rider = 'wilant'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.conn.Exec(`INSERT INTO fitness_snapshots (rider, date, ctl, atl, tsb) VALUES ('wilant', ?, ?, ?, ?)`, date, ctl, atl, ctl-atl); err != nil {
		t.Fatal(err)
	}
}

func (h *projectionHarness) goal(t *testing.T, name, date, priority string) workout.Goal {
	t.Helper()
	g := h.createGoal(t, fmt.Sprintf(`{"name":%q,"eventDate":%q,"priority":%q,"targetDistanceM":100000,"targetElevationM":1500}`, name, date, priority))
	h.srv.WaitForBackground()
	return g
}

func (h *projectionHarness) get(t *testing.T, user, query string) (int, projectionBody) {
	t.Helper()
	resp := h.as(user, "cyclists", http.MethodGet, "/api/training/projection"+query, "")
	var out projectionBody
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, out
}

func TestProjectionHasTheSpecifiedShapeForTheNearestAGoal(t *testing.T) {
	h := newProjectionHarness(t)
	h.goal(t, "Later A", "2027-03-14", "A")
	a := h.goal(t, "Gran Fondo", "2026-12-13", "A")
	h.goal(t, "Club ride", "2026-11-01", "B")

	status, p := h.get(t, "wilant", "")
	if status != http.StatusOK || !p.Available {
		t.Fatalf("status %d, available %v (%s)", status, p.Available, p.Reason)
	}
	if p.Goal == nil || p.Goal.ID != a.ID {
		t.Fatalf("goal = %+v, want the nearest A goal %s (not the B ride, not the later A)", p.Goal, a.ID)
	}
	// Today (2026-10-07) to the event (2026-12-13) is 67 days: 68 points.
	if len(p.Points) != 68 || p.Points[0].Date != "2026-10-07" || p.Points[67].Date != "2026-12-13" {
		t.Fatalf("points = %d (first %v), want 68 from today to the event", len(p.Points), p.Points[0])
	}
	if p.RaceDay == nil || p.RaceDay.Date != "2026-12-13" || p.RaceDay.CTL != p.Points[67].CTL || p.RaceDay.TSB != p.Points[67].TSB {
		t.Errorf("raceDay = %+v, want the last point", p.RaceDay)
	}
	// 100 km and 1500 m is about 5.1 h: the long band.
	if p.Band == nil || p.Band.Low != 5 || p.Band.High != 15 || p.TargetCTL < 60 || p.TargetCTL > 75 {
		t.Errorf("band %+v, target CTL %v, want +5..+15 and a target near 70", p.Band, p.TargetCTL)
	}
	if p.Verdict == nil || p.Verdict.Key == "" || p.Verdict.Message == "" || p.Ramp == nil {
		t.Errorf("verdict %+v, ramp %+v, want both for an A goal", p.Verdict, p.Ramp)
	}
	if p.Assumptions[0] != "Assumes you ride the plan as written." {
		t.Errorf("assumptions = %v", p.Assumptions)
	}
	if !contains(p.Assumptions, "Does not include the load of B and C events.") {
		t.Errorf("assumptions = %v, want the B-event note (a B ride falls before the target)", p.Assumptions)
	}
	if !contains(p.Assumptions, "Event length estimated at 5.1 h from distance and elevation.") || !contains(p.Assumptions, "Days with no planned workout count as rest.") {
		t.Errorf("assumptions = %v", p.Assumptions)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestProjectionLoadComesFromThePlannedWorkoutsAndTheEventDayCarriesNone(t *testing.T) {
	h := newProjectionHarness(t)
	h.goal(t, "Gran Fondo", "2026-12-13", "A")
	_, p := h.get(t, "wilant", "")

	want := map[string]float64{}
	for _, w := range h.workouts(t) {
		if tss, ok := adapter.PlannedTSS(w, 250); ok {
			want[w.Date] += tss
		}
	}
	checked := 0
	for _, pt := range p.Points[:len(p.Points)-1] {
		if pt.Date == "2026-10-07" {
			continue // today may be covered by a synced session, not asserted here
		}
		if d := pt.Load - want[pt.Date]; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s load = %v, want the planned %v", pt.Date, pt.Load, want[pt.Date])
		}
		if want[pt.Date] > 0 {
			checked++
		}
	}
	if checked < 20 {
		t.Errorf("only %d planned days checked: the season did not plan", checked)
	}
	if last := p.Points[len(p.Points)-1]; last.Load != 0 {
		t.Errorf("event day load = %v, want 0", last.Load)
	}
}

func TestProjectionWithoutAnAGoalUsesTheNearestAndGivesNoVerdict(t *testing.T) {
	h := newProjectionHarness(t)
	h.goal(t, "Far C", "2027-02-01", "C")
	b := h.goal(t, "Near B", "2026-11-15", "B")

	_, p := h.get(t, "wilant", "")
	if !p.Available || p.Goal == nil || p.Goal.ID != b.ID {
		t.Fatalf("goal = %+v (available %v), want the nearest goal of any priority", p.Goal, p.Available)
	}
	if p.Verdict != nil || p.Suggestion != nil {
		t.Errorf("verdict %+v, suggestion %+v: a B event gets neither", p.Verdict, p.Suggestion)
	}
	if p.RaceDay == nil {
		t.Error("a B event still reports its projected form")
	}
}

func TestProjectionListsEveryFutureEventAndPicksOneWithGoalParam(t *testing.T) {
	h := newProjectionHarness(t)
	a := h.goal(t, "Gran Fondo", "2026-12-13", "A")
	b := h.goal(t, "Near B", "2026-11-15", "B")
	c := h.goal(t, "Later C", "2027-01-24", "C")

	_, p := h.get(t, "wilant", "")
	if len(p.Events) != 3 {
		t.Fatalf("events = %+v, want all three", p.Events)
	}
	byID := map[string]int{}
	for i, e := range p.Events {
		byID[e.GoalID] = i
		if e.Verdict != nil {
			t.Errorf("event %s carries a verdict: B and C are info only", e.Name)
		}
		if e.CTL <= 0 {
			t.Errorf("event %s has no projected CTL", e.Name)
		}
	}
	if p.Events[0].GoalID != b.ID || p.Events[1].GoalID != a.ID || p.Events[2].GoalID != c.ID {
		t.Errorf("events not in date order: %+v", p.Events)
	}
	// The target's own event entry agrees with the race-day readout.
	if e := p.Events[byID[a.ID]]; e.CTL != p.RaceDay.CTL || e.TSB != p.RaceDay.TSB {
		t.Errorf("event %+v disagrees with raceDay %+v", e, p.RaceDay)
	}

	_, pc := h.get(t, "wilant", "?goal="+c.ID)
	if pc.Goal == nil || pc.Goal.ID != c.ID || len(pc.Points) != 110 || pc.Verdict != nil {
		t.Errorf("?goal=C gave goal %+v, %d points, verdict %+v", pc.Goal, len(pc.Points), pc.Verdict)
	}
}

func TestProjectionOfAPlanStillBeingBuiltIsIncompleteButKeepsItsSeries(t *testing.T) {
	h := newProjectionHarness(t)
	h.goal(t, "Gran Fondo", "2026-12-13", "A")
	if _, err := h.conn.Exec(`DELETE FROM scheduled_weeks WHERE week_start >= '2026-11-02'`); err != nil {
		t.Fatal(err)
	}
	_, p := h.get(t, "wilant", "")
	if !p.Available || p.Verdict == nil || p.Verdict.Key != "incomplete" {
		t.Fatalf("verdict = %+v, want incomplete", p.Verdict)
	}
	if !strings.Contains(p.Verdict.Message, "projection covers") || len(p.Points) != 68 {
		t.Errorf("message %q, %d points: the series must still come back", p.Verdict.Message, len(p.Points))
	}
}

func TestProjectionIsUnavailableWithoutFTPOrFitnessOrAnEvent(t *testing.T) {
	h := newProjectionHarness(t)
	h.goal(t, "Gran Fondo", "2026-12-13", "A")

	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	status, p := h.get(t, "wilant", "")
	if status != http.StatusOK || p.Available || !strings.Contains(p.Reason, "FTP") {
		t.Errorf("no FTP: status %d, available %v, reason %q", status, p.Available, p.Reason)
	}
	if p.Verdict == nil || p.Verdict.Key != "unavailable" {
		t.Errorf("verdict = %+v, want unavailable for an A goal", p.Verdict)
	}

	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 250)
	if _, err := h.conn.Exec(`DELETE FROM fitness_snapshots`); err != nil {
		t.Fatal(err)
	}
	if _, p := h.get(t, "wilant", ""); p.Available || p.Reason == "" {
		t.Errorf("no snapshot: available %v, reason %q", p.Available, p.Reason)
	}

	// A rider with no goal at all.
	if status, p := h.get(t, "someone-else", ""); status != http.StatusOK || p.Available || p.Reason == "" {
		t.Errorf("no goals: status %d, available %v, reason %q", status, p.Available, p.Reason)
	}
}

func TestProjectionIsUnavailableForAnEventTodayOrOverAYearOut(t *testing.T) {
	h := newProjectionHarness(t)
	today := h.goal(t, "Today", "2026-10-07", "A")
	if _, p := h.get(t, "wilant", "?goal="+today.ID); p.Available || p.Reason == "" {
		t.Errorf("event today: available %v, reason %q", p.Available, p.Reason)
	}
	far := h.goal(t, "Far", "2028-01-01", "B")
	if _, p := h.get(t, "wilant", "?goal="+far.ID); p.Available || !strings.Contains(p.Reason, "400") {
		t.Errorf("over 400 days: available %v, reason %q", p.Available, p.Reason)
	}
}

func TestProjectionOfAnotherRidersGoalIsA404NotA403(t *testing.T) {
	h := newProjectionHarness(t)
	g := h.goal(t, "Gran Fondo", "2026-12-13", "A")
	if status, _ := h.get(t, "alice", "?goal="+g.ID); status != http.StatusNotFound {
		t.Errorf("another rider's goal = %d, want 404", status)
	}
	if status, _ := h.get(t, "wilant", "?goal=nope"); status != http.StatusNotFound {
		t.Errorf("an unknown goal = %d, want 404", status)
	}
}

func TestProjectionDegradesToUnavailableWhenAnInputFails(t *testing.T) {
	h := newProjectionHarness(t)
	h.goal(t, "Gran Fondo", "2026-12-13", "A")
	if _, err := h.conn.Exec(`DROP TABLE scheduled_weeks`); err != nil {
		t.Fatal(err)
	}
	status, p := h.get(t, "wilant", "")
	if status != http.StatusOK || p.Available || p.Reason == "" {
		t.Errorf("status %d, available %v, reason %q, want 200 and unavailable", status, p.Available, p.Reason)
	}
}

func TestProjectionWritesNothing(t *testing.T) {
	h := newProjectionHarness(t)
	h.goal(t, "Gran Fondo", "2026-12-13", "A")
	before := h.rowCounts(t)
	h.get(t, "wilant", "")
	h.get(t, "wilant", "?goal=nope")
	if after := h.rowCounts(t); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("row counts changed: %v -> %v", before, after)
	}
}

func (h *projectionHarness) rowCounts(t *testing.T) map[string]int {
	t.Helper()
	rows, err := h.conn.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	_ = rows.Close()
	out := map[string]int{}
	for _, n := range names {
		var c int
		if err := h.conn.QueryRow(`SELECT COUNT(*) FROM "` + n + `"`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		out[n] = c
	}
	return out
}

func TestProjectionLogsRiderGoalAndVerdictOnly(t *testing.T) {
	h := newProjectionHarness(t)
	g := h.goal(t, "Gran Fondo", "2026-12-13", "A")
	var records []spyRecord
	h.srv.Log = slog.New(spyHandler{&records})
	_, p := h.get(t, "wilant", "")

	var found []spyRecord
	for _, r := range records {
		if strings.Contains(r.msg, "projection") {
			found = append(found, r)
		}
		for k, v := range r.attrs {
			lk := strings.ToLower(k + " " + v + " " + r.msg)
			if strings.Contains(lk, "ctl") || strings.Contains(lk, "tsb") || strings.Contains(lk, "watt") || strings.Contains(lk, "ftp") {
				t.Errorf("log %q carries a health value: %s=%s", r.msg, k, v)
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d projection log lines, want 1", len(found))
	}
	a := found[0].attrs
	if a["rider"] != "wilant" || a["goal"] != g.ID || a["verdict"] != p.Verdict.Key || len(a) != 3 {
		t.Errorf("attrs = %v, want exactly rider, goal and verdict", a)
	}
}

// 23:30 UTC on the 7th is already the 8th in Brussels. The projection is
// anchored on the server clock's own date, so it must start on the 7th under
// any TZ the test process runs in.
func TestProjectionStartsOnTheClocksDateLateInTheEvening(t *testing.T) {
	h := newProjectionHarness(t)
	h.now = time.Date(2026, time.October, 7, 23, 30, 0, 0, time.UTC)
	h.goal(t, "Gran Fondo", "2026-12-13", "A")
	_, p := h.get(t, "wilant", "")
	if !p.Available || p.Points[0].Date != "2026-10-07" {
		t.Errorf("first point %v (available %v), want 2026-10-07", p.Points[0], p.Available)
	}
}

func TestProjectionActualSessionTodayReplacesThePlannedWorkout(t *testing.T) {
	h := newProjectionHarness(t)
	h.goal(t, "Gran Fondo", "2026-12-13", "A")
	_, before := h.get(t, "wilant", "")
	if _, err := h.store.UpsertSession(context.Background(), workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ride-today", Sport: "cycling",
		Date: "2026-10-07", DurationSeconds: 3600, TrainingLoad: 12,
	}); err != nil {
		t.Fatal(err)
	}
	_, after := h.get(t, "wilant", "")
	if after.Points[0].Load != 12 {
		t.Errorf("today's load = %v, want the ridden 12 (was %v planned)", after.Points[0].Load, before.Points[0].Load)
	}
}
