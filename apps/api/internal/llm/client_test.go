package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/wncservices/domestique/apps/api/internal/config"
)

// reply wraps content the way an OpenAI-compatible server answers a chat call.
func reply(t *testing.T, content string) string {
	t.Helper()
	quoted, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	return `{"choices":[{"message":{"content":` + string(quoted) + `}}]}`
}

// fake is a stand-in endpoint. handle sees the zero-based request index and
// the decoded request, and writes the answer.
type fake struct {
	srv      *httptest.Server
	requests atomic.Int32

	mu      sync.Mutex
	path    string
	auth    string
	origin  string
	body    map[string]any
	headers http.Header
}

func newFake(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, n int)) *fake {
	t.Helper()
	f := &fake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(f.requests.Add(1)) - 1
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.path = r.URL.Path
		f.auth = r.Header.Get("Authorization")
		f.origin = r.Header.Get("Origin")
		f.body = body
		f.headers = r.Header.Clone()
		f.mu.Unlock()
		handle(w, r, n)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// answer returns a handler that always replies with content.
func answer(t *testing.T, content string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = io.WriteString(w, reply(t, content))
	}
}

type recorder struct {
	mu      sync.Mutex
	results []string
}

func (r *recorder) set(c *Client) {
	c.SetOnResult(func(_ Job, result string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.results = append(r.results, result)
	})
}

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.results...)
}

type textOut struct {
	Text string `json:"text"`
}

var testSchema = map[string]any{
	"type":       "object",
	"properties": map[string]any{"text": map[string]any{"type": "string"}},
	"required":   []string{"text"},
}

func newClient(t *testing.T, baseURL, apiKey string) *Client {
	t.Helper()
	c := New(config.LLMConfig{BaseURL: baseURL, Model: "qwen-test"}, apiKey)
	if c == nil {
		t.Fatal("New returned nil for a configured base_url")
	}
	return c
}

func TestRequestShape(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"hi"}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	var out textOut
	if err := c.Complete(t.Context(), JobExplainPlan, "SYS", "USR", testSchema, 200, &out, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.path != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", f.path)
	}
	if f.body["model"] != "qwen-test" {
		t.Errorf("model = %v", f.body["model"])
	}
	if f.body["temperature"] != 0.2 {
		t.Errorf("temperature = %v, want 0.2", f.body["temperature"])
	}
	if f.body["max_tokens"] != float64(200) {
		t.Errorf("max_tokens = %v, want 200", f.body["max_tokens"])
	}
	rf, _ := f.body["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Errorf("response_format.type = %v", rf["type"])
	}
	js, _ := rf["json_schema"].(map[string]any)
	if js["name"] != string(JobExplainPlan) || js["strict"] != true {
		t.Errorf("json_schema = %v", js)
	}
	if _, ok := js["schema"].(map[string]any); !ok {
		t.Errorf("json_schema.schema missing: %v", js)
	}
	msgs, _ := f.body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v, want 2", msgs)
	}
	first, _ := msgs[0].(map[string]any)
	second, _ := msgs[1].(map[string]any)
	if first["role"] != "system" || first["content"] != "SYS" {
		t.Errorf("first message = %v, want system SYS", first)
	}
	if second["role"] != "user" || second["content"] != "USR" {
		t.Errorf("second message = %v, want user USR", second)
	}
	if f.origin != "" {
		t.Errorf("Origin header sent: %q", f.origin)
	}
	if f.auth != "" {
		t.Errorf("Authorization sent with no key: %q", f.auth)
	}
}

func TestBearerKeyIsSentWhenSet(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"hi"}`))
	c := newClient(t, f.srv.URL, "k")
	var out textOut
	if err := c.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &out, nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "Bearer k" {
		t.Errorf("Authorization = %q, want Bearer k", f.auth)
	}
}

func TestDecodesIntoOut(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"hi"}`))
	c := newClient(t, f.srv.URL, "")
	var rec recorder
	rec.set(c)

	var out textOut
	if err := c.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &out, nil); err != nil {
		t.Fatal(err)
	}
	if out.Text != "hi" {
		t.Errorf("out.Text = %q, want hi", out.Text)
	}
	if got := rec.got(); len(got) != 1 || got[0] != "ok" {
		t.Errorf("results = %v, want [ok]", got)
	}
}

