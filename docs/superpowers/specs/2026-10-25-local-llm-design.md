# Local LLM — design (sub-project 6a)

Status: approved 2026-10-25. 6a implemented 2026-10-25. Detailed design for section 6 of
`2026-10-18-adaptive-training-builder-design.md`, first half. 6a moves the existing
narration features onto the lab's own model. 6b (the morning-changes explanation,
check-in parsing and the weekly summary) is a separate spec.

## Why

The four narration features (plan explanation, goal proposal, profile proposal,
plan edits) call Anthropic. The roadmap decided AI is **local only**, and the lab
already serves a model: llama-server behind agentgateway's `llm:` listener, with
the `domestique` namespace allowed by its NetworkPolicy. 6a points Domestique at it
and removes the Anthropic client.

## Serving (the `lab` repo, a separate PR)

- `llm/values.yaml`:
  - `model.hfRepo: unsloth/Qwen3.5-9B-GGUF:Q4_K_M`;
  - `model.alias: qwen3.5-9b`;
  - memory request 6 Gi, limit 10 Gi.
  - Everything else is unchanged: `reasoning: off`, 2 × 16k context, and idle unload after 30 minutes.
- `agentgateway/values.yaml`: the `llm.models` entry becomes `qwen3.5-9b`, and the
  commented `clients` entries change with it. Home Assistant's model name changes too.
- **Why 9B.** gondlin has 31 GB. Over the last 7 days the lowest free memory was
  16.6 GB, and that was with the 4B model loaded (peak 4.4 GB). 35B-A3B at Q4_K_M
  (about 22–23 GB with KV) does not fit beside Postgres and Vault. 9B needs about
  7–8 GB and leaves roughly 13 GB spare at that peak. The 4B model peaked at about
  7 tokens/s, so 9B gives about 3. Benchmark with `llama-bench -t 4` on gondlin
  before merging.

## The client: `internal/llm`

- **The call.** It is hand-rolled and makes one request: `POST {base_url}/chat/completions`
  (OpenAI-compatible), then parses the JSON, which fits the dependency budget.
  - `response_format` is `{"type": "json_schema", "json_schema": {"name": <job>, "strict": true, "schema": …}}`;
    llama-server enforces it through a grammar.
  - Free-text jobs use a schema with one `text` string, so there is one code path
    and no fences to strip.
  - `temperature` is 0.2.
- **API.** `llm.Client.Complete(ctx, Job, system, user string, schema json.RawMessage,
  maxTokens int, out any) error`.
  - It unmarshals into `out` with `DisallowUnknownFields`.
  - Callers validate the result. A parse failure or a validator rejection triggers
    **one retry**, then returns `llm.ErrInvalid`.
- **Transport.**
  - `http.Client{Transport: telemetry.OutboundTransport(http.DefaultTransport)}`.
  - Every request goes through `http.NewRequestWithContext` with the caller's context.
  - Each call has a 120 s timeout (`context.WithTimeout`), which leaves room for a cold
    model load after an idle unload at ~3 tokens/s.
  - `*url.Error` is unwrapped (keep `.Err`). A non-200 returns the status only; the
    body is never echoed.
  - No `Origin` header is sent: the gateway refuses requests that carry one.
- **Jobs and token caps**: `explain_plan` 200, `propose_profile` 250,
  `propose_goal` 250, `plan_edits` 400.
- **Span.** The span is the otelhttp client span. One attribute, `llm.job`, is set on
  the caller's span. There is no prompt, output or model text.
- **Metric**: `domestique_llm_requests_total{job, result}` with result `ok`, `invalid`,
  `error` or `timeout`. It has no rider label.

## Configuration

- `domestique.yaml`:
  - `llm.base_url`: in the cluster,
    `http://agentgateway.agentgateway.svc.cluster.local:4000/v1`. Leaving it unset
    switches every LLM feature off.
  - `llm.model`: `qwen3.5-9b`.
  - `llm.jobs`: an optional map of job → bool. Every job defaults to true. A job set
    false is off, as if nothing were configured.
