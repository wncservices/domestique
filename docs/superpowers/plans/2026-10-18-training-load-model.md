# Training load model — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Size every plan week in TSS from the rider's current CTL and a phase ramp, with available hours as a hard cap, and show the TSS target and planned CTL on the Plan page.

**Architecture:**
- **Pure math in `internal/periodization`:** a `LoadInput` goes in, and weeks come out with `TargetTSS`, `PlannedCTL` and the `TargetHours` derived from them.
- **`BuildWithLoad` beside `Build`:** `Build` becomes the nil-input path, so every hours-only caller and test is unchanged.
- **Scheduler:** it keeps sizing by hours; one option steps rungs up when the hours cap bites.
- **One TSS estimator:** `PlannedTSS` moves to `workout`, which breaks the import cycle.
- **API:** builds the input from snapshots and sessions.
- **Web:** renders the new fields.

**Tech Stack:** Go stdlib; Vue 3 + Nuxt UI + TypeScript.

**Spec:** `docs/superpowers/specs/2026-10-18-training-load-model-design.md`. Its parent roadmap is `2026-10-18-adaptive-training-builder-design.md`, section 1.

## Global Constraints

- `LoadInput == nil` must produce a `Plan` whose existing fields are byte-identical to today's `Build`. Every existing periodization, scheduler, adapter, season and acceptance test stays green **unchanged**. The only exception is Task 1's removal of the `estimatePlannedTSS` tests, which are replaced.
- `WeeklyTSS(ctl, ramp) = 7 × (ctl + ramp ÷ (1 − e^(−7/42)))`. There is exactly one copy of this formula.
- Ramps:
  - base 3/4/5 and build 5/6/7, by `posInBlock` 0/1/2;
  - peak 1;
  - recovery week = 0.65 × the previous non-recovery week's TSS;
  - taper solved to the form-band midpoint, with daily load clamped to [0.3, 1.0] × the last pre-taper daily load;
  - every ramp capped at `MaxRamp`: 4 beginner, 6 default, 8 advanced.
- Dated goal: scale factor `s ≥ 1`, never below the defaults. `Shortfall` is set when it is still short at `MaxRamp`.
- Undated goal: ceiling `capHours × IF² × 100 ÷ 7`, then hold.
- Spike guard: when `HistoryWeeks < 6` and `BestRecentWeekTSS > 0`, week *i* ≤ `BestRecentWeekTSS × 1.15^(i+1)`.
- `TypicalIF` is clamped to 0.55–0.85, and is 0.70 below 3 hours of history (42-day window).
- `TargetHours = min(capHours, TargetTSS ÷ (TypicalIF² × 100))`.
- No coordinate, FTP, watts or health value in a log line next to a rider. No new dependency. `gofmt`/`go vet` clean.
- DTOs in `api` and `apps/web/src/api/types.ts` change together.
- Web typecheck: annotate template handler parameters with types — the generated `apps/web/components.d.ts` can hide errors CI sees, so run the typecheck once with that file moved aside (restore it after). `just check` includes the web typecheck.

## Review Focus

1. **A rider with FTP but snapshots older than a month** (no recent rides). CTL rolls forward to today with zero load, so it drops. The plan must start from that lower CTL, not the stale one. Test in Task 4.
2. **A rider whose available hours are tiny, so TSS can never fit.** `LoadCapped` is on for every week, the scheduler step-up is applied, and no session exceeds its day's hours. Tests in Task 2 and Task 3.
3. **An event date inside the first week** (taper-only plan). No division by zero, the taper solve still clamps, and there is no NaN in any DTO number. Test in Task 2.
4. **A goal edited from undated to dated.** The plan switches from ceiling-hold to target-CTL without panicking, and `TargetCTL` appears. Test in Task 4.
5. **A running goal with FTP set.** Planned TSS is cycling-only, so `WithTargetTSS` is a no-op and running weeks keep the hours conversion. Test in Task 3.

---

### Task 1: One TSS estimator in `workout`

