# Load ledger — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep each CTL-basis week's TSS target, compute the debt every morning, repay it on the remaining days, carry a little into next week, and forgive the rest.

**Architecture:**
- A `load_ledger` table in `internal/workout` holds the per-week bookkeeping.
- A pure `internal/ledger` package does the arithmetic and plans the repayment actions.
- One small eligibility change: `load_repaid` does not lock a session.
- `internal/api` runs a once-a-morning pass inside the auto-schedule tick and exposes the figures on the week focus.

**Tech Stack:** Go stdlib, SQLite + PostgreSQL; Vue 3 + Nuxt UI.

**Spec:** `docs/superpowers/specs/2026-10-18-load-ledger-design.md`. The roadmap is `2026-10-18-adaptive-training-builder-design.md`, section 2.

## Global Constraints

- The ledger runs only for riders whose focus goal plans on `LoadBasis == "ctl"`. The hours basis and `adapter.Reconcile` are untouched.
- No repayment in recovery or taper weeks.
- **Repayment amounts:**
  - Only sessions dated tomorrow…Sunday.
  - Lengthening adds at most `min(30 %, 45 min)` per session and never goes past `HoursPerAvailableDay`.
  - At most one endurance→structured upgrade per week, only while debt is above 20 TSS.
  - Stop when debt ≤ 5 TSS.
- **Carry-over:** `rolled = min(own, 0.10 × target)`, where `own = max(0, lastDebt − last.carried_in)`. Everything else is forgiven. Recovery or taper weeks receive `rolled = 0`.
- **Eligibility:** `load_repaid` never locks a session. Any other adaptation makes it non-repayable. A session already carrying `load_repaid` is not lengthened again.
- **Never touched by repayment:** routed, test, crew and rider-kept sessions, and blackout days.
- Every write dual-writes flags and the description marker. The parity check (on in `internal/testdb`) must stay green.
- **New rider table:** `load_ledger` is in `riderTables` and purged by `Training.DeleteRider`. It must pass `TestEveryRiderKeyedTableIsRegistered`, `TestPurgeRemovesEveryRidersData`, the rename/merge registry tests and `TestEachEngine`.
- **Logs:** rider and counts only. Never TSS, watts or health values next to a rider.
- **Repo rules:** no new dependency; `gofmt` and `go vet` clean; DTOs in `api` and `apps/web/src/api/types.ts` change together. Never commit `.superpowers/` or `apps/web/components.d.ts`.

## Review Focus

1. **Two replicas, or two ticks, in the same morning.** Exactly one pass per rider per day: `last_pass_date` is a compare-and-set. Test in Task 4.
2. **A rider changes their available days mid-week.** The pass uses the week's stored target, and the lengthening cap uses the current `HoursPerAvailableDay`. Nothing panics, and no session goes past the day's hours. Test in Task 3.
3. **A lengthened session that readiness eases the next morning.** The ease applies, and the eased session is never lengthened again. Test in Task 4.
4. **An indoor endurance ride near the trainer cap.** Lengthening is recomputed through `keepIndoor`; `TSSAdded` may be 0 and the loop must not spin. Test in Task 3 (plan) and Task 4 (apply).
5. **The first week a rider ever has a ledger row** (no previous row). `carried_in` is 0 and nothing is forgiven. Test in Task 3.

---

### Task 1: `load_ledger` store and rider registry

**Files:**
- Modify: `apps/api/internal/workout/db.go` (table DDL in the schema list, migration in `UseDB`, `DeleteRider` list)
- Create: `apps/api/internal/workout/ledger_db.go`, `apps/api/internal/workout/ledger_db_test.go`
- Modify: `apps/api/internal/api/riderdata.go` (registry entry) and the seed list in `api/riderdata_test.go` (`riderSeeds`), plus the rename seeds (`allRenameSeeds`)

**Interfaces:**
- Produces, in package `workout`:

