package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// weekOut mirrors trainingWeekDTO's JSON shape (see trainingweek.go) closely
// enough for assertions — a local struct rather than importing the
// unexported DTO type, the same pattern decodeGoal/decodeWorkoutOut follow.
type weekOut struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Today string `json:"today"`
	Focus *struct {
		GoalID      string  `json:"goalId"`
		Name        string  `json:"name"`
		Priority    string  `json:"priority"`
		Sport       string  `json:"sport"`
		EventDate   string  `json:"eventDate"`
		DaysToEvent *int    `json:"daysToEvent"`
		WeekNumber  int     `json:"weekNumber"`
		TotalWeeks  int     `json:"totalWeeks"`
		Phase       string  `json:"phase"`
		Recovery    bool    `json:"recovery"`
		TargetHours float64 `json:"targetHours"`
	} `json:"focus"`
	Days []struct {
		Date      string          `json:"date"`
		Status    string          `json:"status"`
		Planned   []workoutDTOOut `json:"planned"`
		Completed []struct {
			ID              string       `json:"id"`
			DurationSeconds float64      `json:"durationSeconds"`
			Analysis        *analysisOut `json:"analysis"`
		} `json:"completed"`
	} `json:"days"`
	Totals struct {
		PlannedSeconds   float64 `json:"plannedSeconds"`
		CompletedSeconds float64 `json:"completedSeconds"`
	} `json:"totals"`
}

// weekClock is the fixed "now" every trainingweek test uses: Wednesday
// 2026-09-30, whose Monday is 2026-09-28. Deterministic dates matter here —
// compliance.Day's done/missed/upcoming classification depends on which
// side of "today" a date falls.
func weekClock() time.Time {
	return time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
}

func decodeWeek(t *testing.T, resp *http.Response) weekOut {
	t.Helper()
	var out weekOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func createWorkoutOn(t *testing.T, h *trainingHarness, rider, name, date string) {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"sport":"cycling","date":%q,"steps":[{"name":"ride","duration":"time","seconds":3600,"target":"open"}]}`, name, date)
	resp := h.as(rider, "cyclists", http.MethodPost, "/api/training/workouts", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create workout %q: status = %d, want 201", name, resp.StatusCode)
	}
}

func createGoal(t *testing.T, h *trainingHarness, rider, name, eventDate, priority string) {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"sport":"cycling","eventDate":%q,"priority":%q}`, name, eventDate, priority)
	resp := h.as(rider, "cyclists", http.MethodPost, "/api/training/goals", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create goal %q: status = %d, want 201", name, resp.StatusCode)
	}
}

func TestTrainingWeekDefaultsToThisMonday(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = weekClock

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/week", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	week := decodeWeek(t, resp)
	if week.Start != "2026-09-28" {
		t.Errorf("start = %q, want 2026-09-28", week.Start)
	}
	if week.End != "2026-10-04" {
		t.Errorf("end = %q, want 2026-10-04", week.End)
	}
	if week.Today != "2026-09-30" {
		t.Errorf("today = %q, want 2026-09-30", week.Today)
	}
	if len(week.Days) != 7 {
		t.Fatalf("days = %d, want 7", len(week.Days))
	}
	wantDates := []string{"2026-09-28", "2026-09-29", "2026-09-30", "2026-10-01", "2026-10-02", "2026-10-03", "2026-10-04"}
	for i, d := range week.Days {
		if d.Date != wantDates[i] {
			t.Errorf("days[%d].date = %q, want %q", i, d.Date, wantDates[i])
		}
	}
}

func TestTrainingWeekSnapsToMonday(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = weekClock

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/week?start=2026-10-08", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	week := decodeWeek(t, resp)
	if week.Start != "2026-10-05" {
		t.Errorf("start = %q, want 2026-10-05", week.Start)
	}
}

func TestTrainingWeekRejectsBadStart(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = weekClock

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/week?start=nope", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestTrainingWeekStatusesAndTotals(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = weekClock

	// Mon: workout 1h + session 1h -> done.
	createWorkoutOn(t, h, "wilant", "Monday ride", "2026-09-28")
	h.seedSession("wilant", "2026-09-28", 1)
	// Tue: workout 1h, no session -> missed (in the past relative to Wed).
	createWorkoutOn(t, h, "wilant", "Tuesday ride", "2026-09-29")
	// Thu: workout 1h, no session -> upcoming (in the future).
	createWorkoutOn(t, h, "wilant", "Thursday ride", "2026-10-01")
	// Sat: session 0.5h, no workout -> unplanned.
	h.seedSession("wilant", "2026-10-03", 0.5)
	// Sun: nothing -> rest.

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/week", "")
	week := decodeWeek(t, resp)

	byDate := map[string]int{}
	for i, d := range week.Days {
		byDate[d.Date] = i
	}
	check := func(date, wantStatus string) {
		i, ok := byDate[date]
		if !ok {
			t.Fatalf("no day for %s", date)
		}
		if week.Days[i].Status != wantStatus {
			t.Errorf("%s status = %q, want %q", date, week.Days[i].Status, wantStatus)
		}
	}
	check("2026-09-28", "done")
	check("2026-09-29", "missed")
	check("2026-10-01", "upcoming")
	check("2026-10-03", "unplanned")
	check("2026-10-04", "rest")

	if week.Totals.PlannedSeconds != 10800 {
		t.Errorf("totals.plannedSeconds = %v, want 10800", week.Totals.PlannedSeconds)
	}
	if week.Totals.CompletedSeconds != 5400 {
		t.Errorf("totals.completedSeconds = %v, want 5400", week.Totals.CompletedSeconds)
	}

	mon := week.Days[byDate["2026-09-28"]]
	if len(mon.Planned) != 1 || mon.Planned[0].PlannedSeconds != 3600 {
		t.Errorf("mon planned = %+v", mon.Planned)
	}
}