**Files:**
- Create: `apps/api/internal/workout/plannedtss.go` (moved from `adapter/plannedtss.go`)
- Move: `adapter/plannedtss_test.go` to `workout/plannedtss_test.go`
- Modify: `adapter/plannedtss.go` (now a one-line delegate), `adapter/session.go` (remove `estimatePlannedTSS`, `EstimatePlannedTSS`, `firstPowerTargetMid` and the `defaultPlannedIF` use there; `overloadedWeek` calls `plannedOrHours`), `api/tomorrowforecast.go:~173`
- Test: `adapter/session_test.go` (replace `TestEstimatePlannedTSS`)

**Interfaces:**
- Produces:
  - `func PlannedTSS(w Workout, ftpWatts float64) (tss float64, ok bool)` in package `workout`. Identical body, including `defaultPlannedIF` 0.65 for unpowered steps.
  - `func PlannedTSSOrHours(w Workout, ftpWatts float64) float64` in package `workout`. It returns `PlannedTSS` when ok, else `PlannedSeconds(w.Steps)/3600 × 0.65² × 100`, and 0 when `ftpWatts <= 0`. That last rule matches the old estimator's "no FTP, no estimate", which `TestOverloadTriggerIsSkippedWithoutFTP` relies on.
  - `adapter.PlannedTSS` stays as `return workout.PlannedTSS(w, ftp)`.

- [ ] **Step 1: Write the failing tests**
  - Move the nine `plannedtss_test.go` tests to package `workout`, asserting the same values.
  - Add `TestPlannedTSSOrHoursFallsBackForRunning`: a running workout with 3600 s and FTP 250 gives `42.25`.
  - Add `TestPlannedTSSOrHoursIsZeroWithoutFTP`.
  - In `adapter/session_test.go`, replace `TestEstimatePlannedTSS` with `TestOverloadUsesStepByStepPlannedTSS`. A structured threshold session's planned TSS is now the step-by-step value (assert `== workout.PlannedTSS(w, 250)`), and the existing overload tests keep passing.
- [ ] **Step 2: Run them and confirm they fail**

  Run: `go test ./apps/api/internal/workout/ ./apps/api/internal/adapter/`

  Expected: FAIL (undefined: `workout.PlannedTSS`).
- [ ] **Step 3: Implement**

  Move the code. Point `overloadedWeek` and `todayTrainingLoad` at `workout.PlannedTSSOrHours`. Delete the old estimator and its now-unused helpers.
- [ ] **Step 4: Run them and confirm they pass**

  Run: `go test ./apps/api/internal/... -count=1`

  Expected: PASS. If an api test pinned the old overload number, it fails; adjust it only if the new value is the step-by-step one, and record that in the report.
- [ ] **Step 5: Commit**

  `git commit -m "workout: one planned-TSS estimator"`

---

### Task 2: Load math and `BuildWithLoad`

**Files:**
- Create: `apps/api/internal/periodization/load.go`, `apps/api/internal/periodization/load_test.go`
- Modify: `apps/api/internal/periodization/periodization.go` (the `Week` and `Plan` fields, and `Build` delegating to `BuildWithLoad(…, nil)`)

**Interfaces:**
- Consumes: the `workout.RollFitness` constants. Read them from `workout/metrics.go` and reuse the exported function; do not copy the alphas.
- Produces:

```go
type LoadInput struct {
    CTL, ATL          float64
    TypicalIF         float64
    HistoryWeeks      int
    BestRecentWeekTSS float64
    MaxRamp           float64
    TargetCTL         float64 // 0 for an undated goal
    TaperTSB          float64 // form-band midpoint for a dated goal
}
// Week gains: TargetTSS, PlannedCTL, Ramp float64; LoadCapped bool
// Plan gains: LoadBasis string ("ctl" | "hours"), TargetCTL, Shortfall float64
func WeeklyTSS(ctl, ramp float64) float64
func NextFitness(ctl, atl, weeklyTSS float64) (ctl2, atl2 float64) // 7 days at weeklyTSS/7 per day
func BuildWithLoad(goal workout.Goal, profile workout.RiderProfile, today time.Time, load *LoadInput) (Plan, error)
func Build(goal, profile, today) (Plan, error) // = BuildWithLoad(goal, profile, today, nil)
```

`BuildWithLoad` with a non-nil `load` and `capHours == 0` behaves like nil, with `LoadBasis "hours"`.

- [ ] **Step 1: Write the failing tests** (in `load_test.go`)

