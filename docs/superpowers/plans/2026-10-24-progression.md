# Progression (5b) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:**
- A durability (endurance) level that long rides move, measured by aerobic decoupling, and that the long ride grows with.
- Levels decay for zones left untrained.
- An FTP drop recalibrates levels upward, mirroring a rise.

**Architecture:**
- `rideanalysis` computes `decoupling_pct` at analysis time, and it is stored on `session_analyses`.
- `progression` gains pure rules: `DurabilityDelta`, `Decay`, and symmetric `RecalibrationDelta`.
- The api applies durability moves after ride analysis, runs a once-a-week decay pass in the morning slot, and recalibrates on drops.
- The scheduler weights the long slot by the endurance level.

**Tech Stack:** Go stdlib, SQLite and PostgreSQL.

**Spec:** `docs/superpowers/specs/2026-10-24-intensity-and-progression-design.md`, section 5b.

## Global Constraints

- **Decoupling:**
  - Computed for cycling rides with ≥ 5400 s of samples, power and HR both present on ≥ 90 % of samples, and ride IF ≤ 0.85.
  - The first 600 s are skipped (warm-up). The rest is split into two equal halves by sample count.
  - `EF = NP(half) / avgHR(half)`, and `decoupling_pct = (EF1 − EF2) / EF1 × 100`, rounded to 0.1.
  - Otherwise it is not computed (stored as NULL; Go `*float64` nil).
- **Endurance zone:**
  - `progression.Initial` adds `endurance` for **cycling only**, at the same experience-based starting value as the other zones.
  - `levelsFor` also seeds any `Initial` zone missing for a rider who already has rows ("Starting level from your experience").
  - The endurance level is **excluded from FTP recalibration** because it is not FTP-relative.
- **Durability move:**
  - Only on a ride matched to a generated cycling long ride (`IsLongRideName`, zone endurance), on its first analysis, as levels move today.
  - `DurabilityDelta(decoupling *float64, durationRatio float64) (float64, bool)`:

    | Condition | Delta |
    |---|---|
    | duration ratio < 0.8 | −0.2 |
    | otherwise, decoupling nil | no move (`false`) |
    | decoupling < 5 | +0.3 |
    | 5 ≤ decoupling ≤ 8 | +0.1 |
    | decoupling > 10 | −0.2 |
    | otherwise (8–10) | 0, no move |

  - The result is clamped to [1, 10] and rounded to 0.1, as `Apply` does.
  - Rule `durability_move`, subject `cycling:endurance`, inputs `{decoupling, durationRatio, from, to}`.