```go
type LedgerWeek struct {
    Rider, WeekStart        string
    TargetTSS, CarriedInTSS float64
    ForgivenTSS             float64
    LastPassDate            string
}
func (d *DB) GetLedgerWeek(ctx context.Context, rider, weekStart string) (LedgerWeek, bool, error)
// ClaimLedgerDay creates the row if absent (created=true) and sets last_pass_date = day,
// returning claimed=false when last_pass_date already equals day (compare-and-set; safe across replicas).
func (d *DB) ClaimLedgerDay(ctx context.Context, rider, weekStart, day string) (claimed, created bool, err error)
func (d *DB) OpenLedgerWeek(ctx context.Context, rider, weekStart string, target, carriedIn float64) error
func (d *DB) SetLedgerForgiven(ctx context.Context, rider, weekStart string, forgiven float64) error
```

- Registry entry: `"load_ledger": {Purged: true, Note: "Training.DeleteRider", Rename: renameRule{Columns: []string{"rider"}, Unique: true, With: []string{"week_start"}}}`.

- [ ] **Step 1: Write the failing tests**, on both engines through the package's `eachEngine`:
  - `TestLedgerClaimIsOncePerDay`: the first claim reports claimed and created; the same day reports not claimed; the next day reports claimed but not created.
  - `TestLedgerClaimRacesSafely`: two goroutines claim the same rider and day; exactly one wins.
  - `TestLedgerOpenAndForgiveRoundTrip`.
  - `TestDeleteRiderRemovesLedger`.
  - Also add the `riderSeeds` and rename seeds so the api registry tests cover the new table.
- [ ] **Step 2: Run them and confirm they fail.** Run `go test ./apps/api/internal/workout/ -run Ledger`; expect FAIL.
- [ ] **Step 3: Implement.** The claim is one statement where the engine allows it, an upsert guarded by `last_pass_date <> ?`, and reads `RowsAffected`. Created-vs-existing comes from a preceding `INSERT … ON CONFLICT DO NOTHING`, which `dbx` rebinds per engine.
- [ ] **Step 4: Run them and confirm they pass.** Run `go test ./apps/api/internal/workout/ ./apps/api/internal/api/ -run 'Ledger|Registered|Purge|Rename|Merge|UniqueIndex' -count=1`, then `just docker-test`. Expect PASS.
- [ ] **Step 5: Commit.** `git commit -m "workout: load ledger table"`

---

### Task 2: Eligibility and rules

**Files:**
- Modify:
  - `apps/api/internal/why/why.go`: add `LoadRepaid Rule = "load_repaid"`, `LoadRolled Rule = "load_rolled"` and `LoadForgiven Rule = "load_forgiven"`; `Title` cases "Load made up", "Load carried to next week" and "Load forgiven"; typed `LoadRepaidInputs{DebtBefore, TSSAdded float64}` with facts.
  - `apps/api/internal/workout/flags.go`: add `func (f Flags) EasedBy() []why.Rule`, which returns the adaptation rules other than `why.LoadRepaid`.
  - `apps/api/internal/scheduler/adapt.go`: `IsPlanSession` uses `len(w.Flags.EasedBy()) == 0` instead of `!Flags.Adapted()`. Also add `func StructuredZones(phase periodization.Phase, sport model.Sport, n int) []workout.Zone`, which exports what `structuredSlotsFor` returns as zones.
- Test: `scheduler/adapt_test.go`, `workout/flags_test.go`, `why/why_test.go`

**Interfaces:**
- Produces: `why.LoadRepaid`, `why.LoadRolled`, `why.LoadForgiven`, `why.LoadRepaidInputs`, `workout.Flags.EasedBy`, `scheduler.StructuredZones`.
- The parity check is unchanged: `load_repaid` is not a life rule, so its marker parses as `legacy`, which is Equivalent.