| Test | Assertion |
|---|---|
| `TestWeeklyTSSMovesCTLByTheRamp` | for ctl ∈ {20, 60, 100} and ramp ∈ {−3, 0, 5}, `NextFitness(ctl, ctl, WeeklyTSS(ctl, ramp))` gains `ramp` within 1e-9 |
| `TestNilLoadLeavesNewFieldsZero` | `BuildWithLoad(…, nil)`: every new `Week`/`Plan` field is zero, `LoadBasis == "hours"`; the unchanged existing periodization tests prove the old fields |
| `TestRampsPerPhase` | an 18-week dated goal, CTL 50, MaxRamp 8, TargetCTL 0 (below the start, so `s = 1`): base weeks ramp 3/4/5, recovery weeks have TSS = 0.65 × the previous one, build 5/6/7, peak 1 |
| `TestRampsAreCappedAtMaxRamp` | MaxRamp 4 gives no week with `Ramp > 4` |
| `TestReachableTargetNeverRampsBelowDefaults` | TargetCTL 55 from CTL 50: every ramp equals the default, and `Shortfall == 0` |
| `TestUnreachableTargetSetsShortfall` | 8 weeks, CTL 30, TargetCTL 110, MaxRamp 6: `Shortfall > 0` and every non-recovery base/build/peak ramp is 6 |
| `TestScaledRampsReachTheTarget` | 16 weeks, CTL 40, TargetCTL 80: the CTL at the end of the peak is within 0.5 of 80 |
| `TestTaperLandsInTheBand` | the projected TSB on the event date is within 1 of `TaperTSB` |
| `TestTaperOnlyPlanIsFinite` | an event in 3 days: every number is finite, and the taper clamps hold |
| `TestUndatedHoldsAtTheHoursCeiling` | CTL 40, 5 h/week cap, IF 0.7: `PlannedCTL` never exceeds `5 × 0.49 × 100 / 7`, and the weeks after it is reached have `Ramp == 0` except recovery weeks |
| `TestSpikeGuard` | `HistoryWeeks` 3, best 200: week *i* TSS ≤ `200 × 1.15^(i+1)` |
| `TestHoursCapSetsLoadCapped` | 2 h/day × 2 days, CTL 80: `TargetHours == 4` and `LoadCapped` on every week |
| `TestTargetHoursFromTSS` | uncapped: `TargetHours == TargetTSS / (IF² × 100)` |
| `TestLoadBasis` | `"ctl"` with input, `"hours"` without |

- [ ] **Step 2: Run them and confirm they fail**

  Run: `go test ./apps/api/internal/periodization/`

  Expected: FAIL (undefined).
- [ ] **Step 3: Implement**

  The week loop walks the same weeks `Build` makes; phases, recovery and anchoring are unchanged. It carries `(ctl, atl)` forward with `NextFitness`.
  - **Scale factor:** bisection on `s ∈ [1, MaxRamp ÷ min default ramp]`, to 1e-3, over a full forecast run.
  - **Taper:** bisection on the daily load `L` between the clamps, rolling day by day to the event date.
  - **Undated:** trims the ramp to `ceiling − ctl` when that is smaller.
- [ ] **Step 4: Run them and confirm they pass**

  Run: `go test ./apps/api/internal/periodization/ ./apps/api/internal/scheduler/ ./apps/api/internal/adapter/`

  Expected: PASS.
- [ ] **Step 5: Commit**

  `git commit -m "periodization: size weeks in TSS from CTL and a phase ramp"`

---

### Task 3: `scheduler.WithTargetTSS`

**Files:**
- Modify: `apps/api/internal/scheduler/demand.go` (the option) and `scheduler.go` (`WeekWorkouts`: a level boost on structured slots, and a re-run)
- Test: `apps/api/internal/scheduler/targettss_test.go`

**Interfaces:**
- Consumes: `workout.PlannedTSS` (Task 1).
- Produces: `func WithTargetTSS(tss float64) Option`.
  - If the sum of `workout.PlannedTSS` over the cycling requests, using `profile.FTPWatts`, is below `tss`, rebuild with the structured target levels `+0.5`, then `+1.0`. Return the first result that reaches `tss`, or the `+1.0` result.
  - Running, FTP 0, or `tss <= 0` is a no-op.

