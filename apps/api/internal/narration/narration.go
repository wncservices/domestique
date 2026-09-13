// Package narration is Phase E of docs/training-plan.md: an LLM layer on
// top of the deterministic engine (internal/periodization,
// internal/adapter), never a replacement for it. Two narrow jobs, both
// read-only with respect to anything stored:
//
//   - ExplainPlan turns a periodized plan into a few sentences of plain
//     language — "why did this week ease off" — for a rider who does not
//     want to read a table of phases and hours.
//   - ProposeProfileChange turns a free-text note ("I'm traveling next
//     week") into a suggested edit to exactly the two fields a plan reads
//     to size a week: available days and hours per available day.
//
// Neither method writes anything. A narration is displayed; a proposal is
// handed back to the rider's own profile form for them to review and save
// themselves — the same "AI proposes, rider can still hand-edit"
// relationship this codebase's route library already has between
// auto-import and manual upload, and the same "a manual save always wins"
// rule internal/fitnesstest's own FTP estimate follows. Nothing here is
// allowed to invent a schedule or touch a stored profile on its own.
//
// This is a single POST and a JSON-decode against Anthropic's Messages
// API — the same "genuinely hard, budget-worthy dependency" bar
// AGENTS.md's Conventions section holds FIT encoding and OIDC to is not
// cleared here, so this is a hand-rolled client over net/http, the same
// choice already made for the OIDC token exchange
// ("golang.org/x/oauth2 appears in go.sum only as go-oidc's own indirect
// dependency... hand-rolled rather than pulling in a second direct
// dependency for it") rather than a new SDK dependency for one endpoint.
package narration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// DefaultAPIBase is Anthropic's own Messages API. Not operator-configurable
// the way elevation.DefaultURL is — there is only one Anthropic API to
// call — but still a field on Client, not a constant, so a test can point
// it at a fake server the same way wahoo.Client.APIBase already does.
const DefaultAPIBase = "https://api.anthropic.com"

const anthropicVersion = "2023-06-01"

// model is fixed rather than operator-configurable: this package asks for
// short, well-specified text (an explanation, or a small JSON object), not
// open-ended agentic work, so there is no case here that would want a
// different model per deployment.
const model = "claude-opus-5"

const requestTimeout = 30 * time.Second

// Client calls Anthropic's Messages API. A nil *Client (the zero value of
// Server.Narration) means the deployment has no ANTHROPIC_API_KEY
// configured — every handler checks for nil before reaching for one, the
// same "quietly unavailable" shape Wahoo/Garmin/Komoot already use for
// their own optional credentials.
type Client struct {
	apiKey     string
	APIBase    string
	httpClient *http.Client
}

