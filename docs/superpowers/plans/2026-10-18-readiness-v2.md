# Readiness v2 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Readiness should judge a rider against their own HRV and sleep ranges, add an optional morning check-in, ease only on two independent amber signals or one severe signal, reach endurance rides, and add a Push verdict. On a Push day the ledger raises today's session when load is owed.

**Architecture:**
- `internal/readiness` gets a pure `Evaluate(Input)` that classifies every signal by source and severity. `AssessWithSurvey` becomes a wrapper around it.
- `internal/workout` gets a `daily_checkins` store.
- `internal/adapter` gains a shortening path for endurance rides.
- `internal/ledger` and `api/ledgerpass.go` gain the today-only green push.
- The web gets a check-in card, the `'push'` verdict and new labels.

**Tech Stack:** Go stdlib, SQLite + PostgreSQL; Vue 3 + Nuxt UI.

**Spec:** `docs/superpowers/specs/2026-10-18-readiness-v2-design.md`. Its roadmap is `2026-10-18-adaptive-training-builder-design.md` §3.

## Global Constraints

- **Signals:**
  - Every signal has a source in `hrv|sleep|resting_hr|garmin|load|subjective` and a severity `amber|severe`.
  - The thresholds are exactly the spec's signal table:
    - HRV band: ≥ 14 nights in the last 60. Baseline is mean ± 0.5 SD of ln(HRV) over the 60 nights before today. The 7-night mean includes today.
    - Sleep debt: ≥ 2.5 h over 3 nights against a 28-night median of ≥ 7 nights.
    - Resting HR: +4 is amber, +7 is severe.
    - Garmin readiness: LOW or 25–49 is amber, POOR or 1–24 is severe.
    - Load: ACWR ≥ 1.5 is amber. TSB < −30 is severe.
    - Check-in: a score of 2 is amber. Legs = 1 is severe.
- **Verdict:** Rest on any severe signal. Caution on amber signals from ≥ 2 distinct sources. Push per the spec. Otherwise Ready.
- **Endurance and long rides, today only:** Caution means 20 % shorter, but at least 1800 s. Rest means a 45-minute recovery ride. Hard-session rules are unchanged.
- **Green push:** only on a Push verdict with ledger debt > `ledger.MinDebt`, on the CTL basis with FTP. Today only, at most once a day, through the ledger claim.
  - A structured session goes one rung up, capped at `floor(level)+1`.
  - An endurance session is lengthened with the repayment caps.
  - Rule `green_push`, which `EasedBy` ignores, as it does `load_repaid`.
- **Check-in:**
  - Scores are 1–5, with 0 meaning not answered. The note is ≤ 500 characters.
  - The rider comes from the session, and the date is the training timezone's today.
  - The table is registered in `riderTables`.
  - The note is never logged and never emailed.
- **Privacy:** no health value, reason, verdict or check-in value in a log line next to a rider. The email carries the verdict word only.
- **Repo rules:**
  - Parity dual write stays green.
  - No new dependency. `gofmt`/`go vet` clean.
  - DTOs in `api` and `types.ts` change together.
  - Never commit `.superpowers/` or `apps/web/components.d.ts`.

## Review Focus

1. **A rider with 10 nights of HRV.** No band yet, so Garmin status rules apply and Push is impossible. Test in Task 2.
2. **A rider whose only signal is one amber reading** (e.g. resting HR +4). The verdict is Ready, the reason is listed, and nothing is eased. Today this is Caution, so the existing tests change. Test in Task 2.
3. **A Wahoo-only rider** (no wellness rows) who answers the check-in with legs = 1. The verdict is Rest from the check-in alone. Test in Tasks 2 and 3.
4. **A check-in saved after the morning pass already ran.** The Plan and Today readiness reflect it at once. The automatic easing only happens at the next adaptation tick (every 30 minutes), and the green push only on the next morning. Test in Task 3.
5. **A Push morning where today's session is indoor.** The raised session stays indoor and its gain is priced after `keepIndoor`. Test in Task 4.

---

### Task 1: Check-in store, endpoints, registry

