# Season review and best efforts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Record a rider's best efforts, mark and toast a new season or all-time best, draw a season-vs-last-season power curve, and summarise a chosen season (volume, fitness, FTP, compliance, records, goals).

**Architecture:** A derived `best_efforts` table maintained at analysis time; pure `internal/bestefforts` decides awards; an append-only `ftp_changes` log fills the FTP-history gap; a pure `internal/seasonreview` aggregates a period from stored rows; two read endpoints; a Best efforts card, power-duration chart and Season tab on the Fitness page.

**Tech Stack:** Go stdlib, `internal/dbx`; Vue 3 + Nuxt UI v4, inline SVG.

**Spec:** `docs/superpowers/specs/2026-09-29-season-review-design.md` — metrics, the award rule, scopes, the storage schema and copy are binding.

## Global Constraints

- Read `AGENTS.md` first. No new dependencies, no chart library (inline SVG like `FitnessChart.vue`). gofmt, go vet, `cd apps/api && golangci-lint run ./...` (0 issues) and `just check` green, run under `TZ=UTC` (seven scheduling tests are timezone-sensitive under Europe/Brussels; pre-existing, don't touch).
- Every new test uses a fixed clock with an explicit zone and passes under both `TZ=UTC` and `TZ=Europe/Brussels`. Season and scope boundaries use `YYYY-MM-DD` strings, never `time.Now()`.
- New SQL through `dbx`, covered by `TestEachEngine` (SQLite + PostgreSQL); schema idempotent in `UseDB` (`CREATE TABLE IF NOT EXISTS`, add-column guarded), each migration run twice in a test.
- Owner-only everywhere; the rider comes from the session, never a query parameter. Another rider's goal or session is a 404.
- No health values (watts, HR, CTL, FTP) in log lines next to a rider name: log rider, metric, scope, counts only.
- `session_analyses` stays the source of truth; `best_efforts` is derived and rebuildable. Never award imported or late-analysed rides.
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together. Frontend: Nuxt UI semantic tokens only (`text-muted`, `bg-elevated`, no raw Tailwind colours), typed inline template handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back).
- Never `git stash` (shared across worktrees). Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **The award rule**: at least 1 W or 1 %, at least 3 prior values in scope, all-time over season, compared against all stored values. (Task 1)
2. **No award for imported or late-analysed rides**, and an award is never moved or removed. (Tasks 1-2)
3. **`best_efforts` is derived**: rebuild is complete and idempotent, deleting a session removes its rows. (Task 2)
4. **FTP history is recorded at the single write path** with the right source, and the review never invents a start value. (Task 3)
5. **Season boundaries are date-string inclusive and timezone-free**; missing elevation reads "n/a", not 0. (Task 4)
6. **Nothing reaches another rider**, and no value is logged beside a rider. (Tasks 2, 5)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1-2 award rule, `best_efforts` storage, sync awards | `claude/season-1-best-efforts` |
| 2 | 3 `ftp_changes` and `ascent_m` | `claude/season-2-history-data` |
| 3 | 4-5 season aggregation and the two read APIs | `claude/season-3-review-api` |
| 4 | 6 UI: records card, curve, awards, toast | `claude/season-4-records-ui` |
| 5 | 7 UI: Season tab, print view | `claude/season-5-season-ui` |

Restack right after each squash merge.

---

### Task 1: The award rule

**Files:** create `apps/api/internal/bestefforts/{bestefforts.go,bestefforts_test.go}`.

**Produces:**

```go
type Metric struct { Key, Sport string; MinGain func(prev float64) float64 }
func Metrics() []Metric
func Efforts(a workout.SessionAnalysis, sport string) map[string]float64
func Award(m Metric, value float64, season, allTime []float64) string // "" | "season" | "all_time"
```

- [ ] RED: `Efforts` maps the six power windows for cycling, `speed_1200`/`speed_1800` for running, `max_hr` for any sport, and drops zero and absent values; `Award` fires at exactly `prev + 1` W and `prev * 1.01` for speed and HR and not below; needs 3 prior values in scope; all-time wins over season; an equal value does not award (a tie goes to the earlier ride); a value that beats the season but not the all-time gives `season`.
- [ ] GREEN; `just check`; commit `"Add the best-effort metrics and the new-best award rule"`.

### Task 2: Storage and sync

**Files:** `apps/api/internal/workout/db.go` (table + indexes), create `apps/api/internal/workout/besteffort_db.go` (+ `_test.go`), `apps/api/internal/api/metricssync.go` (+ test), `apps/api/internal/api/server.go` (sync result DTO).

