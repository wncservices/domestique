package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func (h *crewPlanHarness) answer(rider, id, verb string) (*http.Response, string) {
	h.t.Helper()
	resp := h.as(rider, "cyclists", http.MethodPost, "/api/training/ride-together/"+id+"/"+verb, "")
	return resp, bodyOf(h.t, resp)
}

// agreedSetup is two riders opted in with a stored proposal for Saturday.
func (h *crewPlanHarness) agreedSetup() (togetherSetup, proposalOut) {
	h.t.Helper()
	ts := h.togetherSetup()
	h.optIn("wilant", ts.crewID, "sat", "sun")
	h.optIn("sam", ts.crewID, "sat", "sun")
	got := h.proposals("wilant")
	if len(got) != 1 || got[0].Day != cpSaturday {
		h.t.Fatalf("setup: proposals = %+v, want one for Saturday", got)
	}
	return ts, got[0]
}

func TestAcceptingMovesOnlyTheCallersOwnSessionAndNeedsNobodyElse(t *testing.T) {
	h := newCrewPlanHarness(t)
	ts, p := h.agreedSetup()
	samBefore := h.stored("sam")

	resp, body := h.answer("wilant", p.ID, "accept")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept: status = %d: %s", resp.StatusCode, body)
	}

	moved, _ := h.get("wilant", ts.wilant.ID)
	if moved.Date != cpSaturday {
		t.Errorf("wilant's session is on %s, want the shared Saturday", moved.Date)
	}
	from, ok := scheduler.MovedFrom(moved.Description)
	if !ok || from != cpSunday {
		t.Errorf("MovedFrom = %q, %v: scheduling must keep the vacated Sunday taken (%q)", from, ok, moved.Description)
	}
	if !strings.Contains(moved.Description, scheduler.AdjustedMarker) || !strings.Contains(moved.Description, "Ride together: Medium Loop with your crew") {
		t.Errorf("description = %q, want the marker and the ride together note", moved.Description)
	}
	if scheduler.IsGenerated(moved) {
		t.Error("a moved session must not be generated again: it is the rider's now")
	}

	// sam was not touched.
	samAfter := h.stored("sam")
	for i := range samBefore {
		if samBefore[i].UpdatedAt != samAfter[i].UpdatedAt || samBefore[i].Date != samAfter[i].Date {
			t.Errorf("sam's workout %s was written by wilant's accept", samBefore[i].ID)
		}
	}

	// The proposal is still open and still visible to both; only wilant answered.
	for _, rider := range []string{"wilant", "sam"} {
		got := h.proposals(rider)
		if len(got) != 1 || got[0].Status != "open" {
			t.Fatalf("%s sees %+v, want the proposal still open", rider, got)
		}
	}
	if got := h.proposals("sam")[0]; got.YourStatus != "pending" {
		t.Errorf("sam's own status = %q, want pending", got.YourStatus)
	}
	statuses := map[string]string{}
	for _, m := range h.proposals("sam")[0].Members {
		statuses[m.Rider] = m.Status
	}
	if statuses["wilant"] != "accepted" || statuses["sam"] != "pending" {
		t.Errorf("members = %v, want wilant accepted and sam pending", statuses)
	}
}

func TestTheLastAcceptanceMakesItAgreed(t *testing.T) {
	h := newCrewPlanHarness(t)
	ts, p := h.agreedSetup()
	h.answer("wilant", p.ID, "accept")
	resp, body := h.answer("sam", p.ID, "accept")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	got := h.proposals("wilant")
	if len(got) != 1 || got[0].Status != "agreed" {
		t.Errorf("proposals = %+v, want agreed", got)
	}
	sam, _ := h.get("sam", ts.sam.ID)
	if sam.Date != cpSaturday {
		t.Errorf("sam's session is on %s", sam.Date)
	}
	// An agreed week is not proposed again.
	if again := h.proposals("sam"); len(again) != 1 || again[0].ID != p.ID {
		t.Errorf("proposals = %+v", again)
	}
}

func TestAcceptingTwiceMovesNothingTwice(t *testing.T) {
	h := newCrewPlanHarness(t)
	ts, p := h.agreedSetup()
	h.answer("wilant", p.ID, "accept")
	first, _ := h.get("wilant", ts.wilant.ID)
	resp, _ := h.answer("wilant", p.ID, "accept")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second accept: status = %d", resp.StatusCode)
	}
	second, _ := h.get("wilant", ts.wilant.ID)
	if first.UpdatedAt != second.UpdatedAt || first.Description != second.Description {
		t.Error("a second accept rewrote the session")
	}
}