- [ ] **Step 1: Write the failing tests**:
  - `TestLoadRepaidDoesNotLockASession`: `Flags{Origin: plan, AdaptedRules: [load_repaid]}` is `IsGenerated`.
  - `TestAnyOtherRuleStillLocks`: `[load_repaid, readiness_rest]` is not.
  - `TestStructuredZonesMatchesTheScheduler`: for every phase × n from 0 to 7, the result equals the zones `WeekWorkouts` assigns.
  - `TestTitlesForLoadRules`.
- [ ] **Step 2: Run them and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and confirm they pass.** Run `go test ./apps/api/... -count=1`; expect PASS with no other test changed.
- [ ] **Step 5: Commit.** `git commit -m "scheduler: load repayment does not lock a session"`

---

### Task 3: Pure ledger package

**Files:**
- Create: `apps/api/internal/ledger/ledger.go`, `apps/api/internal/ledger/repay.go`, `apps/api/internal/ledger/ledger_test.go`, `apps/api/internal/ledger/repay_test.go`

**Interfaces:**
- Consumes: Task 2's `EasedBy` and `StructuredZones`; `workout.PlannedTSSOrHours`, `workout.PlannedTSS`, `scheduler.BuildEnduranceSession`, `scheduler.IsGenerated`, `scheduler.IsHardSession`, `workoutlib.LadderFor/Pick/Instantiate`.
- Produces:

```go
const (MinDebt = 5.0; UpgradeDebt = 20.0; RollFraction = 0.10; LengthenFraction = 0.30; LengthenMaxSeconds = 2700)
func Debt(target, carriedIn, completed, plannedRemaining float64) float64 // floored at 0
func Target(planTSS float64, availableDays []string, weekStart string, blackout map[string]bool) float64
func Settle(lastDebt, lastCarriedIn, thisTarget float64, thisReduced bool) (rolled, lastForgiven float64)
func Repayable(w workout.Workout, blackout map[string]bool) bool
type Action struct {
    WorkoutID string
    Kind      string // "lengthen" | "upgrade"
    Name      string
    Steps     []workout.WorkoutStep
    Zone      workout.Zone
    Level     float64
    TSSAdded  float64
}
type RepayInput struct {
    Debt     float64
    Week     []workout.Workout // every workout of the rider dated in the week
    From     string            // tomorrow, YYYY-MM-DD
    To       string            // Sunday
    Profile  workout.RiderProfile
    Phase    periodization.Phase
    Recovery bool
    Levels   map[string]float64 // "<sport>:<zone>" as scheduler uses
    Blackout map[string]bool
}
func PlanRepayment(in RepayInput) []Action
```

- **`Repayable`:** `scheduler.IsGenerated(w)`, no `RouteSlug`, no `TestProtocol`, no `CrewRideID`, not on a blackout day, and `load_repaid` not already in `w.Flags.AdaptedRules`.
- **`Target`:** `planTSS × (available weekdays of that week not in blackout) / len(availableDays)`; 0 days gives 0.
- **`PlanRepayment`:**
  - Returns nil for `Recovery`, `PhaseTaper` or `Debt ≤ MinDebt`.
  - **Lengthen:** repayable endurance-zone sessions in [From, To], longest first. The new length is `min(cur × 1.3, cur + 2700, HoursPerAvailableDay × 3600 if > 0)`, sized from `OutdoorSteps` when indoor. Steps come from `BuildEnduranceSession(newHours, isLong, sport, profile)`; `isLong` is true for the week's longest session or `scheduler.IsLongRideName`. Lengthen only while debt remains, and skip any session whose `TSSAdded ≤ 0.5`.
  - **Upgrade (at most one):** only when debt is still > `UpgradeDebt`, `FTPWatts > 0` and the week's structured count is below `len(StructuredZones(phase, sport, len(AvailableDays)))`. Pick the first repayable endurance session (not the long one) whose day−1 and day+1 have no `IsHardSession` workout. Its zone is `StructuredZones(...)[0]`, its rung `workoutlib.Pick(ladder, level, currentSeconds)`, and it is instantiated with `Instantiate`. Record `TSSAdded` = new − old planned TSS; the upgrade is skipped when that is ≤ 0.