- [ ] RED (`TestEachEngine`): `UpsertEfforts` writes one row per metric, a re-run updates value and date but keeps `award`; `BestIn(rider, metric, fromDate, toDate)` returns value, date and session, ties to the earliest; `ScopeValues` feeds `Award`; `RebuildBestEfforts` recreates every row from `session_analyses` + `completed_sessions` with empty award, runs only when the table is empty, and a second run changes nothing; `DeleteEffortsForSession`; the migration runs twice.
- [ ] RED (sync): a ride analysed within 3 days of its date that beats its season by the rule is stored with an award and returned in `bestEfforts` once; a second sync of it returns none; a ride dated more than 3 days back (an import) is stored with no award and no toast; a late-synced older ride does not claim a record a newer ride beats; the log line carries rider, metric and scope only.
- [ ] GREEN; `just check`; commit `"Record best efforts at analysis time and award a new season or all-time best"`.

### Task 3: FTP history and elevation

**Files:** `apps/api/internal/workout/db.go`, create `workout/ftpchange_db.go` (+ test), the `SaveProfile` and threshold auto-apply paths in `apps/api/internal/api/`, `workout/metrics_db.go` and `api/metricssync.go` for `ascent_m`.

- [ ] RED (`TestEachEngine`): `ftp_changes` appended only when the value differs, with `source` `manual` from a profile save, `detection` from an auto-applied finding and `test` from a test ride; accepting a suggestion records once; the one-time copy of accepted suggestions is idempotent and marks a test-ride reason `test`; `ListFTPChanges(rider, from, to)` is date-ordered; `completed_sessions.ascent_m` round-trips and an existing DB gains the column; a sync stores the provider's ascent and leaves 0 when it reports none.
- [ ] GREEN; `just check`; commit `"Record FTP changes with their source and store ride ascent"`.

### Task 4: Season aggregation

**Files:** create `apps/api/internal/seasonreview/{seasonreview.go,seasonreview_test.go}`.

**Produces:**

```go
type Period struct { Kind, Label, From, To string } // kind: "year" | "goal"
type Input struct { Period Period; Today string; Sessions []workout.CompletedSession; Snapshots []workout.FitnessSnapshot; FTPChanges []workout.FTPChange; Workouts []workout.Workout; Efforts []workout.BestEffort; Goals []workout.Goal }
type Review struct { Volume; Fitness; FTP; Plan; BestEfforts; Goals } // see the spec's table
func Compute(in Input) Review
func GoalPeriod(g workout.Goal) (Period, bool)
```

- [ ] RED: volume sums with both boundary days inclusive; elevation "n/a" with no ascent and "from N of M rides" with some; CTL start (nearest snapshot on or before), peak with date, end (today for the current period); FTP start and end from the last change on or before each boundary, "history starts" when none, each change with source; plan counts only past days and shows no percentage under 4 planned; awards in range; a goal in range with event-day CTL/TSB and the longest event-day session, none found handled; `GoalPeriod` from `created_at` to `event_date`; an empty period yields empty sections, not zeros; a 31 December ride belongs to that year.
- [ ] GREEN; `just check`; commit `"Add the season review aggregation"`.

### Task 5: Read endpoints

**Files:** create `apps/api/internal/api/bestefforts.go`, `seasonreview.go` (+ tests), `server.go` routes, `apps/web/src/api/{types.ts,client.ts}`.

- [ ] RED: `GET /api/training/best-efforts` returns per-metric all-time, season and 42-day bests with date and session and the three curves (this season, last season, all-time envelope), filtered by `sport`; `GET /api/training/season` for `year` and for `goal`, 400 on a malformed period, 404 for another rider's goal; `sessionAnalysisDTO.awards`; owner-only (another rider never appears); no value in any log line.
- [ ] GREEN; `just check`; commit `"Expose best efforts and the season review"`.

### Task 6: Records UI

**Files:** create `apps/web/src/components/fitness/{BestEffortsCard.vue,PowerCurveChart.vue}`; modify `TrainingFitnessPage.vue`, `RecentRides.vue`, `apps/web/src/utils/` for the sync toast.

- [ ] Best efforts card (table of metrics by all time, season and 42 days, "312 W, 14 Aug" cells, Cycling/Running switch, empty state for a rider with no analysed rides); `PowerCurveChart` inline SVG on a log duration axis with three lines (this season, last season, all-time dashed), hover readout, no chart library, semantic tokens only; "New best!" chip on a ride with awards; a sync toast for `bestEfforts` (top two plus "and N more").
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with a seeded rider (rows removed afterwards), light/dark, 375px; commit `"Show best efforts, the power curve and new-best moments on the Fitness page"`.

### Task 7: Season tab

**Files:** create `apps/web/src/components/fitness/{SeasonReview.vue,SeasonPicker.vue}`; modify `TrainingFitnessPage.vue`; print styles.

- [ ] A `Season` tab (`?tab=season`) with the period picker (calendar years with sessions, goals with an event date), sections per the spec (volume, fitness start/peak/end, FTP changes with source, plan compliance, records set, goals with event-day form and ride), "n/a" for missing elevation and "FTP history starts <date>" where applicable, an `@media print` view that hides tabs and controls; the reserved `projectedCtl` is not rendered.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with a seeded multi-year rider and one with no history, light/dark, 375px, print preview; commit `"Add the Season review tab to the Fitness page"`.