func TestNonJSONReplyRetriesThenInvalid(t *testing.T) {
	f := newFake(t, answer(t, "Sure! ```json\n{\"text\":\"hi\"}\n```"))
	c := newClient(t, f.srv.URL, "")
	var rec recorder
	rec.set(c)

	var out textOut
	err := c.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &out, nil)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2 (one retry)", n)
	}
	if got := rec.got(); len(got) != 1 || got[0] != "invalid" {
		t.Errorf("results = %v, want [invalid]", got)
	}
}

func TestValidatorRejectionRetriesOnce(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		content := `{"text":"bad"}`
		if n > 0 {
			content = `{"text":"good"}`
		}
		_, _ = io.WriteString(w, reply(t, content))
	})
	c := newClient(t, f.srv.URL, "")
	var rec recorder
	rec.set(c)

	var out textOut
	validate := func() error {
		if out.Text != "good" {
			return errors.New("not good")
		}
		return nil
	}
	if err := c.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &out, validate); err != nil {
		t.Fatalf("err = %v, want nil after the retry", err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
	if got := rec.got(); len(got) != 1 || got[0] != "ok" {
		t.Errorf("results = %v, want [ok]", got)
	}
}

func TestUnknownFieldIsInvalid(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"a","extra":1}`))
	c := newClient(t, f.srv.URL, "")
	var rec recorder
	rec.set(c)

	var out textOut
	err := c.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &out, nil)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if got := rec.got(); len(got) != 1 || got[0] != "invalid" {
		t.Errorf("results = %v, want [invalid]", got)
	}
}

func TestNon200EchoesNoBody(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "SECRET-BODY")
	})
	c := newClient(t, f.srv.URL, "")
	var rec recorder
	rec.set(c)

	var out textOut
	err := c.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &out, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "SECRET-BODY") {
		t.Errorf("error echoes the body: %v", err)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error does not name the status: %v", err)
	}
	if n := f.requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1 (a refused call is not retried)", n)
	}
	if got := rec.got(); len(got) != 1 || got[0] != "error" {
		t.Errorf("results = %v, want [error]", got)
	}
}

func TestTransportErrorIsNotAnInvalid(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"hi"}`))
	base := f.srv.URL
	f.srv.Close()
	c := newClient(t, base, "s3cret")
	var rec recorder
	rec.set(c)

	var out textOut
	err := c.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &out, nil)
	if err == nil || errors.Is(err, ErrInvalid) || errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want a plain transport error", err)
	}
	if strings.Contains(err.Error(), base) || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaks the URL or key: %v", err)
	}
	if got := rec.got(); len(got) != 1 || got[0] != "error" {
		t.Errorf("results = %v, want [error]", got)
	}
}

func TestTimeoutCoversBothAttempts(t *testing.T) {
	t.Run("slow first answer", func(t *testing.T) {
		f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ int) {
			select {
			case <-time.After(300 * time.Millisecond):
				_, _ = io.WriteString(w, reply(t, `{"text":"late"}`))
			case <-r.Context().Done():
			}
		})
		c := newClient(t, f.srv.URL, "")
		c.timeout = 200 * time.Millisecond
		var rec recorder
		rec.set(c)

		var out textOut
		err := c.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &out, nil)
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("err = %v, want ErrTimeout", err)
		}
		if n := f.requests.Load(); n != 1 {
			t.Errorf("requests = %d, want 1", n)
		}
		if got := rec.got(); len(got) != 1 || got[0] != "timeout" {
			t.Errorf("results = %v, want [timeout]", got)
		}
	})

	t.Run("invalid first answer uses the shared budget", func(t *testing.T) {
		f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ int) {
			select {
			case <-time.After(150 * time.Millisecond):
				_, _ = io.WriteString(w, reply(t, "not json"))
			case <-r.Context().Done():
			}
		})
		c := newClient(t, f.srv.URL, "")
		c.timeout = 250 * time.Millisecond
		var rec recorder
		rec.set(c)

		var out textOut
		err := c.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &out, nil)
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("err = %v, want ErrTimeout", err)
		}
		if n := f.requests.Load(); n > 2 {
			t.Errorf("requests = %d, want at most 2", n)
		}
		if got := rec.got(); len(got) != 1 || got[0] != "timeout" {
			t.Errorf("results = %v, want [timeout]", got)
		}
	})
}