- [ ] **Step 1: Write the failing tests**:

  | Test | Asserts |
  |---|---|
  | `TestDebt` | arithmetic; negative values floor at 0 |
  | `TestTargetScalesByBlackoutDays` | 4 days, 1 blacked out → 0.75 × target |
  | `TestSettle` | own debt rolls up to 10 %, the remainder and the unpaid carry are forgiven; a reduced week gives `rolled = 0`; `lastCarriedIn` is never re-rolled |
  | `TestSettleFirstWeek` | zero inputs give (0, 0) |
  | `TestRepayLengthensLongestFirstWithinCaps` | each session's cap is `min(+30 %, +45 min)` |
  | `TestRepayNeverExceedsDayHours` | `HoursPerAvailableDay` 1.5 → no session over 5400 s |
  | `TestRepayUpgradeOncePerWeekWithSpacing` | a hard neighbour blocks it; a full structured count blocks it |
  | `TestRepayLeavesEasedRoutedKeptTestCrewAndBlackoutAlone` | — |
  | `TestRepayNothingInRecoveryOrTaperOrUnderMinDebt` | — |
  | `TestRepayIndoorRideCappedByTrainerDoesNotLoop` | an indoor ride gives `TSSAdded ≤ 0.5`, is skipped, and the function returns |
  | `TestRepayAfterAvailabilityShrinks` | Review Focus 2: `HoursPerAvailableDay` now below a session's current length → no lengthening, no panic |

- [ ] **Step 2: Run them and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and confirm they pass.** Run `go test ./apps/api/internal/ledger/`; expect PASS.
- [ ] **Step 5: Commit.** `git commit -m "ledger: debt, carry-over and repayment plan"`

---

### Task 4: The morning pass

**Files:**
- Create: `apps/api/internal/api/ledgerpass.go`, `apps/api/internal/api/ledgerpass_test.go`
- Modify:
  - `apps/api/internal/api/autoschedule.go`: call `s.repayLoad(ctx)` between `AdaptWorkouts` (~:193) and `autoPushWorkouts` (~:194).
  - `apps/api/internal/api/morningsummary.go`: extract the morning gate (`sched.Last`, `FirstOfDay`, same date, before 12:00) into `func (s *Server) inMorningSlot(now time.Time) bool`, and use it in both places.

