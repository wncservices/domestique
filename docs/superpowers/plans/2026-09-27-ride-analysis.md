# Ride analysis (training adaptation #1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Analyse every synced Garmin/Wahoo ride from its FIT file (summary fallback) — accurate load, zones, power curve, planned steps scored hit/under/over, an outcome per session — and use it in adaptation and the UI.

**Architecture:** A pure `internal/rideanalysis` package; a `session_analyses` table in `internal/workout`; FIT download methods on the Garmin and Wahoo clients; the existing metrics sync calls them and stores results; `internal/adapter` reads outcomes; the API and Vue UI show them.

**Tech Stack:** Go stdlib + `github.com/muktihari/fit` (already a dependency: `decoder`, `profile/filedef`, `profile/mesgdef`, `encoder` for test fixtures), SQLite/PostgreSQL via `internal/dbx`; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-27-training-adaptation-design.md` — every threshold, formula, field name and copy string is there; use them verbatim.

## Global Constraints

- Read `AGENTS.md` (Conventions, Observability checklist, Tests, Security) first.
- No new dependencies. `gofmt` clean, `go vet` clean, `just check` green at the end of every task.
- **Never persist raw ride data**: no FIT bytes, no GPS, no per-second streams in the database, logs or test fixtures. Test fixtures are synthetic FIT files built in-test with `encoder`.
- The rider always comes from the session/goal owner, never a request parameter.
- New outbound HTTP uses the client's existing `otelhttp`-wrapped `http.Client` and `http.NewRequestWithContext` with the caller's ctx.
- Logging levels per AGENTS.md: a failed FIT download or decode is **Warn** (sync still succeeds); a failed write that fails the request is **Error**.
- New SQL goes through `dbx` rebinding and is covered by `TestEachEngine` (SQLite and PostgreSQL).
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together.
- Frontend: tokens only, no hex, no `dark:`; semantic colours only for status; every inline template handler parameter explicitly typed; the CI-equivalent typecheck (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back) passes.
- Commit trailer: `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **Records with gaps / pauses / missing power** — NP and TSS never NaN or Inf; a ride with power on only some records still analyses. (Task 1)
2. **Repeat blocks** — lap `wkt_step_index` maps to the right planned step when the workout has a repeat block (children then the repeat marker). (Task 2)
3. **A provider returning HTML, a 404, or a >32 MiB body** — rejected cleanly as a Warn, summary fallback used. (Task 4)
4. **First sync for a rider with months of history** — at most 20 downloads per provider per sync, newest first, only the last 42 days. (Task 5)
5. **Adapter never touches rider-owned workouts** — outcome-driven changes still respect `scheduler.IsGenerated` and "changed once". (Task 6)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1–2 `rideanalysis` | `claude/ride-analysis-1-package` |
| 2 | 3–4 storage + provider fetch | `claude/ride-analysis-2-fetch` |
| 3 | 5–6 sync + adapter | `claude/ride-analysis-3-sync` |
| 4 | 7–8 API + UI | `claude/ride-analysis-4-ui` |

Each branch starts from the previous; after each squash merge, the next is rebased with `git rebase --onto origin/main <old parent tip> <branch>` and retargeted to `main`.

---

### Task 1: `rideanalysis` metrics

**Files:** Create `apps/api/internal/rideanalysis/metrics.go`, `metrics_test.go`, `fixtures_test.go` (FIT builder helpers for tests only).

**Produces:**

```go
package rideanalysis

type Sample struct { Seconds int; Power float64; HeartRate float64; Speed float64; HasPower, HasHR, HasSpeed bool } // 1 Hz, from ride start
func Resample(records []*mesgdef.Record) []Sample // 1 Hz; gaps ≤ 5 s repeat the previous value, longer gaps are zero-power/no-HR samples
func NormalizedPower(s []Sample) float64          // 30 s rolling mean → ^4 mean → ^(1/4); 0 when < 30 samples with power
func PowerTSS(seconds int, np, ftp float64) (ifactor, tss float64)
func HRLoad(s []Sample, maxHR, restHR int) float64 // spec formula; restHR 0 → 0.5×maxHR
func PowerZoneSeconds(s []Sample, ftp float64) [7]int // Coggan edges 55/75/90/105/120/150 %
func HRZoneSeconds(s []Sample, maxHR int) [5]int      // 60/70/80/90 % edges; below 50 % counts in Z1
func PowerCurve(s []Sample) map[int]float64           // keys 5, 60, 300, 1200, 3600; missing when the ride is shorter
```