func TestTrainingWeekIsOwnerOnly(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = weekClock

	createWorkoutOn(t, h, "other", "Other's Monday ride", "2026-09-28")
	h.seedSession("other", "2026-09-28", 1)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/week", "")
	week := decodeWeek(t, resp)
	mon := week.Days[0]
	if mon.Date != "2026-09-28" {
		t.Fatalf("days[0] = %+v, want 2026-09-28", mon)
	}
	if mon.Status != "rest" {
		t.Errorf("mon status = %q, want rest", mon.Status)
	}
	if len(mon.Planned) != 0 || len(mon.Completed) != 0 {
		t.Errorf("mon = %+v, want empty planned/completed", mon)
	}
}

func TestTrainingWeekIgnoresUndatedWorkouts(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = weekClock

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts",
		`{"name":"Undated template","sport":"cycling","steps":[{"name":"ride","duration":"time","seconds":3600,"target":"open"}]}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create undated workout: status = %d", resp.StatusCode)
	}

	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/week", "")
	week := decodeWeek(t, resp)
	for _, d := range week.Days {
		if len(d.Planned) != 0 {
			t.Errorf("day %s has planned = %+v, want none", d.Date, d.Planned)
		}
	}
}

func TestTrainingWeekFocusPrefersPriorityThenDate(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = weekClock

	createGoal(t, h, "wilant", "B goal", "2026-11-01", "B")
	createGoal(t, h, "wilant", "A goal", "2026-12-20", "A")
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"C goal","sport":"cycling","priority":"C"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create C goal: status = %d", resp.StatusCode)
	}

	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/week", "")
	week := decodeWeek(t, resp)
	if week.Focus == nil {
		t.Fatalf("focus = nil, want the A goal")
	}
	if week.Focus.GoalID != "a-goal" {
		t.Errorf("focus.goalId = %q, want a-goal", week.Focus.GoalID)
	}
	if week.Focus.WeekNumber < 1 {
		t.Errorf("focus.weekNumber = %d, want >= 1", week.Focus.WeekNumber)
	}
	if week.Focus.TotalWeeks < week.Focus.WeekNumber {
		t.Errorf("focus.totalWeeks = %d, want >= weekNumber (%d)", week.Focus.TotalWeeks, week.Focus.WeekNumber)
	}
	if week.Focus.Phase == "" {
		t.Errorf("focus.phase = %q, want non-empty", week.Focus.Phase)
	}
	if week.Focus.DaysToEvent == nil || *week.Focus.DaysToEvent != 81 {
		got := "nil"
		if week.Focus.DaysToEvent != nil {
			got = fmt.Sprintf("%d", *week.Focus.DaysToEvent)
		}
		t.Errorf("focus.daysToEvent = %s, want 81", got)
	}
}

func TestTrainingWeekSkipsPastGoals(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = weekClock

	createGoal(t, h, "wilant", "Past goal", "2026-09-01", "A")

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/week", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	week := decodeWeek(t, resp)
	if week.Focus != nil {
		t.Errorf("focus = %+v, want none", week.Focus)
	}
}

func TestTrainingWeekWithoutProfile(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = weekClock

	createGoal(t, h, "wilant", "A goal", "2026-12-20", "A")

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/week", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	week := decodeWeek(t, resp)
	if week.Focus == nil {
		t.Fatalf("focus = nil, want present")
	}
	if week.Focus.TargetHours != 0 {
		t.Errorf("focus.targetHours = %v, want 0 (no profile saved)", week.Focus.TargetHours)
	}
}

func TestTrainingWeekNoGoals(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = weekClock

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/week", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	week := decodeWeek(t, resp)
	if week.Focus != nil {
		t.Errorf("focus = %+v, want none", week.Focus)
	}
	if len(week.Days) != 7 {
		t.Errorf("days = %d, want 7", len(week.Days))
	}
}

// TestTrainingWeekIncludesAnalysisForCompletedSessions is Task 7's own RED
// case for the week endpoint: a completed session's day carries the same
// analysis /api/training/fitness does, since both funnel through
// completedSessionDTOFrom.
func TestTrainingWeekIncludesAnalysisForCompletedSessions(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = weekClock

	h.seedSession("wilant", "2026-09-28", 1)
	if err := h.store.SaveAnalysis(context.Background(), workout.SessionAnalysis{
		SessionID: "garmin:2026-09-28", Rider: "wilant", Outcome: "struggled", LoadSource: "estimated",
		TSS: 40,
	}); err != nil {
		t.Fatal(err)
	}

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/week", "")
	week := decodeWeek(t, resp)
	byDate := map[string]int{}
	for i, d := range week.Days {
		byDate[d.Date] = i
	}
	mon := week.Days[byDate["2026-09-28"]]
	if len(mon.Completed) != 1 {
		t.Fatalf("mon.Completed = %+v, want 1", mon.Completed)
	}
	if mon.Completed[0].Analysis == nil {
		t.Fatalf("analysis = nil, want present")
	}
	if mon.Completed[0].Analysis.Outcome != "struggled" || mon.Completed[0].Analysis.TSS != 40 {
		t.Errorf("analysis = %+v", mon.Completed[0].Analysis)
	}
}
