package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/narration"
	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The natural-language box. A fake Anthropic server answers every request; the
// real API is never called. The clock is Wednesday 2026-10-07 and the rider
// trains Tue/Thu/Sat/Sun.

const (
	secretRider   = "wilant"
	secretGoal    = "Zoersel Gran Fondo Quest"
	secretSession = "SECRET-SENTENCE-PIZZA"
)

type fakeModel struct {
	srv   *httptest.Server
	body  []byte
	reply string
	code  int
	calls int
}

func (h *onceHarness) withNarration(t *testing.T, reply string) *fakeModel {
	t.Helper()
	f := &fakeModel{reply: reply, code: 200}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls++
		f.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.code)
		_, _ = io.WriteString(w, f.reply)
	}))
	t.Cleanup(f.srv.Close)
	c := narration.New("test-key")
	c.APIBase = f.srv.URL
	h.srv.Narration = c
	return f
}

func tool(input string) string {
	return `{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"propose_plan_edits","input":` + input + `}]}`
}

func intent(typ string, fields map[string]string) string {
	get := func(k string) string {
		if v, ok := fields[k]; ok {
			return `"` + v + `"`
		}
		return "null"
	}
	return fmt.Sprintf(`{"type":%q,"kind":%s,"start_date":%s,"end_date":%s,"option":%s,"workout":%s,"to_date":%s,"alternate":%s}`,
		typ, get("kind"), get("start_date"), get("end_date"), get("option"), get("workout"), get("to_date"), get("alternate"))
}

func intents(list ...string) string {
	return `{"intents":[` + strings.Join(list, ",") + `],"unsupported":null}`
}

type editResponse struct {
	Items []struct {
		Intent struct {
			Type      string `json:"type"`
			Kind      string `json:"kind"`
			StartDate string `json:"startDate"`
			WorkoutID string `json:"workoutId"`
			ToDate    string `json:"toDate"`
		} `json:"intent"`
		Diff struct {
			Changes []lifeChange `json:"changes"`
		} `json:"diff"`
	} `json:"items"`
	Dropped []struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	} `json:"dropped"`
	Unsupported string `json:"unsupported"`
	Error       string `json:"error"`
}

func (h *onceHarness) propose(t *testing.T, user, text string) (int, editResponse) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"text": text})
	resp := h.as(user, "cyclists", http.MethodPost, "/api/training/plan/propose-edit?today=2026-10-07", string(b))
	var out editResponse
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// handles is the model's handle for each session of the next 14 days: w1, w2,
// ... in date order, the way the server numbers them.
func (h *onceHarness) handles(t *testing.T) map[string]workout.Workout {
	t.Helper()
	ws := h.all(t)
	sort.SliceStable(ws, func(i, j int) bool {
		if ws[i].Date != ws[j].Date {
			return ws[i].Date < ws[j].Date
		}
		return ws[i].ID < ws[j].ID
	})
	out := map[string]workout.Workout{}
	n := 0
	for _, w := range ws {
		if w.Date >= "2026-10-07" && w.Date <= "2026-10-20" {
			n++
			out[fmt.Sprintf("w%d", n)] = w
		}
	}
	return out
}

func (h *onceHarness) handleFor(t *testing.T, date string) (string, workout.Workout) {
	t.Helper()
	for handle, w := range h.handles(t) {
		if w.Date == date {
			return handle, w
		}
	}
	t.Fatalf("no session on %s", date)
	return "", workout.Workout{}
}

func (h *onceHarness) snapshot(t *testing.T) string {
	t.Helper()
	ws := h.all(t)
	var parts []string
	for _, w := range ws {
		parts = append(parts, w.ID+"|"+w.Date+"|"+w.Name+"|"+w.Description)
	}
	events, _ := h.store.ListLifeEvents(context.Background(), "wilant", "")
	return strings.Join(parts, "\n") + fmt.Sprintf("\nevents=%d", len(events))
}