- [ ] **Step 1: fixtures** — `fixtures_test.go`: `func buildActivity(t *testing.T, samples []fixture, laps []lapFixture) *filedef.Activity` encoding with `encoder` then decoding with `decoder` + `filedef.NewActivity`, so tests exercise the real decode path. `fixture{Sec int; Power, HR int; Speed float64}`; `lapFixture{StartSec, EndSec int; StepIndex int}` (StepIndex −1 = no `wkt_step_index`).
- [ ] **Step 2: RED** — tests: constant 250 W for 3600 s → NP 250 ± 0.5; `PowerTSS(3600, 250, 250)` → IF 1, TSS 100; alternating 60 s at 400 W / 60 s at 100 W for 1 h → NP > average power (≈ 250) and NP > 290; HR constant at 0.9×190 for 1 h with rest 50 → load 100 ± 1; a 20 s gap is filled as zeros, a 3 s gap repeats the previous sample; no power at all → NP 0 and no NaN; zone seconds sum to the sample count; power curve on a 10-minute ride has keys 5, 60, 300 only. Run `go test ./apps/api/internal/rideanalysis/` → FAIL.
- [ ] **Step 3: GREEN** — implement; comment the formulas' sources (Coggan NP/TSS; the spec's HR-load definition). Run → PASS; `gofmt -l`, `go vet`.
- [ ] **Step 4: Commit** — `"Add ride metrics: normalized power, TSS, HR load, zones, power curve"`.

---

### Task 2: `rideanalysis` scoring and outcome

**Files:** Create `apps/api/internal/rideanalysis/analyze.go`, `analyze_test.go`.

**Consumes:** Task 1; `workout.Workout`, `workout.WorkoutStep`, `workout.RiderProfile`, `workout.TrainingLoad`.

**Produces:**

```go
type Outcome string // "nailed" | "completed" | "struggled" | "incomplete" | "unplanned"
type LoadSource string // "fit_power" | "provider_tss" | "fit_hr" | "estimate"
type StepResult struct { Index int; Name string; Target string; Low, High, Actual float64; Result string } // Result "hit"|"under"|"over"
type Summary struct { DurationSeconds float64; AvgPower float64; AvgHR int; NormalizedPower, TSS, IntensityFactor float64 } // provider summary numbers; 0 = unknown
type Input struct { Activity *filedef.Activity; Summary Summary; Planned *workout.Workout; Profile workout.RiderProfile }
type Analysis struct {
    Outcome Outcome; LoadSource LoadSource; Load float64
    NormalizedPower, IntensityFactor, TSS, DurationRatio float64
    PowerZoneSeconds [7]int; HRZoneSeconds [5]int; PowerCurve map[int]float64
    Steps []StepResult
}
func Analyze(in Input) Analysis
func FlattenSteps(steps []workout.WorkoutStep) []workout.WorkoutStep // FIT encode order: a repeat block's children, then the block itself as its repeat marker
func MatchPlanned(date, sport string, rideSeconds float64, planned []workout.Workout) *workout.Workout
```

- [ ] **Step 1: verify flatten order** — read `apps/api/internal/fitworkout/fitworkout.go` Encode and write `TestFlattenMatchesFITEncode`: encode a workout with a repeat block via `fitworkout`, decode its `workout_step` messages, and assert `FlattenSteps` yields the same sequence (name per index). If the order differs from the spec's description, follow the encoder and note it in the report.
- [ ] **Step 2: RED** — tests (use Task 1 fixtures):
  - laps with step indexes for warmup / 4 × (on 260–280 W, off) / cooldown: all on-laps at 270 W → 4 hits, outcome `nailed`; two on-laps at 230 W → 2 under, outcome `struggled` (2 of 4 < 75 %); one lap at 300 W → `over` and still counts as not-under.
  - tolerance: target 260–280, actual 247 → hit (≥ 0.95×260 = 247); 246 → under.
  - no step laps, main step target 180–220 W, 80 % of the ride inside → hit → `nailed`; 60 % → `struggled`.
  - duration ratio 0.45 → `incomplete`; 0.85 with all hits → `completed`; `Planned` nil → `unplanned` with no steps.
  - load source order: activity with power + FTP → `fit_power`; no power, summary TSS 80 → `provider_tss`, load 80; no power, HR present, max HR set → `fit_hr`; nothing → `estimate` equal to `workout.TrainingLoad(...)`.
  - `Activity` nil and summary NP/TSS present → metrics from summary, zones empty, outcome from duration ratio and summary NP vs the main step's target.
  - `MatchPlanned`: two planned rides that day (60 and 120 min), ride 110 min → the 120-min one; other sport ignored.
- [ ] **Step 3: GREEN** — implement; comment why tolerance and thresholds are what they are (spec).
- [ ] **Step 4:** `go test ./apps/api/internal/rideanalysis/`, `gofmt -l`, `go vet`, `just check`.
- [ ] **Step 5: Commit** — `"Score rides against the planned workout: per-step hit/under/over and an outcome"`.