- [ ] **Step 1: Write the failing tests**

| Test | Assertion |
|---|---|
| `TestWithTargetTSSRaisesStructuredRungs` | build week, 4 days × 1 h, FTP 250, `tss` = 1.3 × the unboosted week's planned TSS: planned TSS is higher than unboosted, and every structured `Level ≤` unboosted level + 1.0 |
| `TestWithTargetTSSNeverLengthensAWeek` | total planned seconds ≤ `TargetHours × 3600 × 1.1` (the existing 10% tolerance) |
| `TestWithTargetTSSReachedIsUnchanged` | `tss` below the unboosted TSS gives output equal to no option |
| `TestWithTargetTSSIsANoOpForRunningAndWithoutFTP` | — |

- [ ] **Step 2: Run them and confirm they fail**

  Run: `go test ./apps/api/internal/scheduler/ -run TargetTSS`

  Expected: FAIL.
- [ ] **Step 3: Implement**

  Add an unexported `levelBoost float64` to `options`, applied to the structured `targetLevels`. `WeekWorkouts` re-invokes itself with the boost when `targetTSS` is set and not yet met. Guard against recursion by building with the boost set and the target cleared.
- [ ] **Step 4: Run them and confirm they pass**

  Run: `go test ./apps/api/internal/scheduler/`

  Expected: PASS.
- [ ] **Step 5: Commit**

  `git commit -m "scheduler: step rungs up when a capped week falls short of its TSS"`

---

### Task 4: API — build the load input, wire it, DTOs

**Files:**
- Create: `apps/api/internal/api/loadinput.go`, `apps/api/internal/api/loadinput_test.go`
- Modify:
  - `api/training.go`:
    - `reconciledPeriodizationPlan` (~524);
    - `periodizationWeekDTO` and `periodizationPlanDTO` (~454-468) and their builder (~508);
    - `fillWeek` and `scheduleGoalWeek`: add `WithTargetTSS` when `week.LoadCapped`.
  - `api/seasonplan.go`: `refreshWeek`, same option.
  - `api/trainingweek.go`: `weekFocusDTO.TargetTSS json:"targetTss,omitempty"` (~28, ~219).
  - `narration/narration.go`: around line 233, append `, about %.0f TSS` when `plan.LoadBasis == "ctl"`.

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces:
  - `func (s *Server) loadInput(ctx context.Context, g workout.Goal, rider string, profile workout.RiderProfile) (*periodization.LoadInput, error)`.
    - Returns `nil, nil` for FTP ≤ 0, `capHours == 0`, or no snapshot.
    - CTL/ATL: the latest snapshot rolled forward to `localToday` with zero daily load (`workout.RollFitness`), one roll per day between the snapshot date and today.
    - The rest is computed from `ListSessions`, as the spec's table says.
    - `TargetCTL` and `TaperTSB` come from the projection package (`EventDuration`, `TargetCTL`, `BandFor` midpoint) for a dated goal, and are 0 otherwise.
  - DTO JSON fields:

    | DTO | Fields |
    |---|---|
    | week | `targetTss`, `plannedCtl`, `loadCapped` (all `omitempty`) |
    | plan | `loadBasis`, `targetCtl`, `shortfall` (`omitempty` for the numbers) |
    | week focus | `targetTss` |

  - `adapter.Reconcile` runs only when `plan.LoadBasis == "hours"`.

- [ ] **Step 1: Write the failing tests** (`loadinput_test.go`, over real HTTP with the existing season harness; seed sessions, then `RecomputeFitnessSnapshots`)

| Test | Assertion |
|---|---|
| `TestPeriodizationIsSizedFromCTLForARiderWithFitness` | `GET /api/training/goals/{id}/periodization` has `loadBasis == "ctl"`, a first week with `targetTss > 0` and `plannedCtl` equal to the seeded CTL within 0.5 |
| `TestPeriodizationWithoutFTPIsTheHoursPlan` | `loadBasis == "hours"`, no `targetTss`, and `targetHours` equal to the pre-change value |
| `TestStaleSnapshotRollsForwardToToday` | a snapshot 30 days old means the first week's `plannedCtl` is below the snapshot CTL |
| `TestRefreshResizesNextWeekFromNewCTL` | fill, add a big ride, recompute, run a season pass that refreshes next week: next week's planned TSS rises |
| `TestDatedGoalReportsTargetCTL` | an undated goal edited to dated has `targetCtl > 0` and no panic |
| `TestLoadInputUnit` | table over `TypicalIF` clamps and the 3 h default, `HistoryWeeks`, `BestRecentWeekTSS` (last 4 complete weeks only) and `MaxRamp` by experience |