// New builds a Client. apiKey must be non-empty — the caller (main.go's
// runServe) only constructs one when ANTHROPIC_API_KEY is actually set,
// the same pattern wahoo.New/komoot sign-in already follow.
func New(apiKey string) *Client {
	return &Client{
		apiKey:  apiKey,
		APIBase: DefaultAPIBase,
		httpClient: &http.Client{
			Timeout:   requestTimeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

// ExplainPlan describes goal's periodized plan in a few plain-language
// sentences. Read-only: plan and goal are only ever read, never written
// back anywhere.
func (c *Client) ExplainPlan(ctx context.Context, goal workout.Goal, plan periodization.Plan) (string, error) {
	return c.complete(ctx, explainPlanPrompt(goal, plan), 500)
}

// ProfileChangeProposal is a suggested edit to exactly the two fields a
// periodization plan reads to size a week. Never applied automatically —
// see this package's own doc comment.
type ProfileChangeProposal struct {
	AvailableDays        []string `json:"availableDays,omitempty"`
	HoursPerAvailableDay float64  `json:"hoursPerAvailableDay,omitempty"`
	// Explanation is the model's own one-line reasoning for the proposed
	// values, shown to the rider alongside them.
	Explanation string `json:"explanation,omitempty"`
}

// ProposeProfileChange turns a free-text note into a ProfileChangeProposal,
// falling back to profile's own current values for anything the note does
// not clearly imply changing — see parseProfileProposal.
func (c *Client) ProposeProfileChange(ctx context.Context, note string, profile workout.RiderProfile) (ProfileChangeProposal, error) {
	text, err := c.complete(ctx, proposeProfilePrompt(note, profile), 300)
	if err != nil {
		return ProfileChangeProposal{}, err
	}
	return parseProfileProposal(text, profile)
}

type messageRequest struct {
	Model     string           `json:"model"`
	MaxTokens int              `json:"max_tokens"`
	Messages  []messageContent `json:"messages"`
}

type messageContent struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type messageResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// complete sends one user-turn request and returns the concatenated text
// of every text block in the reply.
func (c *Client) complete(ctx context.Context, prompt string, maxTokens int) (string, error) {
	body, err := json.Marshal(messageRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  []messageContent{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.APIBase+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Capped the same way elevation.Client caps an error body: an
		// operator-configured (here, Anthropic's own) endpoint is still a
		// third party from this process's point of view.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("narration: anthropic api returned %s: %s", resp.Status, strings.TrimSpace(string(detail)))
	}

	var out messageResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("narration: decode response: %w", err)
	}

	var text strings.Builder
	for _, block := range out.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	if text.Len() == 0 {
		return "", errors.New("narration: model returned no text")
	}
	return strings.TrimSpace(text.String()), nil
}

// explainPlanPrompt summarizes plan by phase (date range, week count,
// recovery weeks) rather than listing every week — a plan can run 40+
// weeks, and a rider wants three sentences, not a rehash of the table
// they can already see.
func explainPlanPrompt(goal workout.Goal, plan periodization.Plan) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "A rider is training for %q", goal.Name)
	if goal.EventDate != "" {
		fmt.Fprintf(&sb, ", on %s", goal.EventDate)
	}
	sb.WriteString(".\n\n")

	if len(plan.Weeks) == 0 {
		sb.WriteString("The plan has no weeks yet — say so briefly and encourage them to set an event date.")
		return sb.String()
	}

	current := plan.Weeks[0]
	fmt.Fprintf(&sb, "This week (starting %s) is in the %s phase", current.StartDate, current.Phase)
	if current.Recovery {
		sb.WriteString(" (a deliberate recovery week)")
	}
	fmt.Fprintf(&sb, ", targeting %.1f hours.\n\n", current.TargetHours)

	sb.WriteString("The plan's phases:\n")
	for _, ph := range summarizePhases(plan.Weeks) {
		fmt.Fprintf(&sb, "- %s: %s to %s (%d weeks", ph.phase, ph.start, ph.end, ph.weeks)
		if ph.recoveries > 0 {
			fmt.Fprintf(&sb, ", %d recovery", ph.recoveries)
		}
		sb.WriteString(")\n")
	}

	if plan.Adjustment != 0 && plan.Adjustment != 1 {
		fmt.Fprintf(&sb, "\nUpcoming weeks were scaled to %.0f%% of the original plan based on how much the rider actually trained over the last few weeks.\n", plan.Adjustment*100)
	}

	sb.WriteString("\nIn 3-4 short sentences, explain this plan to the rider in plain language: why it is shaped this way, and call out anything worth knowing right now (an upcoming recovery week, or the adjustment above, if there is one). Do not invent any numbers beyond what is given above. Keep it warm and encouraging, not clinical.")
	return sb.String()
}

type phaseSummary struct {
	phase             periodization.Phase
	start, end        string
	weeks, recoveries int
}

func summarizePhases(weeks []periodization.Week) []phaseSummary {
	var out []phaseSummary
	for _, w := range weeks {
		if len(out) == 0 || out[len(out)-1].phase != w.Phase {
			out = append(out, phaseSummary{phase: w.Phase, start: w.StartDate})
		}
		last := &out[len(out)-1]
		last.end = w.StartDate
		last.weeks++
		if w.Recovery {
			last.recoveries++
		}
	}
	return out
}

func proposeProfilePrompt(note string, profile workout.RiderProfile) string {
	var sb strings.Builder
	sb.WriteString("A rider's current training-availability profile:\n")
	fmt.Fprintf(&sb, "- Available days: %s\n", strings.Join(profile.AvailableDays, ", "))
	fmt.Fprintf(&sb, "- Hours per available day: %.1f\n\n", profile.HoursPerAvailableDay)
	fmt.Fprintf(&sb, "They just wrote this note about an upcoming change: %q\n\n", note)
	sb.WriteString("Reply with ONLY a JSON object (no other text, no markdown fences) with these exact fields, changing only what the note actually implies and leaving anything unmentioned at its current value above:\n")
	sb.WriteString(`{"availableDays": ["mon","wed"], "hoursPerAvailableDay": 1.5, "explanation": "one short sentence"}`)
	sb.WriteString("\n\nDays must only be drawn from this set: mon, tue, wed, thu, fri, sat, sun. If the note does not clearly imply any change, return the current values unchanged and say so in explanation.")
	return sb.String()
}

var validDays = map[string]bool{"mon": true, "tue": true, "wed": true, "thu": true, "fri": true, "sat": true, "sun": true}

// maxSaneHoursPerDay bounds what a proposal may claim regardless of what
// the model returns — a rider training more than this in a single day is
// implausible enough to be a model error, not a real suggestion to hand
// back into the profile form.
const maxSaneHoursPerDay = 12.0

// parseProfileProposal decodes the model's JSON reply, defensively
// stripping markdown fences first (a "no other text" instruction in the
// prompt is not a guarantee), and falls back to profile's own current
// value for either field when the model's own value is missing or fails
// the same sanity bounds this package's callers would want checked before
// ever showing it to a rider.
func parseProfileProposal(text string, profile workout.RiderProfile) (ProfileChangeProposal, error) {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var raw struct {
		AvailableDays        []string `json:"availableDays"`
		HoursPerAvailableDay float64  `json:"hoursPerAvailableDay"`
		Explanation          string   `json:"explanation"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return ProfileChangeProposal{}, fmt.Errorf("narration: model did not return valid JSON: %w", err)
	}

	proposal := ProfileChangeProposal{Explanation: raw.Explanation}
	for _, d := range raw.AvailableDays {
		d = strings.ToLower(strings.TrimSpace(d))
		if validDays[d] {
			proposal.AvailableDays = append(proposal.AvailableDays, d)
		}
	}
	if len(proposal.AvailableDays) == 0 {
		proposal.AvailableDays = profile.AvailableDays
	}

	if raw.HoursPerAvailableDay > 0 && raw.HoursPerAvailableDay <= maxSaneHoursPerDay {
		proposal.HoursPerAvailableDay = raw.HoursPerAvailableDay
	} else {
		proposal.HoursPerAvailableDay = profile.HoursPerAvailableDay
	}
	return proposal, nil
}
