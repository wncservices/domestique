package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// threshold plans a rider-built 20-minute threshold session at level on date.
func (h *ftpResultHarness) threshold(date string, level float64) string {
	h.t.Helper()
	wk, err := h.srv.Training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "Threshold " + date, Date: date,
		Zone: workout.ZoneThreshold, Level: level,
		Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 1200, Target: workout.TargetOpen}},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return wk.ID
}

func (h *ftpResultHarness) rate(body string) {
	h.t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/sessions/"+linkedRide+"/feel", body)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("rate %s = %d", body, resp.StatusCode)
	}
}

func (h *ftpResultHarness) analysis() workout.SessionAnalysis {
	h.t.Helper()
	a, ok, err := h.srv.Training.GetAnalysis(context.Background(), linkedRide)
	if err != nil || !ok {
		h.t.Fatalf("analysis = %v, %v", ok, err)
	}
	return a
}

// relinkSetup rides a threshold session, rates the ride, and plans a second
// session on another day for the ride to be re-linked to.
func relinkSetup(t *testing.T, watts int, rating string) (*ftpResultHarness, string) {
	t.Helper()
	h := newFTPResultHarness(t, "ramp", watts, workout.RiderProfile{FTPWatts: 250})
	h.moveTest(time.Now().AddDate(0, 0, 3).Format("2006-01-02")) // out of the ride's way
	h.threshold(h.date, 4)
	other := h.threshold(time.Now().AddDate(0, 0, 2).Format("2006-01-02"), 4)
	h.sync()
	h.rate(rating)
	return h, other
}

// TestRelinkingARideKeepsItsSurvey: the link deletes the analysis so the ride
// is scored afresh, and the rider's own answers about how it felt are not
// something a re-score may throw away.
func TestRelinkingARideKeepsItsSurvey(t *testing.T) {
	h, other := relinkSetup(t, 200, `{"feel":3,"legs":"heavy","stress":"high"}`)

	if resp, _ := h.link(`{"workoutId":"` + other + `"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("link = %d", resp.StatusCode)
	}
	a := h.analysis()
	if a.WorkoutID != other {
		t.Fatalf("analysed against %q, want the new link", a.WorkoutID)
	}
	if a.Feel != 3 || a.Legs != "heavy" || a.Stress != "high" {
		t.Errorf("survey after re-link = %d/%q/%q, want it carried", a.Feel, a.Legs, a.Stress)
	}

	// Unlinking and handing back to the automatic match carry it too.
	if resp, _ := h.link(`{"workoutId":""}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("unlink = %d", resp.StatusCode)
	}
	if a := h.analysis(); a.Feel != 3 || a.Legs != "heavy" || a.Stress != "high" {
		t.Errorf("survey after unlink = %d/%q/%q", a.Feel, a.Legs, a.Stress)
	}
}

// TestAnAllOutRatingStillCountsAsAStruggleOnTheNewLink: effort 5 turns a nailed
// or completed ride into a struggle, which earns no level move; that has to
// hold for the session the ride is re-linked to, not just the first one.
func TestAnAllOutRatingStillCountsAsAStruggleOnTheNewLink(t *testing.T) {
	h, other := relinkSetup(t, 200, `{"feel":5}`)
	if resp, _ := h.link(`{"workoutId":"` + other + `"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("link = %d", resp.StatusCode)
	}
	a := h.analysis()
	if a.Outcome != "nailed" && a.Outcome != "completed" {
		t.Fatalf("outcome = %q: the ride was meant to score well, so the all-out rating is what makes it a struggle", a.Outcome)
	}
	if a.EffectiveOutcome() != "struggled" {
		t.Errorf("effective outcome = %q, want struggled", a.EffectiveOutcome())
	}
	if a.LevelDelta != 0 {
		t.Errorf("level delta = %v, want 0: an all-out ride earns no bump on the new link either", a.LevelDelta)
	}
}

// TestTheEffortLoadSurvivesARelink: a ride with no power or heart rate
// carries a session-RPE load, and a re-link must not put the flat guess back.
func TestTheEffortLoadSurvivesARelink(t *testing.T) {
	h, other := relinkSetup(t, 0, `{"feel":3}`)
	want := 1200.0 / 3600 * 0.78 * 0.78 * 100
	load := func() float64 {
		s, err := h.srv.Training.GetSession(context.Background(), linkedRide)
		if err != nil {
			t.Fatal(err)
		}
		return s.TrainingLoad
	}
	if got := load(); !sameLoad(got, want) {
		t.Fatalf("before the re-link the load is %v, want the effort load %v", got, want)
	}
	if resp, _ := h.link(`{"workoutId":"` + other + `"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("link = %d", resp.StatusCode)
	}
	if got := load(); !sameLoad(got, want) {
		t.Errorf("after the re-link the load is %v, want %v kept", got, want)
	}
	if a := h.analysis(); a.LoadSource != "session_rpe" {
		t.Errorf("load basis = %q, want session_rpe", a.LoadSource)
	}
}
