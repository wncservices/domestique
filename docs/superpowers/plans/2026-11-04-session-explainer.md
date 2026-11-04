# Session explainer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every planned session gets a "why it's in your plan" line and a "how to ride
it" explanation. The how-to is written once per session shape by the local model and
shared across riders.

**Architecture:**
- **Shape key.** A pure package turns a session into a canonical shape text. It holds
  no watts and no rider. The key is the text's sha256.
- **Model job.** An `explain_workout` job in `internal/llm` writes `{summary, blocks}`.
- **Shared cache.** A `workout_explanations` table, with no rider column, caches the
  explanations.
- **When explanations are written.** The morning notes pass pre-writes them. The API
  reads the cache, writes on a miss, or falls back to a fixed per-zone text.
- **UI.** The session panel and the Today/Plan session cards show it.

**Tech Stack:** Go stdlib, Vue 3 / Nuxt UI.

**Spec:** `docs/superpowers/specs/2026-10-26-explainer-and-today-design.md`, part 1.

## Global Constraints

- **Shape text.** One line per step, with repeat blocks nested.
  - Each line holds the kind (warmup/work/recovery/cooldown, from `Intensity`), the
    duration, and the target.
  - **Duration:** whole minutes, or seconds under 2 min, or metres for distance.
  - **Target:**
    - Power is a %FTP range rounded to 5 %, computed from the rider's FTP. With no
      FTP the line reads `power` with no number.
    - Heart rate and pace read `heart rate` / `pace` with no number.
    - Open reads `open`.
  - It also includes the sport and the session name, and it **never** holds watts,
    bpm, m/s, dates or the rider.
  - Key = `sha256(shape text)` as hex.
- **Job.** `explain_workout` returns `{summary: string, blocks: [{label, cue}]}`.
  - `summary` is 1–4 sentences, at most 400 runes. `blocks` has 1–8 entries, each
    `cue` at most 160 runes. `max_tokens` is 500.
  - No invented numbers: every number in the output must appear in the shape text.
  - The input is the shape text only.
- **Table.** `workout_explanations(key TEXT PRIMARY KEY, text TEXT NOT NULL,
  created_at TEXT NOT NULL)`.
  - `text` is the JSON of `{summary, blocks}`. An empty text is a claim and reads as
    absent.
  - The claim is released (row deleted) on a cancelled call or a failed save.
  - The table has no rider column and is **not** in `riderTables`. The registry test
    must stay green, which needs an explicit allow-entry, so check how
    `TestEveryRiderKeyedTableIsRegistered` treats a table without a rider column.
- **Pre-writing.** The morning notes pass writes explanations after the changes and
  weekly notes, at most 10 shapes per pass, for distinct shapes of planned sessions
  in the next 7 days across riders with a goal. It runs only with a model configured
  and `Enabled(JobExplainWorkout)`.
- **Endpoint.** `GET /api/training/workouts/{id}/explanation` returns
  `{why: string, summary: string, blocks: [{label, cue}], source: "model"|"template"}`.
  - Access is owner-only, with a 404 otherwise, the same as the neighbouring workout
    endpoints.
  - A cache miss is written within `llmRequestContext`, the 90 s cap.
- **Template fallback.** A fixed text per zone, written in code: endurance, recovery,
  tempo, sweet_spot, threshold, vo2max, anaerobic, and an FTP test (`test_protocol`
  set). A fallback is never stored. Its `blocks` come from the shape's own steps with
  generic cues per step kind.
- **"Why" line.** It is built from the plan, without the model. It has three parts:
  - the week's phase, the week number and total weeks, and the goal name, for example
    "Build week 3 of 12 for Spring sportive";
  - " · recovery week next" or " · taper next" when it applies;
  - an empty string for a session with no goal.
- **Privacy.** No log line carries the shape, the text or a rider. Logs are counts
  only.
- **Rules.** No new dependency, `gofmt` and `go vet` clean, never commit `.superpowers/`.

## Review Focus

1. **An eased or repaid session.** Its shape differs from the generated one, so it
   gets its own key and its own explanation. Test in Task 1:
   `TestEasedSessionHasItsOwnKey`.
2. **Two riders with different FTPs.** The same generated session yields the same
   %FTP shape, so one explanation is shared. Test in Task 1:
   `TestSameSessionDifferentFTPSameKey`.
3. **Two requests for the same missing explanation at once.** Only one model call is
   made: the loser of the claim gets the template now. Test in Task 3:
   `TestConcurrentMissMakesOneCall`.
4. **A model reply that cites a watt figure.** No watts are in the input, so the
   number check rejects it, retries, and then falls back to the template. Test in
   Task 2: `TestExplainWorkoutRejectsWatts`.
5. **Another rider's workout id.** The endpoint answers 404. Test in Task 3.

---

### Task 1: Canonical shape (pure)

**Files:**
- Create: `apps/api/internal/workoutshape/shape.go` and `shape_test.go`.

**Interfaces (produces):**

```go
func Text(w workout.Workout, ftp float64) string // canonical shape text per the constraints
func Key(text string) string                     // hex sha256
```