- **Long slot weight:** `1.8 + 0.1 × (endurance level − starting level)`, clamped to [1.8, 2.4]. With no endurance level, the weight is 1.8, today's.
- **Decay:**
  - Once per rider per ISO week (Monday key), in the morning slot (`inMorningSlot`), inside the tick lock.
  - For each levelled zone of the rider's sport whose last matched ride (an analysis linked to a workout of that zone, by completed-session date) is more than 21 days ago, or never:
    - the level drops by 0.1, never below the starting value (`Initial` for the rider's experience);
    - it is recorded with rule `level_decay`, inputs `{from, to, lastTrained}`.
  - A zone at or below its starting value is left alone.
  - The once-a-week claim is a new table, `level_decay_runs(rider, week_start, created_at)`, with PK `(rider, week_start)`. It is registered in `riderTables` (`Purged`, `Rename{rider; Unique; With: week_start}`) and deleted by `DeleteRider`.
- **Symmetric recalibration:**
  - An FTP **drop** of 3–25 % (new FTP ≥ old / 1.25 and ≤ old / 1.03) raises cycling structured-zone levels by `−10 × log2(new/old)`, capped at +2.0.
  - It uses the same compare-and-set marker, the same skip-if-reason-matches rule and the same `level_recalibration` why record.
  - Rises are unchanged.
  - Drops over 25 % only move the marker, as large rises do.
- **Logs:** never a level value, decoupling or FTP next to a rider; counts and stage only.
- No new dependency. `gofmt` and `go vet` clean. Both engines. Never commit `.superpowers/`.

## Review Focus

1. **A long ride with power but HR dropouts on 15 % of samples.** No decoupling, so no durability move unless the ride was cut short. Test in Task 1 and Task 2.
2. **An existing rider (rows already present, no `endurance`).** The first `levelsFor` seeds `endurance`, and the progression endpoint shows it. Test in Task 2.
3. **Decay in the same week as a ride in that zone.** The 21-day rule reads the ride, so no decay. A second morning pass in the same week does nothing. Test in Task 4.
4. **FTP 250 → 230 (−8 %).** Levels rise by `−10 × log2(0.92)` ≈ +1.2. A repeat save at 230 does nothing, because the marker is already set. Test in Task 5.
5. **A rider at their starting level in every zone.** Decay never takes anyone below where they started. Test in Task 4.

---

### Task 1: Decoupling at analysis

**Files:**
- Modify: `apps/api/internal/rideanalysis/metrics.go` (add `Decoupling(samples []Sample, ftp float64) (pct float64, ok bool)`)
- Modify: `apps/api/internal/rideanalysis/analyze.go` (`Analysis.DecouplingPct *float64`, set in `Analyze` for cycling)
- Modify: `apps/api/internal/workout/analysis_db.go` and `workout/db.go` (`decoupling_pct DOUBLE PRECISION NULL` via the existing tolerant ALTER pattern; scan and save it)
- Modify: `apps/api/internal/api/rideanalysis.go` (copy the value into `SessionAnalysis.DecouplingPct`)
- Test: `rideanalysis/metrics_test.go`, `workout/analysis_db_test.go`

**Interfaces:**
- Produces: `rideanalysis.Decoupling`, `Analysis.DecouplingPct *float64`, `workout.SessionAnalysis.DecouplingPct *float64`

- [ ] **Step 1: Write the failing tests:**
  - `TestDecouplingSteadyRideIsNearZero`: constant power and HR give 0.0.
  - `TestDecouplingDriftingHR`: HR rises 8 % in the second half at the same power, giving ≈ 7.4.
  - `TestDecouplingNeedsNinetyMinutesPowerAndHRAndLowIF`: each condition missing gives `false` (Review Focus 1, with 15 % HR dropouts).
  - `TestDecouplingSkipsTheWarmup`.
  - Store: decoupling round-trips nil and a value on both engines.
- [ ] **Step 2: Run the tests and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests and confirm they pass.** `go test ./apps/api/... -count=1`
- [ ] **Step 5: Commit.** `git commit -m "rideanalysis: aerobic decoupling on long steady rides"`

---

### Task 2: Endurance level and durability moves

**Files:**
- Modify: `apps/api/internal/progression/progression.go`
  - `cyclingZones` gains `endurance` for `Initial` only, so recalibration must not pick it up (see Task 5).
  - Add `DurabilityDelta`.
- Modify: `apps/api/internal/api/training.go` (`levelsFor` seeds missing `Initial` zones)
- Modify: `apps/api/internal/api/rideanalysis.go` (`applyProgressionForAnalysis` handles a matched long ride: `DurabilityDelta`, `SaveLevel`, `recordSubjectAdjustment` with `why.DurabilityMove`)
- Modify: `apps/api/internal/why/why.go` (add `DurabilityMove Rule = "durability_move"`, title "Long-ride durability", `DurabilityInputs` and facts)
- Modify: `apps/web/src/utils/zones.ts` (the `endurance` label "Endurance", if missing)
- Test: `progression/progression_test.go`, `api/durability_test.go`

**Interfaces:**
- Produces:
  - `func DurabilityDelta(decoupling *float64, durationRatio float64) (float64, bool)`
  - `why.DurabilityMove`, `why.DurabilityInputs`

- [ ] **Step 1: Write the failing tests:**
  - Pure: every row of the `DurabilityDelta` table, at the boundaries.
  - `TestInitialIncludesEnduranceForCyclingOnly`. Update `TestInitial`.
  - API:
    - `TestALongRideWithLowDecouplingRaisesEndurance`
    - `TestACutShortLongRideLowersEndurance`
    - `TestNoDecouplingNoMove`
    - `TestExistingRiderGetsEnduranceSeeded` (Review Focus 2)
    - `TestDurabilityMoveRecordsItsWhy`
    - `TestDurabilityLogsCarryNoValues`
- [ ] **Step 2: Run the tests and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests and confirm they pass.**
- [ ] **Step 5: Commit.** `git commit -m "progression: endurance level moved by long-ride durability"`

---

### Task 3: The long ride grows with the endurance level

**Files:**
- Modify: `apps/api/internal/scheduler/scheduler.go` (long-slot weight from `levels["endurance"]` and the starting value, `progression.Initial(profile.ExperienceLevel, sport)`, cycling only)
- Test: `scheduler/durability_test.go`

- [ ] **Step 1: Write the failing tests:**
  - `TestLongSlotWeightGrowsWithEnduranceLevel`: at starting level + 3, the long ride is longer than with no level, and the week's total hours are unchanged.
  - `TestLongSlotWeightIsCapped`: level 10 gives weight 2.4.
  - `TestNoEnduranceLevelIsTodaysWeight`: existing tests stay unchanged.
- [ ] **Step 2: Run the tests and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests and confirm they pass.** `go test ./apps/api/... -count=1` (the crewplan agreement and `StructuredZones` tests must stay green).
- [ ] **Step 5: Commit.** `git commit -m "scheduler: the long ride grows with the durability level"`

---

### Task 4: Weekly decay

**Files:**
- Modify: `apps/api/internal/workout/db.go`
  - Add the `level_decay_runs` DDL and include it in `DeleteRider`.
  - Add a store method `LastTrainedByZone(ctx, rider, sport) (map[string]string, error)`, which follows the join already used by `backfillProgressionHistory`.
  - Add `ClaimDecayWeek(ctx, rider, weekStart) (bool, error)`, an insert-on-conflict-do-nothing claim.
- Modify: `apps/api/internal/api/riderdata.go` (register the table) and `api/riderdata_test.go` (add `riderSeeds`)
- Modify: `apps/api/internal/progression/progression.go` (add `Decay(cur, start float64, lastTrained, today string) (float64, bool)`)
- Create: `apps/api/internal/api/leveldecay.go` (`decayLevels(ctx)`, called in `AutoScheduleTick` after `reoptimise` and before `AdaptWorkouts`, morning slot only)
- Modify: `apps/api/internal/why/why.go` (add `LevelDecay` with the title "Not trained lately" and inputs)
- Test: `progression/progression_test.go`, `workout/leveldecay_db_test.go`, `api/leveldecay_test.go`

- [ ] **Step 1: Write the failing tests:**
  - Pure `Decay`: under 21 days gives no change; over gives −0.1; the starting floor holds; already at the start gives no change; never trained gives a decay.
  - Store: the claim happens once per week; `LastTrainedByZone` on both engines.
  - API:
    - `TestUntrainedZoneDecaysOncePerWeek`
    - `TestARideInTheZoneStopsDecay` (Review Focus 3)
    - `TestDecayNeverGoesBelowStart` (Review Focus 5)
    - `TestDecayOutsideTheMorningSlotDoesNothing`
    - `TestDecayRecordsItsWhy`
  - Registry tests cover the new table.
- [ ] **Step 2: Run the tests and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests and confirm they pass.** `go test ./apps/api/... -count=1`, then `just docker-test`.
- [ ] **Step 5: Commit.** `git commit -m "progression: levels decay for zones left untrained"`

---

### Task 5: Symmetric recalibration

**Files:**
- Modify: `apps/api/internal/progression/progression.go`
  - `RecalibrationDelta` is clamped to [−2, +2].
  - Add `RecalibrationDownFactor = 1/1.03` with a comment.
- Modify: `apps/api/internal/api/recalibration.go` (allow drops of 3–25 %; keep `recalibratedZones` to the five cycling structured zones, so `endurance` is excluded)
- Modify: `api/recalibration_test.go`
  - Rewrite `TestRecalibrateNeverMovesLevelsOnADecrease` as `TestRecalibrateRaisesLevelsOnADrop` (Review Focus 4).
  - Add `TestALargeDropOnlyMovesTheMarker`, `TestAnFTPDropUnderThreePercentMovesNothing` and `TestEnduranceIsNeverRecalibrated`.
- Modify: `docs/superpowers/specs/2026-09-28-level-recalibration-design.md` (a status note: drops now recalibrate, superseded by the 2026-10-24 spec)

- [ ] **Step 1: Write the failing tests.**
- [ ] **Step 2: Run the tests and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests and confirm they pass.** `go test ./apps/api/... -count=1`
- [ ] **Step 5: Commit.** `git commit -m "recalibration: an FTP drop raises levels too"`

---

### Task 6: Docs

- [ ] **Step 1:** In `AGENTS.md`, under "FTP tests" or near the progression mentions, add one paragraph covering:
  - decoupling: how it is computed, when it is null, and that it applies to new analyses only;
  - the endurance level: what moves it, and its exclusion from recalibration;
  - the long-slot weight;
  - weekly decay: the claim table in `riderTables`, the floor, and the morning slot;
  - symmetric recalibration.

  Also set the spec status to "5b implemented", and the roadmap row 5 to "(shipped)".
- [ ] **Step 2: Commit.** `git commit -m "docs: progression"`
