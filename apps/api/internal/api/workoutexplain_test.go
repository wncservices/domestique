package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
)

// The model's reply for a session. It cites no number, so the number rule in
// internal/llm never refuses it.
const (
	workoutExplainReply = `{"summary":"A steady session to build the base.","blocks":[{"label":"Main","cue":"Hold the target and stay relaxed."}]}`
	workoutExplainText  = "A steady session to build the base."
)

type explanationBody struct {
	Why     string `json:"why"`
	Summary string `json:"summary"`
	Blocks  []struct {
		Label string `json:"label"`
		Cue   string `json:"cue"`
	} `json:"blocks"`
	Source  string `json:"source"`
	Pending bool   `json:"pending"`
}

// getWrittenExplanation reads a session's explanation, lets the background
// write finish, and reads it again: the first read answers the template.
func getWrittenExplanation(t *testing.T, h *ledgerHarness, user, id string) explanationBody {
	t.Helper()
	getExplanation(t, h, user, id)
	h.srv.WaitForBackground()
	_, got := getExplanation(t, h, user, id)
	return got
}

func getExplanation(t *testing.T, h *ledgerHarness, user, id string) (int, explanationBody) {
	t.Helper()
	resp := h.as(user, "cyclists", http.MethodGet, "/api/training/workouts/"+id+"/explanation", "")
	var out explanationBody
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, out
}

