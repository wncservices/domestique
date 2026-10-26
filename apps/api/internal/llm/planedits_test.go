package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/config"
)

func testView() PlanView {
	return PlanView{
		Today:         "2026-10-07",
		AvailableDays: []string{"tue", "thu", "sat", "sun"},
		Days: []ViewDay{
			{Date: "2026-10-07", Sessions: nil},
			{Date: "2026-10-08", Sessions: []ViewSession{
				{Handle: "w1", Name: "Threshold 3×12", Sport: "cycling", Zone: "threshold", Minutes: 75},
			}},
			{Date: "2026-10-10", Sessions: []ViewSession{
				{Handle: "w2", Name: "Long ride", Sport: "cycling", Zone: "endurance", Minutes: 180, Indoor: true},
				{Handle: "w3", RiderBuilt: true, Sport: "cycling", Minutes: 45},
			}},
		},
		Capabilities: Capabilities{SwapAlternate: true, ConvertIndoor: true},
	}
}

// planClient is a client for a fake endpoint that answers every call with
// content. The fake is returned so a test can read what was sent.
func planClient(t *testing.T, content string) (*Client, *fake) {
	t.Helper()
	f := newFake(t, answer(t, content))
	return newClient(t, f.srv.URL+"/v1", ""), f
}

func propose(t *testing.T, c *Client, text string) (PlanEdits, error) {
	t.Helper()
	return c.ProposePlanEdits(t.Context(), text, testView())
}

func TestTheRequestIsOneStrictJSONSchemaWithNoTools(t *testing.T) {
	c, f := planClient(t, `{"refused":false,"intents":[],"unsupported":null}`)
	if _, err := propose(t, c, "I'm travelling Thursday to Sunday"); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	body := f.body
	f.mu.Unlock()
	if body["max_tokens"] != float64(700) {
		t.Errorf("max_tokens = %v, want 700", body["max_tokens"])
	}
	if _, ok := body["tools"]; ok {
		t.Error("the request uses tools; the reply is a schema-constrained object now")
	}
	if _, ok := body["tool_choice"]; ok {
		t.Error("the request carries a tool_choice")
	}
	rf, _ := body["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	if js["name"] != string(JobPlanEdits) || js["strict"] != true {
		t.Errorf("json_schema = %v, want plan_edits with strict true", js)
	}
	schema, _ := js["schema"].(map[string]any)
	if schema["additionalProperties"] != false {
		t.Error("the top level of the schema allows extra properties")
	}
	items := schema["properties"].(map[string]any)["intents"].(map[string]any)["items"].(map[string]any)
	if items["additionalProperties"] != false {
		t.Error("an intent's schema allows extra properties")
	}
}

func TestTheUserMessageCarriesTheNoteTheDatesAndTheOpaquePlan(t *testing.T) {
	c, f := planClient(t, `{"refused":false,"intents":[],"unsupported":null}`)
	if _, err := propose(t, c, "I'm travelling Thursday to Sunday"); err != nil {
		t.Fatal(err)
	}
	_, user := sentMessages(t, f)
	for _, want := range []string{
		"I'm travelling Thursday to Sunday",
		"2026-10-07", "Wednesday", // today, with its weekday: the model has no clock
		"2026-12-06", // the last day it may pick: today + 60
		"tue", "sun", // available days
		"Thursday", // a plan day with its weekday
		"w1", "w2", "w3",
		"Threshold 3×12", "Long ride",
		"threshold", "75",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("the user message does not contain %q:\n%s", want, user)
		}
	}
	// A session the rider built is described, never named.
	if !strings.Contains(user, "rider_built") {
		t.Error("the user message does not say which session the rider built")
	}
}

func TestTheSystemMessageHasNoRiderNote(t *testing.T) {
	c, f := planClient(t, `{"refused":false,"intents":[],"unsupported":null}`)
	const note = "UNIQUE-NOTE-9921 travelling"
	if _, err := propose(t, c, note); err != nil {
		t.Fatal(err)
	}
	system, user := sentMessages(t, f)
	if strings.Contains(system, "UNIQUE-NOTE-9921") {
		t.Errorf("system message carries the rider's note:\n%s", system)
	}
	if !strings.Contains(user, "UNIQUE-NOTE-9921") {
		t.Errorf("user message does not carry the rider's note:\n%s", user)
	}
}

func intentTypesOf(caps Capabilities) []string {
	schema := planEditSchema(caps)
	return schema["properties"].(map[string]any)["intents"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["type"].(map[string]any)["enum"].([]string)
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestPlanEditSchemaFollowsCapabilities(t *testing.T) {
	full := intentTypesOf(Capabilities{SwapAlternate: true, ConvertIndoor: true})
	for _, want := range []string{IntentCreateLifeEvent, IntentMoveWorkout, IntentSwapAlternate, IntentConvertIndoor} {
		if !has(full, want) {
			t.Errorf("a full deployment's enum %v lacks %s", full, want)
		}
	}

	bare := intentTypesOf(Capabilities{})
	if has(bare, IntentSwapAlternate) || has(bare, IntentConvertIndoor) || !has(bare, IntentMoveWorkout) {
		t.Errorf("a bare deployment's enum = %v", bare)
	}

	// With indoor off the schema lacks the indoor type, while the rest stay.
	noIndoor := intentTypesOf(Capabilities{SwapAlternate: true})
	if has(noIndoor, IntentConvertIndoor) || !has(noIndoor, IntentSwapAlternate) {
		t.Errorf("indoor off: enum = %v", noIndoor)
	}
}

func TestAnIntentTheDeploymentCannotDoIsInvalid(t *testing.T) {
	c, f := planClient(t, `{"refused":false,"intents":[{"type":"swap_alternate","kind":null,"start_date":null,"end_date":null,"option":null,"workout":"w1","to_date":null,"alternate":"easier"}],"unsupported":null}`)
	view := testView()
	view.Capabilities = Capabilities{}
	if _, err := c.ProposePlanEdits(t.Context(), "hi", view); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an intent the deployment cannot do: %v, want ErrInvalid", err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2 (one retry)", n)
	}
}

