package api_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The parity check is on for every test that has a database (testdb.Path), so
// these tests hold the cases where the text and the typed flags could drift
// apart without any write failing: each one reads the stored flags back as well
// as the behaviour the old markers gave.

func (h *onceHarness) putWorkout(t *testing.T, id, body string) {
	t.Helper()
	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+id, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH workout: status %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
}

func (h *onceHarness) stored(t *testing.T, id string) workout.Workout {
	t.Helper()
	wk, err := h.store.GetWorkout(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return wk
}

// A rider who rewrites a plan session's description has taken it over: replan
// leaves it alone because its origin is the rider's now, not the plan's.
func TestRiderDescriptionEditLeavesSessionToTheRider(t *testing.T) {
	h := newOnceHarness(t)
	session := h.hardSession(t, "2026-10-10", 60)
	if got := h.stored(t, session.ID).Flags.Origin; got != workout.OriginPlan {
		t.Fatalf("a fresh plan session has origin %q, want plan", got)
	}

	h.putWorkout(t, session.ID, `{"description":"My own session, my own words"}`)
	if got := h.stored(t, session.ID).Flags.Origin; got != workout.OriginRider {
		t.Errorf("origin after the rider's edit = %q, want rider", got)
	}

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/replan", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("replan status %d", resp.StatusCode)
	}
	after := h.stored(t, session.ID)
	if after.Description != "My own session, my own words" || !reflect.DeepEqual(after.Steps, session.Steps) || after.Flags.Origin != workout.OriginRider {
		t.Errorf("replan touched the rider's session: %+v", after)
	}
}

// Moving a plan session by hand records the day it left, as the old note did,
// so a fill does not plan the same session onto the vacated day.
func TestRiderDateMoveKeepsVacatedDayTaken(t *testing.T) {
	h := newOnceHarness(t)
	session := h.hardSession(t, "2026-10-10", 60)

	h.putWorkout(t, session.ID, `{"date":"2026-10-11"}`)
	moved := h.stored(t, session.ID)
	if moved.Flags.MovedFrom != "2026-10-10" || moved.Flags.MovedBy != workout.MovedByRider {
		t.Errorf("flags after the move = %+v, want moved from 2026-10-10 by the rider", moved.Flags)
	}

	thisMon, _ := weekBounds(h.now)
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals/"+h.goal.ID+"/schedule",
		`{"weekStart":"`+thisMon.Format("2006-01-02")+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fill status %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	for _, wk := range h.all(t) {
		if wk.Date == "2026-10-10" {
			t.Errorf("the fill planned %q onto the day the rider vacated", wk.Name)
		}
	}

	// Moving it again keeps the first day it left.
	h.putWorkout(t, session.ID, `{"date":"2026-10-12"}`)
	if got := h.stored(t, session.ID).Flags; got.MovedFrom != "2026-10-10" {
		t.Errorf("a second move forgot the first day: %+v", got)
	}
}

// A ride-together answer that cannot be recorded puts the session back exactly,
// flags included.
func TestFailedTogetherAnswerRestoresFlags(t *testing.T) {
	h := newCrewPlanHarness(t)
	ts, p := h.agreedSetup()
	before, _ := h.get("wilant", ts.wilant.ID)

	// The answer is recorded by updating the caller's member row; make that fail.
	if _, err := h.db.Conn().Exec(`CREATE TRIGGER refuse_member_update BEFORE UPDATE ON ride_together_members
		BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	resp, _ := h.answer("wilant", p.ID, "accept")
	if resp.StatusCode == http.StatusOK {
		t.Fatal("accept succeeded although recording the answer failed")
	}

	after, _ := h.get("wilant", ts.wilant.ID)
	if after.Date != before.Date || after.Description != before.Description || !reflect.DeepEqual(after.Flags, before.Flags) {
		t.Errorf("the failed answer left the session changed:\nbefore %+v %q\nafter  %+v %q",
			before.Flags, before.Description, after.Flags, after.Description)
	}
}

// Swapping, reverting and swapping again keeps Swapped true, as the note's
// marker does each time.
func TestSwapRevertSwapKeepsSwapped(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	h.altLevel("threshold", 5.2)
	orig := h.altRung(goal, indoorFuture, "threshold", 4)
	stored := func() workout.Workout {
		wk, err := h.training.GetWorkout(context.Background(), orig.ID)
		if err != nil {
			t.Fatal(err)
		}
		return wk
	}
	if stored().Flags.Swapped {
		t.Fatal("a fresh session is already swapped")
	}
	for i, step := range []func() *http.Response{
		func() *http.Response { r, _ := h.altSwap("wilant", "cyclists", orig.ID, "harder"); return r },
		func() *http.Response { r, _ := h.altRevert("wilant", "cyclists", orig.ID); return r },
		func() *http.Response { r, _ := h.altSwap("wilant", "cyclists", orig.ID, "harder"); return r },
	} {
		if resp := step(); resp.StatusCode != http.StatusOK {
			t.Fatalf("step %d: status %d", i, resp.StatusCode)
		}
		if !stored().Flags.Swapped {
			t.Errorf("step %d: Flags.Swapped = false, but the description carries the swap note", i)
		}
	}
}