func TestNilClientIsDisabled(t *testing.T) {
	if c := New(config.LLMConfig{}, ""); c != nil {
		t.Fatal("New with no base_url should return nil")
	}
	var c *Client
	if c.Enabled(JobExplainPlan) {
		t.Error("nil client reports a job enabled")
	}
}

func TestJobsDefaultOnAndSwitchOff(t *testing.T) {
	c := New(config.LLMConfig{
		BaseURL: "http://llm.example/v1",
		Model:   "m",
		Jobs:    map[string]bool{"plan_edits": false},
	}, "")
	if c.Enabled(JobPlanEdits) {
		t.Error("plan_edits should be off")
	}
	for _, j := range []Job{JobExplainPlan, JobProposeProfile, JobProposeGoal} {
		if !c.Enabled(j) {
			t.Errorf("%s should default to on", j)
		}
	}
}

func TestTransportIsOutbound(t *testing.T) {
	c := newClient(t, "http://llm.example/v1", "")
	if c.httpClient.Transport == http.DefaultTransport {
		t.Fatal("the transport is the bare default; it must be wrapped in telemetry.OutboundTransport")
	}
}

// The three text and check-in jobs must be switchable from config like the rest.
func TestNewJobsAreInTheJobListAndConfig(t *testing.T) {
	want := []Job{JobExplainChanges, JobUnderstandCheckIn, JobWeeklySummary}
	for _, j := range want {
		if !slices.Contains(Jobs, j) || !slices.Contains(config.LLMJobNames, string(j)) {
			t.Errorf("%q is missing from llm.Jobs or config.LLMJobNames", j)
		}
	}
}

func TestJobListMatchesConfig(t *testing.T) {
	if len(Jobs) != len(config.LLMJobNames) {
		t.Fatalf("llm.Jobs has %d entries, config.LLMJobNames has %d", len(Jobs), len(config.LLMJobNames))
	}
	for i, j := range Jobs {
		if string(j) != config.LLMJobNames[i] {
			t.Errorf("Jobs[%d] = %q, config says %q", i, j, config.LLMJobNames[i])
		}
	}
}

// A reply that is valid JSON but not an object must not count as an answer:
// json.Decode accepts null into a struct without error, so it has to be refused
// before decoding.
func TestNullReplyIsInvalid(t *testing.T) {
	for _, content := range []string{"null", "[]", `"x"`} {
		t.Run(content, func(t *testing.T) {
			f := newFake(t, answer(t, content))
			c := newClient(t, f.srv.URL, "")
			var rec recorder
			rec.set(c)

			var out textOut
			err := c.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &out, nil)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if n := f.requests.Load(); n != 2 {
				t.Errorf("requests = %d, want 2 (one retry)", n)
			}
			if got := rec.got(); len(got) != 1 || got[0] != "invalid" {
				t.Errorf("results = %v, want [invalid]", got)
			}
		})
	}
}

// A failed first attempt must not leave fields behind in out: the second
// attempt's {} has no text, so out.Text has to be empty, and on ErrInvalid out
// has to be zero.
func TestRetryDoesNotKeepStaleFields(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, n int) {
		content := `{"text":"a","extra":1}`
		if n > 0 {
			content = `{}`
		}
		_, _ = io.WriteString(w, reply(t, content))
	})
	c := newClient(t, f.srv.URL, "")

	var out textOut
	if err := c.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &out, nil); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if out.Text != "" {
		t.Errorf("out.Text = %q, want empty: a failed attempt left a field behind", out.Text)
	}

	g := newFake(t, answer(t, `{"text":"a","extra":1}`))
	d := newClient(t, g.srv.URL, "")
	var stale textOut
	if err := d.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &stale, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if stale.Text != "" {
		t.Errorf("out.Text = %q after ErrInvalid, want zero", stale.Text)
	}
}