**Files:**
- Modify: `apps/api/internal/workout/db.go` (DDL, `DeleteRider`)
- Create: `apps/api/internal/workout/checkin_db.go`, `apps/api/internal/workout/checkin_db_test.go`
- Create: `apps/api/internal/api/checkin.go`, `apps/api/internal/api/checkin_test.go`
- Modify: `apps/api/internal/api/server.go` (routes), `api/riderdata.go` (entry), and `api/riderdata_test.go` (`riderSeeds`)

**Interfaces:**
- Produces:

```go
type CheckIn struct{ Rider, Date string; Legs, Mood, SleepQuality int; Note string; UpdatedAt string }
func (d *DB) SaveCheckIn(ctx context.Context, c CheckIn) error         // upsert on (rider,date)
func (d *DB) GetCheckIn(ctx context.Context, rider, date string) (CheckIn, bool, error)
func (d *DB) ListCheckIns(ctx context.Context, rider, sinceDate string) ([]CheckIn, error)
```

- Registry entry: `"daily_checkins": {Purged: true, Note: "Training.DeleteRider", Rename: renameRule{Columns: []string{"rider"}, Unique: true, With: []string{"date"}}}`.
- HTTP:
  - `GET /api/training/checkin` returns `{"date","legs","mood","sleepQuality","note"}`, with zeros when unanswered.
  - `PUT` takes the same body without `date`.
    - It returns 400 for a score outside 0–5 or a note over 500 characters.
    - Rider and date come from the session and `localToday`.
  - Both need `PermManageTraining`.
  - DTO `checkInDTO` in `api`, mirrored in `apps/web/src/api/types.ts` as `CheckIn`.

- [ ] **Step 1: Write the failing tests.**
  - Store, on both engines: round trip, upsert, list since date, delete rider.
  - API:
    - `TestCheckInRoundTrip`
    - `TestCheckInIgnoresABodyDateAndRider`
    - `TestCheckInRejectsOutOfRangeScoresAndLongNotes`
    - `TestCheckInIsOwnerOnly`
    - `TestCheckInNoteIsNeverLogged`: capture the logger and assert the note text is absent.
  - Registry tests cover the new table through its seed.
- [ ] **Step 2: Run the tests and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run `go test ./apps/api/... -count=1`, then `just docker-test`.
  - Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "checkin: morning check-in store and endpoints"`

---

### Task 2: `readiness.Evaluate`

**Files:**
- Modify: `apps/api/internal/readiness/readiness.go` (add `Push`, `Source`/`Severity` on signals, `Input`, `Evaluate`; `AssessWithSurvey` delegates)
- Create: `apps/api/internal/readiness/v2.go`, `apps/api/internal/readiness/v2_test.go`
- Modify: `apps/api/internal/why/why.go` (`why.Signal` gains `Source, Severity string` with `json:",omitempty"`)
- Modify (expected changes): the existing tests in `readiness_test.go`, `signals_test.go` and `survey_test.go` that assert single-amber → Caution. Change each to Ready with the reason still listed, and list every changed test name in the report.

**Interfaces:**
- Produces:

```go
const Push Verdict = "push"
type CheckIn struct{ Legs, Mood, SleepQuality int } // 0 = unanswered
type Input struct {
    Today    Day
    History  []Day      // any order; up to 67 days before today
    TSB      *float64
    TSBDate  string
    Loads    []Load
    Survey   []SurveyDay
    CheckIn  *CheckIn   // nil = not answered today
    Now      time.Time
}
func Evaluate(in Input) Assessment
func HRVBand(history []Day, today Day) (mean7, baseMean, baseSD float64, ok bool)
```

- `Assess` and `AssessWithSurvey` keep their signatures and call `Evaluate`, with `CheckIn` nil.

- [ ] **Step 1: Write the failing tests** in `v2_test.go`. There is one table test per signal-table row, at each boundary, plus:
  - `TestTwoSourcesMakeCaution`: sleep amber plus resting-HR amber.
  - `TestTwoAmbersFromOneSourceAreReady`: sleep score 55 plus sleep debt.
  - `TestSingleAmberIsReadyWithTheReasonListed`: Review Focus 2.
  - `TestBandNeedsFourteenNights`: Review Focus 1.
  - `TestHRVBelowBandTwoDaysIsRest`.
  - `TestCheckInLegsOneAloneIsRest`: Review Focus 3, with no wellness rows.
  - `TestPushNeedsEveryCondition`: one case per missing condition gives Ready.
  - `TestUnansweredCheckInIsNoSignal`.
- [ ] **Step 2: Run the tests and confirm they fail.**
- [ ] **Step 3: Implement.**
  - Reuse the existing `restingHRBaseline`, `acwr`, `tsbFresh` and `surveyCaution` helpers.
  - Classify each existing finding into source and severity.
  - Then update the expectations of the changed existing tests.
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run `go test ./apps/api/internal/readiness/ ./apps/api/internal/why/`.
  - Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "readiness: own-range HRV and sleep, two-source easing, push verdict"`

