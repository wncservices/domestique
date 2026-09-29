# Race-day projection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Project CTL, ATL and form from the latest snapshot to the A goal's event date using the planned season, and say plainly whether the taper lands.

**Architecture:** A better planned-TSS estimate in `adapter`; a pure `internal/projection` (roll, bands, verdict, ramp, what-if taper); one read-only endpoint assembling existing reads; the Fitness chart, a race-day card and a Plan header chip.

**Tech Stack:** Go stdlib, `internal/dbx`; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-29-race-day-projection-design.md` — math, bands, thresholds and copy are binding.

## Global Constraints

- Read `AGENTS.md` first. No new dependencies. gofmt, go vet, `cd apps/api && golangci-lint run ./...` (0 issues) and `just check` green, run under `TZ=UTC`.
- Every new test uses a fixed clock with an explicit zone and passes under both `TZ=UTC` and `TZ=Europe/Brussels` (include a `now` of 23:30 UTC).
- SQL, where touched, goes through `dbx` and is covered by `TestEachEngine` (SQLite + PostgreSQL). Schema stays idempotent in `UseDB`; this feature adds none.
- Owner-only: the rider comes from the session; another rider's goal is a 404. Read-only: the endpoint writes nothing.
- No health values (CTL, TSB, FTP, watts) in log lines next to a rider name: log rider, goal id, verdict key.
- Reuse `workout.RollFitness`; never a second copy of the 42/7-day constants. Do not change `adapter.EstimatePlannedTSS`.
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together. Frontend: Nuxt UI semantic tokens only, typed inline template handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back).
- Never `git stash` (shared across worktrees). Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **Race-day TSB is the start-of-day value**, matching `ComputeFitness`; the event's own load is excluded. (Task 2)
2. **A completed session replaces its day's planned workout**, never both; missing days are 0. (Task 2)
3. **`PlannedTSS` uses fourth-power NP over all steps**, not the first target. (Task 1)
4. **`undertrained` outranks `fatigued`; band edges are inclusive.** (Task 3)
5. **Nothing is applied or stored**; suggestions are text plus numbers. (Tasks 3-4)
6. **Ramp warning fires above 8, not at 8, and not under CTL 20.** (Task 3)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1 planned TSS estimate | `claude/projection-1-planned-tss` |
| 2 | 2-3 projection, bands, verdict, ramp, what-if | `claude/projection-2-engine` |
| 3 | 4 endpoint | `claude/projection-3-api` |
| 4 | 5-6 chart, race-day card, Plan header chip | `claude/projection-4-ui` |

Restack right after each squash merge.

---

### Task 1: Planned TSS estimate

**Files:** `apps/api/internal/adapter/session.go` (+ `session_test.go`).

- [ ] RED: `PlannedTSS(w, ftp) (float64, bool)`: hand-computed NP and TSS for warm-up + repeat block + cool-down; a power target uses its midpoint; open, HR and other targets use 0.55 (warm-up/cool-down), 0.45 (rest), 0.65 (active) of FTP; distance-only steps count 0; ftp 0 or no timed steps gives `ok=false`; a structured session scores above `EstimatePlannedTSS` for the same workout; `EstimatePlannedTSS` results unchanged (existing tests untouched).
- [ ] GREEN; `just check`; commit `"Estimate a planned workout's TSS from every step, not the first target"`.

### Task 2: Projection roll

**Files:** create `apps/api/internal/projection/{projection.go,projection_test.go}`.

**Produces:**

```go
type Input struct { Start workout.FitnessSnapshot; Actual map[string]float64; Planned map[string]float64; Today, Event string }
type Point struct { Date string; CTL, ATL, TSB, Load float64 }
func Roll(in Input) []Point // today..event, TSB start-of-day
```

- [ ] RED: a 3-day roll equals `workout.RollFitness` by hand; the snapshot-to-today gap uses `Actual` with 0 for empty days; a day present in `Actual` ignores `Planned`; today uses actual else planned; the event-day point excludes the event's own load; an event today, past or over 400 days returns no points; a snapshot dated after today is treated as today.
- [ ] GREEN; `just check`; commit `"Roll CTL and form forward day by day to an event date"`.

### Task 3: Bands, verdict, ramp, what-if taper

**Files:** `apps/api/internal/projection/{bands.go,verdict.go,ramp.go,taper.go}` (+ tests).

- [ ] RED: `EventDuration`, `BandFor`, `TargetCTL` (28 km/h + 1000 m/h, three bands, clamp [30,120], no-distance default medium); the verdict table, every key, edges inclusive (+5/+15 on_track, 85 % boundary), `undertrained` outranking `fatigued` with the form appended, `incomplete` with N of M, `unavailable` reasons; ramp per Monday with 8.0 no warning and 8.01 warning, `excessTss = (ramp-8)*42`, CTL under 20 exempt; the what-if taper tries 7, 10, 14, 21 days at 60 %, returns the first reaching the band low, none says so, an event under 7 days offers nothing.
- [ ] GREEN; `just check`; commit `"Judge a projected race day against a target form band"`.

### Task 4: Endpoint

**Files:** create `apps/api/internal/api/projection.go` (+ `projection_test.go`); `server.go` route `GET /api/training/projection`; `apps/web/src/api/{types.ts,client.ts}`.

- [ ] RED (TestEachEngine, real HTTP): without `?goal` the nearest A goal, else the nearest of any priority, is used; 200 shape per spec (points, raceDay, band, targetCtl, verdict, ramp, suggestion, events, assumptions); B and C events listed with projected CTL/TSB and no verdict; plan weeks missing from `scheduled_weeks` give `incomplete`; no FTP gives `unavailable`; another rider's goal is 404; one failing input degrades to `unavailable` rather than 500; the request performs no write (row counts unchanged); assumptions include "Assumes you ride the plan as written."; captured logs hold no CTL/TSB/watts beside the rider.
- [ ] GREEN; `just check`; commit `"Add GET /api/training/projection for the planned season"`.

### Task 5: Chart and race-day card

**Files:** modify `apps/web/src/utils/fitnessMath.ts` (+ test), `components/fitness/FitnessChart.vue`, `pages/TrainingFitnessPage.vue`; create `components/fitness/RaceDayCard.vue`.

- [ ] Pure helpers first (extend series, band clamp, "To race" range) with tests; the chart takes an optional `projection`: dashed future CTL/ATL/TSB, event marker with name, success-token band strip, crosshair says "Projected"; unchanged with no projection. Card: verdict, numbers, ramp warning, suggestion, "About this projection" disclosure with the assumptions.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with a seeded A goal and season (rows removed afterwards), light/dark, 375px; commit `"Extend the Fitness chart to race day with a target band and verdict"`.

### Task 6: Plan header chip and timeline markers

**Files:** create `components/plan/RaceDayChip.vue`; modify `PlanGoalHeader.vue`, `SeasonTimeline.vue`, `TrainingPlanPage.vue` (fetch once).

- [ ] Chip beside the priority badge: an A goal shows the verdict (tone colour, numbers in `title`), B/C a neutral "Form +8 projected"; nothing while loading or `unavailable`. Timeline: a marker per future event, info-only colour.
- [ ] Verify as Task 5; commit `"Show the race-day verdict on the Plan header"`.
