# Post-ride survey and Why? Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the feel rating into a ten-second survey (effort, legs, life stress) that can only ease the plan, and record a structured reason for every automatic change so a rider can tap "Why?".

**Architecture:** Pure `internal/why` (rule ids, input structs, facts); an `adjustments` table written where each change is applied; `readiness` gains signals and survey days; `SessionAnalysis.EffectiveOutcome` carries the effort-5 struggle rule; `legs`/`stress` columns; `why` on the workout and level DTOs; `WhyPopover.vue` and an extended `FeelRating.vue`.

**Tech Stack:** Go stdlib, `internal/dbx`; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-29-ride-survey-and-why-design.md` — rules, copy, thresholds and the IF table are binding.

## Global Constraints

- Read `AGENTS.md` first. No new dependencies. gofmt, go vet, `cd apps/api && golangci-lint run ./...` (0 issues) and `just check` green, run under `TZ=UTC` (seven scheduling tests are timezone-sensitive under Europe/Brussels; pre-existing, don't touch).
- Every new test uses a fixed clock with an explicit zone (UTC and Europe/Brussels) and passes under both `TZ=UTC` and `TZ=Europe/Brussels`. No `time.Now()` in a new rule; dates come from the caller's clock.
- New SQL through `dbx`, covered by `TestEachEngine` (SQLite + PostgreSQL), schema idempotent in `UseDB` (`CREATE TABLE IF NOT EXISTS`, add-column that ignores "already exists"; an old database must gain the columns).
- Owner-only everywhere; the rider comes from the session, never the body. `why` is returned only inside DTOs the owner already gets; no endpoint lists adjustments.
- **No health value (HRV, sleep, resting HR, load, effort, legs, stress) in a log line next to a rider name.** Replace `"reason", c.Reason` in adaptation logs with the rule id, and drop `feel` from the feel log line; add a test that captures logs.
- Nothing here ever makes a day harder; the survey adds no new writer of workouts, and one automatic change per workout still holds (`appliedFor`, `scheduler.IsGenerated`).
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together. Frontend: Nuxt UI semantic tokens only (`text-muted`, `bg-elevated`, ...), typed inline template handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back).
- Never `git stash` (shared across worktrees). Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **Effort 5 counts as `struggled` in one place** (`EffectiveOutcome`) and only for a planned, `nailed`/`completed` ride; effort 1-4 behave as before. (Task 5)
2. **Heavy legs only ever yields a caution reason**, never `rest`, never a harder day. (Task 5)
3. **Every rule in the spec's table writes exactly one row**, only after the change landed, idempotently. (Tasks 2-4)
4. **No health value beside a rider name in any log.** (Tasks 2 and 5)
5. **Old text-only adjustments still show a popover**, with the text alone. (Task 7)
6. **A measured load is never replaced by effort**; RPE only replaces the flat `hours x 50` fallback. (Task 6)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1-2 `why` package, adjustments storage, adaptation rules record, log fix | `claude/why-1-record` |
| 2 | 3-4 level, threshold and season-refresh records, `why` on DTOs | `claude/why-2-levels` |
| 3 | 5-6 survey backend: columns, effective outcome, readiness, RPE load | `claude/why-3-survey` |
| 4 | 7-8 UI: popover, survey card | `claude/why-4-ui` |

Restack right after each squash merge.

---

### Task 1: The `why` package and storage

**Files:** create `apps/api/internal/why/{why.go,why_test.go}`; `apps/api/internal/workout/db.go` (table + index), create `apps/api/internal/workout/adjustments_db.go` (+ test).

**Produces:**

```go
type Rule string // readiness_rest ... season_refresh, one const per spec row
type Fact struct{ Label, Value string }
type Record struct { Rule Rule; Inputs map[string]any; Text string }
func Facts(rule Rule, inputs map[string]any) []Fact
func Title(rule Rule) string

