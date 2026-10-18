# Adaptation state in columns — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every automatic-adaptation decision reads typed columns on `workouts` instead of matching marker text in the description, with behaviour identical to today.

**Architecture:** A `workout.Flags` value (origin, adaptations, swap, keep, move) becomes six columns, backfilled from today's description markers by one pure parser (`LegacyFlags`). Writers set `Flags` explicitly next to the marker text they already write (dual write); a test-only parity check in the store fails any write whose columns disagree with `LegacyFlags(description)`, so the whole existing suite proves the dual write. Only then do the readers switch to the columns.

**Tech Stack:** Go (stdlib, `database/sql`), SQLite + PostgreSQL via `internal/dbx`.

**Spec:** `docs/superpowers/specs/2026-10-18-adaptive-training-builder-design.md` (section 0).

## Global Constraints

- Pure refactor: no change in what any automatic path does. Every existing test passes unchanged, apart from call-site signature updates in Task 6.
- Description markers stay written as display text in this release; only the readers stop relying on them.
- Every new column must work on both engines; `TestEachEngine` covers new store behaviour.
- `workouts` is already in `riderTables` with `Purged: true`. New columns need no registry change, but the rename/merge tests must stay green.
- No new dependency. `gofmt`, `go vet` clean. Comments explain why.
- Rule ids are `why.Rule` values; stored values are never renamed, only added.

## Deviations from the spec's section 0 (decided while mapping the code)

| Spec said | Plan does | Why |
|---|---|---|
| `rider_kept bool` | `kept TEXT` (`''`/`crew`/`life`) | Readers ask which feature the rider kept it through (`api/crewgoing.go:505`, `api/lifeevents.go:511`, `lifeevents.Touched`). |
| `last_adapted_by`, `last_adapted_at` | `adapted_rules TEXT` (comma list, in order) | `lifeevents.lifeRamped` needs the count and the kind of every adaptation, not the last one. |
| — | `moved_from`, `moved_by` | `scheduler.MovedFrom` parses "moved from DATE" from the text; fill-week, crew align and life events read it. |
| `rider_edited_at` | `edited_at`, set by every `UpdateWorkout` | Today "untouched" is `UpdatedAt == CreatedAt`, which counts *every* update. Parity first; sub-project 2 marks system writes. |
| origin includes `route` | origin is `plan`/`rider`/`crew`/`test` | `route_slug` is already a column; `IsPlanMade` keeps checking it. |
| "at most once per rule per day" | not in this plan | It changes behaviour; it lands with sub-project 2's ledger, which needs it. |

Task 7 updates the spec to match.

## Review Focus

1. **A rider edits a plan session's description in the workout form** (`PUT /api/training/workouts/{id}`), removing the "Generated from…" prefix. Today it stops being plan-made and replan leaves it alone, so after this change it must still be left alone. Test in Task 5.
2. **A rider moves a session by changing its date.** `api/training.go:1409` appends "Rescheduled by you: moved from …", and fill-week must still treat the vacated day as taken. Test in Task 5.
3. **A deployment upgrading with existing adjusted, swapped and kept rows.** The backfill must run once and leave every row's flags equal to `LegacyFlags` of its description, on both engines. Test in Task 2.
4. **The ride-together answer fails after moving a session.** `api/togetheranswer.go:229` restores the date and description, and must restore the flags too, or the session stays adapted and moved. Test in Task 5.
5. **Swap, then "Back to planned version".** `api/alternates.go:361` writes the snapshot description plus a swapped note, and the flags must say swapped again, exactly as the text does. Already covered by the parity check; Task 5 adds an explicit test.

---

### Task 1: `Flags` and the legacy parser

**Files:**
- Create: `apps/api/internal/workout/flags.go`
- Create: `apps/api/internal/workout/flags_test.go`
- Modify: `apps/api/internal/scheduler/adapt.go` (marker consts become aliases of the `workout` ones)

**Interfaces:**
- Produces, in package `workout`:

