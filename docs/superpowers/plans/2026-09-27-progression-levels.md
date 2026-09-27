# Progression levels and workout library Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Per-zone progression levels moved by ride outcomes (and an optional feel rating), a leveled workout library, and plans that pick each week's workouts from the library at the rider's level.

**Architecture:** Two new pure packages — `internal/workoutlib` (ladders, instantiation) and `internal/progression` (initial levels, update rule). Storage in `internal/workout` (workout zone/level columns, `progression_levels`, feel/delta on analyses). The scheduler takes levels and a phase zone mix. The sync applies level changes after each analysis; the adapter steps a struggled zone down; new endpoints and UI show levels and take feel.

**Tech Stack:** Go stdlib, `internal/dbx` (SQLite/PostgreSQL), existing `fitworkout` encoder; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-27-progression-levels-design.md` — ladders, level rules, phase mix and copy are binding; use them verbatim.

## Global Constraints

- Read `AGENTS.md` first (Conventions, Tests, Observability, Security).
- No new dependencies. gofmt/go vet clean; `just check` green at the end of every task.
- New SQL through `dbx` rebinding, covered by `TestEachEngine` (SQLite + PostgreSQL). Schema additions follow the existing idempotent `UseDB` pattern (see how earlier columns such as `addTimeColumn` / estimated columns were added).
- The rider always comes from the session; owner-only everywhere.
- Only workouts the scheduler generated and nobody has touched are ever changed (`scheduler.IsGenerated`), each at most once.
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together.
- Frontend: tokens only, no hex, no `dark:`; semantic colours only for status; every inline template handler parameter explicitly typed; the CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit` in apps/web, move back).
- Never `git stash` (shared across worktrees). Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **Existing workouts without zone** — `IsKeySession`/`IsHardSession` still recognise them by name; nothing that worked before stops working. (Task 1)
2. **A rider with no FTP / no threshold pace / no max HR** — library workouts instantiate with the same fallback chain as today (power → HR → open), never NaN targets. (Task 2)
3. **Re-rating feel** replaces the ride's previous level change instead of stacking; rating another rider's session is a 404. (Tasks 3, 5)
4. **Short weeks** (n ≤ 2 available days, or hours too small for a rung) — no hard sessions forced in; the time-budget cap picks a lower rung or falls back to endurance. (Task 4)
5. **Level changes are idempotent across syncs** — re-syncing the same analysed ride never moves a level twice. (Task 5)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1–3 foundations | `claude/progression-1-foundations` |
| 2 | 4 scheduler | `claude/progression-2-scheduler` |
| 3 | 5 sync + feel + adapter + API | `claude/progression-3-apply` |
| 4 | 6 UI | `claude/progression-4-ui` |

After each squash merge, rebase the next branch with `git rebase --onto origin/main <old parent tip> <branch>`, retarget, and `--force-with-lease` push immediately.

---

### Task 1: Workout zone and level

**Files:** `apps/api/internal/workout/workout.go` (Workout, CreateWorkoutRequest, UpdateWorkoutRequest), `db.go` (schema + add-column migration, scan/insert/update), tests; `apps/api/internal/scheduler/adapt.go` (+ `adapt_test.go`); `apps/api/internal/api/training.go` (`workoutDTO`, create/update request DTOs); `apps/web/src/api/types.ts` (`Workout.zone?`, `Workout.level?`).

- [ ] RED: store round-trips `Zone`/`Level` on create/update/get/list under both engines; an existing DB without the columns gains them on `UseDB` (reopen test); `IsKeySession`: zone `threshold` → key, zone `endurance` + name "Long ride" → key (long), zone "" + name "Tempo ride" → key (legacy), rider workout name "Hill repeats" zone "" → not key; `IsHardSession`: structured zones hard, `endurance` not, legacy names still hard. API: POST/PATCH accept and return `zone`/`level`.
- [ ] GREEN: `Zone string`, `Level float64` (0 = none). Structured zones are the spec's list; export `workout.StructuredZones` (set) and `workout.ZoneEndurance`. Key = structured zone OR (zone endurance/"" AND long name) OR legacy key names; hard = structured zone OR legacy hard names.
- [ ] `just check`; commit `"Give workouts a training zone and level"`.

### Task 2: `internal/workoutlib`

**Files:** create `apps/api/internal/workoutlib/{ladders.go,library.go,library_test.go}`.

**Produces:**

```go
type Rung struct { Level int; Reps int; WorkSeconds, RestSeconds int; LowPct, HighPct float64 } // pct of FTP (bike) or threshold speed (run)
type Ladder struct { Sport model.Sport; Zone string; Label string /* "Threshold", "Tempo run" */; Rungs [10]Rung }
func LadderFor(sport model.Sport, zone string) (Ladder, bool)
func Pick(l Ladder, targetLevel float64, maxSeconds float64) (Rung, bool) // closest level (ties → lower), skipping rungs whose total seconds (warmup+cooldown+work+rest) exceed maxSeconds; false when none fits
func Instantiate(l Ladder, r Rung, profile workout.RiderProfile) workout.CreateWorkoutRequest // name "<Label> <reps>×<work>", zone, level, warmup/repeat block/cooldown, GeneratedDescription set by the caller
func TotalSeconds(r Rung) float64
```