func TestADeclineEndsItForTheWeekAndMovesNothing(t *testing.T) {
	h := newCrewPlanHarness(t)
	ts, p := h.agreedSetup()
	wBefore, sBefore := h.stored("wilant"), h.stored("sam")

	resp, body := h.answer("sam", p.ID, "decline")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decline: status = %d: %s", resp.StatusCode, body)
	}
	if got := h.proposals("wilant"); len(got) != 0 {
		t.Errorf("wilant still sees %+v", got)
	}
	if got := h.proposals("sam"); len(got) != 0 {
		t.Errorf("sam still sees %+v", got)
	}
	// Declining writes no workout, anybody's.
	for who, before := range map[string][]workout.Workout{"wilant": wBefore, "sam": sBefore} {
		after := h.stored(who)
		for i := range before {
			if before[i].UpdatedAt != after[i].UpdatedAt || before[i].Date != after[i].Date {
				t.Errorf("%s's workout %s changed on a decline", who, before[i].ID)
			}
		}
	}
	// The week is not proposed again behind their back.
	h.optIn("wilant", ts.crewID, "sat", "sun")
	if got := h.proposals("wilant"); len(got) != 0 {
		t.Errorf("a declined week was proposed again: %+v", got)
	}
	// And answering an ended proposal is refused.
	if resp, _ := h.answer("wilant", p.ID, "accept"); resp.StatusCode != http.StatusConflict {
		t.Errorf("accepting an ended proposal: status = %d, want 409", resp.StatusCode)
	}
}

func TestADeclineAfterAcceptingIsRefused(t *testing.T) {
	h := newCrewPlanHarness(t)
	_, p := h.agreedSetup()
	h.answer("wilant", p.ID, "accept")
	if resp, _ := h.answer("wilant", p.ID, "decline"); resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409: their session has already moved", resp.StatusCode)
	}
}

func TestAStalePremiseIsA409WithTheFreshProposalAndMovesNothing(t *testing.T) {
	h := newCrewPlanHarness(t)
	ts, p := h.agreedSetup()
	// sam's long ride goes away after the proposal was made.
	if err := h.training.DeleteWorkout(context.Background(), ts.sam.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := h.get("wilant", ts.wilant.ID)

	resp, body := h.answer("wilant", p.ID, "accept")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, body)
	}
	var out struct {
		Error     string        `json:"error"`
		Proposals []proposalOut `json:"proposals"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Error == "" || out.Proposals == nil || len(out.Proposals) != 0 {
		t.Errorf("body = %s, want an error and the fresh (now empty) proposals", body)
	}
	after, _ := h.get("wilant", ts.wilant.ID)
	if after.Date != before.Date || after.UpdatedAt != before.UpdatedAt {
		t.Error("a stale accept still moved the session")
	}
}

func TestNobodyAnswersForAnotherRiderOrOutsideTheProposal(t *testing.T) {
	h := newCrewPlanHarness(t)
	_, p := h.agreedSetup()
	if resp, _ := h.answer("alex", p.ID, "accept"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a crew member not in it: status = %d, want 404", resp.StatusCode)
	}
	if resp, _ := h.answer("outsider", p.ID, "decline"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an outsider: status = %d, want 404", resp.StatusCode)
	}
	if resp, _ := h.answer("wilant", "no-such-proposal", "accept"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown proposal: status = %d, want 404", resp.StatusCode)
	}
	if resp := h.as("wilant", "guests", http.MethodPost, "/api/training/ride-together/"+p.ID+"/accept", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer: status = %d, want 403", resp.StatusCode)
	}
	// A body trying to name the rider is not read at all.
	resp := h.as("sam", "cyclists", http.MethodPost, "/api/training/ride-together/"+p.ID+"/accept", `{"rider":"wilant"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	for _, m := range h.proposals("wilant")[0].Members {
		if m.Rider == "wilant" && m.Status != "pending" {
			t.Error("sam's request answered for wilant")
		}
	}
}

// Every write endpoint of the feature, called as a second rider, changes none
// of the first rider's workouts.
func TestNoRideTogetherEndpointWritesAnotherRidersWorkouts(t *testing.T) {
	h := newCrewPlanHarness(t)
	ts, p := h.agreedSetup()
	before := h.stored("wilant")

	h.optIn("sam", ts.crewID, "sat")
	h.optIn("sam", ts.crewID, "sat", "sun")
	h.proposals("sam")
	h.answer("sam", p.ID, "accept")
	h.mustGo("sam", ts.ride)
	h.setGoing("sam", ts.ride, `{"going":false}`)
	h.setGoing("sam", ts.ride, `{"going":true,"dryRun":true}`)
	h.answer("sam", p.ID, "decline")

	after := h.stored("wilant")
	if len(after) != len(before) {
		t.Fatalf("wilant has %d workouts, had %d", len(after), len(before))
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].UpdatedAt != after[i].UpdatedAt ||
			before[i].Date != after[i].Date || before[i].Description != after[i].Description {
			t.Errorf("wilant's workout %s was written by sam's requests", before[i].ID)
		}
	}
}

func TestAnsweringLogsNoHealthValuesNextToARider(t *testing.T) {
	h := newCrewPlanHarness(t)
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ts := h.togetherSetup()
	if _, err := h.training.SaveProfile(context.Background(), workout.RiderProfile{
		Rider: "wilant", FTPWatts: 2718, MaxHR: 1893, HoursPerAvailableDay: 2, AvailableDays: []string{"sat", "sun"},
	}); err != nil {
		t.Fatal(err)
	}
	h.optIn("wilant", ts.crewID, "sat", "sun")
	h.optIn("sam", ts.crewID, "sat", "sun")
	p := h.proposals("wilant")[0]
	h.answer("wilant", p.ID, "accept")
	h.answer("sam", p.ID, "decline")
	for _, secret := range []string{"2718", "1893"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log holds %s:\n%s", secret, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "ride together") {
		t.Error("answering a proposal logs nothing")
	}
}