```go
const GeneratedDescription = "Generated from the periodization plan."
const AdjustedMarker = "Adjusted automatically:"
const SwappedMarker = "Swapped by you:"
const CrewKeptMarker = "Kept as planned by you around your crew ride."
const LifeKeptMarker = "Kept as planned by you through the return to training."
const LifeNotePrefix = "Life event:" // lifeevents.rampNotePrefix becomes this

type Origin string // OriginPlan "plan", OriginRider "rider", OriginCrew "crew", OriginTest "test"
type Kept string   // KeptNone "", KeptCrew "crew", KeptLife "life"
type MovedBy string // MovedNone "", MovedByLife "life", MovedByRider "rider", MovedByAuto "auto"

type Flags struct {
    Origin       Origin
    AdaptedRules []why.Rule // in order; nil = never adapted
    Swapped      bool
    Kept         Kept
    MovedFrom    string // first original date, "YYYY-MM-DD"; "" = never moved
    MovedBy      MovedBy
}

// In package why, next to the other rules:
//   Legacy    Rule = "legacy"     // an adaptation the backfill could not name
//   LifeEvent Rule = "life_event" // a life event's move, ease or shortening

func IsLifeRule(r why.Rule) bool          // r == why.LifeEvent || r == why.IllnessRamp
func (f Flags) Adapted() bool             // len(AdaptedRules) > 0
func (f Flags) WithAdaptation(r why.Rule) Flags // copy with r appended (never aliases f's slice)
func (f Flags) Equivalent(g Flags) bool   // the parity relation, see Step 3
func LegacyFlags(w Workout) Flags         // derive from w.Description, GoalID, CrewRideID, TestProtocol
```

- `scheduler.GeneratedDescription` etc. stay as `= workout.X` aliases so no caller changes in this task.

- [ ] **Step 1: Write the failing tests**

`TestLegacyFlags` is a table over descriptions built from the real constants, with `w.GoalID = "g1"` unless the case says otherwise:

| Case | Input | Expected |
|---|---|---|
| plain plan | `GeneratedDescription` | `{Origin: plan}` |
| crew row | `CrewRideID: "r1"`, any text | `Origin: crew` |
| test | `TestProtocol: "ramp"`, no prefix | `Origin: test` |
| hand-built | `GoalID: ""` | `Origin: rider` |
| adjusted twice | `Gen + " " + AdjustedMarker + " Tired. " + AdjustedMarker + " Life event: illness."` | `AdaptedRules: [legacy, life_event]` |
| swapped | `Gen + " " + SwappedMarker + " easier: X"` | `Swapped: true`, `Origin: plan` |
| crew kept | `Gen + " " + CrewKeptMarker` | `Kept: crew` |
| life kept | `Gen + " " + LifeKeptMarker` | `Kept: life` |
| life move | `Gen + "\n\nRescheduled by a life event: moved from 2026-10-01."` | `MovedFrom: "2026-10-01", MovedBy: life` |
| life move again | `"... Rescheduled by a life event: moved from 2026-10-01.\n\nRescheduled again by a life event (it was on 2026-10-02)."` | `MovedFrom: "2026-10-01", MovedBy: life` |
| rider move | `"... Rescheduled by you: moved from 2026-10-03."` | `MovedBy: rider` |
| auto move | `Gen + " " + AdjustedMarker + " missed, moved from 2026-10-04."` | `MovedBy: auto, MovedFrom: "2026-10-04"` |

`TestFlagsEquivalent` checks four cases:

- `legacy` ≡ `missed_moved`.
- `life_event` ≡ `illness_ramp`.
- `legacy` ≢ `life_event`.
- Different counts are not equivalent.

