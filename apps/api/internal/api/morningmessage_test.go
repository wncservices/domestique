package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type morningMessageOut struct {
	Headline string   `json:"headline"`
	Lines    []string `json:"lines"`
}

func (h *weatherHarness) morningMessage(user, groups string) (int, morningMessageOut, string) {
	h.t.Helper()
	resp := h.as(user, groups, http.MethodGet, "/api/training/morning-message", "")
	raw := readAll(h.t, resp)
	var out morningMessageOut
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &out); err != nil {
			h.t.Fatalf("decoding %s: %v", raw, err)
		}
	}
	return resp.StatusCode, out, string(raw)
}

func seedTodayWorkout(t *testing.T, h *weatherHarness, rider string) workout.Workout {
	t.Helper()
	w, err := h.training.CreateWorkout(t.Context(), api.LegacyFlagged(workout.CreateWorkoutRequest{
		Rider: rider, Name: "Endurance ride", Date: "2026-03-25", Zone: workout.ZoneEndurance,
		Steps: []workout.WorkoutStep{{Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 7200, Target: workout.TargetOpen}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestMorningMessageShape(t *testing.T) {
	h := newWeatherHarness(t)
	seedTodayWorkout(t, h, "alice")
	code, out, raw := h.morningMessage("alice", "cyclists")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, raw)
	}
	if out.Headline != "Today: Endurance ride (2h00)" {
		t.Errorf("headline = %q", out.Headline)
	}
	if out.Lines == nil || strings.Contains(raw, `"lines":null`) {
		t.Errorf("lines must be an array: %s", raw)
	}
}

func TestMorningMessageOnARestDay(t *testing.T) {
	h := newWeatherHarness(t)
	code, out, _ := h.morningMessage("alice", "cyclists")
	if code != http.StatusOK || out.Headline != "Rest day" {
		t.Errorf("status %d headline %q", code, out.Headline)
	}
}

// The rider is the session's: each rider reads their own day and nothing else.
func TestMorningMessageIsTheCallersOwn(t *testing.T) {
	h := newWeatherHarness(t)
	seedTodayWorkout(t, h, "alice")
	_, bob, _ := h.morningMessage("bob", "cyclists")
	if bob.Headline != "Rest day" {
		t.Errorf("bob read %q", bob.Headline)
	}
	// A rider parameter is not a thing: it is ignored, not honoured.
	resp := h.as("bob", "cyclists", http.MethodGet, "/api/training/morning-message?rider=alice", "")
	var out morningMessageOut
	if err := json.Unmarshal(readAll(t, resp), &out); err != nil || out.Headline != "Rest day" {
		t.Errorf("a rider parameter changed the answer: %+v %v", out, err)
	}
}

func TestMorningMessageNeedsATrainingRole(t *testing.T) {
	h := newWeatherHarness(t)
	seedTodayWorkout(t, h, "alice")
	if code, _, _ := h.morningMessage("guest", "guests"); code != http.StatusForbidden {
		t.Errorf("a viewer got %d, want 403", code)
	}
	req, _ := http.NewRequest(http.MethodGet, h.base+"/api/training/morning-message", nil)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("nobody signed in got %d, want 401", resp.StatusCode)
	}
}

// Weather only when bad, from the same rule the Plan page uses, and only for a
// rider who opted in to weather.
func TestMorningMessageCarriesBadWeatherForARiderWhoOptedIn(t *testing.T) {
	h := newWeatherHarness(t)
	seedTodayWorkout(t, h, "alice")
	if r := h.setLocation("alice", ghentBody); r.StatusCode != 200 {
		t.Fatalf("setting a town = %d", r.StatusCode)
	}
	_, out, raw := h.morningMessage("alice", "cyclists")
	if !strings.Contains(strings.Join(out.Lines, "\n"), "Rain likely") {
		t.Errorf("bad weather is not in the message: %s", raw)
	}
	for _, banned := range []string{"Ghent", "51.05", "3.71"} {
		if strings.Contains(raw, banned) {
			t.Errorf("%q (a place or coordinate) is in the message", banned)
		}
	}
}

func TestMorningMessageHasNoWeatherForARiderWithoutATown(t *testing.T) {
	h := newWeatherHarness(t)
	seedTodayWorkout(t, h, "alice")
	before := h.requests.Load()
	_, out, _ := h.morningMessage("alice", "cyclists")
	if strings.Contains(strings.Join(out.Lines, "\n"), "Rain") {
		t.Errorf("lines = %q", out.Lines)
	}
	if h.requests.Load() != before {
		t.Error("a forecast was fetched for a rider who never opted in to weather")
	}
}

func TestMorningMessageSurvivesAFailingForecast(t *testing.T) {
	h := newWeatherHarness(t)
	seedTodayWorkout(t, h, "alice")
	h.setLocation("alice", ghentBody)
	h.setForecast(500, "boom")
	code, out, raw := h.morningMessage("alice", "cyclists")
	if code != http.StatusOK || !strings.HasPrefix(out.Headline, "Today:") || strings.Contains(raw, "Rain") {
		t.Fatalf("a failed forecast must leave the line out, not the message: %d %s", code, raw)
	}
}

// A day inside a life event is "A planned break", and the kind of event, here
// an illness, never reaches the message.
func TestMorningMessageOnALifeEventDaySaysBreakWithoutTheKind(t *testing.T) {
	h := newWeatherHarness(t)
	if _, err := h.training.CreateLifeEvent(t.Context(), workout.LifeEvent{
		Rider: "alice", Kind: "illness", Option: "proper", Start: "2026-03-24", End: "2026-03-26", Note: "flu-private-note",
	}); err != nil {
		t.Fatal(err)
	}
	_, out, raw := h.morningMessage("alice", "cyclists")
	if out.Headline != "A planned break" {
		t.Errorf("headline = %q", out.Headline)
	}
	for _, banned := range []string{"illness", "flu", "Rest day"} {
		if strings.Contains(strings.ToLower(raw), strings.ToLower(banned)) {
			t.Errorf("%q reached the message", banned)
		}
	}
}