// The caller's span is tagged with the job and nothing else from the call: the
// system and user text carry the rider's words and must never reach a span.
func TestCompleteTagsTheCallersSpan(t *testing.T) {
	const systemText = "SYSTEM-SECRET-OSPREY"
	const userText = "USER-SECRET-HERON"
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	ctx, span := tp.Tracer("test").Start(context.Background(), "caller")

	f := newFake(t, answer(t, `{"text":"ok"}`))
	c := newClient(t, f.srv.URL, "")
	var out struct {
		Text string `json:"text"`
	}
	schema := map[string]any{"type": "object"}
	if err := c.Complete(ctx, JobExplainPlan, systemText, userText, schema, 200, &out, nil); err != nil {
		t.Fatal(err)
	}
	span.End()

	// The outbound HTTP client span is recorded too; only the caller's matters.
	var caller tracetest.SpanStub
	for _, sp := range exp.GetSpans() {
		if sp.Name == "caller" {
			caller = sp
		}
	}
	if caller.Name == "" {
		t.Fatal("the caller's span was not recorded")
	}
	var job string
	for _, kv := range caller.Attributes {
		if kv.Key == "llm.job" {
			job = kv.Value.AsString()
		}
		for _, secret := range []string{systemText, userText} {
			if strings.Contains(kv.Value.String(), secret) {
				t.Errorf("span attribute %s carries the prompt text", kv.Key)
			}
		}
	}
	if job != string(JobExplainPlan) {
		t.Errorf("llm.job = %q, want %q", job, JobExplainPlan)
	}
}

// A 503 is how a cold model load answers while the model is still loading.
// It is retried after a pause, inside the one budget, and it does not spend
// the invalid-answer retry.
func TestColdLoad503IsRetriedWithinTheBudget(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, n int) {
		if n < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, reply(t, `{"text":"warm"}`))
	})
	c := newClient(t, f.srv.URL, "")
	c.retryPause = time.Millisecond
	var rec recorder
	rec.set(c)

	var out textOut
	if err := c.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &out, nil); err != nil {
		t.Fatalf("err = %v, want nil once the model is warm", err)
	}
	if out.Text != "warm" {
		t.Errorf("out.Text = %q, want warm", out.Text)
	}
	if n := f.requests.Load(); n != 3 {
		t.Errorf("requests = %d, want 3 (two 503s then the answer)", n)
	}
	if got := rec.got(); len(got) != 1 || got[0] != "ok" {
		t.Errorf("results = %v, want [ok]", got)
	}
}

func TestA503DoesNotSpendTheInvalidRetry(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, n int) {
		switch n {
		case 0:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 1:
			_, _ = io.WriteString(w, reply(t, "not json"))
		default:
			_, _ = io.WriteString(w, reply(t, `{"text":"fine"}`))
		}
	})
	c := newClient(t, f.srv.URL, "")
	c.retryPause = time.Millisecond

	var out textOut
	if err := c.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &out, nil); err != nil {
		t.Fatalf("err = %v, want nil: the invalid answer's retry must still be available", err)
	}
	if n := f.requests.Load(); n != 3 {
		t.Errorf("requests = %d, want 3", n)
	}
}

func TestPersistent503EndsInTimeout(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c := newClient(t, f.srv.URL, "")
	c.timeout = 200 * time.Millisecond
	c.retryPause = 10 * time.Millisecond
	var rec recorder
	rec.set(c)

	var out textOut
	err := c.Complete(t.Context(), JobExplainPlan, "s", "u", testSchema, 200, &out, nil)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if n := f.requests.Load(); n < 2 {
		t.Errorf("requests = %d, want the 503 retried until the budget ran out", n)
	}
	if got := rec.got(); len(got) != 1 || got[0] != "timeout" {
		t.Errorf("results = %v, want [timeout]", got)
	}
}

// A caller that goes away is not a model failure: the result hook, which feeds
// the llm_requests metric, must not count it.
func TestCallerCancelIsNotCounted(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		cancel()
		<-r.Context().Done()
	})
	c := newClient(t, f.srv.URL, "")
	var rec recorder
	rec.set(c)

	var out textOut
	err := c.Complete(ctx, JobExplainPlan, "s", "u", testSchema, 200, &out, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := rec.got(); len(got) != 0 {
		t.Errorf("results = %v, want none for a cancelled caller", got)
	}
}