`TestWithAdaptationDoesNotAlias` appends to a copy and checks the original slice is unchanged.

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./apps/api/internal/workout/ -run 'TestLegacyFlags|TestFlagsEquivalent|TestWithAdaptation'`
Expected: FAIL (undefined: LegacyFlags).

- [ ] **Step 3: Implement `flags.go`**

Rules for `LegacyFlags`:

- **Origin:** check `CrewRideID != ""` first, then `GoalID != "" && HasPrefix(desc, GeneratedDescription)` gives plan, then `TestProtocol != ""` gives test, else rider.
- **AdaptedRules:** one entry per occurrence of `AdjustedMarker`, in order. An entry is `life_event` when the text right after `AdjustedMarker + " "` starts with `LifeNotePrefix`, else `legacy`.
- **Moves:** `MovedFrom` uses the existing `movedFromRE` (move it here from `scheduler`), taking the first match. `MovedBy`:
  - `life` if the text contains "by a life event";
  - else `rider` if it contains "Rescheduled by you";
  - else `auto` when there is a match.

`Equivalent` holds when origin, swapped, kept and both move fields are equal, `len(AdaptedRules)` is equal, and for each `i`, `IsLifeRule(a[i]) == IsLifeRule(b[i])`. That is the most the text can tell.

Add `LifeEvent Rule = "life_event"` and `Legacy Rule = "legacy"` to `why/why.go`'s const block, with a one-line comment each.

- [ ] **Step 4: Run them and confirm they pass**

Run: `go test ./apps/api/internal/workout/ ./apps/api/internal/scheduler/ ./apps/api/internal/why/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/api/internal/workout/flags.go apps/api/internal/workout/flags_test.go apps/api/internal/scheduler/adapt.go apps/api/internal/why/why.go
git commit -m "workout: Flags and the legacy marker parser"
```

---

### Task 2: Columns, backfill, read and write

**Files:**
- Modify: `apps/api/internal/workout/db.go`: the migration list near `addRouteLinkColumns()` (line ~544), `scanWorkout` (line ~1274), `CreateWorkout` (~1314), `UpdateWorkout` (~1374, the `UPDATE workouts SET` at ~1462), and the workout `SELECT` column list
- Modify: `apps/api/internal/workout/workout.go`: `Workout` gains `Flags Flags` and `EditedAt string`; `CreateWorkoutRequest` gains `Flags Flags`; `UpdateWorkoutRequest` gains `Flags *Flags`
- Test: `apps/api/internal/workout/flags_db_test.go`

**Interfaces:**
- Consumes: Task 1's `Flags`, `LegacyFlags`.
- Produces:
  - Columns on `workouts`:

    | Column | Type |
    |---|---|
    | `origin` | `TEXT NOT NULL DEFAULT ''` |
    | `adapted_rules` | `TEXT NOT NULL DEFAULT ''` |
    | `swapped` | `dialect.Boolean NOT NULL DEFAULT FALSE` |
    | `kept` | `TEXT NOT NULL DEFAULT ''` |
    | `moved_from` | `TEXT NOT NULL DEFAULT ''` |
    | `moved_by` | `TEXT NOT NULL DEFAULT ''` |
    | `edited_at` | `TEXT NOT NULL DEFAULT ''` |

  - `CreateWorkout` stores `req.Flags`. An empty `Origin` is stored as `rider`, which fails closed: an unmarked new row is never treated as plan-made.
  - `UpdateWorkout` replaces all flag columns when `req.Flags != nil`, and leaves them alone otherwise.
  - Every `UpdateWorkout` sets `edited_at` to the same timestamp as `updated_at`. So do any other statements in `internal/workout` that set `workouts.updated_at`; grep `UPDATE workouts`.
  - `func (d *DB) backfillFlags(ctx context.Context) error` runs from `UseDB` after the column migration. It processes rows `WHERE origin = ''` in chunks of 500, setting the six flag columns from `LegacyFlags(row)` and `edited_at = updated_at` where `updated_at <> created_at`. It is idempotent because every written row has a non-empty origin.

- [ ] **Step 1: Write the failing tests** (in `flags_db_test.go`, run under `TestEachEngine` the way neighbouring `*_db_test.go` files do)
  - `TestFlagsRoundTrip`: create with `Flags{Origin: plan, AdaptedRules: [readiness_rest, life_event], Swapped: true, Kept: crew, MovedFrom: "2026-10-01", MovedBy: auto}`, read it back, and expect it equal. Then update with `Flags: nil` and expect the flags unchanged. Then update with a new `Flags` and expect it replaced.
  - `TestCreateDefaultsOriginToRider`: a zero `Flags` reads back with `Origin: rider`.
  - `TestEditedAtFollowsUpdates`: a new row has `EditedAt == ""`. After any `UpdateWorkout`, `EditedAt != ""` and equals `UpdatedAt`.
  - `TestBackfillFlagsFromMarkers`:
    1. Insert raw rows with SQL, with `origin = ''`, using the Task 1 table's descriptions.
    2. Run `backfillFlags`.
    3. Expect every row to satisfy `Equivalent(LegacyFlags(row))` with the exact legacy rules.
    4. Run `backfillFlags` again and expect no change: no row with origin `''` remains and the rows are byte-identical.
  - `TestBackfillSetsEditedAtOnlyForEditedRows`: a row with `updated_at <> created_at` gets `edited_at = updated_at`; one with them equal stays `''`.

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./apps/api/internal/workout/ -run 'TestFlags|TestCreateDefaults|TestEditedAt|TestBackfill'`
Expected: FAIL (unknown field Flags).

