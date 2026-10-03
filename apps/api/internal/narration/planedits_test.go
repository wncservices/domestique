package narration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A fake Anthropic server only: nothing here ever reaches the real API.

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

// replyWith is a Messages API reply that calls the tool once with input.
func toolReply(input string) string {
	return `{"stop_reason":"tool_use","content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"toolu_1","name":"propose_plan_edits","input":` + input + `}]}`
}

// fakeServer answers every request with status and body, and records the last
// request body.
func fakeServer(t *testing.T, status int, body string) (*Client, *[]byte) {
	t.Helper()
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path %q", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c := New("test-key")
	c.APIBase = srv.URL
	return c, &got
}

func propose(t *testing.T, c *Client, text string) (PlanEdits, error) {
	t.Helper()
	return c.ProposePlanEdits(context.Background(), text, testView())
}

func TestTheRequestIsOneStrictToolWithAutoChoice(t *testing.T) {
	c, sent := fakeServer(t, 200, toolReply(`{"intents":[],"unsupported":null}`))
	if _, err := propose(t, c, "I'm travelling Thursday to Sunday"); err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal(*sent, &req); err != nil {
		t.Fatal(err)
	}
	if req["model"] != model {
		t.Errorf("model = %v, want the package's own constant %q", req["model"], model)
	}
	tools, _ := req["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want exactly one", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "propose_plan_edits" || tool["strict"] != true {
		t.Errorf("tool = %v, want propose_plan_edits with strict true", tool["name"])
	}
	schema := tool["input_schema"].(map[string]any)
	if schema["additionalProperties"] != false {
		t.Error("the top level of the schema allows extra properties")
	}
	items := schema["properties"].(map[string]any)["intents"].(map[string]any)["items"].(map[string]any)
	if items["additionalProperties"] != false {
		t.Error("an intent's schema allows extra properties")
	}
	// Auto, never forced: a forced choice is a 400 on the newer models.
	choice, _ := req["tool_choice"].(map[string]any)
	if choice["type"] != "auto" {
		t.Errorf("tool_choice = %v, want {type: auto}", req["tool_choice"])
	}
	for _, forced := range []string{`"type":"any"`, `"type":"tool"`} {
		if strings.Contains(string(*sent), forced) {
			t.Errorf("request forces a tool: contains %s", forced)
		}
	}
}

func TestTheRequestCarriesTheNoteTheDatesAndTheOpaquePlan(t *testing.T) {
	c, sent := fakeServer(t, 200, toolReply(`{"intents":[],"unsupported":null}`))
	if _, err := propose(t, c, "I'm travelling Thursday to Sunday"); err != nil {
		t.Fatal(err)
	}
	body := string(*sent)
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
		if !strings.Contains(body, want) {
			t.Errorf("the request does not contain %q:\n%s", want, body)
		}
	}
	// A session the rider built is described, never named.
	if !strings.Contains(body, "rider_built") {
		t.Error("the request does not say which session the rider built")
	}
}