// workout.DB
func (d *DB) RecordAdjustment(ctx, rider, kind, subjectID string, r why.Record, day string) error
func (d *DB) LatestAdjustments(ctx, rider, kind string, subjectIDs []string) (map[string]Adjustment, error)
```

- [ ] RED: `Facts` for every rule (labels/values, HRV numbers as "38, 41 ms vs usual 52"), unknown rule gives none, inputs round-trip JSON; storage on `TestEachEngine`: upsert on `(kind, subject, day, rule)` is idempotent, latest by `created_at` wins, filtered by rider, batched read of many ids in one query, `DeleteWorkout` removes its rows, a level subject keeps the latest five, table created on a database that predates it.
- [ ] GREEN; `just check`; commit `"Add the why package and an adjustments table"`.

### Task 2: Record the adaptation rules, fix the logs

**Files:** `apps/api/internal/adapter/session.go` (`Change` gains `Why why.Record`), `apps/api/internal/readiness/readiness.go` (`Signals`, `Day.HRVLastNight/HRVWeeklyAvg`), `apps/api/internal/api/adaptation.go` (`assessReadinessAt` fills the HRV fields; record after `applyChange` succeeds), `apps/api/internal/api/ftptests.go` (`easeBeforeFTPTests`), `apps/api/internal/api/riderdelete.go` (+ tests).

- [ ] RED: each `Change` (rest, caution, missed move with and without a replaced easy day, both fatigue triggers, struggle step-down) carries its rule and inputs; `Signals` for every readiness rule carry the numbers and `Reasons` are byte-identical to before; `adaptRider` writes one row per applied change and none for the ignored second change, and none when `applyChange` fails; `ftp_test_eve` recorded; a record-write failure logs a Warn and the change stands; captured logs contain no reason text, sleep score or HRV, only the rule id; rider delete removes the rider's rows.
- [ ] GREEN; `just check`; commit `"Record a structured reason for every automatic plan change"`.

### Task 3: Levels, thresholds, season refresh

**Files:** `apps/api/internal/api/recalibration.go`, `apps/api/internal/api/metricssync.go`, `apps/api/internal/api/thresholds.go`, `apps/api/internal/api/seasonplan.go` (+ tests).

- [ ] RED: a recalibration writes `level_recalibration` per lowered zone (subject `sport:zone`) with FTP from/to and the trigger rule; an auto-applied threshold writes `threshold_auto` (field, from, to, the finding's own reason) and an accepted suggestion does not; a rider-typed change writes nothing; `refreshWeek` writes `season_refresh` only for a session whose name, steps or level changed, leaves its description generated, and a second pass writes nothing more; none of these logs a value beside the rider.
- [ ] GREEN; `just check`; commit `"Record why levels, thresholds and refreshed weeks changed"`.

### Task 4: `why` on the DTOs

**Files:** `apps/api/internal/api/training.go` (`workoutDTO`), `apps/api/internal/api/progression.go` (level DTO), `apps/api/internal/api/trainingweek.go`, `apps/web/src/api/types.ts` (+ tests).

- [ ] RED: `why` on the week and workout responses from one batched read; absent for a workout with no row; the latest row wins; the level DTO carries `why` from its `level` row, and only `reason` when there is none; another rider's workout is 404 as before and never leaks a row.
- [ ] GREEN; `just check`; commit `"Return a why with adjusted workouts and levels"`.

### Task 5: Survey backend

**Files:** `apps/api/internal/workout/{db.go,analysis_db.go,analysis.go}` (columns `legs`, `stress`; `SetAnalysisSurvey`; `EffectiveOutcome`), `apps/api/internal/api/progression.go` (`handleSetSessionFeel`, `applyProgressionForAnalysis`), `apps/api/internal/api/training.go` (analysis DTO), `apps/api/internal/adapter/session.go`, `apps/api/internal/readiness/readiness.go`, `apps/api/internal/api/adaptation.go` (+ tests).

- [ ] RED: body `{feel,legs?,stress?}` is a full replace, `feel` required, unknown `legs`/`stress` 400, owner-only 404, and the feel log line carries no value; the columns exist on an old database and survive a re-analysis (excluded from the upsert like `feel`); `EffectiveOutcome`: effort 5 on a matched nailed/completed ride is struggled, effort 4, an unplanned ride and an incomplete ride are not; the adapter steps the next same-zone session down after an effort-5 nailed ride, counts it toward the two-struggles swap with the "felt all-out" reason, and still yields one `Change` per workout; `progression.Delta` gives 0 for effort-5 nailed and the old table for effort 1-4; readiness: heavy on two consecutive days is caution ("you reported heavy legs on Monday and Tuesday"), heavy plus high stress on two days is caution, one day, a gap day, or stress alone is nothing, never `rest`, and today's step-down applies exactly as an HRV caution does.
- [ ] GREEN; `just check`; commit `"Feed the ride survey into adaptation and readiness, easing only"`.

### Task 6: Session-RPE load

**Files:** `apps/api/internal/workout/metrics.go` (`TrainingLoad` gains the effort argument or a sibling), `apps/api/internal/api/metricssync.go`, `apps/api/internal/api/progression.go` (+ tests).

- [ ] RED: with power or HR the load is unchanged for every effort; with neither and effort 1-5 it is `hours x 100 x IF^2` (0.55, 0.65, 0.78, 0.90, 1.00); with no effort it stays `hours x 50`; the sync looks up the ride's feel; rating a ride recomputes its load and the fitness snapshots when the fallback applied, and never when a measured load exists.
- [ ] GREEN; `just check`; commit `"Use session-RPE for load when a ride has no power or heart rate"`.

### Task 7: Why? popover

**Files:** create `apps/web/src/components/plan/WhyPopover.vue`; modify `TodayCard.vue`, `WorkoutSlideover.vue`, `apps/web/src/components/fitness/ProgressionCard.vue`, `apps/web/src/utils/workoutMath.ts` (unchanged parser, used as fallback).

- [ ] `WhyPopover` (`UPopover`, typed props): title, text, facts list, day; falls back to the text alone when only `adjustmentNote` exists; replaces the plain adjustment line on the day card and slide-over, and sits on each Progression zone with a `why`; the week strip keeps its tooltip. Semantic tokens only, typed template handler params.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with a seeded adjusted workout and a recalibrated level (rows removed afterwards), a text-only adjusted workout, light/dark, 375px; commit `"Show a Why? on adjusted sessions and on the Progression card"`.

### Task 8: Survey card

**Files:** `apps/web/src/components/plan/FeelRating.vue`, `apps/web/src/api/{client.ts,types.ts}`, `TodayCard.vue`, `StepResultsTable.vue`.

- [ ] Effort row as today; once chosen, an optional legs row (Fresh / Normal / Heavy) and stress row (Low / Normal / High), each saving on tap with the full state; failed save restores the previous value; `SessionAnalysis` carries `legs`/`stress`; the card is one component in both hosts.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser (rate, add legs, clear stress, re-rate), light/dark, 375px; commit `"Ask about legs and life stress after a ride"`.