- [ ] **Step 3: Implement**
  - Add `addFlagColumns()` alongside the existing pattern: ALTER statements that tolerate "duplicate column" and "already exists". `adapted_rules` is serialised as comma-joined rule ids and parsed back by splitting, with `""` meaning nil.
  - Add `backfillFlags` and call it from `UseDB` after `addFlagColumns`.
  - Thread the columns through the SELECT list, `scanWorkout`, the INSERT and the UPDATE.

- [ ] **Step 4: Run the package and confirm it passes**

Run: `go test ./apps/api/internal/workout/`, then `just docker-test` for PostgreSQL.
Expected: PASS on both engines, with no skips under `DOMESTIQUE_REQUIRE_POSTGRES=1`.

- [ ] **Step 5: Commit**

```bash
git add apps/api/internal/workout/
git commit -m "workout: adaptation flag columns with a one-off backfill from markers"
```

---

### Task 3: The parity check

**Files:**
- Modify: `apps/api/internal/workout/flags.go` (the switch) and `db.go` (the check in `CreateWorkout`/`UpdateWorkout`)
- Test: `apps/api/internal/workout/flags_db_test.go`

**Interfaces:**
- Produces:
  - `func EnableLegacyParityCheck()` sets a package-level `atomic.Bool`. It is idempotent and safe from parallel tests. It is called only from `_test.go` code and from `internal/testdb` (Task 5).
  - `var ErrFlagsDisagree = errors.New("workout: flags disagree with description markers")`.
  - While the switch is on, `CreateWorkout` and `UpdateWorkout` compute the row as it will be stored. If `!stored.Flags.Equivalent(LegacyFlags(stored))`, they return a wrapped `ErrFlagsDisagree` naming the workout id and the two values, before writing anything.
  - With the switch off, no check and no cost.