- [ ] **Step 1: Write the failing tests.**
  - `TestShapeHasNoWatts`: a 250 W FTP rider's sweet spot session gives text with no
    "W", "250" or any target watt value, and with `%` ranges.
  - `TestSameSessionDifferentFTPSameKey`: the same steps, scaled to FTP 200 and 300,
    give the same key.
  - `TestEasedSessionHasItsOwnKey`.
  - `TestRepeatBlocksNest`.
  - `TestNoFTPWritesPowerWithoutNumber`.
  - `TestHeartRateAndPaceHaveNoNumbers`.
  - `TestDistanceSteps`.
- [ ] **Step 2: Run them and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and confirm they pass.** Run `go vet` as well.
- [ ] **Step 5: Commit.** `workoutshape: a session's shape without watts or rider`

### Task 2: `explain_workout` job and template fallback

**Files:**
- Create: `apps/api/internal/llm/workout.go` and `workout_test.go`, plus
  `apps/api/internal/api/explaintemplate.go` and its test.
- Modify:
  - `internal/llm/client.go`: add `JobExplainWorkout` to `Jobs`.
  - `internal/config/config.go`: add it to `LLMJobNames`.

**Interfaces (produces):**

```go
type WorkoutExplanation struct{ Summary string `json:"summary"`; Blocks []ExplanationBlock `json:"blocks"` }
type ExplanationBlock struct{ Label string `json:"label"`; Cue string `json:"cue"` }
func (c *Client) ExplainWorkout(ctx context.Context, shapeText string) (WorkoutExplanation, error)
// api package:
func templateExplanation(w workout.Workout, shapeText string) llm.WorkoutExplanation
```

- [ ] **Step 1: Write the failing tests.**
  - Request shape: the model, the schema, `max_tokens` 500, and the user message
    equals the shape text.
  - The limits.
  - `TestExplainWorkoutRejectsWatts`.
  - Retry once, then `ErrInvalid`.
  - The template covers every zone and the test protocol, and its blocks follow the
    steps.
  - The job list agrees with the config list.
- [ ] **Step 2: Run them and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and confirm they pass.**
- [ ] **Step 5: Commit.** `llm: explain_workout; per-zone template`

### Task 3: Store, endpoint, pre-writing

**Files:**
- Create:
  - `apps/api/internal/workout/explanations_db.go` and its test, covering both
    engines: `GetExplanation`, `ClaimExplanation`, `SaveExplanation`,
    `ReleaseExplanation`.
  - `apps/api/internal/api/workoutexplain.go` and its test.
- Modify:
  - `workout/db.go`: the DDL.
  - `api/riderdata*` or its test: the explicit not-rider-data entry.
  - `api/llmnotes.go`: pre-writing after the existing notes.
  - `server.go`: the route, and `llmJobs.explainWorkout` on `/api/me`.
  - `apps/web/src/api/types.ts`: the flag.
- **"Why" line:** build it with the same plan reader the Plan page uses
  (`reconciledPeriodizationPlan`). Grep for how `trainingweek.go` gets the phase,
  week index and recovery flag, and reuse it.
- [ ] **Step 1: Write the failing tests.**
  - Cache hit: no model call.
  - Cache miss: one call, then stored.
  - Job off or no model: the template, with `source` "template" and nothing stored.
  - `TestConcurrentMissMakesOneCall`.
  - A cancelled call releases the claim.
  - Owner-only, with a 404 for another rider's workout.
  - The "why" line for a build week, for a week before a recovery week, and with no
    goal.
  - Pre-writing writes at most 10 shapes, only for the next 7 days, and nothing
    without a model.
  - The logs never contain the shape or the text: use `scanLogger`.
- [ ] **Step 2: Run them and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and confirm they pass.** Run `go test ./apps/api/... -count=1`,
  `-race` on `internal/api`, and `just docker-test`.
- [ ] **Step 5: Commit.** `api: session explanations, cached by shape`

### Task 4: UI

**Files:**
- Modify:
  - `apps/web/src/api/client.ts`: `workoutExplanation(id)`.
  - `components/plan/WorkoutSlideover.vue`: a "How to ride it" section with the
    "why" line, the summary, and the blocks as a compact list. It loads when the
    panel opens and shows a skeleton while loading.
  - The session card on Today (`pages/TodayPage.vue`) and Plan's today card
    (`components/plan/TodayCard.vue`): a collapsed "About this session" `UCollapsible`
    that fetches on first open.
- **Rules:** Nuxt UI components and tokens; the shared link style. When the source is
  the template, show no "model" label. Errors show one quiet line.
- [ ] **Step 1: Implement.**
- [ ] **Step 2: Typecheck.** Run `cd apps/web && npm run typecheck` and `npm test`, then
  `npx vite build`.
- [ ] **Step 3: Commit.** `web: how to ride it`
- The controller checks it in a browser against a stub model.

### Task 5: Eval and docs

- Create `testdata/eval/workout.json` with 8 shapes, one per zone plus an FTP test,
  and add an `explain_workout` block in `eval_test.go`. A case passes on no error.
- `AGENTS.md`: one paragraph under "Local LLM" covering the shape key, the shared
  cache (not rider data), the template fallback, the "why" line from the plan, and
  pre-writing.
- [ ] **Commit.** `llm: evaluation for explain_workout; docs`
