package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/fitnesstest"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const linkedRide = "garmin:9100"

func (h *ftpResultHarness) link(body string) (*http.Response, syncFTPTestsOut) {
	h.t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/sessions/"+linkedRide+"/workout", body)
	var out syncFTPTestsOut
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			h.t.Fatal(err)
		}
	}
	return resp, out
}

func (h *ftpResultHarness) analysedAgainst() string {
	h.t.Helper()
	a, ok, err := h.srv.Training.GetAnalysis(context.Background(), linkedRide)
	if err != nil {
		h.t.Fatal(err)
	}
	if !ok {
		return "<none>"
	}
	return a.WorkoutID
}

func (h *ftpResultHarness) moveTest(date string) {
	h.t.Helper()
	if _, err := h.srv.Training.UpdateWorkout(context.Background(), h.testID, workout.UpdateWorkoutRequest{Date: &date}); err != nil {
		h.t.Fatal(err)
	}
}

// plainWorkout plans an ordinary (not a test) session on date.
func (h *ftpResultHarness) plainWorkout(date string) string {
	h.t.Helper()
	req, _ := fitnesstest.BuildTestWorkout(fitnesstest.ProtocolTwentyMinute, 250)
	req.Rider, req.Date, req.Name = "wilant", date, "Threshold"
	wk, err := h.srv.Training.CreateWorkout(context.Background(), req)
	if err != nil {
		h.t.Fatal(err)
	}
	return wk.ID
}

// The case that prompted this: a test planned for another day is ridden
// early, so the automatic match (same date) never finds it. Linking the ride
// by hand reads the test and moves it to the day it was ridden.
func TestLinkingARideToATestPlannedForAnotherDayReadsTheTest(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 400, workout.RiderProfile{FTPWatts: 250})
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	h.moveTest(tomorrow)

	if out := h.sync(); len(out.FTPTests) != 0 {
		t.Fatalf("ftpTests = %+v before linking, want none (the ride is on another day)", out.FTPTests)
	}
	if got := h.analysedAgainst(); got != "" {
		t.Fatalf("analysed against %q before linking, want no planned session", got)
	}

	resp, out := h.link(`{"workoutId":"` + h.testID + `"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("link status = %d", resp.StatusCode)
	}
	if len(out.FTPTests) != 1 || out.FTPTests[0].WorkoutID != h.testID || out.FTPTests[0].FTPWatts != 300 {
		t.Fatalf("ftpTests = %+v, want the ramp read at 300 W", out.FTPTests)
	}
	if got := h.analysedAgainst(); got != h.testID {
		t.Errorf("analysed against %q, want the test", got)
	}
	wk, err := h.srv.Training.GetWorkout(context.Background(), h.testID)
	if err != nil {
		t.Fatal(err)
	}
	if wk.Date != h.date {
		t.Errorf("test dated %s, want moved to the ride's day %s", wk.Date, h.date)
	}

	// A later sync keeps the link rather than re-matching.
	h.sync()
	if got := h.analysedAgainst(); got != h.testID {
		t.Errorf("after another sync analysed against %q, want the test still", got)
	}
}

// "Not a planned session" sticks across syncs, and auto hands the ride back
// to the automatic match.
func TestUnlinkingARideSticksAndAutoRestoresTheMatch(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 200, workout.RiderProfile{FTPWatts: 250})
	h.moveTest(time.Now().AddDate(0, 0, 3).Format("2006-01-02"))
	plain := h.plainWorkout(h.date)

	h.sync()
	if got := h.analysedAgainst(); got != plain {
		t.Fatalf("analysed against %q, want the day's session %q", got, plain)
	}

	if resp, _ := h.link(`{"workoutId":""}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("unlink status = %d", resp.StatusCode)
	}
	if got := h.analysedAgainst(); got != "" {
		t.Errorf("analysed against %q after unlinking, want none", got)
	}
	h.sync()
	if got := h.analysedAgainst(); got != "" {
		t.Errorf("a sync re-matched an unlinked ride to %q", got)
	}

	if resp, _ := h.link(`{"auto":true}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("auto status = %d", resp.StatusCode)
	}
	if got := h.analysedAgainst(); got != plain {
		t.Errorf("analysed against %q after auto, want the day's session again", got)
	}
}

// One result per test, ever: a ride that already gave a test its result
// cannot be moved off it.
func TestARideThatGaveATestItsResultStaysLinked(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 400, workout.RiderProfile{FTPWatts: 250})
	if out := h.sync(); len(out.FTPTests) != 1 {
		t.Fatalf("ftpTests = %+v, want the same-day test read", out.FTPTests)
	}
	if resp, _ := h.link(`{"workoutId":""}`); resp.StatusCode != http.StatusConflict {
		t.Errorf("unlink status = %d, want 409", resp.StatusCode)
	}
	if got := h.analysedAgainst(); got != h.testID {
		t.Errorf("analysed against %q, want the test untouched", got)
	}
}

// A session another ride is already scored against is refused, rather than
// silently changing that ride's verdict.
func TestLinkingRefusesASessionAnotherRideHas(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 200, workout.RiderProfile{FTPWatts: 250})
	h.moveTest(time.Now().AddDate(0, 0, 3).Format("2006-01-02"))
	earlier := time.Now().AddDate(0, 0, -3)
	plain := h.plainWorkout(earlier.Format("2006-01-02"))
	h.fake.activities = append(h.fake.activities, garmin.Activity{ID: "9200", Sport: "cycling", StartTime: earlier, DurationSeconds: 1200, AvgPowerWatts: 200})
	h.sync()

	if resp, _ := h.link(`{"workoutId":"` + plain + `"}`); resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409 for a session another ride has", resp.StatusCode)
	}
}

func TestLinkingIsOwnerOnly(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 200, workout.RiderProfile{FTPWatts: 250})
	h.sync()

	resp := h.as("someone", "cyclists", http.MethodPut, "/api/training/sessions/"+linkedRide+"/workout", `{"workoutId":""}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("another rider's ride: status = %d, want 404", resp.StatusCode)
	}

	req, _ := fitnesstest.BuildTestWorkout(fitnesstest.ProtocolTwentyMinute, 250)
	req.Rider, req.Date = "someone", h.date
	theirs, err := h.srv.Training.CreateWorkout(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.link(`{"workoutId":"` + theirs.ID + `"}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another rider's workout: status = %d, want 404", resp.StatusCode)
	}
}