- `DOMESTIQUE_LLM_API_KEY` (environment only) is sent as `Authorization: Bearer` when
  set. The gateway's client keys are off today. Turning `LLM_KEY_DOMESTIQUE` on is then
  a Vault entry and an ExternalSecret in `domestique-infra`.
- `domestique validate` checks that `base_url` parses as http(s), that `model` is set
  whenever `base_url` is, and that every `jobs` key is a known job.
- `domestique.example.yaml` documents the block. The chart repo renders it (its own PR).

## What it replaces

- `internal/narration` is removed and `ANTHROPIC_API_KEY` is no longer read.
  - `main.go` builds `llm.New(cfg.LLM, os.Getenv("DOMESTIQUE_LLM_API_KEY"))` when
    `base_url` is set.
  - It logs `llm enabled` at Info (job names only), or `no llm.base_url configured` at Info.
- **The four handlers keep their contracts.** That covers their routes, request and
  response shapes, the existing server-side validators, the rate limiter, and these
  status codes:
  - 412 with a Warn when the feature is off;
  - 502 on a transport error or `ErrInvalid`;
  - 429 from the limiter.
  - The prompts and validators move into `internal/llm`, one file per job
    (`explain.go`, `profile.go`, `goal.go`, `planedits.go`).
  - Plan edits use the JSON schema built from `Capabilities` (today the tool's
    `input_schema`). The model replies with `{"intents": [...]}` or `{"refused": true}`.
- **Frontend.**
  - `/api/me` keeps `narrationEnabled` (true when any job is on).
  - It adds `llmJobs: {explainPlan, proposeProfile, proposeGoal, planEdits}`, and each
    button checks its own flag.
  - `PlanEditBox.vue`'s privacy line changes from "sent to Anthropic" to "sent to the
    app's own model, which runs on our server".
  - The loading copy on the four actions says it can take up to a minute.
  - DTOs change in `server.go` and `types.ts` together.

## Privacy

- **Logs.** Prompts, outputs and the plan-edit sentence are never logged. Logs carry
  the job and an outcome word.
- **Health data.** It may reach the model, which runs on gondlin and never leaves the
  cluster.
- **Unchanged.** The morning email does not change. No new data reaches a third party.

## Evaluation gate

- **The set.** `apps/api/internal/llm/testdata/eval/*.json` holds about 30 synthetic cases
  in English and Dutch, each with the expected validated result:
  - 10 profile notes;
  - 10 goal notes;
  - 10 plan-edit sentences, including 2 that must be refused.
- **The runner.** `go test -tags llmeval ./apps/api/internal/llm/ -run TestEval` runs
  against `DOMESTIQUE_LLM_EVAL_URL` (plus an optional key and model). It prints a pass
  rate per job and fails a job under 80 %.
  - It is never part of `just check` or CI, which cannot reach the model.
  - There is a `just llm-eval` recipe.
- **The gate.** Before the deployment's config enables a job, it must pass against
  `qwen3.5-9b`. The result is recorded in the PR description. A job that fails ships
  with `llm.jobs.<job>: false`.

## Testing

- **`internal/llm` unit tests.** An `httptest` fake OpenAI server covers:
  - the request shape (path, model, `response_format` schema, `max_tokens`, no `Origin`,
    Bearer only with a key);
  - one retry on invalid output, then `ErrInvalid`;
  - the timeout;
  - a non-200 with no body echoed;
  - the metric labels;
  - each job's validator (ported from the narration tests).
- **API tests.** The handler tests move from the fake Anthropic server to the fake
  OpenAI one, keeping their assertions:
  - 412 with no config, and with a job switched off;
  - the plan-edit privacy check on the real request body;
  - the no-note-in-logs scan;
  - the rate limit.
- **Main and config.**
  - `TestANTHROPICKeyIsNotRead`: setting the variable enables nothing.
  - `validate` cases for the `llm` block.
- **Web.** Typecheck passes, and each action is hidden when its job flag is false.

## Out of scope (6b)

The morning-changes explanation, check-in note parsing into life-event proposals, and the
weekly coaching summary.