---

### Task 3: Wire readiness v2 into the app

**Files:**
- Modify: `apps/api/internal/api/adaptation.go`.
  - `readinessHistoryDays` goes to 67.
  - `assessReadinessWith` reads today's check-in, sets `Input.CheckIn` and calls `readiness.Evaluate`.
  - The forecast path (`assessReadinessForForecast`) keeps the check-in out, as it keeps the survey out.
- Modify: `apps/api/internal/adapter/session.go`.
  - Caution: on an endurance or long `IsGenerated` session today, not done and not on a blackout day, produce `Change{ShortenSeconds: max(0.8 × planned, 1800)}`. The rule is `ReadinessCaution`.
  - Rest: produce `Change{RecoveryRide: true}`. The rule is `ReadinessRest`.
  - Hard sessions are untouched.
  - The `Change` struct gains `ShortenSeconds float64` and `RecoveryRide bool`.
- Modify: `apps/api/internal/api/adaptation.go` `applyChange`.
  - `ShortenSeconds` becomes `BuildEnduranceSession(hours, isLong, …)`, keeping the name.
  - `RecoveryRide` becomes `BuildEnduranceSession(0.75, false, …)`, named "Recovery ride".
  - Both go through `keepIndoor`. Dual write with the existing marker and flags.
- Modify the verdict consumers:
  - `api/alternates.go` `readinessIsLow`: true only for Caution or Rest.
  - `trainnow`: Push is treated as Ready.
  - `api/crewadvice.go`: Push is treated as Ready.
  - `morningsummary/compose.go` `verdictWord`: Push reads "Good to push". Add the check-in line `Answer today's check-in: <AppURL>/today` to the footer when `AppURL` is set.
- Tests:
  - `adapter/session_test.go`: `TestCautionShortensTodaysEnduranceRide`, `TestRestSwapsTodaysLongRideForARecoveryRide`, `TestHardSessionRulesUnchanged`.
  - `api/readiness_v2_test.go`:
    - `TestCheckInLegsOneEasesTodaysEnduranceRideOnTheNextTick` (Review Focus 3 and 4)
    - `TestSingleAmberEasesNothing`
    - `TestReadinessEndpointShowsAPushVerdict`
    - `TestShortenedSessionStaysIndoor`
  - `morningsummary/compose_test.go`: `TestPushIsInWords`, `TestCheckInLinkInFooter`.
  - Update existing api readiness tests whose fixture relied on single-amber Caution, and list them in the report.

