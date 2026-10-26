package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/llm"
)

// The self-hosted model is faked as an OpenAI-compatible chat endpoint. The
// handlers and the client are the real ones; only the model is stand-in. The
// fake answers each request with reply(job, body), where job is the schema
// name the request asked for. When code is not 200 it answers with that status
// and an error body that quotes the request, which the client must never echo.
type fakeLLM struct {
	srv   *httptest.Server
	reply func(job string, req []byte) string
	code  int
	calls atomic.Int32
	last  []byte
}

func withLLM(t *testing.T, s *api.Server, reply func(job string, req []byte) string) *fakeLLM {
	t.Helper()
	return withLLMJobs(t, s, nil, reply)
}

// withLLMJobs is withLLM with some jobs switched off in config.
func withLLMJobs(t *testing.T, s *api.Server, jobs map[string]bool, reply func(job string, req []byte) string) *fakeLLM {
	t.Helper()
	f := &fakeLLM{reply: reply, code: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		f.last = body
		var req struct {
			ResponseFormat struct {
				JSONSchema struct {
					Name string `json:"name"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		if f.code != http.StatusOK {
			quoted, _ := json.Marshal("bad prompt: " + string(body))
			w.WriteHeader(f.code)
			_, _ = fmt.Fprintf(w, `{"error":%s}`, quoted)
			return
		}
		content, _ := json.Marshal(f.reply(req.ResponseFormat.JSONSchema.Name, body))
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%s}}]}`, content)
	}))
	t.Cleanup(f.srv.Close)
	s.UseLLM(llm.New(config.LLMConfig{BaseURL: f.srv.URL, Model: "qwen3.5-9b", Jobs: jobs}, ""))
	return f
}

// fixed answers every job with the same content.
func fixed(content string) func(string, []byte) string {
	return func(string, []byte) string { return content }
}

// withLLMReply is the plan-edit tests' shorthand: every call gets content.
func (h *onceHarness) withLLMReply(t *testing.T, content string) *fakeLLM {
	t.Helper()
	return withLLM(t, h.srv, fixed(content))
}

// llmCount reads domestique_llm_requests_total for one job and result. The
// registry is process-wide, so tests compare before and after.
func llmCount(t *testing.T, job, result string) float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	api.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	prefix := `domestique_llm_requests_total{job="` + job + `",result="` + result + `"} `
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			v, err := strconv.ParseFloat(rest, 64)
			if err != nil {
				t.Fatalf("bad sample %q: %v", line, err)
			}
			return v
		}
	}
	return 0
}

// llmJobsView is the part of /api/me the LLM switches appear in.
type llmJobsView struct {
	NarrationEnabled bool `json:"narrationEnabled"`
	LLMJobs          struct {
		ExplainPlan    bool `json:"explainPlan"`
		ProposeProfile bool `json:"proposeProfile"`
		ProposeGoal    bool `json:"proposeGoal"`
		PlanEdits      bool `json:"planEdits"`
	} `json:"llmJobs"`
}

func (h *onceHarness) me(t *testing.T) llmJobsView {
	t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/me", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/me: status %d", resp.StatusCode)
	}
	var out llmJobsView
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// explainStatus, profileStatus, goalStatus and planEditStatus call each of the
// four AI-assisted endpoints once, as the owner, and return the status.
func (h *onceHarness) explainStatus(t *testing.T) int {
	t.Helper()
	return h.as("wilant", "cyclists", http.MethodGet, "/api/training/goals/"+h.goal.ID+"/explain", "").StatusCode
}

func (h *onceHarness) profileStatus(t *testing.T, note string) int {
	t.Helper()
	return h.as("wilant", "cyclists", http.MethodPost, "/api/training/profile/propose", fmt.Sprintf(`{"note":%q}`, note)).StatusCode
}

func (h *onceHarness) goalStatus(t *testing.T, note string) int {
	t.Helper()
	return h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals/propose", fmt.Sprintf(`{"note":%q}`, note)).StatusCode
}

// TestASwitchedOffJobIs412 is review focus 3: one job off in config turns only
// that job's endpoint off, and /api/me says so.
func TestASwitchedOffJobIs412(t *testing.T) {
	h := newOnceHarness(t)
	withLLMJobs(t, h.srv, map[string]bool{"plan_edits": false}, func(job string, _ []byte) string {
		if job == "explain_plan" {
			return `{"text":"Base phase first."}`
		}
		return intents()
	})

	if status, _ := h.propose(t, "wilant", "x"); status != http.StatusPreconditionFailed {
		t.Errorf("plan edit with the job off: status %d, want 412", status)
	}
	if status := h.explainStatus(t); status != http.StatusOK {
		t.Errorf("explain with only plan_edits off: status %d, want 200", status)
	}
	me := h.me(t)
	if me.LLMJobs.PlanEdits || !me.LLMJobs.ExplainPlan || !me.LLMJobs.ProposeProfile || !me.LLMJobs.ProposeGoal {
		t.Errorf("llmJobs = %+v, want only planEdits false", me.LLMJobs)
	}
	if !me.NarrationEnabled {
		t.Error("narrationEnabled is false while three jobs are on")
	}
}

