package api_test

import (
	"context"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// futureBySport counts the sessions dated today or later, by sport.
func (h *seasonHarness) futureBySport(t *testing.T) map[model.Sport]int {
	t.Helper()
	today := h.now.Format("2006-01-02")
	out := map[model.Sport]int{}
	for _, w := range h.workouts(t) {
		if w.Date >= today {
			out[w.Sport]++
		}
	}
	return out
}

// The rider's journey: a cycling goal, deleted, then a running goal. From today
// on the calendar holds the running plan and nothing of the cycling one.
func TestACyclingGoalDeletedThenARunningGoalLeavesOnlyRunning(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	cycling := h.createGoal(t, `{"name":"Big ride","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	if h.futureBySport(t)[model.SportCycling] == 0 {
		t.Fatal("fixture: the cycling goal planned nothing")
	}

	h.deleteGoal(t, cycling.ID)
	if n := len(h.futureBySport(t)); n != 0 {
		t.Fatalf("with no goal the calendar still holds %v", h.futureBySport(t))
	}

	h.createGoal(t, `{"name":"Half marathon","sport":"running","eventDate":"2027-04-11"}`)
	h.srv.WaitForBackground()
	got := h.futureBySport(t)
	if got[model.SportCycling] != 0 || got[model.SportRunning] == 0 {
		t.Errorf("after switching to running the calendar holds %v, want running only", got)
	}
}

// A goal deleted before deletes took its sessions left them as the rider's own,
// unchanged plan sessions with no goal. They held their days, so a new goal
// planned around them. The one-time cleanup removes those from today on, keeps
// the past (history) and anything the rider changed, and the rider's goals take
// the freed days.
func TestLeftoversOfAnOldGoalDeleteAreRemovedAndThePlanRefills(t *testing.T) {
	h := newSeasonHarness(t)
	ctx := context.Background()
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	old := h.createGoal(t, `{"name":"Big ride","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()

	// What a delete on 2.15.0 did: the sessions stay, goal-less and the rider's.
	yesterday := h.now.AddDate(0, 0, -1).Format("2006-01-02")
	past, err := h.store.CreateWorkout(ctx, api.LegacyFlagged(generated("wilant", old.ID, "Endurance ride", yesterday, 3600)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.conn.Exec(`UPDATE workouts SET goal_id = '', origin = 'rider' WHERE goal_id = ?`, old.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.store.DeleteGoal(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	// One leftover the rider has since changed is theirs.
	var edited workout.Workout
	today := h.now.Format("2006-01-02")
	for _, w := range h.workouts(t) {
		if w.Date > today {
			edited = w
			break
		}
	}
	if _, err := h.conn.Exec(`UPDATE workouts SET edited_at = '2026-10-06T08:00:00Z' WHERE id = ?`, edited.ID); err != nil {
		t.Fatal(err)
	}

	h.createGoal(t, `{"name":"Half marathon","sport":"running","eventDate":"2027-04-11"}`)
	h.srv.WaitForBackground()
	before := h.futureBySport(t)
	if before[model.SportCycling] < 2 {
		t.Fatalf("fixture: the leftovers are not in the way (%v)", before)
	}

	if err := h.srv.RemoveGoalLeftovers(ctx); err != nil {
		t.Fatal(err)
	}
	h.srv.WaitForBackground()

	after := h.futureBySport(t)
	if after[model.SportCycling] != 1 {
		t.Errorf("cycling sessions from today on = %d, want only the one the rider edited", after[model.SportCycling])
	}
	if after[model.SportRunning] <= before[model.SportRunning] {
		t.Errorf("running sessions %d before, %d after: the freed days were not planned", before[model.SportRunning], after[model.SportRunning])
	}
	for name, id := range map[string]string{"past": past.ID, "edited": edited.ID} {
		if _, err := h.store.GetWorkout(ctx, id); err != nil {
			t.Errorf("the %s leftover was removed: %v", name, err)
		}
	}
	if d := twoOnADay(h.workouts(t), today); d != "" {
		t.Errorf("two sessions on %s", d)
	}

	// It runs once: a later goal-less plan session is left alone.
	later, err := h.store.CreateWorkout(ctx, api.LegacyFlagged(generated("wilant", "", "Endurance ride", h.now.AddDate(0, 0, 30).Format("2006-01-02"), 3600)))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.srv.RemoveGoalLeftovers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.GetWorkout(ctx, later.ID); err != nil {
		t.Errorf("a second run removed a session: %v", err)
	}
}
