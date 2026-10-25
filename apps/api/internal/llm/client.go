// Package llm is the client for the self-hosted model behind the AI-assisted
// features: any OpenAI-compatible chat endpoint (vLLM, llama.cpp, Ollama and
// the like) reached at llm.base_url.
//
// Every answer is a JSON object that must match a schema the caller supplies.
// The schema is sent with the request so a server that honours response_format
// constrains the output; the decode below is the check for one that does not.
// Unknown fields are refused, and a caller's validate hook can reject a value
// the schema cannot express. One bad answer earns one retry; after that the
// caller gets ErrInvalid and must not use anything it half parsed.
//
// Privacy: prompts and answers carry a rider's own words and numbers. Nothing
// here logs them or puts them in a span, and errors never echo the endpoint's
// reply body, since a gateway's error page can quote the request.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/telemetry"
)

// Job names one AI-assisted feature. Its string is the schema name sent to the
// endpoint and the value config.LLMConfig.Jobs switches by.
type Job string

const (
	JobExplainPlan    Job = "explain_plan"
	JobProposeProfile Job = "propose_profile"
	JobProposeGoal    Job = "propose_goal"
	JobPlanEdits      Job = "plan_edits"
)

// Jobs is every job, in config order. A test keeps it equal to
// config.LLMJobNames.
var Jobs = []Job{JobExplainPlan, JobProposeProfile, JobProposeGoal, JobPlanEdits}

// ErrInvalid means the model answered twice without a usable object.
var ErrInvalid = errors.New("llm: the model's answer did not pass validation")

// ErrTimeout means the call, both attempts included, ran out of time.
var ErrTimeout = errors.New("llm: the model did not answer in time")

const (
	// defaultTimeout covers both attempts together. A cold model load after
	// an idle unload can take about a minute, so the budget is generous, but
	// it is one budget: a slow first answer leaves less time for the retry,
	// and no call ever runs past it.
	defaultTimeout = 120 * time.Second

	temperature = 0.2

	// maxBodyBytes bounds a reply. A structured answer is a few hundred
	// bytes; this only stops a runaway server.
	maxBodyBytes = 1 << 20

	attempts = 2
)

// Client calls the endpoint. A nil *Client means the features are off; every
// method is safe on nil.
type Client struct {
	baseURL    string
	model      string
	apiKey     string // never logged, never in an error
	jobs       map[Job]bool
	httpClient *http.Client
	timeout    time.Duration
	onResult   func(job Job, result string)
}

// New builds a Client from the llm block. It returns nil when base_url is
// empty, so the caller's Enabled checks turn every job off without a special
// case. apiKey may be empty.
func New(cfg config.LLMConfig, apiKey string) *Client {
	if cfg.BaseURL == "" {
		return nil
	}
	jobs := make(map[Job]bool, len(cfg.Jobs))
	for name, on := range cfg.Jobs {
		jobs[Job(name)] = on
	}
	return &Client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		model:   cfg.Model,
		apiKey:  apiKey,
		jobs:    jobs,
		httpClient: &http.Client{
			Transport: telemetry.OutboundTransport(http.DefaultTransport),
		},
		timeout: defaultTimeout,
	}
}

// Enabled reports whether a job may be called. Every job is on unless config
// switches it off.
func (c *Client) Enabled(j Job) bool {
	if c == nil {
		return false
	}
	on, set := c.jobs[j]
	return !set || on
}

// SetOnResult registers a hook called once per Complete with the job and one
// of "ok", "invalid", "error" or "timeout". Call it before the client is in
// use; it is not synchronised.
func (c *Client) SetOnResult(f func(job Job, result string)) {
	if c == nil {
		return
	}
	c.onResult = f
}

// Complete asks the model for one answer to system and user, constrained to
// schema, and decodes it into out. It makes at most two attempts: a reply that
// does not decode strictly into out, or that validate rejects, is retried once.
// validate may be nil.
//
// Errors: ErrInvalid after two bad answers, ErrTimeout when the budget runs out,
// and a plain error for a refused or unreachable endpoint.
func (c *Client) Complete(ctx context.Context, j Job, system, user string, schema map[string]any, maxTokens int, out any, validate func() error) error {
	result := "error"
	defer func() {
		if c != nil && c.onResult != nil {
			c.onResult(j, result)
		}
	}()
	if c == nil {
		return errors.New("llm: not configured")
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	body, err := json.Marshal(chatRequest{
		Model: c.model,
		Messages: []message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: temperature,
		MaxTokens:   maxTokens,
		ResponseFormat: responseFormat{
			Type: "json_schema",
			JSONSchema: jsonSchema{
				Name:   string(j),
				Strict: true,
				Schema: schema,
			},
		},
	})
	if err != nil {
		return errors.New("llm: encode request failed")
	}

	for range attempts {
		content, err := c.send(ctx, body)
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				result = "timeout"
				return ErrTimeout
			}
			return err
		}
		if err := decodeStrict(content, out); err != nil {
			continue
		}
		if validate != nil && validate() != nil {
			continue
		}
		result = "ok"
		return nil
	}
	result = "invalid"
	return ErrInvalid
}

// send makes one POST and returns the first choice's content.
func (c *Client) send(ctx context.Context, body []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", errors.New("llm: build request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// url.Error's text embeds the endpoint; keep only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return "", fmt.Errorf("llm: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm: endpoint returned %d", resp.StatusCode)
	}

	var out chatResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&out); err != nil {
		return "", errors.New("llm: decode response failed")
	}
	if len(out.Choices) == 0 {
		return "", nil // an empty answer fails decodeStrict and is retried
	}
	return out.Choices[0].Message.Content, nil
}

// decodeStrict decodes exactly one JSON object into out, refusing unknown
// fields and any trailing text. Prose or fenced JSON therefore fails here.
func decodeStrict(content string, out any) error {
	dec := json.NewDecoder(strings.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("llm: trailing content after the object")
	}
	return nil
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []message      `json:"messages"`
	Temperature    float64        `json:"temperature"`
	MaxTokens      int            `json:"max_tokens"`
	ResponseFormat responseFormat `json:"response_format"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type       string     `json:"type"`
	JSONSchema jsonSchema `json:"json_schema"`
}

type jsonSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}