- [ ] **Step 2: Run them and confirm they fail**

  Run: `go test ./apps/api/internal/api/ -run 'LoadInput|SizedFromCTL|WithoutFTPIsTheHoursPlan|StaleSnapshot|ResizesNextWeek|DatedGoalReportsTargetCTL' -count=1`

  Expected: FAIL.
- [ ] **Step 3: Implement**
- [ ] **Step 4: Run the full suite and confirm it passes**

  Run: `go test ./apps/api/... -count=1`, then `just docker-test`.

  Expected: PASS. Existing hours tests stay unchanged, because their harnesses have no FTP or no snapshots.
- [ ] **Step 5: Commit**

  `git commit -m "api: plan weeks from CTL; expose TSS targets and planned CTL"`

---

### Task 5: Web — TSS target and planned CTL

**Files:**
- Modify:
  - `apps/web/src/api/types.ts` (~1405 `PeriodizationWeek`, the plan type, ~1431 `WeekFocus`);
  - `apps/web/src/components/plan/SeasonTimeline.vue` (~53-63, ~259, ~277);
  - `apps/web/src/components/plan/WeekStrip.vue` (~79-81, ~300);
  - `apps/web/src/utils/today.ts` (~120 `weekHoursLabel`) and its caller `pages/TodayPage.vue:222`.
- Test: the existing unit test file next to `utils/today.ts` if one exists, else `apps/web/src/utils/today.test.ts` in the repo's test runner style. Check `package.json` for vitest; if there is no runner, cover the label logic as pure functions and verify in a browser in Step 4.

**Interfaces:**
- Consumes: Task 4's JSON fields.
- Produces:
  - `weekLoadLabel(completedSeconds, plannedSeconds, focus): string`. It returns `"done / target h"` as today when there is no `targetTss`, and otherwise `"{done TSS} / {targetTss} TSS · {hours} h"`. Done TSS comes from what the page already has; if it has none, show only `"{targetTss} TSS · {hours} h"`.
  - SeasonTimeline: with `loadBasis === 'ctl'`:
    - bars are scaled by `targetTss` and their title reads `"420 TSS · 7.5 h"`;
    - an SVG polyline of `plannedCtl` sits over the bars, using a theme token colour (`--ui-primary` or the timeline's existing token), with an aria-label;
    - when `shortfall > 0`, one muted line reads `"Target fitness out of reach by N CTL at your maximum ramp."`.

- [ ] **Step 1: Write the failing tests** for `weekLoadLabel` (the three cases above), if a runner exists.
- [ ] **Step 2: Implement** the types, label and components, using Nuxt UI semantic classes only.
- [ ] **Step 3: Typecheck**

  Run: `just check` (it includes the web typecheck), then repeat the typecheck with `apps/web/components.d.ts` moved aside and restored.

  Expected: PASS both times.
- [ ] **Step 4: Verify in a browser**

  Run `just api` and `just web`, sign in to a local demo with a profile that has FTP and seeded rides, and confirm:
  - the timeline shows TSS bars and the CTL line in light and dark;
  - the week strip label reads correctly;
  - there are no CSP violations in the console.

  Report what was seen.
- [ ] **Step 5: Commit**

  `git commit -m "web: show weekly TSS targets and the planned CTL line"`

---

### Task 6: Docs

**Files:**
- Modify:
  - `AGENTS.md`, "The whole season is planned" section. Add one paragraph:
    - weeks are sized in TSS from CTL (`periodization.BuildWithLoad`, `api.loadInput`), with hours as the cap;
    - the hours model is the fallback without FTP or a snapshot;
    - `Reconcile` runs only on the fallback;
    - one TSS estimator, `workout.PlannedTSS`.
  - The spec status line: implemented.

- [ ] **Step 1: Edit**
- [ ] **Step 2: Commit**

  `git commit -m "docs: TSS load model"`