func TestARequestCarriesTheMinimumAndNothingPrivate(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	// A rider with a name, health values and a goal that must never leave.
	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{
		Rider: secretRider, FTPWatts: 287, MaxHR: 191, ThresholdHR: 173, RestingHR: 44, ThresholdPaceSecPerKM: 247,
		HoursPerAvailableDay: 3, AvailableDays: []string{"tue", "thu", "sat", "sun"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: secretRider, Name: secretGoal, EventDate: weeksEventDate}); err != nil {
		t.Fatal(err)
	}
	// A session the rider named themselves, and one dated today.
	if _, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: secretRider, Sport: "cycling", Name: "Ride with Pieter to Oudenaarde", Date: "2026-10-09", Description: "mine",
	}); err != nil {
		t.Fatal(err)
	}
	// Another rider's session, in the same window.
	if _, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "sam", Sport: "cycling", Name: "Sam's secret intervals", Date: "2026-10-08", Description: "mine",
	}); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(ctx)
	// A plan-made session the rider renamed: the generated description is
	// untouched, so only the name gives it away, and the name is theirs.
	renamed := false
	for _, w := range h.all(t) {
		if w.GoalID != "" && w.Date >= "2026-10-07" && w.Date <= "2026-10-20" && !renamed {
			name := "Pieter's club tempo with Marthe"
			if _, err := h.store.UpdateWorkout(ctx, w.ID, workout.UpdateWorkoutRequest{Name: &name}); err != nil {
				t.Fatal(err)
			}
			renamed = true
		}
	}
	if !renamed {
		t.Fatal("no plan-made session to rename")
	}
	f := h.withNarration(t, tool(intents()))

	if status, _ := h.propose(t, "wilant", "I am travelling Thursday to Sunday "+secretSession); status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	body := string(f.body)
	if f.calls != 1 {
		t.Fatalf("%d calls to the model, want 1", f.calls)
	}

	for _, secret := range []string{
		secretRider, "Wilant", "287", "191", "173", "247", "44,", // name, FTP, heart rates, pace
		secretGoal, "Zoersel", // goal
		"Pieter", "Oudenaarde", "Marthe", "club tempo", // a name the rider typed, or renamed a plan session to
		"Sam's", "sam", // another rider
		"watts", "heart", "readiness", "wellness",
	} {
		if strings.Contains(body, secret) {
			t.Errorf("the request contains %q:\n%s", secret, body)
		}
	}
	for _, w := range h.all(t) {
		if strings.Contains(body, w.ID) {
			t.Errorf("the request contains the database id %q", w.ID)
		}
	}
	// What it does need.
	for _, want := range []string{secretSession, "2026-10-07", "Wednesday", "tue", "w1", "rider_built", "propose_plan_edits"} {
		if !strings.Contains(body, want) {
			t.Errorf("the request lacks %q", want)
		}
	}
}