func explanationRows(t *testing.T, h *ledgerHarness) int {
	t.Helper()
	var n int
	if err := h.conn.QueryRow(`SELECT COUNT(1) FROM workout_explanations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func explainCalls(counter *int, mu *sync.Mutex, then func(string, []byte) string) func(string, []byte) string {
	return func(job string, req []byte) string {
		if job == "explain_workout" {
			mu.Lock()
			*counter++
			mu.Unlock()
		}
		return then(job, req)
	}
}

func TestExplanationMissCallsOnceThenHitsTheCache(t *testing.T) {
	h := newLedgerHarness(t, 250)
	f := withExplainLLM(t, h.srv, fixed(workoutExplainReply))
	id := firstWorkoutIDs(t, h, 1)[0]

	code, first := getExplanation(t, h, "wilant", id)
	if code != http.StatusOK || first.Source != "template" || !first.Pending {
		t.Fatalf("first read = %d %+v, want the template, pending", code, first)
	}
	h.srv.WaitForBackground()
	code, got := getExplanation(t, h, "wilant", id)
	if code != http.StatusOK || got.Source != "model" || got.Pending || got.Summary != workoutExplainText || len(got.Blocks) != 1 {
		t.Fatalf("written read = %d %+v", code, got)
	}
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("model calls = %d, want 1", n)
	}
	if n := explanationRows(t, h); n != 1 {
		t.Fatalf("stored rows = %d, want 1", n)
	}

	code, got = getExplanation(t, h, "wilant", id)
	if code != http.StatusOK || got.Source != "model" || got.Summary != workoutExplainText {
		t.Fatalf("second read = %d %+v", code, got)
	}
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("a cache hit called the model: calls = %d", n)
	}
}

func TestExplanationWhenTheJobIsOffIsTheTemplateAndNothingIsStored(t *testing.T) {
	h := newLedgerHarness(t, 250)
	f := withLLMJobs(t, h.srv, map[string]bool{"explain_workout": false}, fixed(workoutExplainReply))
	id := firstWorkoutIDs(t, h, 1)[0]

	code, got := getExplanation(t, h, "wilant", id)
	if code != http.StatusOK || got.Source != "template" || got.Summary == "" || got.Blocks == nil {
		t.Fatalf("read = %d %+v", code, got)
	}
	if f.calls.Load() != 0 || explanationRows(t, h) != 0 {
		t.Fatalf("calls = %d, rows = %d; want none", f.calls.Load(), explanationRows(t, h))
	}
}

func TestExplanationWithNoModelIsTheTemplate(t *testing.T) {
	h := newLedgerHarness(t, 250)
	id := firstWorkoutIDs(t, h, 1)[0]
	code, got := getExplanation(t, h, "wilant", id)
	if code != http.StatusOK || got.Source != "template" || got.Summary == "" {
		t.Fatalf("read = %d %+v", code, got)
	}
	if explanationRows(t, h) != 0 {
		t.Fatal("the template was stored")
	}
}

func TestExplanationOfAnotherRidersWorkoutIs404(t *testing.T) {
	h := newLedgerHarness(t, 250)
	f := withExplainLLM(t, h.srv, fixed(workoutExplainReply))
	id := firstWorkoutIDs(t, h, 1)[0]
	if code, _ := getExplanation(t, h, "alice", id); code != http.StatusNotFound {
		t.Fatalf("another rider's workout = %d, want 404", code)
	}
	if code, _ := getExplanation(t, h, "alice", "no-such-id"); code != http.StatusNotFound {
		t.Fatalf("an unknown workout = %d, want 404", code)
	}
	if f.calls.Load() != 0 {
		t.Fatal("the model was called for a workout that is not the caller's")
	}
}

// Two requests for the same missing explanation make one model call: both get
// the template at once and both are told it is on its way.
func TestConcurrentMissMakesOneCall(t *testing.T) {
	h := newLedgerHarness(t, 250)
	var mu sync.Mutex
	calls := 0
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	withExplainLLM(t, h.srv, explainCalls(&calls, &mu, func(string, []byte) string {
		once.Do(func() { close(started) })
		<-release
		return workoutExplainReply
	}))
	id := firstWorkoutIDs(t, h, 1)[0]

	code, first := getExplanation(t, h, "wilant", id)
	if code != http.StatusOK || first.Source != "template" || !first.Pending {
		t.Fatalf("the starter = %d %+v, want the template, pending", code, first)
	}
	<-started
	code, second := getExplanation(t, h, "wilant", id)
	if code != http.StatusOK || second.Source != "template" || !second.Pending {
		t.Fatalf("the second request = %d %+v, want the template, pending", code, second)
	}
	close(release)
	h.srv.WaitForBackground()
	if _, got := getExplanation(t, h, "wilant", id); got.Source != "model" {
		t.Fatalf("after the write = %+v, want the model's text", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("model calls = %d, want 1", calls)
	}
}

// A slow model never holds the request: the answer is the template while the
// call is still running, and the text arrives for the next view.
func TestASlowModelDoesNotHoldTheRequest(t *testing.T) {
	h := newLedgerHarness(t, 250)
	release := make(chan struct{})
	withExplainLLM(t, h.srv, func(string, []byte) string {
		<-release
		return workoutExplainReply
	})
	id := firstWorkoutIDs(t, h, 1)[0]

	begun := time.Now()
	code, got := getExplanation(t, h, "wilant", id)
	if code != http.StatusOK || got.Source != "template" || !got.Pending {
		t.Fatalf("read = %d %+v, want the template, pending", code, got)
	}
	if d := time.Since(begun); d > 5*time.Second {
		t.Fatalf("the request took %s with the model still running", d)
	}
	close(release)
	h.srv.WaitForBackground()
	if _, got = getExplanation(t, h, "wilant", id); got.Source != "model" {
		t.Fatalf("after the write = %+v", got)
	}
}

// The background write runs on the server's lifecycle, so a shutdown stops it,
// and the claim it held is given back: after a restart the next view tries again
// instead of showing "being written" for the five minutes a stale claim lasts.
func TestAShutdownDuringTheBackgroundWriteReleasesTheClaim(t *testing.T) {
	h := newLedgerHarness(t, 250)
	ctx, stop := context.WithCancel(context.Background())
	h.srv.Lifecycle = ctx
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	withExplainLLM(t, h.srv, func(string, []byte) string {
		once.Do(func() { close(started) })
		<-release
		return workoutExplainReply
	})
	id := firstWorkoutIDs(t, h, 1)[0]

	if _, got := getExplanation(t, h, "wilant", id); !got.Pending {
		t.Fatalf("first read = %+v, want pending", got)
	}
	<-started
	stop()
	h.srv.WaitForBackground()
	close(release)
	if n := explanationRows(t, h); n != 0 {
		t.Fatalf("rows after a shutdown = %d, want 0 (claim released, nothing stored)", n)
	}
}

// The background write stays in the request's trace: a span for the write is a
// child of the request, carries llm.job, and ends after the response. Without
// this the model call showed up as an unrelated root trace with no job tag.
func TestTheBackgroundWriteIsInTheRequestsTrace(t *testing.T) {
	serialOnly(t) // swaps the global tracer provider
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	h := newLedgerHarness(t, 250)
	withExplainLLM(t, h.srv, fixed(workoutExplainReply))
	id := firstWorkoutIDs(t, h, 1)[0]
	getExplanation(t, h, "wilant", id)
	h.srv.WaitForBackground()

	var request, write sdktrace.ReadOnlySpan
	for _, sp := range rec.Ended() {
		switch {
		case sp.Name() == "explain_workout.write":
			write = sp
		case strings.HasSuffix(sp.Name(), "/explanation"):
			request = sp
		}
	}
	if write == nil || request == nil {
		t.Fatalf("spans: request %v, write %v", request != nil, write != nil)
	}
	if write.SpanContext().TraceID() != request.SpanContext().TraceID() {
		t.Fatal("the background write is in a different trace from the request")
	}
	if write.Parent().SpanID() != request.SpanContext().SpanID() {
		t.Fatal("the background write is not a child of the request span")
	}
	var job string
	for _, kv := range write.Attributes() {
		if kv.Key == attribute.Key("llm.job") {
			job = kv.Value.AsString()
		}
	}
	if job != "explain_workout" {
		t.Fatalf("llm.job on the write span = %q", job)
	}
}

func TestAFailedModelCallIsNotStoredAndNotKept(t *testing.T) {
	h := newLedgerHarness(t, 250)
	f := withExplainLLM(t, h.srv, fixed(workoutExplainReply))
	f.code = http.StatusInternalServerError
	id := firstWorkoutIDs(t, h, 1)[0]
	if code, got := getExplanation(t, h, "wilant", id); code != http.StatusOK || got.Source != "template" {
		t.Fatalf("a failed call = %d %+v, want the template", code, got)
	}
	h.srv.WaitForBackground()
	if n := explanationRows(t, h); n != 0 {
		t.Fatalf("rows after a failed call = %d, want 0", n)
	}
}

func TestExplanationWhyLineForAGoalSession(t *testing.T) {
	h := newLedgerHarness(t, 250)
	h.at(6, 9, 0)
	id := firstWorkoutIDs(t, h, 1)[0]
	code, got := getExplanation(t, h, "wilant", id)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(got.Why, " week 1 of ") || !strings.Contains(got.Why, " for Gran Fondo") {
		t.Fatalf("why = %q, want phase, week 1 of N and the goal name", got.Why)
	}
}

func TestExplanationWhyLineIsEmptyWithNoGoal(t *testing.T) {
	h := newLedgerHarness(t, 250)
	w, err := h.store.CreateWorkout(context.Background(), api.LegacyFlagged(generated("wilant", "", "Solo spin", "2026-10-07", 1800)))
	if err != nil {
		t.Fatal(err)
	}
	code, got := getExplanation(t, h, "wilant", w.ID)
	if code != http.StatusOK || got.Why != "" {
		t.Fatalf("a session with no goal = %d, why %q; want empty", code, got.Why)
	}
}

func TestARateLimitedMissServesTheTemplateAndKeepsNoClaim(t *testing.T) {
	h := newLedgerHarness(t, 250)
	f := withExplainLLM(t, h.srv, fixed(workoutExplainReply))
	h.srv.NarrationLimiter = ratelimit.New(1, time.Hour)
	h.srv.NarrationLimiter.Allow("wilant") // the budget is spent
	ids := firstWorkoutIDs(t, h, 1)

	code, got := getExplanation(t, h, "wilant", ids[0])
	if code != http.StatusOK || got.Source != "template" || len(got.Blocks) == 0 {
		t.Fatalf("limited miss = %d %+v, want the template", code, got)
	}
	if got.Pending {
		t.Fatal("a refused miss was reported as pending")
	}
	if f.calls.Load() != 0 || explanationRows(t, h) != 0 {
		t.Fatalf("calls = %d, rows = %d; want none (claim given back)", f.calls.Load(), explanationRows(t, h))
	}
}

func TestACacheHitIsNotChargedToTheLimiter(t *testing.T) {
	h := newLedgerHarness(t, 250)
	withExplainLLM(t, h.srv, fixed(workoutExplainReply))
	id := firstWorkoutIDs(t, h, 1)[0]
	if got := getWrittenExplanation(t, h, "wilant", id); got.Source != "model" {
		t.Fatalf("warm-up = %+v", got)
	}
	h.srv.NarrationLimiter = ratelimit.New(1, time.Hour)
	h.srv.NarrationLimiter.Allow("wilant")
	if code, got := getExplanation(t, h, "wilant", id); code != 200 || got.Source != "model" {
		t.Fatalf("hit with a spent limiter = %d %+v", code, got)
	}
}

func TestAStepLessWorkoutGetsTheTemplateWithNoCall(t *testing.T) {
	h := newLedgerHarness(t, 250)
	f := withExplainLLM(t, h.srv, fixed(workoutExplainReply))
	wk := generated("wilant", h.goal.ID, "Empty", "2026-10-07", 1800)
	wk.Steps = nil
	w, err := h.store.CreateWorkout(context.Background(), api.LegacyFlagged(wk))
	if err != nil {
		t.Fatal(err)
	}
	code, got := getExplanation(t, h, "wilant", w.ID)
	if code != http.StatusOK || got.Source != "template" {
		t.Fatalf("step-less = %d %+v", code, got)
	}
	if f.calls.Load() != 0 || explanationRows(t, h) != 0 {
		t.Fatalf("calls = %d, rows = %d; want none", f.calls.Load(), explanationRows(t, h))
	}
}

// ---- pre-writing in the morning notes pass ----

// clearWorkouts removes the harness's generated week so a test controls which
// sessions the pass can see.
func clearWorkouts(t *testing.T, h *ledgerHarness) {
	t.Helper()
	for _, w := range h.workouts(t) {
		if err := h.store.DeleteWorkout(context.Background(), w.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func addSession(t *testing.T, h *ledgerHarness, date string, minutes int) {
	t.Helper()
	if _, err := h.store.CreateWorkout(context.Background(), api.LegacyFlagged(generated("wilant", h.goal.ID, "Spin", date, float64(minutes*60)))); err != nil {
		t.Fatal(err)
	}
}

func TestPrewritingStopsAtTenShapes(t *testing.T) {
	h := newLedgerHarness(t, 250)
	clearWorkouts(t, h)
	for i := 0; i < 13; i++ {
		addSession(t, h, "2026-10-08", 30+i*5)
	}
	var mu sync.Mutex
	calls := 0
	withExplainLLM(t, h.srv, explainCalls(&calls, &mu, fixed(workoutExplainReply)))

	h.at(6, 6, 45)
	h.srv.RunLLMNotesForTest(context.Background())
	if calls != 10 || explanationRows(t, h) != 10 {
		t.Fatalf("calls = %d, rows = %d; want 10 and 10", calls, explanationRows(t, h))
	}

	// The next pass carries on with the ones left, without redoing any.
	h.srv.RunLLMNotesForTest(context.Background())
	if calls != 13 || explanationRows(t, h) != 13 {
		t.Fatalf("after the next pass calls = %d, rows = %d; want 13 and 13", calls, explanationRows(t, h))
	}
}

func TestPrewritingClaimsTheEarliestDatesFirst(t *testing.T) {
	h := newLedgerHarness(t, 250)
	clearWorkouts(t, h)
	// Twelve shapes: the two on the last day are the ones left out.
	for i := 0; i < 10; i++ {
		addSession(t, h, "2026-10-06", 30+i*5)
	}
	addSession(t, h, "2026-10-12", 200)
	addSession(t, h, "2026-10-12", 205)
	var mu sync.Mutex
	calls := 0
	var seen []string
	withExplainLLM(t, h.srv, explainCalls(&calls, &mu, func(job string, req []byte) string {
		mu.Lock()
		seen = append(seen, string(req))
		mu.Unlock()
		return workoutExplainReply
	}))
	h.at(6, 6, 45)
	h.srv.RunLLMNotesForTest(context.Background())
	if calls != 10 {
		t.Fatalf("calls = %d, want 10", calls)
	}
	for _, r := range seen {
		if strings.Contains(r, "200min") || strings.Contains(r, "205min") {
			t.Fatalf("a later shape was claimed before an earlier one: %s", r)
		}
	}
}

func TestPrewritingCoversOnlyTheNextSevenDaysAndSharesShapes(t *testing.T) {
	h := newLedgerHarness(t, 250)
	clearWorkouts(t, h)
	addSession(t, h, "2026-10-05", 40) // yesterday
	addSession(t, h, "2026-10-06", 50) // today
	addSession(t, h, "2026-10-12", 60) // day 7: last in the window
	addSession(t, h, "2026-10-13", 70) // day 8: out
	addSession(t, h, "2026-10-07", 50) // same shape as today's
	var mu sync.Mutex
	calls := 0
	withExplainLLM(t, h.srv, explainCalls(&calls, &mu, fixed(workoutExplainReply)))

	h.at(6, 6, 45)
	h.srv.RunLLMNotesForTest(context.Background())
	if calls != 2 || explanationRows(t, h) != 2 {
		t.Fatalf("calls = %d, rows = %d; want 2 and 2", calls, explanationRows(t, h))
	}
}

func TestPrewritingWritesNothingWithoutAModelOrWithTheJobOff(t *testing.T) {
	h := newLedgerHarness(t, 250)
	clearWorkouts(t, h)
	addSession(t, h, "2026-10-07", 50)

	h.at(6, 6, 45)
	h.srv.RunLLMNotesForTest(context.Background())
	if n := explanationRows(t, h); n != 0 {
		t.Fatalf("rows with no model = %d", n)
	}

	f := withLLMJobs(t, h.srv, map[string]bool{"explain_workout": false}, fixed(workoutExplainReply))
	h.srv.RunLLMNotesForTest(context.Background())
	if n := explanationRows(t, h); n != 0 || f.calls.Load() != 0 {
		t.Fatalf("rows = %d, calls = %d with the job off", n, f.calls.Load())
	}
}

func TestPrewritingSkipsAShapeAlreadyStored(t *testing.T) {
	h := newLedgerHarness(t, 250)
	clearWorkouts(t, h)
	addSession(t, h, "2026-10-07", 50)
	var mu sync.Mutex
	calls := 0
	withExplainLLM(t, h.srv, explainCalls(&calls, &mu, fixed(workoutExplainReply)))
	w := h.workouts(t)[0]

	if got := getWrittenExplanation(t, h, "wilant", w.ID); got.Source != "model" {
		t.Fatalf("first view = %+v", got)
	}
	h.at(6, 6, 45)
	h.srv.RunLLMNotesForTest(context.Background())
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (the pass found the shape stored)", calls)
	}
}

// ---- privacy ----

func TestExplanationsNeverLogTheShapeTheTextOrTheRider(t *testing.T) {
	h := newLedgerHarness(t, 250)
	var logs bytes.Buffer
	h.srv.Log = scanLogger(&logs)
	id := firstWorkoutIDs(t, h, 1)[0]
	const secret = "BANANAZEBRA"
	f := withExplainLLM(t, h.srv, fixed(strings.Replace(workoutExplainReply, "steady", secret, 1)))

	h.at(6, 6, 45)
	getExplanation(t, h, "wilant", id)
	h.srv.WaitForBackground()
	h.srv.RunLLMNotesForTest(context.Background())
	f.code = http.StatusInternalServerError
	clearWorkouts(t, h)
	addSession(t, h, "2026-10-07", 55)
	h.srv.RunLLMNotesForTest(context.Background())
	getExplanation(t, h, "alice", id)

	out := logs.String()
	if out == "" {
		t.Fatal("precondition: nothing was logged")
	}
	for _, bad := range []string{secret, "sport cycling", "zone ", "% FTP", "wilant", "Gran Fondo", "Spin"} {
		if strings.Contains(out, bad) {
			t.Errorf("a log line carries %q:\n%s", bad, out)
		}
	}
}