// TestNoLLMConfigTurnsEverythingOff is the default deployment: no llm.base_url.
func TestNoLLMConfigTurnsEverythingOff(t *testing.T) {
	h := newOnceHarness(t)
	if status, _ := h.propose(t, "wilant", "x"); status != http.StatusPreconditionFailed {
		t.Errorf("plan edit: status %d, want 412", status)
	}
	if status := h.explainStatus(t); status != http.StatusPreconditionFailed {
		t.Errorf("explain: status %d, want 412", status)
	}
	if status := h.profileStatus(t, "traveling next week"); status != http.StatusPreconditionFailed {
		t.Errorf("profile proposal: status %d, want 412", status)
	}
	if status := h.goalStatus(t, "gran fondo in june"); status != http.StatusPreconditionFailed {
		t.Errorf("goal proposal: status %d, want 412", status)
	}
	me := h.me(t)
	if me.NarrationEnabled || me.LLMJobs.ExplainPlan || me.LLMJobs.ProposeProfile || me.LLMJobs.ProposeGoal || me.LLMJobs.PlanEdits {
		t.Errorf("/api/me = %+v, want every AI feature off", me)
	}
}

// TestLLMMetricCountsResults: one answer that passes and one that never does
// (prose, retried, then invalid) each show up once under their result label.
func TestLLMMetricCountsResults(t *testing.T) {
	h := newOnceHarness(t)
	f := withLLM(t, h.srv, fixed(`{"text":"Base phase first."}`))
	okBefore := llmCount(t, "explain_plan", "ok")
	invalidBefore := llmCount(t, "explain_plan", "invalid")

	if status := h.explainStatus(t); status != http.StatusOK {
		t.Fatalf("valid answer: status %d", status)
	}
	f.reply = fixed("Sure! Here is the plan.")
	if status := h.explainStatus(t); status != http.StatusBadGateway {
		t.Fatalf("invalid answer: status %d, want 502", status)
	}
	if got := f.calls.Load(); got != 3 {
		t.Errorf("%d model calls, want 3 (one answer, then two for the invalid one)", got)
	}
	if d := llmCount(t, "explain_plan", "ok") - okBefore; d != 1 {
		t.Errorf("result=ok moved by %v, want 1", d)
	}
	if d := llmCount(t, "explain_plan", "invalid") - invalidBefore; d != 1 {
		t.Errorf("result=invalid moved by %v, want 1", d)
	}
}

// TestNoPromptOrNoteInLogs is review focus on privacy: the rider's sentence and
// the model's words reach neither the logs nor the metrics, on success or on a
// failed call whose error body quotes the request.
func TestNoPromptOrNoteInLogs(t *testing.T) {
	const secretNote = "SECRET-NOTE-ZEPHYR"
	const secretReply = "SECRET-REPLY-QUASAR"
	h := newOnceHarness(t)
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f := withLLM(t, h.srv, func(job string, _ []byte) string {
		switch job {
		case "explain_plan":
			return `{"text":"` + secretReply + ` Base phase first."}`
		case "propose_profile":
			return `{"availableDays":["sat"],"hoursPerAvailableDay":2,"explanation":"` + secretReply + `"}`
		case "propose_goal":
			return `{"name":"Gran Fondo","sport":"cycling","eventDate":"","priority":"B","targetDistanceKm":0,"targetElevationM":0,"explanation":"` + secretReply + `"}`
		default:
			return `{"intents":[],"unsupported":"` + secretReply + `"}`
		}
	})

	if status := h.explainStatus(t); status != http.StatusOK {
		t.Errorf("explain: status %d", status)
	}
	if status := h.profileStatus(t, secretNote); status != http.StatusOK {
		t.Errorf("profile: status %d", status)
	}
	if status := h.goalStatus(t, secretNote); status != http.StatusOK {
		t.Errorf("goal: status %d", status)
	}
	if status, _ := h.propose(t, "wilant", secretNote); status != http.StatusOK {
		t.Errorf("plan edit: status %d", status)
	}
	f.code = http.StatusInternalServerError
	if status, _ := h.propose(t, "wilant", secretNote); status != http.StatusBadGateway {
		t.Errorf("plan edit against a failing endpoint: status %d, want 502", status)
	}

	rec := httptest.NewRecorder()
	api.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for name, out := range map[string]string{"logs": logs.String(), "metrics": rec.Body.String()} {
		for _, secret := range []string{secretNote, secretReply} {
			if strings.Contains(out, secret) {
				t.Errorf("%s contain %q:\n%s", name, secret, out)
			}
		}
	}
}

// A model call made while the rider waits gives up before Cloudflare's 100 s
// limit, so the rider gets the app's own answer, not a 524 page.
func TestARequestModelCallStopsAtTheRequestBudget(t *testing.T) {
	h := newOnceHarness(t)
	t.Cleanup(api.SetLLMRequestBudgetForTest(300 * time.Millisecond))
	withLLM(t, h.srv, func(string, []byte) string {
		time.Sleep(2 * time.Second)
		return `{"text":"too late"}`
	})

	start := time.Now()
	status := h.explainStatus(t)
	if status != http.StatusBadGateway {
		t.Errorf("explain past the budget: status %d, want 502", status)
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Errorf("explain took %v, want it to stop at the 300ms budget", took)
	}
}
