package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type goalCheckBody struct {
	Key     string `json:"key"`
	Message string `json:"message"`
	Tone    string `json:"tone"`
}

func (h *seasonHarness) goalCheck(t *testing.T, user, groups, body string) (int, goalCheckBody, string) {
	t.Helper()
	resp := h.as(user, groups, http.MethodPost, "/api/training/goals/check", body)
	var raw strings.Builder
	var out goalCheckBody
	dec := json.NewDecoder(resp.Body)
	var m json.RawMessage
	if err := dec.Decode(&m); err == nil {
		raw.Write(m)
		_ = json.Unmarshal(m, &out)
	}
	return resp.StatusCode, out, raw.String()
}

func TestGoalCheckGivesAVerdictForARiderWithFitness(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 250)
	h.ride(t, h.now, 40, 1, 60)

	code, got, raw := h.goalCheck(t, "wilant", "cyclists", `{"eventDate":"2027-04-14","sport":"cycling","targetDistanceM":120000,"targetElevationM":1500,"priority":"A"}`)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	switch got.Key {
	case "comfortable", "achievable", "stretch", "unrealistic":
	default:
		t.Fatalf("key = %q, want one of the four verdicts", got.Key)
	}
	if got.Message == "" || got.Tone == "" {
		t.Errorf("verdict lacks message or tone: %+v", got)
	}
	if strings.Contains(raw, "wilant") {
		t.Errorf("response carries the rider name: %s", raw)
	}
	// It saves nothing.
	if goals, err := h.store.ListGoals(t.Context(), "wilant"); err != nil || len(goals) != 0 {
		t.Errorf("goals after a check = %d, %v; want none", len(goals), err)
	}
}

func TestGoalCheckAUnrealisticEventIsSaidSo(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 250)
	h.ride(t, h.now, 40, 1, 30)

	// A week and a half out, for a huge day.
	_, got, _ := h.goalCheck(t, "wilant", "cyclists", `{"eventDate":"2026-10-18","sport":"cycling","targetDistanceM":300000,"targetElevationM":5000}`)
	if got.Key != "unrealistic" && got.Key != "stretch" {
		t.Errorf("key = %q, want a warning for an event days away needing far more fitness", got.Key)
	}
}

func TestGoalCheckWithoutFTPNeedsAProfile(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	h.ride(t, h.now, 40, 1, 60)

	code, got, _ := h.goalCheck(t, "wilant", "cyclists", `{"eventDate":"2027-04-14","sport":"cycling"}`)
	if code != http.StatusOK || got.Key != "needs_profile" || got.Tone != "neutral" || got.Message == "" {
		t.Errorf("got %d %+v, want needs_profile", code, got)
	}
}

func TestGoalCheckSaysNothingWhereItCannotJudge(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 250)
	h.ride(t, h.now, 40, 1, 60)

	for name, body := range map[string]string{
		"running":   `{"eventDate":"2027-04-14","sport":"running"}`,
		"past date": `{"eventDate":"2026-01-01","sport":"cycling"}`,
		"no date":   `{"sport":"cycling"}`,
		"bad date":  `{"eventDate":"soon","sport":"cycling"}`,
	} {
		code, got, _ := h.goalCheck(t, "wilant", "cyclists", body)
		if code != http.StatusOK || got.Key != "none" {
			t.Errorf("%s: got %d %+v, want 200 none", name, code, got)
		}
	}
}

func TestGoalCheckWithoutFitnessHistoryIsNone(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 250)

	code, got, _ := h.goalCheck(t, "wilant", "cyclists", `{"eventDate":"2027-04-14","sport":"cycling"}`)
	if code != http.StatusOK || got.Key != "none" {
		t.Errorf("got %d %+v, want none on the hours basis", code, got)
	}
}

func TestGoalCheckRefusesViewersAndUnknownFields(t *testing.T) {
	h := newSeasonHarness(t)
	if code, _, _ := h.goalCheck(t, "watcher", "viewers", `{"eventDate":"2027-04-14","sport":"cycling"}`); code != http.StatusForbidden {
		t.Errorf("viewer: status %d, want 403", code)
	}
	if code, _, _ := h.goalCheck(t, "wilant", "cyclists", `{"eventDate":"2027-04-14","rider":"someone"}`); code != http.StatusBadRequest {
		t.Errorf("unknown field: status %d, want 400", code)
	}
}