---

### Task 3: `session_analyses` storage

**Files:** Modify `apps/api/internal/workout/db.go` (schema), create `apps/api/internal/workout/analysis_db.go`, `analysis_db_test.go`; add the new behavioural test to the `TestEachEngine` suite list (see `apps/api/internal/source/db_test.go` for how suites are registered and how other packages hook in — follow the existing workout-store pattern).

**Produces:**

```go
type SessionAnalysis struct {
    SessionID, Rider, WorkoutID, Outcome, LoadSource string
    NormalizedPower, IntensityFactor, TSS, DurationRatio float64
    PowerZoneSeconds []int; HRZoneSeconds []int; PowerCurve map[string]float64
    Steps []AnalysisStep; AnalysedAt string
}
type AnalysisStep struct { Index int `json:"index"`; Name string `json:"name"`; Target string `json:"target"`; Low float64 `json:"low"`; High float64 `json:"high"`; Actual float64 `json:"actual"`; Result string `json:"result"` }
func (d *DB) SaveAnalysis(ctx context.Context, a SessionAnalysis) error              // upsert on session_id
func (d *DB) GetAnalysis(ctx context.Context, sessionID string) (SessionAnalysis, bool, error)
func (d *DB) ListAnalyses(ctx context.Context, rider, sinceDate string) ([]SessionAnalysis, error) // joined to completed_sessions.date >= sinceDate
func (d *DB) SetSessionLoad(ctx context.Context, sessionID string, load float64) error
```

`workout` must not import `rideanalysis` (keep the store dependency-free); the API layer converts between the two.

- [ ] **Step 1: RED** — store tests: save → get round-trips every field (JSON columns included); upsert replaces; `ListAnalyses` filters by rider and date; another rider's rows never returned; `SetSessionLoad` then `RecomputeFitnessSnapshots` changes the snapshot; runs under both engines.
- [ ] **Step 2: GREEN** — schema with `session_id` primary key, index on `(rider)`; JSON stored as TEXT; use `QueryContext`/`ExecContext` with the caller ctx.
- [ ] **Step 3:** `go test ./apps/api/internal/workout/`, and the PostgreSQL run via `just docker-test` if Docker is available (report if not — CI covers it).
- [ ] **Step 4: Commit** — `"Store ride analyses per completed session"`.

---

### Task 4: FIT download and summary fallback fields

**Files:** Modify `apps/api/internal/garmin/activities.go` (+ tests), create `apps/api/internal/garmin/activityfit.go` (+ test); modify `apps/api/internal/wahoo/workouts.go` (+ tests).

**Produces:**

```go
// garmin
type Activity struct { /* existing */; NormalizedPower, TrainingStressScore, IntensityFactor float64; BestPower map[int]float64 } // from normPower, trainingStressScore, intensityFactor, maxAvgPower_5/60/300/1200/3600
func (c *Client) ActivityFIT(ctx context.Context, activityID string) ([]byte, error) // GET {APIBase}/download-service/files/activity/{id}; unzip the single .fit in memory
// wahoo
type Workout struct { /* existing */; NormalizedPower, TSS float64; FileURL string } // power_bike_np_last, power_bike_tss_last, workout_summary.file.url
func (c *Client) WorkoutFIT(ctx context.Context, fileURL string) ([]byte, error)
const MaxFITBytes = 32 << 20 // both packages
```

- [ ] **Step 1: RED** — `httptest` servers: Garmin returns a zip containing `123_ACTIVITY.fit` → bytes equal the fixture; a plain (non-zip) FIT body is also accepted; 404 and HTML bodies → error; an uncompressed entry over `MaxFITBytes` → error without reading it all (use `io.LimitReader`). Wahoo: 200 with FIT bytes; 403 → error; empty URL → error before any request. Activity/workout list parsing picks up the new summary fields from a JSON fixture; absent fields stay 0.
- [ ] **Step 2: GREEN** — reuse each client's existing request helper, auth headers and `otelhttp` client; the Garmin path must also send the headers `Activities` sends. Validate the FIT header (`.FIT` signature at bytes 8–11) before returning.
- [ ] **Step 3:** package tests, `just check`.
- [ ] **Step 4: Commit** — `"Download ride FIT files from Garmin and Wahoo, and read their summary NP/TSS"`.

---

### Task 5: Analyse on sync

**Files:** Modify `apps/api/internal/api/metricssync.go`; create `apps/api/internal/api/rideanalysis.go` (the glue); tests in `apps/api/internal/api/rideanalysis_test.go` using the existing metrics-sync test harness (`metricssync_test.go`) with fake Garmin/Wahoo servers.

**Consumes:** Tasks 1–4.