- [ ] **Step 1: Write the failing tests**
  - `TestParityCheckRejectsMismatch`:
    1. Call `EnableLegacyParityCheck()`.
    2. Create with description `GeneratedDescription`, `GoalID: "g"` and `Flags{Origin: rider}`, and expect `errors.Is(err, ErrFlagsDisagree)`.
    3. Expect no row to have been written.
  - `TestParityCheckAcceptsMatch`: the same row with `Origin: plan` succeeds. An update that appends `" " + AdjustedMarker + " Tired."` to the description fails without `Flags`. The same update with `Flags = current.WithAdaptation(why.ReadinessRest)` succeeds.
  - Put these in their own test function that does not call `t.Parallel()`, since the switch is process-wide; Task 5 turns it on for the api suite anyway.

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./apps/api/internal/workout/ -run TestParityCheck`
Expected: FAIL (undefined: EnableLegacyParityCheck).

- [ ] **Step 3: Implement the check**

- [ ] **Step 4: Run them and confirm they pass**

Run: `go test ./apps/api/internal/workout/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/api/internal/workout/
git commit -m "workout: test-only parity check between flags and description markers"
```

---

### Task 4: Pure packages produce flags

**Files:**
- Modify:
  - `apps/api/internal/scheduler/scheduler.go:255`, `:567`: requests carrying `GeneratedDescription` set `Flags{Origin: OriginPlan}`;
  - `apps/api/internal/adapter/session.go`: `Note(c)` users;
  - `apps/api/internal/crewplan/preview.go:404`, `:489`;
  - `apps/api/internal/lifeevents/place.go:613`, `:709` (`movedDescription`, `replaceMarked`) and `lifeevents.go:297` (`rampNotePrefix` becomes `workout.LifeNotePrefix`);
  - `apps/api/internal/workout/fillweek.go` if it builds requests itself.
- Test: next to each changed file (`*_test.go` in the same package)

**Interfaces:**
- Consumes: `workout.Flags`, `Flags.WithAdaptation`, the `why` rules.
- Produces:
  - Every `CreateWorkoutRequest` or `UpdateWorkoutRequest` these packages build, whose description gains a marker or the generated prefix, also carries the matching `Flags`.
  - Rules:
    - adapter: `c.Why.Rule`;
    - crew eases: `why.CrewRideEve` or `why.CrewRideAfter`, from the change's `Eve`/`After`;
    - life-event moves, eases and shortenings: `why.LifeEvent`.
  - A move also sets `MovedFrom` (kept when already set, matching "the first date is the one the scheduler must keep reading") and `MovedBy`.
  - `adapter.Change` gains nothing: the api writer (Task 5) builds `Flags` from `c.Why.Rule`.

- [ ] **Step 1: Write the failing tests**

For each package, one test asserting that `req.Flags.Equivalent(workout.LegacyFlags(workout.Workout{GoalID: …, Description: *req.Description}))` holds for every request a representative scenario produces:

| Test | Scenario |
|---|---|
| `TestWeekWorkoutsCarryPlanOrigin` | scheduler |
| `TestCrewEaseRequestsCarryFlags` | crewplan, eve and after |
| `TestLifeEventRequestsCarryFlags` | lifeevents: move, move-again, ease, shorten, illness ramp |

Each also checks the concrete rule, e.g. `Flags.AdaptedRules[len-1] == why.CrewRideEve`.

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./apps/api/internal/scheduler/ ./apps/api/internal/crewplan/ ./apps/api/internal/lifeevents/`
Expected: FAIL (Flags zero).

- [ ] **Step 3: Implement**

Each builder starts from `LegacyFlags` of the session *before* the change, then applies the change: `WithAdaptation(rule)`, set move fields, and so on. It never re-derives the result from the new text.

- [ ] **Step 4: Run them and confirm they pass**

Run: `go test ./apps/api/internal/...`, excluding `internal/api` for speed: `go test $(go list ./apps/api/internal/... | grep -v /internal/api$)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/api/internal/scheduler apps/api/internal/adapter apps/api/internal/crewplan apps/api/internal/lifeevents apps/api/internal/workout
git commit -m "planner packages: set adaptation flags next to the markers they write"
```

---

### Task 5: api writers set flags; parity on for the whole suite