func TestTheSchemaIsBuiltPerDeployment(t *testing.T) {
	enumOf := func(t *testing.T, caps Capabilities) []string {
		t.Helper()
		c, sent := fakeServer(t, 200, toolReply(`{"intents":[],"unsupported":null}`))
		view := testView()
		view.Capabilities = caps
		if _, err := c.ProposePlanEdits(context.Background(), "hi", view); err != nil {
			t.Fatal(err)
		}
		var req struct {
			Tools []struct {
				InputSchema struct {
					Properties struct {
						Intents struct {
							Items struct {
								Properties struct {
									Type struct {
										Enum []string `json:"enum"`
									} `json:"type"`
								} `json:"properties"`
							} `json:"items"`
						} `json:"intents"`
					} `json:"properties"`
				} `json:"input_schema"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(*sent, &req); err != nil {
			t.Fatal(err)
		}
		return req.Tools[0].InputSchema.Properties.Intents.Items.Properties.Type.Enum
	}
	has := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}

	full := enumOf(t, Capabilities{SwapAlternate: true, ConvertIndoor: true})
	for _, want := range []string{"create_life_event", "move_workout", "swap_alternate", "convert_indoor"} {
		if !has(full, want) {
			t.Errorf("a full deployment's enum %v lacks %s", full, want)
		}
	}
	bare := enumOf(t, Capabilities{})
	if has(bare, "swap_alternate") || has(bare, "convert_indoor") || !has(bare, "move_workout") {
		t.Errorf("a bare deployment's enum = %v", bare)
	}

	// And a reply that uses what was not offered is rejected.
	c, _ := fakeServer(t, 200, toolReply(`{"intents":[{"type":"swap_alternate","kind":null,"start_date":null,"end_date":null,"option":null,"workout":"w1","to_date":null,"alternate":"easier"}],"unsupported":null}`))
	view := testView()
	view.Capabilities = Capabilities{}
	if _, err := c.ProposePlanEdits(context.Background(), "hi", view); !errors.Is(err, ErrRejected) {
		t.Fatalf("an intent the deployment cannot do: %v, want ErrRejected", err)
	}
}

func TestAWellFormedReplyBecomesIntents(t *testing.T) {
	c, _ := fakeServer(t, 200, toolReply(`{"intents":[
		{"type":"create_life_event","kind":"travel","start_date":"2026-10-08","end_date":"2026-10-11","option":"no_bike","workout":null,"to_date":null,"alternate":null},
		{"type":"move_workout","kind":null,"start_date":null,"end_date":null,"option":null,"workout":"w1","to_date":"2026-10-10","alternate":null}
	],"unsupported":"I can't book flights"}`))
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

func TestTheWholeReplyIsRejectedWhenItIsNotTheSchema(t *testing.T) {
	const ok = `{"type":"create_life_event","kind":"busy","start_date":"2026-10-08","end_date":"2026-10-09","option":null,"workout":null,"to_date":null,"alternate":null}`
	cases := map[string]string{
		"unknown type":           `{"intents":[{"type":"delete_everything","kind":null,"start_date":null,"end_date":null,"option":null,"workout":null,"to_date":null,"alternate":null}],"unsupported":null}`,
		"an extra property":      `{"intents":[{"type":"create_life_event","kind":"busy","start_date":"2026-10-08","end_date":"2026-10-09","option":null,"workout":null,"to_date":null,"alternate":null,"force":true}],"unsupported":null}`,
		"an extra top-level key": `{"intents":[],"unsupported":null,"apply":true}`,
		"more than five intents": `{"intents":[` + strings.Repeat(ok+",", 5) + ok + `],"unsupported":null}`,
		"a wrong type":           `{"intents":[{"type":"create_life_event","kind":7,"start_date":"2026-10-08","end_date":"2026-10-09","option":null,"workout":null,"to_date":null,"alternate":null}],"unsupported":null}`,
		"intents is not a list":  `{"intents":"travel","unsupported":null}`,
		"not json at all":        `nope`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			reply := toolReply(input)
			if name == "not json at all" {
				reply = `{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t","name":"propose_plan_edits","input":"nope"}]}`
			}
			c, _ := fakeServer(t, 200, reply)
			if _, err := propose(t, c, "x"); !errors.Is(err, ErrRejected) {
				t.Fatalf("err = %v, want ErrRejected", err)
			}
		})
	}
	// Five is the limit, and allowed.
	c, _ := fakeServer(t, 200, toolReply(`{"intents":[`+strings.Repeat(ok+",", 4)+ok+`],"unsupported":null}`))
	if got, err := propose(t, c, "x"); err != nil || len(got.Intents) != 5 {
		t.Fatalf("five intents: %d, %v", len(got.Intents), err)
	}
}

func TestNoToolBlockOrARefusalIsAFailure(t *testing.T) {
	c, _ := fakeServer(t, 200, `{"stop_reason":"end_turn","content":[{"type":"text","text":"Sure, I moved it!"}]}`)
	if _, err := propose(t, c, "x"); !errors.Is(err, ErrNoToolCall) {
		t.Errorf("a reply with no tool block: %v, want ErrNoToolCall", err)
	}
	c, _ = fakeServer(t, 200, `{"stop_reason":"refusal","content":[]}`)
	if _, err := propose(t, c, "x"); !errors.Is(err, ErrNoToolCall) {
		t.Errorf("a refusal: %v, want ErrNoToolCall", err)
	}
	// A different tool is not ours.
	c, _ = fakeServer(t, 200, `{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t","name":"something_else","input":{}}]}`)
	if _, err := propose(t, c, "x"); !errors.Is(err, ErrNoToolCall) {
		t.Errorf("another tool: %v, want ErrNoToolCall", err)
	}
}

func TestAModelErrorAndATimeoutAreErrors(t *testing.T) {
	c, _ := fakeServer(t, 500, `{"error":{"type":"api_error","message":"boom"}}`)
	if _, err := propose(t, c, "x"); err == nil || errors.Is(err, ErrRejected) {
		t.Errorf("a 500: %v, want a plain error", err)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	c = New("test-key")
	c.APIBase = slow.URL
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.ProposePlanEdits(ctx, "x", testView()); err == nil {
		t.Error("a request that outlives its context succeeded")
	}
}

func TestUnsupportedIsPlainTextOfBoundedLength(t *testing.T) {
	long := strings.Repeat("é", 400)
	c, _ := fakeServer(t, 200, toolReply(`{"intents":[],"unsupported":"`+long+`<b>x</b>\u0007"}`))
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