func TestAWellFormedReplyIsAPreviewAndNothingIsWritten(t *testing.T) {
	h := newOnceHarness(t)
	h.tickedWithFreeSaturday(t)
	handle, thu := h.handleFor(t, "2026-10-08")
	h.withNarration(t, tool(intents(
		intent("create_life_event", map[string]string{"kind": "travel", "start_date": "2026-10-14", "end_date": "2026-10-15", "option": "no_bike"}),
		intent("move_workout", map[string]string{"workout": handle, "to_date": "2026-10-10"}),
	)))
	before := h.snapshot(t)

	status, res := h.propose(t, "wilant", "away on the 14th and 15th, and put Thursday's ride on Saturday")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, res.Error)
	}
	if len(res.Items) != 2 || len(res.Dropped) != 0 {
		t.Fatalf("items %d dropped %d: %+v", len(res.Items), len(res.Dropped), res.Dropped)
	}
	ev := res.Items[0]
	if ev.Intent.Type != "create_life_event" || ev.Intent.Kind != "travel" || len(ev.Diff.Changes) == 0 {
		t.Errorf("the life event item = %+v, want a travel event with the diff the form would give", ev)
	}
	mv := res.Items[1]
	if mv.Intent.WorkoutID != thu.ID || mv.Intent.ToDate != "2026-10-10" || len(mv.Diff.Changes) != 1 || mv.Diff.Changes[0].Op != "move" {
		t.Errorf("the move item = %+v", mv)
	}
	if after := h.snapshot(t); after != before {
		t.Fatalf("proposing wrote something:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestAnIntentThatBreaksARuleIsDroppedWithAReason(t *testing.T) {
	h := newOnceHarness(t)
	h.tickedWithFreeSaturday(t)
	h.event(t, "busy", "2026-10-17", "2026-10-17", "")
	if _, err := h.store.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: "cycling", Name: "Done today", Date: "2026-10-07", Description: "mine",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.UpsertSession(context.Background(), workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ride-today", Sport: "cycling", Date: "2026-10-07", DurationSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	riddenHandle, _ := h.handleFor(t, "2026-10-07")
	thuHandle, _ := h.handleFor(t, "2026-10-08")
	sunHandle, _ := h.handleFor(t, "2026-10-11")

	h.withNarration(t, tool(intents(
		intent("create_life_event", map[string]string{"kind": "travel", "start_date": "2026-13-45", "end_date": "2026-10-15"}),                   // bad date
		intent("create_life_event", map[string]string{"kind": "busy", "start_date": "2027-03-01", "end_date": "2027-03-02"}),                     // beyond 60 days
		intent("create_life_event", map[string]string{"kind": "travel", "start_date": "2026-10-20", "end_date": "2026-10-21", "option": "mild"}), // illegal pair
		intent("move_workout", map[string]string{"workout": "w99", "to_date": "2026-10-10"}),                                                     // foreign handle
		intent("move_workout", map[string]string{"workout": riddenHandle, "to_date": "2026-10-10"}),                                              // ridden
		intent("move_workout", map[string]string{"workout": thuHandle, "to_date": "2026-10-09"}),                                                 // not an available day
		intent("move_workout", map[string]string{"workout": thuHandle, "to_date": "2026-10-17"}),                                                 // inside an event
		intent("move_workout", map[string]string{"workout": sunHandle, "to_date": "2026-10-05"}),                                                 // past
	)))
	// Eight intents is over the limit, so this reply is rejected outright:
	// check the drops with five at a time below.
	status, res := h.propose(t, "wilant", "x")
	if status != http.StatusBadGateway || !strings.Contains(res.Error, "couldn't understand") {
		t.Fatalf("eight intents: status %d %q, want the whole reply rejected", status, res.Error)
	}

	want := map[string][]string{
		"bad date":          {intent("create_life_event", map[string]string{"kind": "travel", "start_date": "2026-13-45", "end_date": "2026-10-15"}), "YYYY-MM-DD"},
		"beyond 60 days":    {intent("create_life_event", map[string]string{"kind": "busy", "start_date": "2027-03-01", "end_date": "2027-03-02"}), "between today and"},
		"illegal pair":      {intent("create_life_event", map[string]string{"kind": "travel", "start_date": "2026-10-20", "end_date": "2026-10-21", "option": "mild"}), "not an option"},
		"foreign handle":    {intent("move_workout", map[string]string{"workout": "w99", "to_date": "2026-10-10"}), "not in the next two weeks"},
		"ridden":            {intent("move_workout", map[string]string{"workout": riddenHandle, "to_date": "2026-10-10"}), "already ridden"},
		"unavailable day":   {intent("move_workout", map[string]string{"workout": thuHandle, "to_date": "2026-10-09"}), "available days"},
		"inside an event":   {intent("move_workout", map[string]string{"workout": thuHandle, "to_date": "2026-10-17"}), "life event"},
		"a day that's gone": {intent("move_workout", map[string]string{"workout": sunHandle, "to_date": "2026-10-05"}), "between today and"},
	}
	for name, c := range want {
		t.Run(name, func(t *testing.T) {
			h.withNarration(t, tool(intents(c[0])))
			status, res := h.propose(t, "wilant", "x")
			if status != http.StatusOK {
				t.Fatalf("status %d: %s", status, res.Error)
			}
			if len(res.Items) != 0 || len(res.Dropped) != 1 {
				t.Fatalf("items %d dropped %d, want the intent dropped", len(res.Items), len(res.Dropped))
			}
			if !strings.Contains(res.Dropped[0].Reason, c[1]) {
				t.Errorf("reason %q does not mention %q", res.Dropped[0].Reason, c[1])
			}
		})
	}
}

func TestTheWholeReplyIsRejectedWhenItIsNotTheSchema(t *testing.T) {
	cases := map[string]string{
		"unknown type": tool(intents(intent("wipe_plan", nil))),
		"extra field":  tool(`{"intents":[],"unsupported":null,"apply":true}`),
		"no tool call": `{"stop_reason":"end_turn","content":[{"type":"text","text":"done!"}]}`,
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			h := newOnceHarness(t)
			h.withNarration(t, reply)
			before := h.snapshot(t)
			status, res := h.propose(t, "wilant", "x")
			if status != http.StatusBadGateway {
				t.Fatalf("status %d, want 502", status)
			}
			if name != "no tool call" && !strings.Contains(res.Error, "Life event form") {
				t.Errorf("error %q should point at the form", res.Error)
			}
			if h.snapshot(t) != before {
				t.Error("a rejected reply changed something")
			}
		})
	}
}

func TestUnsupportedIsPassedAlongAsPlainText(t *testing.T) {
	h := newOnceHarness(t)
	h.withNarration(t, tool(`{"intents":[],"unsupported":"I can't book flights"}`))
	status, res := h.propose(t, "wilant", "book me a flight")
	if status != http.StatusOK || res.Unsupported != "I can't book flights" || len(res.Items) != 0 {
		t.Fatalf("status %d: %+v", status, res)
	}
}