**Files:**
- Modify:
  - `apps/api/internal/testdb/testdb.go`: `Path(t)` calls `workout.EnableLegacyParityCheck()` once;
  - every api writer that puts a marker, a "moved from" note or the generated prefix into a description:

    | File | Lines |
    |---|---|
    | `api/adaptation.go` | `:224`, `:312` |
    | `api/lifeblackout.go` | `:60` (rule `why.IllnessRamp`) |
    | `api/lifeevents.go` | `:515` (`Kept: life`) |
    | `api/crewgoing.go` | `:487`, `:510` (`Kept: crew`), `:362`, `:531` (`Origin: crew`) |
    | `api/togetheranswer.go` | `:221` (`why.RideTogetherMove`, new rule `"ride_together_move"`, plus `MovedFrom` = old date, `MovedBy: auto`), `:229` (restore the *previous* `Flags` with the date and description) |
    | `api/trainnow.go` | `:374` (`Swapped: true`) |
    | `api/alternates.go` | `:320`, `:369` (`Swapped`) |
    | `api/ftptests.go` | `:376` (`Origin: test`), `:635` |
    | `api/seasonplan.go` | `:211` |
    | `api/training.go` | `:994`, `:1303`, and the rider PUT at `:1375` (see Step 3) |

- Test: `apps/api/internal/api/flags_parity_test.go`

**Interfaces:**
- Consumes: Tasks 2–4.
- Produces: with the parity check on in every `testdb.Path` harness, the existing `internal/api` suite passes. That is the proof the dual write is complete.

- [ ] **Step 1: Turn the check on and see what fails**

Add the `EnableLegacyParityCheck()` call to `testdb.Path`.

Run: `go test ./apps/api/internal/api/ 2>&1 | grep -c ErrFlagsDisagree`
Expected: a non-zero count, and the failing tests name the writers still missing flags.

- [ ] **Step 2: Write the Review Focus tests** (in `flags_parity_test.go`, over real HTTP like `acceptance_test.go`)
  - `TestRiderDescriptionEditLeavesSessionToTheRider`: a plan session is edited through `PUT` with a new description lacking the prefix. Replan runs, and the session is untouched (same id, same steps) and `Flags.Origin == rider`.
  - `TestRiderDateMoveKeepsVacatedDayTaken`: a plan session's date is changed through `PUT`. A fill of that week creates nothing on the old date, and `Flags.MovedFrom` is the old date with `MovedBy == rider`.
  - `TestFailedTogetherAnswerRestoresFlags`: force `SetMemberStatus` to fail (the existing fake-store seam in the together tests). The session's date, description *and* flags equal their values before the answer.
  - `TestSwapRevertSwapKeepsSwapped`: swap, revert, swap again; `Flags.Swapped` follows the text each time.

- [ ] **Step 3: Implement the writers**

Each writer builds `Flags` from the workout it already loaded:

```go
f := wk.Flags.WithAdaptation(rule)
req.Flags = &f
```

and sets swap, keep or move fields where its marker says so.

The rider PUT at `training.go:1375` sets `Flags.Origin = rider` exactly when the final description no longer starts with `workout.GeneratedDescription` and the origin was `plan`. It sets the move fields when `rescheduleNote` (`:1400` area) appends "Rescheduled by you: moved from".

- [ ] **Step 4: Run the full suite and confirm it passes**

Run: `just check`, then `just docker-test`.
Expected: PASS, with no `ErrFlagsDisagree` anywhere and no PostgreSQL skips.

- [ ] **Step 5: Commit**

```bash
git add apps/api/internal/
git commit -m "api: every workout writer sets adaptation flags; parity check on in tests"
```

---

### Task 6: Readers switch to the columns

**Files:**
- Modify:

  | File | Change |
  |---|---|
  | `scheduler/adapt.go` | `RiderKept(w workout.Workout) bool` (was `description string`), `IsPlanSession`, `IsPlanMade`, `MovedFrom(w workout.Workout) (string, bool)` |
  | `alternates/alternates.go` | `:106` |
  | `crewplan/preview.go` | `:321-325` |
  | `crewplan/align.go` | `:228` |
  | `lifeevents/lifeevents.go` | `:308`, `Touched(w workout.Workout) bool` |
  | `lifeevents/place.go` | `:112`, `:344-359`, `:604` |
  | `api/lifeevents.go` | `:511` |
  | `api/crewgoing.go` | `:505` |
  | `api/seasonplan.go` | `:140`, `:254-260` (`untouchedPlanSession`: `wk.EditedAt == ""` replaces `UpdatedAt == CreatedAt`) |
  | `api/replan.go` | `:199`, `:288` |
  | `api/training.go` | `:259`, `:835`, `:1409` |
  | `api/ftptests.go` | `:337` |