**Interfaces:**
- Consumes: Tasks 1–3; `reconciledPeriodizationPlan`; `lifeEventsFor`/`lifeevents.Blackout` (read the store directly from the week's Monday); `ListSessions`; `ListWorkouts`; `levelsFor`; `keepIndoor`; `recordAdjustment`; `adapter.WorkoutDone`.
- Produces: `func (s *Server) repayLoad(ctx context.Context)`. Per rider with a goal (dedupe as `AdaptWorkouts` does), focus goal first by date priority:
  - Skip unless `plan.LoadBasis == "ctl"`.
  - `ClaimLedgerDay(rider, weekStart, localToday)`. If not claimed, skip.
  - If created:
    - `target = ledger.Target(plan.Weeks[0].TargetTSS, …)`.
    - Settle last week: compute `lastDebt` from last week's row, completed and 0 remaining. Call `ledger.Settle` with `thisReduced = Recovery || Taper`, then `OpenLedgerWeek` and `SetLedgerForgiven(last)`.
    - Record `load_rolled` / `load_forgiven` only as log counts. No adjustments: they are week-level.
  - Compute the debt, then `PlanRepayment`, then apply each action. The update request is:
    - `UpdateWorkoutRequest{Name, Steps, Zone, Level, Description: desc + " " + AdjustedMarker + " Load made up: +N TSS.", Flags: &CurrentFlags(wk).WithAdaptation(why.LoadRepaid)}`;
    - passed through `keepIndoor` when `wk.Indoor`.
  - Update the workout first, then `recordAdjustment(LoadRepaid, LoadRepaidInputs)`; the why staleness check needs that order.
  - Errors are logged per rider at Warn (rider and stage only). The pass never fails the tick.

- [ ] **Step 1: Write the failing tests** (HTTP and season harness with FTP, snapshots and sessions so the plan is CTL-basis; drive the tick with a fixed clock at 07:00 local):

  | Test | Asserts |
  |---|---|
  | `TestMissedSessionIsRepaidOnTheNextMorning` | Tuesday's structured session unridden; Wednesday 07:00 tick → a later endurance session is longer, carries `load_repaid`, has a why "Load made up" |
  | `TestLedgerPassRunsOncePerDay` | a second tick at 07:30 changes nothing |
  | `TestLedgerPassOutsideTheMorningSlotDoesNothing` | 13:00 |
  | `TestReadinessCanStillEaseARepaidSession` | Review Focus 3, plus a later morning never lengthens it again |
  | `TestRecoveryWeekIsNotRepaid` | — |
  | `TestMondayPassRollsAndForgives` | seed last week's row and sessions; `carried_in ≤ 0.10 × target`, last week `forgiven` = the rest |
  | `TestHoursBasisRiderIsUntouched` | no FTP |
  | `TestLedgerLogsCarryNoLoadValues` | capture the logger; no digits after "TSS" in any line with the rider |
  | `TestRepaidIndoorRideStaysIndoor` | Review Focus 4 |

- [ ] **Step 2: Run them and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and confirm they pass.** Run `go test ./apps/api/... -count=1`, then `just docker-test`. Expect PASS, including every existing adaptation, morning summary and tick test unchanged.
- [ ] **Step 5: Commit.** `git commit -m "api: morning load repayment pass"`

---

### Task 5: Week focus figures and the strip

**Files:**
- Modify:
  - `apps/api/internal/api/trainingweek.go`: `weekFocusDTO` gains `DebtTSS`, `CarriedTSS` and `ForgivenTSS float64` with JSON `debtTss`, `carriedTss` and `forgivenTss`, all `omitempty`, filled for the current week from the ledger row and the same debt computation as Task 4. Extract a shared `func (s *Server) weekDebt(ctx, rider, weekStart, row workout.LedgerWeek) (float64, error)` in `ledgerpass.go` and use it in both places.
  - `apps/web/src/api/types.ts` `WeekFocus`
  - `apps/web/src/utils/today.ts` (a `weekLedgerNote(focus): string` returning `""`, `"+12 TSS carried"`, `"8 forgiven"` or `"+12 TSS carried · 8 forgiven"`, all integers)
  - `apps/web/src/components/plan/WeekStrip.vue` (appended after the load label as `· {note}` when non-empty)
  - `apps/web/tests/today.test.mjs`

- [ ] **Step 1: Write the failing tests**:
  - Go: `TestWeekFocusShowsCarriedAndForgiven`.
  - Web: the four `weekLedgerNote` cases.
- [ ] **Step 2: Run them and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and confirm they pass.** Run `go test ./apps/api/... -count=1`, `npm test` in `apps/web`, and the web typecheck once more with `components.d.ts` moved aside (restore it after). The known `@grafana/faro-*` and `LifeEventModal.vue:360` errors are local-environment only. Expect PASS.
- [ ] **Step 5: Commit.** `git commit -m "plan: show carried and forgiven load on the week"`

---

### Task 6: Docs

**Files:**
- Modify:
  - `AGENTS.md`: one paragraph under "The whole season is planned" covering:
    - the morning ledger pass (CTL basis only, once per rider per day, after adaptation and before push, under the lock);
    - the debt formula;
    - the carry and forgive rules;
    - `load_repaid` not locking a session;
    - `load_ledger` in `riderTables`.
  - The spec status line, set to implemented.
  - The roadmap row for sub-project 2, set to "(shipped; green-day push moves to 3)".

- [ ] **Step 1: Edit.**
- [ ] **Step 2: Commit.** `git commit -m "docs: load ledger"`
