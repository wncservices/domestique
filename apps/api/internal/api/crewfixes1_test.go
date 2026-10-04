package api_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const crewMoveRefused = "A crew ride's day is the crew's. Leave the ride to change your plan."

func (h *crewPlanHarness) patch(rider, id, body string) (*http.Response, string) {
	h.t.Helper()
	resp := h.as(rider, "cyclists", http.MethodPatch, "/api/training/workouts/"+id, body)
	return resp, bodyOf(h.t, resp)
}

// A crew ride's day is the crew's: moving the session would desync it from the
// ride. Editing its name or steps stays allowed.
func TestACrewRideCannotBeMovedToAnotherDayButCanBeRenamed(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	h.mustGo("wilant", s.ride)
	row, _ := h.fixedRow("wilant", s.ride)

	resp, body := h.patch("wilant", row.ID, `{"date":"`+cpFriday+`"}`)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, crewMoveRefused) {
		t.Fatalf("move: status %d body %s, want 409 with the crew ride message", resp.StatusCode, body)
	}
	after, _ := h.fixedRow("wilant", s.ride)
	if after.Date != cpSaturday || after.UpdatedAt != row.UpdatedAt {
		t.Error("a refused move still wrote the session")
	}

	if resp, _ := h.patch("wilant", row.ID, `{"name":"Sunday spin"}`); resp.StatusCode != http.StatusOK {
		t.Errorf("rename: status = %d, want 200", resp.StatusCode)
	}
	if resp, _ := h.patch("wilant", row.ID, `{"date":"`+cpSaturday+`"}`); resp.StatusCode != http.StatusOK {
		t.Errorf("a date that does not change: status = %d, want 200", resp.StatusCode)
	}
}

// Linking a ride to a crew ride session on another day would move it, the same
// thing by another door.
func TestLinkingARideCannotMoveACrewRide(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	h.mustGo("wilant", s.ride)
	row, _ := h.fixedRow("wilant", s.ride)
	sess, err := h.training.UpsertSession(context.Background(), workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "x1", Sport: "cycling", Date: cpToday, DurationSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/sessions/"+sess.ID+"/workout", `{"workoutId":"`+row.ID+`"}`)
	body := bodyOf(t, resp)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, crewMoveRefused) {
		t.Fatalf("status %d body %s, want 409 with the crew ride message", resp.StatusCode, body)
	}
	after, _ := h.fixedRow("wilant", s.ride)
	if after.Date != cpSaturday {
		t.Errorf("the crew ride moved to %s", after.Date)
	}
}

// A rider no longer in the crew keeps their own session and the orphaned note,
// but sees nobody else's name and not the crew's.
func TestARemovedMemberSeesNoGoingRosterAndNoCrewName(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	h.mustGo("sam", s.ride)
	h.mustGo("alex", s.ride)
	if resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/crews/"+s.crewID+"/members/sam", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("remove: status = %d", resp.StatusCode)
	}

	var found bool
	for _, w := range h.workoutsOf("sam") {
		if w.CrewRide == nil {
			continue
		}
		found = true
		if w.CrewRide.Orphaned != "left" {
			t.Errorf("orphaned = %q, want left", w.CrewRide.Orphaned)
		}
		if len(w.CrewRide.GoingNames) != 0 || w.CrewRide.CrewName != "" || w.CrewRide.CrewID != "" {
			t.Errorf("a removed member sees crew %q/%q and going %v", w.CrewRide.CrewName, w.CrewRide.CrewID, w.CrewRide.GoingNames)
		}
	}
	if !found {
		t.Fatal("sam lost the session")
	}
	// An approved member still sees both.
	for _, w := range h.workoutsOf("alex") {
		if w.CrewRide != nil && (len(w.CrewRide.GoingNames) != 1 || w.CrewRide.CrewName == "") {
			t.Errorf("alex sees %+v", w.CrewRide)
		}
	}
}

// A failed cleanup after removing a member is a write that should have landed.
func TestAFailedMemberCleanupIsLoggedAtError(t *testing.T) {
	h := newCrewPlanHarness(t)
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := h.setup()
	if _, err := h.db.Conn().Exec(`DROP TABLE crew_ride_going`); err != nil {
		t.Fatal(err)
	}
	if resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/crews/"+s.crewID+"/members/sam", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("remove: status = %d", resp.StatusCode)
	}
	var line string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "could not clear a removed member") {
			line = l
		}
	}
	if !strings.Contains(line, "level=ERROR") {
		t.Errorf("log line = %q, want level ERROR", line)
	}
}
