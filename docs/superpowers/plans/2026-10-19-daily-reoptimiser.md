# Daily re-optimiser — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On a Caution or Rest morning, swap today's hard session with a later easy session in the same week, so the hard work keeps its place in the week instead of being eased away. Readiness then eases the easy session that is now today's.

**Architecture:**
- A pure `internal/weekplan` package holds one hard-day spacing rule and the swap planner.
- One new why rule, `reshuffle_readiness`. `Flags.EasedBy` ignores it, so it does not lock a session.
- The adapter's missed-session make-up adopts the spacing rule.
- `internal/api` gets a morning pass, `reoptimise`, placed in `AutoScheduleTick` before `AdaptWorkouts`.

**Tech Stack:** Go stdlib; SQLite and PostgreSQL.

**Spec:** `docs/superpowers/specs/2026-10-19-daily-reoptimiser-design.md`.

## Global Constraints

- **When the pass runs:** in the morning slot only (`inMorningSlot`), inside the tick's scheduling lock, before `AdaptWorkouts`.
- **Verdict:** the pass acts only on Caution or Rest.
- **Today's session:** must be swappable, `scheduler.IsHardSession`, and not done (`adapter.WorkoutDone`).
- **Candidate sessions:** swappable, not hard, dated tomorrow through Sunday of this week.
- **Swappable** means all of:
  - `scheduler.IsGenerated`;
  - no route, no `TestProtocol`, no `CrewRideID`;
  - cycling;
  - `reshuffle_readiness` not already in `Flags.AdaptedRules`;
  - no manual push record.
- **Target day:**
  - not in `lifeevents.NoTestDays(events)`;
  - no `weekplan.Hard` session on the day before or after, with the two swapped sessions counted at their new dates;
  - not the eve of an FTP test or a crew ride.
- **Day preference:** earliest allowed day on or after the day after tomorrow. Tomorrow only when `outlookTomorrow` gives Ready. Ties go to the longer planned seconds, then the lower id.
- **Writes:**
  - Both sessions move: Date, plus the description `+ " " + AdjustedMarker + " moved from <date> — swapped with an easier day."`.
  - Flags are `CurrentFlags(w).WithAdaptation(why.ReshuffleReadiness)`. `MovedFrom` keeps an existing value, otherwise it takes the old date. `MovedBy` is auto.
  - Record the adjustment after each write.
  - If the second write fails, restore the first.
  - The parity check stays green.
- **Locking:** `Flags.EasedBy` ignores `reshuffle_readiness`.
- **Logs:** counts only. Never a verdict, reason or health value next to a rider.
- **Repo rules:** no new dependency; `gofmt`/`go vet` clean; never commit `.superpowers/`.

## Review Focus

1. **A Rest morning where the only later easy day is tomorrow, and tomorrow's outlook is Caution.** No swap happens, and today is eased as before. Test in Task 1.
2. **Today's hard session is indoor.** Swapping keeps each session's own indoor state, because only the dates move. Test in Task 3.
3. **Today's hard session was already pushed (auto) this morning by an earlier tick.** The push pass in the same tick withdraws the moved copy, now dated in the future, and pushes today's easy session. Test in Task 3.
4. **A rider who checked in at 13:00 with legs = 1.** There is no swap outside the morning slot. Adaptation still eases at the next tick. Test in Task 3.
5. **A swap whose first write lands and second fails.** The first session is back on its original date with its original description and flags. Test in Task 3.

---

### Task 1: `internal/weekplan`

**Files:**
- Create: `apps/api/internal/weekplan/weekplan.go`, `apps/api/internal/weekplan/weekplan_test.go`

**Interfaces:**
- Produces:

```go
func Hard(w workout.Workout) bool // scheduler.IsHardSession(w) || w.TestProtocol != "" || w.CrewRideID != ""
func HardNeighbour(date string, week []workout.Workout, ignore ...string) bool // a Hard session (not in ignore ids) on date−1 or date+1
func EveOfFixed(date string, week []workout.Workout) bool // a test or crew ride on date+1
type SwapInput struct {
    Today          string            // YYYY-MM-DD
    Sunday         string
    Week           []workout.Workout // the rider's workouts Mon–Sun of this week
    Done           map[string]bool   // workout id → already ridden
    ManualPushed   map[string]bool   // workout id → has a manual Garmin copy
    NoTestDays     map[string]bool
    TomorrowReady  bool
}
type Swap struct{ HardID, EasyID, HardTo, EasyTo string }
func Swappable(w workout.Workout, manualPushed map[string]bool) bool
func PlanSwap(in SwapInput) (Swap, bool)
```

`PlanSwap` does not take the verdict; the caller checks it.

- [ ] **Step 1: Write the failing tests** (table-driven):
  - `TestHardIncludesTestsAndCrewRides`
  - `TestHardNeighbourIgnoresGivenIDs`
  - `TestPlanSwapPrefersTheDayAfterTomorrow`
  - `TestPlanSwapUsesTomorrowOnlyWhenReady` (Review Focus 1)
  - `TestPlanSwapSkipsNoTestDays`
  - `TestPlanSwapNeverCreatesBackToBackHardDays` (count the swapped sessions at their new dates)
  - `TestPlanSwapSkipsTheEveOfATestOrCrewRide`
  - `TestPlanSwapTieBreaksOnLongerThenID`
  - `TestNothingToSwapWhenTodayIsNotHardOrDoneOrNotSwappable`
  - `TestSwappableExcludesRoutedTestCrewKeptRunningManualAndAlreadyReshuffled`