func TestAWellFormedReplyBecomesIntents(t *testing.T) {
	c, _ := planClient(t, `{"refused":false,"intents":[
		{"type":"create_life_event","kind":"travel","start_date":"2026-10-08","end_date":"2026-10-11","option":"no_bike","workout":null,"to_date":null,"alternate":null},
		{"type":"move_workout","kind":null,"start_date":null,"end_date":null,"option":null,"workout":"w1","to_date":"2026-10-10","alternate":null}
	],"unsupported":"I can't book flights"}`)
	got, err := propose(t, c, "travelling Thursday to Sunday, and move Thursday's session to Saturday")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Intents) != 2 {
		t.Fatalf("intents = %+v", got.Intents)
	}
	a, b := got.Intents[0], got.Intents[1]
	if a.Type != "create_life_event" || a.Kind != "travel" || a.StartDate != "2026-10-08" || a.EndDate != "2026-10-11" || a.Option != "no_bike" {
		t.Errorf("first intent = %+v", a)
	}
	if b.Type != "move_workout" || b.Workout != "w1" || b.ToDate != "2026-10-10" {
		t.Errorf("second intent = %+v", b)
	}
	if got.Unsupported != "I can't book flights" {
		t.Errorf("unsupported = %q", got.Unsupported)
	}
}

func TestTheWholeReplyIsInvalidWhenItIsNotTheSchema(t *testing.T) {
	const ok = `{"type":"create_life_event","kind":"busy","start_date":"2026-10-08","end_date":"2026-10-09","option":null,"workout":null,"to_date":null,"alternate":null}`
	cases := map[string]string{
		"unknown type":           `{"refused":false,"intents":[{"type":"delete_everything","kind":null,"start_date":null,"end_date":null,"option":null,"workout":null,"to_date":null,"alternate":null}],"unsupported":null}`,
		"an extra property":      `{"refused":false,"intents":[{"type":"create_life_event","kind":"busy","start_date":"2026-10-08","end_date":"2026-10-09","option":null,"workout":null,"to_date":null,"alternate":null,"force":true}],"unsupported":null}`,
		"an extra top-level key": `{"refused":false,"intents":[],"unsupported":null,"apply":true}`,
		"more than five intents": `{"refused":false,"intents":[` + strings.Repeat(ok+",", 5) + ok + `],"unsupported":null}`,
		"a wrong type":           `{"refused":false,"intents":[{"type":"create_life_event","kind":7,"start_date":"2026-10-08","end_date":"2026-10-09","option":null,"workout":null,"to_date":null,"alternate":null}],"unsupported":null}`,
		"intents is not a list":  `{"refused":false,"intents":"travel","unsupported":null}`,
		"not json at all":        `nope`,
		"prose":                  `Sure, I moved it!`,
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := planClient(t, reply)
			if _, err := propose(t, c, "x"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}

	// Five is the limit, and allowed.
	c, _ := planClient(t, `{"refused":false,"intents":[`+strings.Repeat(ok+",", 4)+ok+`],"unsupported":null}`)
	if got, err := propose(t, c, "x"); err != nil || len(got.Intents) != 5 {
		t.Fatalf("five intents: %d, %v", len(got.Intents), err)
	}
}

// A refusal is a complete, valid answer. It costs one request, not two.
func TestRefusedIsErrRejected(t *testing.T) {
	c, f := planClient(t, `{"refused":true}`)
	if _, err := propose(t, c, "book me a flight"); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
	if n := f.requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1: a refusal is not retried", n)
	}
}

func TestAModelErrorAndATimeoutAreErrors(t *testing.T) {
	srv := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	})
	c := newClient(t, srv.srv.URL+"/v1", "")
	if _, err := propose(t, c, "x"); err == nil || errors.Is(err, ErrRejected) || errors.Is(err, ErrInvalid) {
		t.Errorf("a 500: %v, want a plain error", err)
	}

	slow := newFake(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	c = newClient(t, slow.srv.URL+"/v1", "")
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.ProposePlanEdits(ctx, "x", testView()); err == nil {
		t.Error("a request that outlives its context succeeded")
	}
}

func TestUnsupportedIsPlainTextOfBoundedLength(t *testing.T) {
	long := strings.Repeat("é", 400)
	encoded, err := json.Marshal(long + "<b>x</b>\u0007")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := planClient(t, `{"refused":false,"intents":[],"unsupported":`+string(encoded)+`}`)
	got, err := propose(t, c, "x")
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(got.Unsupported)); n > 140 {
		t.Errorf("unsupported is %d characters, want at most 140", n)
	}
	if strings.ContainsRune(got.Unsupported, '\a') {
		t.Error("a control character survived")
	}
}

// The job's client is built from config like every other job's.
func TestPlanEditsJobIsSwitchedOffByConfig(t *testing.T) {
	c := New(config.LLMConfig{BaseURL: "http://x/v1", Model: "m", Jobs: map[string]bool{string(JobPlanEdits): false}}, "")
	if c.Enabled(JobPlanEdits) {
		t.Error("plan_edits is on although config switches it off")
	}
	if !c.Enabled(JobExplainPlan) {
		t.Error("explain_plan is off although config leaves it on")
	}
}