- Test: existing suites, plus `scheduler/adapt_test.go`

**Interfaces:**
- Consumes: `Workout.Flags`, `Workout.EditedAt`.
- Produces predicates with the same names and meaning:
  - `IsPlanSession(w)` = `GoalID != "" && CrewRideID == "" && Origin == plan && !Adapted() && !Swapped`.
  - `IsGenerated(w)` = `IsPlanSession(w) && !RiderKept(w)`.
  - `IsPlanMade(w)` = `GoalID != "" && CrewRideID == "" && RouteSlug == "" && Origin == plan && !Swapped`.
  - `AutoReplaceable(w)` = `IsPlanMade(w) && !RiderKept(w)`.
  - `RiderKept(w)` = `Kept != KeptNone`.
  - `MovedFrom(w)` = `(MovedFrom, MovedFrom != "")`.
  - `lifeRamped(w)`: plan origin, `len(AdaptedRules) == 1 && IsLifeRule(AdaptedRules[0])`, not swapped.
  - `lifeevents.Touched(w)`: `MovedBy == life || Kept == life || any IsLifeRule(rule)`.

- [ ] **Step 1: Write the failing test**

`TestPredicatesIgnoreDescriptionText` in `scheduler/adapt_test.go`:

- a workout with `Flags{Origin: plan}` but description `"anything"` is `IsGenerated`;
- one with description `GeneratedDescription + " " + AdjustedMarker` but zero adaptations in `Flags` is still `IsGenerated`, which proves the text is no longer read;
- one with `Kept: crew` is not `IsGenerated` but is `IsPlanSession`.

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./apps/api/internal/scheduler/ -run TestPredicatesIgnoreDescriptionText`
Expected: FAIL.

- [ ] **Step 3: Switch every reader in the file list.** The parity check stays on, so existing tests still guard that text and columns agree.

- [ ] **Step 4: Confirm nothing reads markers for decisions any more**

Run: `grep -rnE 'Contains\(.*(AdjustedMarker|SwappedMarker|KeptMarker)|HasPrefix\(.*GeneratedDescription|MovedFrom\(.*Description|UpdatedAt == .*CreatedAt' apps/api/internal --include='*.go' | grep -v _test.go`
Expected: only `api/indoor.go:286`, which strips the note for display, and `workout/flags.go`, the legacy parser.

Run: `just check`, then `just docker-test`.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/api/internal/
git commit -m "planner: eligibility predicates read adaptation columns, not description text"
```

---

### Task 7: Docs

**Files:**
- Modify:
  - `AGENTS.md`: the "Architecture" bullet on `scheduler/adapt.go` predicates. Add that eligibility state lives in `workouts.origin/adapted_rules/swapped/kept/moved_from/moved_by`, that writers set `workout.Flags` next to any marker text, and that the parity check in `testdb` fails a write that disagrees. Also note that `edited_at` is "any edit" for now.
  - `docs/superpowers/specs/2026-10-18-adaptive-training-builder-design.md`: section 0's column table matches the Deviations table above; "at most once per rule per day" moves to section 2.

- [ ] **Step 1: Edit both files**

- [ ] **Step 2: Commit**

```bash
git add AGENTS.md docs/superpowers/specs/2026-10-18-adaptive-training-builder-design.md
git commit -m "docs: adaptation state columns"
```

## After this plan

- The next release stops writing the markers. That is when to delete `LegacyFlags` callers outside the backfill, delete the parity check, and keep the backfill until no deployment predates the columns.
- Sub-project 1 (load model) gets its own plan from the spec's section 1.
