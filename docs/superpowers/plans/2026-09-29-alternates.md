# Workout alternates and "I have N minutes today" Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** From a planned session offer an easier, harder, shorter or longer version with a predicted difficulty, and let a rider say how many minutes they have today and put one of 2 or 3 fitted suggestions on the day.

**Architecture:** A pure `internal/alternates` (options and difficulty) and `internal/trainnow` (suggestions) over `workoutlib`, `progression` and `readiness`; one nullable `planned_snapshot` column; `scheduler.SwappedMarker` makes a swap rider-touched; four endpoints; a day card menu and a Plan page slideover.

**Tech Stack:** Go stdlib, `internal/dbx`; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-29-alternates-design.md` — option rules, the 75 %/125 % thresholds, the level + 1 cap, difficulty bands, suggestion order and readiness/load shaping are binding.

## Global Constraints

- Read `AGENTS.md` first. No new dependencies. gofmt, go vet, `cd apps/api && golangci-lint run ./...` (0 issues) and `just check` green, run under `TZ=UTC` (seven scheduling tests are timezone-sensitive under Europe/Brussels; pre-existing, don't touch).
- Every new test uses a fixed clock with an explicit zone and passes under both `TZ=UTC` and `TZ=Europe/Brussels`.
- New SQL through `dbx`, `TestEachEngine` (SQLite + PostgreSQL), idempotent schema in `UseDB` (add-column only when absent). Owner-only everywhere; the rider comes from the session, and a workout id that is not theirs is a 404.
- Nothing applies on its own: GET never writes, and a swap or "Use this" is always a rider POST.
- A swap never moves a progression level, never touches an FTP test, and always counts as rider-touched (`SwappedMarker`).
- No health values (watts, FTP, HR, readiness reasons) in log lines next to a rider name: log rider, workout id, kind and outcome only.
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together. Frontend: Nuxt UI semantic tokens only (`text-muted`, `bg-elevated`, semantic colours), typed inline template handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back).
- Keep the indoor and push-today-only work working: an indoor session stays indoor (`keepIndoor`) and a swap of today's session re-pushes. If those PRs are unmerged when a task lands, use the seam they document and say so in the PR body.
- Never `git stash` (shared across worktrees). Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **Harder is capped at floor(rider level) + 1, not the session's level + 1**, and shorter/longer use exactly 75 % and 125 % of the current total. (Task 1)
2. **A swap is rider-touched everywhere**: `IsGenerated`, replan's `isPlanMade`, and the season refresh's untouched test all say no. (Task 2)
3. **`planned_snapshot` is written by the first swap only**, and revert is exact, including indoor state. (Task 2)
4. **No level is written by a swap.** (Task 2)
5. **Every suggestion fits N; `rest` gives easy only; `caution` one rung down; a third hard day drops `wanted`.** (Task 3)
6. **"Use this" replaces only a plan-made untouched session, otherwise adds; it never overwrites a rider's own session.** (Task 3)
7. **Nothing applies without a POST.** (Tasks 2, 3)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1 options, difficulty, marker | `claude/alternates-1-options` |
| 2 | 2 snapshot, swap, revert | `claude/alternates-2-swap` |
| 3 | 3 "I have N minutes" | `claude/alternates-3-trainnow` |
| 4 | 4 UI | `claude/alternates-4-ui` |

Restack right after each squash merge.

---

### Task 1: Options, difficulty and the marker

**Files:** create `apps/api/internal/alternates/{alternates.go,alternates_test.go}`; modify `apps/api/internal/scheduler/{adapt.go,scheduler.go}` (`SwappedMarker`; export `BuildEnduranceSession`; `IsGenerated` false when the marker is present), `apps/api/internal/adapter/session.go` (export `PlannedTSS` from `estimatePlannedTSS`), `apps/api/internal/api/replan.go` (`isPlanMade` false for a swapped one) (+ tests).

**Produces:**

```go
type Kind string // easier | harder | shorter | longer
type Option struct { Kind Kind; Name string; Zone workout.Zone; Level float64; Seconds float64; TSS float64; Difficulty string; Steps []workout.WorkoutStep }
func Options(w workout.Workout, riderLevel float64, profile workout.RiderProfile) []Option
func Difficulty(rung, riderLevel float64) string
```

- [ ] RED: rung 1 has no easier; harder is `cur + 1` and stops at `floor(L) + 1` (L 4.3 gives 5, a session at 6 offers none); shorter takes the longest rung with level <= cur and total <= 0.75 x current, longer the shortest with level >= cur, <= the cap and >= 1.25 x, and both are omitted when no rung qualifies; endurance and long rides scale the main step 75 % / 125 %, round to 5 minutes, floor at 30 and cap at 6 hours, with no easier or harder; `Difficulty` at every boundary (-2, -0.5, 0.5, 1); a test, a ridden, a past, a rider-built and a zone-less session return nil; every ladder for cycling and running is exercised, and a table asserts what `shorter`/`longer` return per rung (if a ladder's durations are not monotone in level the test documents it and the rule stays as specced); `IsGenerated` and `isPlanMade` are false once `Swapped by you:` is in the description, true otherwise.
- [ ] GREEN; `just check`; commit `"Compute workout alternates and their predicted difficulty"`.

### Task 2: Swap, snapshot and revert

**Files:** `apps/api/internal/workout/{workout.go,db.go}` (`planned_snapshot`, NULL-safe scan, `SetPlannedSnapshot`), create `apps/api/internal/api/alternates.go` (+ test), `server.go` routes, `apps/web/src/api/{types.ts,client.ts}`.

- [ ] RED: `GET .../alternates` returns the options with TSS and difficulty and a `warning` on a harder option when today's or tomorrow's verdict is `caution`/`rest`; `POST` applies `kind` in place (same id, date, goal), appends the marker note, writes `planned_snapshot` only when it is empty (a second swap keeps the first), leaves the level table untouched, and 409s for a ridden, past, test, or no-longer-offered option; `POST .../revert` restores name, zone, level, steps and indoor state from the snapshot, clears it, is idempotent, and 409s for a ridden or past session; after a swap the whole-season refresh (`untouchedPlanSession`), replan, `AdaptSessions` and the tomorrow forecast all leave it alone; an indoor session stays indoor after a swap through `keepIndoor` (or the documented no-op if that PR is unmerged); a swap of today's session calls the push helper once, a future one not at all; the column round-trips on both engines under `TestEachEngine` and an existing DB gains it; owner-only and a foreign workout id 404; logs carry no readiness reasons.
- [ ] GREEN; `just check`; commit `"Swap a planned session for an easier, harder, shorter or longer one, reversibly"`.

### Task 3: "I have N minutes today"

**Files:** create `apps/api/internal/trainnow/{trainnow.go,trainnow_test.go}`; create `apps/api/internal/api/trainnow.go` (+ test); `server.go` routes; `apps/web/src/api/{types.ts,client.ts}`.

**Produces:**

```go
type Input struct { Minutes int; Level func(zone string) float64; Profile workout.RiderProfile; Today *workout.Workout; WeekZones []string; DoneZones map[string]bool; HardDaysLast7 int; Verdict readiness.Verdict; ReadinessReasons []string; Sport model.Sport; HasPlan bool }
type Suggestion struct { Kind, Name string; Zone workout.Zone; Level float64; Seconds, TSS float64; Difficulty, Why, Warning string; Steps []workout.WorkoutStep }
func Suggest(in Input) []Suggestion
```

- [ ] RED: minutes outside 30 to 180 is a 400; every suggestion's total is <= N for every picker value on every ladder, and a rung-1-too-long case demotes to endurance; the 1.25 cap on an endurance stand-in while `easy` takes all of N; `planned` is today's session else the next undone plan session this week; `wanted` is the first week zone not done and not `planned`, in plan order; dedup on zone, level and minutes; `rest` returns only `easy` capped at 60 minutes with the readiness reasons as `why`; `caution` uses level - 1 (floor 1) and drops `wanted`; two hard days in the last 7 drop `wanted` and warn on `planned`; no plan returns `easy` only; no wellness data behaves as `ok`. API: `GET` writes nothing; `POST apply` replaces today's plan-made untouched session in place (same id, `planned_snapshot` written, marker note, revert works) and adds a new workout with the "Chosen by you" description beside a rider-built, adjusted, swapped or test day; a stale `{kind, zone, level}` is a 409; the added workout is not deleted by replan or the season refresh; today's copy is pushed through the push-today helper when auto-push is on; owner-only.
- [ ] GREEN; `just check`; commit `"Suggest sessions that fit the minutes a rider has today"`.

### Task 4: UI

**Files:** create `apps/web/src/components/plan/{AlternatesMenu.vue,TrainNowSlideover.vue,DifficultyChip.vue}`; modify `TodayCard.vue`, `TrainingPlanPage.vue`.

- [ ] `AlternatesMenu` (`UDropdownMenu`) in the day card actions beside Move, FIT and Edit, fed by the selected day's workout (works with click-to-show, #325): options as "Easier: name, duration, difficulty, TSS", a muted warning line, "Back to planned version" when `hasPlannedSnapshot`, hidden for ridden, past, test or no-option days, a toast on swap and revert and a refresh of the week. `TrainNowSlideover` from an "I have ... minutes" button by the week strip: chip picker (30, 45, 60, 75, 90, 120, 150, 180, other), suggestion cards with `DifficultyChip`, why, warning and "Use this", a "nothing applied until you choose" line, and a refetch on a 409. Chip colours are Nuxt UI semantic colours only; template handler params typed inline.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with a seeded plan (rows removed afterwards), light/dark, 375px; commit `"Add the Alternates menu and the 'I have N minutes' sheet to the Plan page"`.