- [ ] **Step 2: Run them and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and confirm they pass.** Run `go test ./apps/api/internal/weekplan/`; expect PASS.
- [ ] **Step 5: Commit.** `git commit -m "weekplan: hard-day spacing rule and the readiness swap planner"`

---

### Task 2: The rule, and the missed-session make-up adopts the spacing rule

**Files:**
- Modify: `apps/api/internal/why/why.go`. Add `ReshuffleReadiness Rule = "reshuffle_readiness"`, its title "Swapped with an easier day", and `ReshuffleInputs{From, To, Verdict string}` with facts.
- Modify: `apps/api/internal/workout/flags.go`. `EasedBy` also ignores `ReshuffleReadiness`; update its comment.
- Modify: `apps/api/internal/adapter/session.go`. When the session being made up is `weekplan.Hard`, `nextFreeDay` and `nextEasySlot` skip any day where `weekplan.HardNeighbour(day, week, w.ID, slot.ID)` holds.
- Modify: `apps/api/internal/api/morningsummary.go`. Add `ReshuffleReadiness` to `easingRules`.
- Tests:
  - `adapter/session_test.go`: `TestAMissedHardSessionIsNotMadeUpNextToAnotherHardDay`. The existing missed-move tests stay green, or are listed in the report if their fixture placed back-to-back hard days.
  - `scheduler/adapt_test.go`: `TestReshuffleDoesNotLockASession`.
  - `why/why_test.go`: the title and facts.

- [ ] **Step 1: Write the failing tests.**
- [ ] **Step 2: Run them and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and confirm they pass.** Run `go test ./apps/api/... -count=1`; expect PASS.
- [ ] **Step 5: Commit.** `git commit -m "adapter: make-up moves respect hard-day spacing; reshuffle rule"`

---

### Task 3: The morning pass

**Files:**
- Create: `apps/api/internal/api/reoptimise.go`, `apps/api/internal/api/reoptimise_test.go`
- Modify: `apps/api/internal/api/autoschedule.go`. Call `s.reoptimise(ctx)` between the `planSeason` loop and `AdaptWorkouts`.

**Interfaces:**
- Consumes:
  - Tasks 1–2.
  - `inMorningSlot`, `assessReadiness`, `outlookTomorrow`, `lifeEventsFor`.
  - `lifeevents.NoTestDays`, `adapter.WorkoutDone`.
  - The push records store, to find `PushOriginManual`. Locate how `workoutpush.go` reads them.
  - `recordAdjustment`.
- Produces: `func (s *Server) reoptimise(ctx context.Context)`. For each rider with a goal (dedupe as `AdaptWorkouts` does):
  - Skip if the verdict is not Caution or Rest.
  - Build `SwapInput` and call `PlanSwap`.
  - Apply the swap: update the hard session, then the easy one, compensating on failure. Then record two adjustments.
  - On an error, log a Warn with rider and stage only.

- [ ] **Step 1: Write the failing tests** (HTTP harness with FTP, sessions and wellness, as in `ledgerpass_test.go`/`readiness_v2_test.go`; fixed clock):

  | Test | What it asserts |
  |---|---|
  | `TestCautionMorningSwapsTodaysHardSessionWithALaterEasyDay` | The dates are swapped. Today's session is the shortened easy ride (readiness eased it in the same tick). The hard session's why title is "Swapped with an easier day". The fake Garmin received today's easy session. |
  | `TestReoptimiseIsIdempotentAcrossTicks` | — |
  | `TestReoptimiseOutsideTheMorningSlotDoesNothing` | Review Focus 4 |
  | `TestReadyMorningSwapsNothing` | — |
  | `TestAManuallyPushedSessionIsNeverMoved` | — |
  | `TestSwapKeepsEachSessionsIndoorState` | Review Focus 2 |
  | `TestAMovedAutoCopyIsWithdrawnAndTodayIsPushed` | Review Focus 3 |
  | `TestAFailedSecondWriteRestoresTheFirst` | Review Focus 5. Use a store seam or a fault-injecting wrapper; follow how `togetheranswer`'s restore is tested. |
  | `TestReoptimiseLogsCarryNoVerdict` | — |

- [ ] **Step 2: Run them and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and confirm they pass.** Run `go test ./apps/api/... -count=1`, then `just docker-test`. Expect PASS, with the tick, ledger and readiness tests unchanged.
- [ ] **Step 5: Commit.** `git commit -m "api: morning readiness swap before easing"`

---

### Task 4: Docs

**Files:**
- Modify:
  - `AGENTS.md`: add one paragraph under "The whole season is planned", after the ledger paragraph. It covers:
    - the morning swap and where it sits in the tick (before adaptation);
    - what "swappable" means;
    - the spacing rule in `internal/weekplan`, now also used by the missed make-up;
    - that `reshuffle_readiness` does not lock a session.
  - The spec status line (implemented) and the roadmap row for sub-project 4 ("(shipped, narrowed)").

- [ ] **Step 1: Edit.**
- [ ] **Step 2: Commit.** `git commit -m "docs: daily re-optimiser"`