- [ ] RED: every ladder in the spec table exists with levels 1…10 and non-decreasing work time; `Pick` closest/tie-lower/clamp/time-cap cases; `Instantiate` with FTP → watts targets on/rest; running with threshold pace → m/s low<high; only max HR → HR targets (same % mapping as `scheduler.zoneTarget` uses for HR: map power % to HR via the existing `hrZone`-style fractions — use: rest 50–60 % max HR, tempo/sweet spot 80–88 %, threshold 88–93 %, vo2max/intervals 90–97 %, anaerobic 93–100 %); nothing → open targets; 10′ warmup ramp and 10′ cooldown present; every rung of every ladder encodes via `fitworkout` without error.
- [ ] GREEN: ladders transcribed from the spec table verbatim (a single table-driven source, not ten functions); names use "×" and minutes/seconds like the spec ("VO2max 5×4", "Anaerobic 6×30s").
- [ ] `just check`; commit `"Add a leveled workout library"`.

### Task 3: `internal/progression` + storage

**Files:** create `apps/api/internal/progression/{progression.go,progression_test.go}`; `apps/api/internal/workout/progression_db.go` (+ test), `db.go` schema, `analysis_db.go` (feel, level_delta).

**Produces:**

```go
// progression
type Level struct { Sport, Zone string; Value float64; Reason string }
func Initial(experience string, sport model.Sport) []Level        // spec starting values per sport's structured zones
func Delta(cur, workoutLevel float64, outcome string, feel int) float64 // the spec's update table incl. feel; result such that clamp(round(cur+delta)) is the new level
func Apply(cur, delta float64) float64                            // clamp [1,10], round 1 dp
func Reason(workoutName string, workoutLevel, from, to float64, outcome string) string
// workout store
func (d *DB) ListLevels(ctx, rider string) ([]ProgressionLevel, error)
func (d *DB) SaveLevel(ctx, l ProgressionLevel) error // upsert on (rider, sport, zone)
func (d *DB) SetAnalysisFeel(ctx, sessionID string, feel int, levelDelta float64) error
// SessionAnalysis gains Feel int, LevelDelta float64
```

- [ ] RED: every row of the spec's update table; feel mapping incl. the 0.1 floor; clamp at 1 and 10; rounding; `Initial` values per experience and sport; store round-trips (both engines), upsert, owner isolation, analysis feel/delta columns added to an existing DB.
- [ ] GREEN.
- [ ] `just check`; commit `"Add progression levels and their storage"`.

### Task 4: Scheduler uses the library

**Files:** `apps/api/internal/scheduler/scheduler.go` (+ tests); `apps/api/internal/api/training.go` (`scheduleGoal` loads levels, initialising from the profile when missing, and passes them in); any other `scheduler.NextWorkouts`/`WeekWorkouts` callers (grep).

- [ ] RED: phase × sport × n table from the spec (session zones and count); hard sessions never on consecutive days; long on the last available day; target level = level + 0.5 picks the right rung; time cap drops to a lower rung or to endurance; taper opener at level − 2; recovery week has no structured session; endurance/long sessions keep zone `endurance` and have a 10′ warmup/cooldown; generated workouts carry `GeneratedDescription`, zone and level; total week hours still land on `TargetHours` within 10 %.
- [ ] GREEN: `WeekWorkouts(week, profile, levels map[string]float64 /* zone → level for this sport */, rider, goalID, sport)`; keep `EasyVariant` working (endurance zone).
- [ ] `just check`; commit `"Build weekly plans from the workout library at the rider's level"`.

### Task 5: Apply levels, feel, step down, API

**Files:** `apps/api/internal/api/rideanalysis.go` (after SaveAnalysis), new `apps/api/internal/api/progression.go` (+ tests), `server.go` routes, `adaptation.go`, `apps/api/internal/adapter/session.go` (+ tests), `training.go` (analysis DTO `feel`), `apps/web/src/api/types.ts`, `apps/web/src/api/client.ts`.

- [ ] RED: an analysed ride matched to a workout with zone/level updates that zone's level and stores `level_delta`; re-syncing the same ride doesn't move it again; unplanned/endurance rides change nothing; `PUT /api/training/sessions/{id}/feel` 200 re-applies (old delta removed, new applied), 400 outside 1–5, 404 for another rider's session; `GET /api/training/progression` initialises and returns levels; adapter: a `struggled` key session with a zone → the next untouched generated workout in that zone within 7 days gets a `StepDown` change; the API replaces it with the rung one level lower (via `workoutlib`), reason per spec; rider-owned workouts untouched.
- [ ] GREEN. TS: `ProgressionLevel`, `api.progression()`, `api.setSessionFeel(id, feel)`, `SessionAnalysis.feel?`.
- [ ] `just check` + CI-equivalent typecheck; commit `"Move progression levels from ride outcomes and feel, and step struggled zones down"`.

### Task 6: UI

**Files:** create `apps/web/src/components/fitness/ProgressionCard.vue`, `apps/web/src/components/plan/FeelRating.vue`, `apps/web/src/components/plan/ZoneLevelBadge.vue`; modify `TrainingFitnessPage.vue`, `TodayCard.vue`, `WeekStrip.vue`, `StepResultsTable.vue` (or its modal host), `WorkoutSlideover.vue`.

- [ ] Progression card on the Fitness page after the status card: one row per zone with a 1–10 bar (categorical accent per zone family via the existing `--app-accent-*` tokens), the level to one decimal (mono), and the last reason muted.
- [ ] `ZoneLevelBadge` ("Threshold · 5.2") on the today card, week tiles (compact) and the workout slide-over header, only when zone is structured.
- [ ] `FeelRating`: five buttons 1–5 labelled Easy / Moderate / Hard / Very hard / All-out (icons optional, text required), current value selected, calls `api.setSessionFeel`, toast on success, updates the level card/week without a full reload where simple (otherwise reload the week and progression).
- [ ] Shown on the today card's done state and in the step-results modal.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with seeded rows in `data/demo.db` (removed afterwards), light/dark, 375px.
- [ ] Commit `"Show progression levels, workout levels and a feel rating"`.
