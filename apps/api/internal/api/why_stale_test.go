package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A why explains the workout as the automatic change left it. Once anything
// else has rewritten the workout, the old explanation describes a session that
// no longer exists, so the API stops sending it.

const (
	jan  = "2026-01-01T00:00:00Z"
	june = "2026-06-01T00:00:00.000000000Z"
)

// staleSetup makes a plan session whose last write was in January and gives it
// a why written in June: a genuine row, recorded after the change it explains.
func staleSetup(t *testing.T, h *pushHarness, goal string) workout.Workout {
	t.Helper()
	w := h.altRung(goal, indoorFuture, "threshold", 4)
	conn := h.db.Conn()
	if _, err := conn.Exec(`UPDATE workouts SET created_at = ?, updated_at = ? WHERE id = ?`, jan, jan, w.ID); err != nil {
		t.Fatal(err)
	}
	rec := why.NewRecord(why.ReadinessCaution, "Eased one level — HRV is unbalanced today", why.ReadinessInputs{Verdict: "caution"})
	if err := h.training.RecordAdjustment(context.Background(), "wilant", workout.SubjectWorkout, w.ID, rec, "2026-06-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`UPDATE adjustments SET created_at = ? WHERE subject_id = ?`, june, w.ID); err != nil {
		t.Fatal(err)
	}
	return w
}

func (h *pushHarness) whyOf(id string) *whyOut {
	h.t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+id, "")
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("get %s = %d", id, resp.StatusCode)
	}
	var out whyWorkoutOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	return out.Why
}

// shownThenHiddenBy checks the control (the why shows while the workout's last
// write predates it) and then that action, which rewrites the workout, hides it.
func shownThenHiddenBy(t *testing.T, h *pushHarness, id string, action func()) {
	t.Helper()
	if h.whyOf(id) == nil {
		t.Fatal("control: the why is missing before anything rewrote the workout")
	}
	action()
	if got := h.whyOf(id); got != nil {
		t.Errorf("a why from before the rewrite is still shown: %+v", got)
	}
}

func TestAnOlderWhyIsHiddenAfterAManualEdit(t *testing.T) {
	h := newReplanHarness(t)
	w := staleSetup(t, h, h.indoorSetup(false))
	shownThenHiddenBy(t, h, w.ID, func() {
		resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+w.ID, `{"name":"My own session"}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("patch = %d", resp.StatusCode)
		}
	})
}

func TestAnOlderWhyIsHiddenAfterAnIndoorConvert(t *testing.T) {
	h := newReplanHarness(t)
	w := staleSetup(t, h, h.indoorSetup(false))
	shownThenHiddenBy(t, h, w.ID, func() {
		if resp, _ := h.convert("wilant", w.ID, ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("convert = %d", resp.StatusCode)
		}
	})
}

func TestAnOlderWhyIsHiddenAfterAnAlternateSwapAndItsRevert(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	h.altLevel("threshold", 5.2)
	w := staleSetup(t, h, goal)
	shownThenHiddenBy(t, h, w.ID, func() {
		if resp, _ := h.altSwap("wilant", "cyclists", w.ID, "harder"); resp.StatusCode != http.StatusOK {
			t.Fatalf("swap = %d", resp.StatusCode)
		}
	})
	// Put the workout's last write back before the why, then revert it.
	if _, err := h.db.Conn().Exec(`UPDATE workouts SET updated_at = ? WHERE id = ?`, jan, w.ID); err != nil {
		t.Fatal(err)
	}
	shownThenHiddenBy(t, h, w.ID, func() {
		if resp, _ := h.altRevert("wilant", "cyclists", w.ID); resp.StatusCode != http.StatusOK {
			t.Fatalf("revert = %d", resp.StatusCode)
		}
	})
}

func TestAnOlderWhyIsHiddenAfterATrainNowReplace(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.tnSetup(false)
	w := h.altRung(goal, indoorToday, "threshold", 4)
	conn := h.db.Conn()
	if _, err := conn.Exec(`UPDATE workouts SET created_at = ?, updated_at = ? WHERE id = ?`, jan, jan, w.ID); err != nil {
		t.Fatal(err)
	}
	rec := why.NewRecord(why.ReadinessCaution, "Eased", why.ReadinessInputs{Verdict: "caution"})
	if err := h.training.RecordAdjustment(context.Background(), "wilant", workout.SubjectWorkout, w.ID, rec, "2026-06-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`UPDATE adjustments SET created_at = ? WHERE subject_id = ?`, june, w.ID); err != nil {
		t.Fatal(err)
	}
	shownThenHiddenBy(t, h, w.ID, func() {
		_, out := h.tnGet("wilant", "cyclists", "?minutes=75")
		p, ok := out.get("planned")
		if !ok {
			t.Fatalf("no planned suggestion in %s", out.kinds())
		}
		if resp, _ := h.tnApply("wilant", "cyclists", 75, p); resp.StatusCode != http.StatusOK {
			t.Fatalf("apply = %d", resp.StatusCode)
		}
	})
}

// TestTheResponsesOfTheRewritingEndpointsCarryNoStaleWhy checks the answer of
// the action itself, not only a later read.
func TestTheResponseToARewriteCarriesNoStaleWhy(t *testing.T) {
	h := newReplanHarness(t)
	w := staleSetup(t, h, h.indoorSetup(false))
	resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+w.ID, `{"name":"My own session"}`)
	var out whyWorkoutOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Why != nil {
		t.Errorf("the PATCH response carries %+v", out.Why)
	}
}

// TestAFreshAutomaticEasingStillShowsItsWhy: the automatic write precedes its
// record, so a genuine row is never older than the workout it explains.
func TestAFreshAutomaticEasingStillShowsItsWhy(t *testing.T) {
	h := newTomorrowHarness(t)
	orig := h.cautionToday()
	h.srv.AdaptWorkouts(context.Background())

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+orig.ID, "")
	var out whyWorkoutOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Why == nil || out.Why.Rule != "readiness_caution" {
		t.Fatalf("why = %+v, want the fresh easing's reason", out.Why)
	}
	week := weekWorkoutsFor(t, h)
	if week[orig.ID].Why == nil {
		t.Error("the week lost a fresh why")
	}
}

func weekWorkoutsFor(t *testing.T, h *tomorrowHarness) map[string]whyWorkoutOut {
	t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts", "")
	var rows []whyWorkoutOut
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	out := map[string]whyWorkoutOut{}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}