- [ ] **Step 1: RED** — handler tests:
  - a synced ride with a matching planned workout gets an analysis (outcome stored, `training_load` updated to the analysed TSS, snapshots recomputed);
  - a ride older than 42 days is not analysed;
  - 25 new rides → exactly 20 FIT downloads per provider in one sync, newest first; the next sync does the remaining 5;
  - the FIT download returns 500 → analysis from summary (`provider_tss` or `estimate`), sync response 200, a Warn logged;
  - an already-analysed session is not downloaded again;
  - another rider's workouts are never matched.
- [ ] **Step 2: GREEN** — after sessions are upserted in `syncRiderMetrics`, run `analyseNewSessions(ctx, rider, provider, sessions)`: select sessions without an analysis in the last 42 days, newest first, cap 20; for each, fetch FIT (Garmin via activity id, Wahoo via file URL — carry these through from the provider list), decode with `decoder` + `filedef.NewActivity` (a decode error → Warn, summary fallback), `MatchPlanned` against the rider's workouts, `Analyze`, `SaveAnalysis`, `SetSessionLoad`; call `RecomputeFitnessSnapshots` once at the end when any load changed. Keep the provider id / file URL on the in-memory session list only — do not add them to `completed_sessions` unless the existing `external_id` already is the Garmin activity id (check; it likely is).
- [ ] **Step 3:** `go test ./apps/api/...`, `just check`.
- [ ] **Step 4: Commit** — `"Analyse each synced ride against its planned workout"`.

---

### Task 6: Adapter uses outcomes

**Files:** Modify `apps/api/internal/adapter/session.go` (+ tests), `apps/api/internal/api/adaptation.go` (pass analyses in).

**Produces:** `AdaptSessions(..., analyses map[string]workout.SessionAnalysis /* by workout id */)` — or an added field on its existing input struct; follow the current signature style.

- [ ] **Step 1: RED** — adapter tests: a key session with an `incomplete` analysis is treated as missed (made up per the existing rule); `struggled` is done (not made up); the last two analysed key sessions `struggled` → next hard session swapped for easy even with TSB −10; 7-day analysed TSS ≥ 1.3 × planned → swap; without analyses the ≥ 50 % time rule still applies; a rider-owned workout is never changed; reasons name the ride ("Tuesday's threshold intervals were under target (2 of 4)").
- [ ] **Step 2: GREEN** — implement; keep each rule's comment explaining why.
- [ ] **Step 3:** `go test ./apps/api/...`, `just check`.
- [ ] **Step 4: Commit** — `"Adapt sessions from ride outcomes: missed, struggled and overload"`.

---

### Task 7: API

**Files:** Modify `apps/api/internal/api/training.go` (`completedSessionDTO` + `completedSessionDTOFrom`), `trainingweek.go`, tests; `apps/web/src/api/types.ts`.

- [ ] **Step 1: RED** — `/api/training/fitness` and `/api/training/week` include `analysis` for analysed sessions (outcome, loadSource, np, if, tss, durationRatio, steps) and omit it otherwise; owner-only still holds.
- [ ] **Step 2: GREEN** — load analyses once per request (`ListAnalyses` for the rider and the earliest date in the response), attach by session id. TS: `SessionAnalysis`, `AnalysisStep`, `Outcome` types; `CompletedSession.analysis?: SessionAnalysis`.
- [ ] **Step 3:** `just check` + the CI-equivalent typecheck.
- [ ] **Step 4: Commit** — `"Return ride analyses with completed sessions"`.

---

### Task 8: UI

**Files:** Modify `apps/web/src/components/plan/WeekStrip.vue`, `TodayCard.vue`, `apps/web/src/components/fitness/RecentRides.vue`; create `apps/web/src/components/plan/OutcomeChip.vue`, `apps/web/src/components/plan/StepResultsTable.vue`; wire the table into the completed-workout view (the week-strip `open` flow on a past day / the today card's done state).

- [ ] **Step 1:** `OutcomeChip` — props `{ outcome: Outcome }`; label + icon + semantic colour per the spec (Nailed it success `i-lucide-circle-check`, Completed neutral `i-lucide-check`, Struggled warning `i-lucide-trending-down`, Incomplete error `i-lucide-circle-x`, Unplanned info `i-lucide-circle-plus`).
- [ ] **Step 2:** week-strip tiles and today card show the chip for days with an analysed completed session; recent rides show NP / IF / TSS (mono) when present.
- [ ] **Step 3:** `StepResultsTable` — columns step, target (range + unit), actual, result chip; shown for a completed planned workout with steps.
- [ ] **Step 4: Verify** — `just check`, CI-equivalent typecheck, browser check with seeded analyses in `data/demo.db` (remove afterwards), light/dark, 375px.
- [ ] **Step 5: Commit** — `"Show ride outcomes and per-step results on the Plan and Fitness pages"`.
