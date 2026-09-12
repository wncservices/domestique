package narration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// fakeAnthropic returns a canned Messages API response and records the
// request body it was sent, so a test can assert both what was asked and
// what came back.
func fakeAnthropic(t *testing.T, text string) (*httptest.Server, *string) {
	t.Helper()
	var lastRequest string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q, want test-key", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Error("anthropic-version header missing")
		}
		var req messageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if len(req.Messages) != 1 {
			t.Fatalf("messages = %d, want 1", len(req.Messages))
		}
		lastRequest = req.Messages[0].Content

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(messageResponse{
			Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{{Type: "text", Text: text}},
		})
	}))
	t.Cleanup(server.Close)
	return server, &lastRequest
}

func TestExplainPlanSendsTheGoalAndPlanShapeAndReturnsTheModelsText(t *testing.T) {
	server, lastRequest := fakeAnthropic(t, "This is your base phase, building steadily.")
	client := New("test-key")
	client.APIBase = server.URL

	goal := workout.Goal{Name: "Local Century", EventDate: "2026-11-11"}
	plan := periodization.Plan{
		Adjustment: 0.7,
		Weeks: []periodization.Week{
			{Number: 1, StartDate: "2026-09-07", Phase: periodization.PhaseBase, TargetHours: 3},
			{Number: 2, StartDate: "2026-09-14", Phase: periodization.PhaseBase, TargetHours: 2.1, Adjusted: true},
			{Number: 3, StartDate: "2026-09-21", Phase: periodization.PhaseBase, Recovery: true, TargetHours: 1.2, Adjusted: true},
		},
	}

	text, err := client.ExplainPlan(context.Background(), goal, plan)
	if err != nil {
		t.Fatalf("ExplainPlan: %v", err)
	}
	if text != "This is your base phase, building steadily." {
		t.Errorf("text = %q", text)
	}

	prompt := *lastRequest
	for _, want := range []string{"Local Century", "2026-11-11", "base", "recovery", "70%"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not mention %q:\n%s", want, prompt)
		}
	}
}

func TestExplainPlanHandlesAnEmptyPlanWithoutCallingItAnError(t *testing.T) {
	server, _ := fakeAnthropic(t, "Set an event date to get started.")
	client := New("test-key")
	client.APIBase = server.URL

	text, err := client.ExplainPlan(context.Background(), workout.Goal{Name: "Someday Race"}, periodization.Plan{})
	if err != nil {
		t.Fatalf("ExplainPlan: %v", err)
	}
	if text == "" {
		t.Error("expected non-empty text")
	}
}

func TestProposeProfileChangeParsesAWellFormedProposal(t *testing.T) {
	server, lastRequest := fakeAnthropic(t, `{"availableDays": ["sat", "sun"], "hoursPerAvailableDay": 3, "explanation": "Only free on weekends while traveling."}`)
	client := New("test-key")
	client.APIBase = server.URL

	profile := workout.RiderProfile{AvailableDays: []string{"mon", "wed", "fri"}, HoursPerAvailableDay: 1.5}
	proposal, err := client.ProposeProfileChange(context.Background(), "I'm traveling for work next week, only free on the weekend", profile)
	if err != nil {
		t.Fatalf("ProposeProfileChange: %v", err)
	}
	if len(proposal.AvailableDays) != 2 || proposal.AvailableDays[0] != "sat" || proposal.AvailableDays[1] != "sun" {
		t.Errorf("AvailableDays = %v", proposal.AvailableDays)
	}
	if proposal.HoursPerAvailableDay != 3 {
		t.Errorf("HoursPerAvailableDay = %v", proposal.HoursPerAvailableDay)
	}
	if proposal.Explanation == "" {
		t.Error("expected a non-empty explanation")
	}
	if !strings.Contains(*lastRequest, "mon, wed, fri") {
		t.Errorf("prompt did not include the current profile:\n%s", *lastRequest)
	}
}

// The prompt asks for bare JSON, but the model may still wrap it in a
// markdown fence — this must not be treated as a parse failure.
func TestProposeProfileChangeStripsMarkdownFences(t *testing.T) {
	server, _ := fakeAnthropic(t, "```json\n{\"availableDays\": [\"tue\"], \"hoursPerAvailableDay\": 2, \"explanation\": \"ok\"}\n```")
	client := New("test-key")
	client.APIBase = server.URL

	proposal, err := client.ProposeProfileChange(context.Background(), "note", workout.RiderProfile{})
	if err != nil {
		t.Fatalf("ProposeProfileChange: %v", err)
	}
	if len(proposal.AvailableDays) != 1 || proposal.AvailableDays[0] != "tue" {
		t.Errorf("AvailableDays = %v", proposal.AvailableDays)
	}
}

func TestProposeProfileChangeFallsBackToCurrentProfileOnInvalidValues(t *testing.T) {
	// "friday-ish" is not a valid day and 40 hours/day fails the sanity
	// bound — both must fall back to the rider's own current values rather
	// than being handed back as a real suggestion.
	server, _ := fakeAnthropic(t, `{"availableDays": ["friday-ish"], "hoursPerAvailableDay": 40, "explanation": "no real change"}`)
	client := New("test-key")
	client.APIBase = server.URL

	profile := workout.RiderProfile{AvailableDays: []string{"mon", "thu"}, HoursPerAvailableDay: 2}
	proposal, err := client.ProposeProfileChange(context.Background(), "note", profile)
	if err != nil {
		t.Fatalf("ProposeProfileChange: %v", err)
	}
	if len(proposal.AvailableDays) != 2 || proposal.AvailableDays[0] != "mon" || proposal.AvailableDays[1] != "thu" {
		t.Errorf("AvailableDays = %v, want fallback to profile's own %v", proposal.AvailableDays, profile.AvailableDays)
	}
	if proposal.HoursPerAvailableDay != 2 {
		t.Errorf("HoursPerAvailableDay = %v, want fallback to profile's own 2", proposal.HoursPerAvailableDay)
	}
}

func TestProposeProfileChangeRejectsNonJSONText(t *testing.T) {
	server, _ := fakeAnthropic(t, "I think you should take it easy next week.")
	client := New("test-key")
	client.APIBase = server.URL

	if _, err := client.ProposeProfileChange(context.Background(), "note", workout.RiderProfile{}); err == nil {
		t.Fatal("expected an error for a non-JSON reply")
	}
}

func TestCompleteReturnsAnErrorForANonOKResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": {"message": "invalid x-api-key"}}`))
	}))
	t.Cleanup(server.Close)
	client := New("bad-key")
	client.APIBase = server.URL

	_, err := client.ExplainPlan(context.Background(), workout.Goal{}, periodization.Plan{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v, want it to mention the 401 status", err)
	}
}