func TestASwapAndAnIndoorConversionAreValidatedByTheirOwnRules(t *testing.T) {
	h := newOnceHarness(t)
	h.srv.AutoScheduleTick(context.Background())
	handle, wk := h.handleFor(t, "2026-10-08")

	// Endurance has no "easier": that alternate is not on offer.
	kind := "shorter"
	if wk.Zone != workout.ZoneEndurance {
		kind = "easier"
	}
	h.withNarration(t, tool(intents(
		intent("swap_alternate", map[string]string{"workout": handle, "alternate": kind}),
		intent("convert_indoor", map[string]string{"workout": handle}),
	)))
	status, res := h.propose(t, "wilant", "make Thursday easier and indoors")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, res.Error)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items %d dropped %+v", len(res.Items), res.Dropped)
	}
	if res.Items[0].Diff.Changes[0].Op != "swap" || res.Items[1].Diff.Changes[0].Op != "indoor" {
		t.Errorf("ops = %s, %s", res.Items[0].Diff.Changes[0].Op, res.Items[1].Diff.Changes[0].Op)
	}

	// Now with an alternate that does not exist for this session, and for an
	// FTP test, which can be moved but not swapped or converted.
	other := "longer"
	if kind == "shorter" {
		other = "easier"
	}
	test, err := h.store.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: h.goal.ID, Sport: "cycling", Name: "FTP test", Date: "2026-10-13", Description: "FTP", TestProtocol: "ramp",
	})
	if err != nil {
		t.Fatal(err)
	}
	testHandle := ""
	for hd, w := range h.handles(t) {
		if w.ID == test.ID {
			testHandle = hd
		}
	}
	h.withNarration(t, tool(intents(
		intent("swap_alternate", map[string]string{"workout": handle, "alternate": other}),
		intent("convert_indoor", map[string]string{"workout": testHandle}),
	)))
	_, res = h.propose(t, "wilant", "x")
	if len(res.Dropped) < 1 {
		t.Fatalf("expected drops, got items %d dropped %d", len(res.Items), len(res.Dropped))
	}
	for _, d := range res.Dropped {
		if d.Type == "convert_indoor" && !strings.Contains(d.Reason, "FTP test") {
			t.Errorf("an FTP test conversion was dropped for %q", d.Reason)
		}
	}
}

func TestAModelFailureIs502WithAWarnAndNoSentenceInTheLog(t *testing.T) {
	h := newOnceHarness(t)
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f := h.withNarration(t, `{"error":{"message":"boom"}}`)
	f.code = 500

	status, _ := h.propose(t, "wilant", secretSession)
	if status != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", status)
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "rider=wilant") || !strings.Contains(out, "outcome=") {
		t.Errorf("no Warn with rider and outcome:\n%s", out)
	}
	if strings.Contains(out, secretSession) {
		t.Errorf("the typed sentence was logged:\n%s", out)
	}

	// And a success logs counts only.
	logs.Reset()
	f.code, f.reply = 200, tool(intents())
	if status, _ := h.propose(t, "wilant", secretSession); status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if strings.Contains(logs.String(), secretSession) {
		t.Errorf("the typed sentence was logged on success:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "plan edit proposed") {
		t.Errorf("a success should log an Info line:\n%s", logs.String())
	}
}

func TestWithoutAKeyTheBoxLogsAndSaysSo(t *testing.T) {
	h := newOnceHarness(t)
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	status, res := h.propose(t, "wilant", "x")
	if status != http.StatusPreconditionFailed || res.Error == "" {
		t.Fatalf("status %d %q, want 412 with the guard message", status, res.Error)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "ANTHROPIC_API_KEY") {
		t.Errorf("the guard did not log a Warn:\n%s", logs.String())
	}
}

func TestInputLimitsOwnerOnlyAndTheLimiter(t *testing.T) {
	h := newOnceHarness(t)
	f := h.withNarration(t, tool(intents()))
	if status, _ := h.propose(t, "wilant", ""); status != http.StatusBadRequest {
		t.Errorf("empty text: status %d, want 400", status)
	}
	if status, _ := h.propose(t, "wilant", strings.Repeat("a", 301)); status != http.StatusBadRequest {
		t.Errorf("301 characters: status %d, want 400", status)
	}
	if status, _ := h.propose(t, "wilant", strings.Repeat("é", 300)); status != http.StatusOK {
		t.Errorf("300 characters: status %d, want 200", status)
	}
	if f.calls != 1 {
		t.Errorf("%d model calls for three requests, want 1", f.calls)
	}
	// A viewer cannot manage training.
	resp := h.as("watcher", "viewers", http.MethodPost, "/api/training/plan/propose-edit", `{"text":"x"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer: status %d, want 403", resp.StatusCode)
	}

	h.srv.NarrationLimiter = ratelimit.New(1, time.Hour)
	if status, _ := h.propose(t, "wilant", "one"); status != http.StatusOK {
		t.Fatalf("first request: status %d", status)
	}
	if status, _ := h.propose(t, "wilant", "two"); status != http.StatusTooManyRequests {
		t.Errorf("second request: status %d, want 429", status)
	}
}
