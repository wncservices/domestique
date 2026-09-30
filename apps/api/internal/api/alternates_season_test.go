package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The whole-season refresh rebuilds untouched sessions from the plan as it
// stands; a session the rider swapped is not one of them, however old it is.
func TestTheSeasonRefreshLeavesASwappedSessionAlone(t *testing.T) {
	h := newSeasonHarness(t)
	ctx := context.Background()
	days := []string{"mon", "tue", "wed", "thu", "fri", "sat"}
	h.profile(t, days, 0)
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
	week := h.week(t, target)
	if len(week) < 2 {
		t.Fatalf("target week has %d sessions, want several", len(week))
	}
	control, swapTarget := week[0], week[1]

	// Swap whichever alternate the plan offers for that session.
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+swapTarget.ID+"/alternates", "")
	var opts altOut
	if err := json.NewDecoder(resp.Body).Decode(&opts); err != nil || len(opts.Options) == 0 {
		t.Fatalf("options = %+v (err %v), want at least one", opts, err)
	}
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+swapTarget.ID+"/alternates",
		`{"kind":"`+opts.Options[0].Kind+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("swap = %d", resp.StatusCode)
	}
	swapped, err := h.store.GetWorkout(ctx, swapTarget.ID)
	if err != nil {
		t.Fatal(err)
	}

	// FTP becomes known, so a rebuilt session would carry power targets.
	h.profile(t, days, 250)
	h.now = utcNoon(2026, time.December, 9)
	h.srv.AutoScheduleTick(ctx)

	got, err := h.store.GetWorkout(ctx, swapTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, swapped) {
		t.Errorf("the refresh rewrote a swapped session: %+v -> %+v", swapped, got)
	}
	if rebuilt, _ := h.store.GetWorkout(ctx, control.ID); !hasPowerTarget(rebuilt) {
		t.Error("the untouched control session was not refreshed, so this test proves nothing")
	}
}