- [ ] **Step 1: Write the failing tests.**
- [ ] **Step 2: Run the tests and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run `go test ./apps/api/... -count=1`, then `just docker-test`.
  - Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "readiness v2: check-in, endurance easing, push in every consumer"`

---

### Task 4: Green-day push

**Files:**
- Modify: `apps/api/internal/why/why.go`. Add `GreenPush Rule = "green_push"` with the title "Pushed on a good day" and `GreenPushInputs{DebtBefore, TSSAdded float64}` with facts.
- Modify: `apps/api/internal/workout/flags.go`. `EasedBy` also ignores `why.GreenPush`.
- Modify: `apps/api/internal/ledger/repay.go`.
  - Add `func PlanGreenPush(in RepayInput, today string) *Action`.
  - It acts only on a repayable session dated `today`.
  - A structured session gets the next rung of its own zone's ladder, from `workoutlib.LadderFor` (Kind `"push"`). The ladder level is capped at `floor(level)+1`.
  - An endurance session is lengthened with the repayment caps (Kind `"push"`).
  - It returns nil for recovery or taper, for `Debt ≤ MinDebt`, or when no session qualifies.
- Modify: `apps/api/internal/api/ledgerpass.go` `repayRider`.
  - After the claim, when today's verdict is `readiness.Push` (use `assessReadiness`), apply `PlanGreenPush` before tomorrow's repayment.
  - Use the same `applyRepayment` path with rule `GreenPush`, and subtract its gain from the debt.
  - A failure is a Warn and does not stop repayment.
- Tests:
  - `ledger/repay_test.go`:
    - `TestGreenPushRaisesTodaysStructuredSessionOneRung`
    - `TestGreenPushCappedAtLevelPlusOne`
    - `TestGreenPushLengthensTodaysEnduranceRide`
    - `TestGreenPushNothingWithoutDebtOrInReducedWeeks`
  - `api/ledgerpass_test.go`:
    - `TestPushMorningRaisesTodayAndThePushSendsTheRaisedSession`, using the fake Garmin connector; the workout pushed is the raised one.
    - `TestPushMorningWithoutDebtChangesNothing`
    - `TestGreenPushedIndoorSessionStaysIndoor` (Review Focus 5)
  - `scheduler/adapt_test.go`: `TestGreenPushDoesNotLockASession`.

- [ ] **Step 1: Write the failing tests.**
- [ ] **Step 2: Run the tests and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run `go test ./apps/api/... -count=1`, then `just docker-test`.
  - Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "ledger: green-day push on a push verdict"`

---

### Task 5: Web

**Files:**
- Modify: `apps/web/src/api/types.ts`. `ReadinessVerdict` gains `'push'`. Add the `CheckIn` type, and give `why.Signal` the optional `source` and `severity` fields where it is mirrored.
- Modify: `apps/web/src/api/client.ts`. Add `checkIn()` and `saveCheckIn(c)`.
- Create: `apps/web/src/components/today/CheckInCard.vue`.
  - Three rows (Legs, Mood, Sleep), each a 1–5 `URadioGroup` or button group, plus an optional `UTextarea` note limited to 500 characters and a Save button.
  - It emits `saved`.
  - Its labels:
    - Legs: 1 "Dead", 3 "Normal", 5 "Fresh".
    - Mood: 1 "Low", 5 "Great".
    - Sleep: 1 "Awful", 5 "Great".
- Modify: `apps/web/src/pages/TodayPage.vue`. Show the card above the readiness chip, and re-load readiness on `saved`.
- Modify: `apps/web/src/components/plan/ReadinessChip.vue`. Add a `push` entry to `CONFIG`, labelled "Good to push", with the success colour from theme tokens.
- Modify: `apps/web/src/utils/today.ts`. `todayHeadline` handles `push`. Add a pure `checkInComplete(c)` helper.
- Tests: `apps/web/tests/today.test.mjs` covers `todayHeadline('push')` and `checkInComplete`.

- [ ] **Step 1: Write the failing tests.**
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Run the tests, the typecheck and a browser check.**
  - Run `npm test`.
  - Run the typecheck, once more with `components.d.ts` moved aside (then restore it). The known local errors `@grafana/faro-*` and `LifeEventModal.vue:360` are expected.
  - Check in a browser: run `just api` with auth none on SQLite, then `just web`. Save a check-in, see readiness re-load, and check both light and dark. Report what you saw.
- [ ] **Step 4: Commit.** `git commit -m "web: morning check-in and the push verdict"`

---

### Task 6: Docs

**Files:**
- Modify: `AGENTS.md`.
  - Add one short paragraph on readiness v2 and the check-in, in a new subsection "Readiness" near "Morning summary". Cover the sources and severities, the two-source rule, Push, endurance easing, the check-in privacy rules, and `daily_checkins` in `riderTables`.
  - In the ledger paragraph, mention green push.
  - In "Morning summary", mention the check-in link.
- Modify: the spec status line, and the roadmap row for sub-project 3 ("(shipped)").

- [ ] **Step 1: Edit.**
- [ ] **Step 2: Commit.** `git commit -m "docs: readiness v2"`
