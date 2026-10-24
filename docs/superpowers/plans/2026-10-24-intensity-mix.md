# Intensity mix (5a) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Choose each week's structured zones from the rider's last 28 days of power-scored training rather than from a fixed table. Allow a third hard session for high-volume riders, and keep one short structured session in recovery weeks.

**Architecture:**
- A pure `internal/intensity` package computes the 28-day mix from analyses and picks zones by shortfall.
- `scheduler.WithZoneMix` feeds that mix into slot assignment.
- The api computes the mix once per season pass and passes it through `weekOptions`, which every week-building path uses.

**Tech Stack:** Go stdlib.

**Spec:** `docs/superpowers/specs/2026-10-24-intensity-and-progression-design.md` (section 5a).

## Global Constraints

- **Buckets:**
  - low = power zones 1–2 (< 75 % FTP), high = zones 3–4 (75–105 %), anaerobic = zones 5–7 (> 105 %).
  - Only analyses with `LoadSource == "fit_power"` and `NormalizedPower > 0` count.
  - Under 3 h of counted time (10800 s) there is no mix.
- **Targets (low/high/anaerobic):** base 80/18/2, build 75/20/5, peak 75/15/10. Taper has no mix (today's table).
- **Allowed zones and buckets** (cycling only; running keeps today's table):

  | Phase | Allowed zones (bucket) |
  |---|---|
  | base | sweet_spot, tempo (high) |
  | build | threshold, sweet_spot (high); vo2max (anaerobic) |
  | peak | vo2max, anaerobic (anaerobic); threshold (high) |

- **Choice rules:**
  - Pick the largest shortfall first, keeping today's order inside a bucket.
  - No repeated zone until the allowed set is exhausted.
  - If every bucket gap is within 1 percentage point of the others, use today's table.
- **Third structured slot:** only in build or peak, with `len(AvailableDays) >= 6` and `week.PlannedCTL >= 60`, never adjacent (`placeStructuredDays` decides). If no placement exists, keep two.
- **Recovery week:**
  - Exactly one structured slot when n ≥ 3: the phase's first allowed zone, or today's table's first zone when the phase has none (base → sweet_spot).
  - Target level = level − 1.0, floored at 1.
  - Cap 3600 s.
  - Placed on the available day furthest from the last available day.
  - No long ride.
- `StructuredZones(phase, sport, n)` keeps today's table and behaviour.
- Without `WithZoneMix`, and with PlannedCTL < 60, every existing scheduler test is unchanged. The exceptions are the recovery-week tests, which change by design; list them in the report.
- No new dependency. `gofmt` and `go vet` clean. Never commit `.superpowers/`.

## Review Focus

1. **A rider with only HR rides.** No mix, so today's table. Test in Task 1.
2. **A build week where high is already over target and anaerobic is short.** The first slot is vo2max, the second threshold. Test in Task 1.
3. **A 6-day rider at CTL 65 whose available days are Mon–Sat.** The third structured slot is placed only where nothing ends up adjacent. With Sat as the long day, the structured days come from Mon–Fri, which allows three non-adjacent days (Mon, Wed, Fri). Test in Task 2.
4. **A recovery week for a 3-day rider.** One short structured session, plus easy days. Test in Task 2.
5. **Train Now and life-event refill agree with fill** for the same week and mix. Test in Task 3.

---

### Task 1: `internal/intensity`

**Files:**
- Create: `apps/api/internal/intensity/intensity.go`
- Create: `apps/api/internal/intensity/intensity_test.go`

**Interfaces:**
- Produces:

```go
type Mix struct{ Low, High, Anaerobic float64; Seconds int } // shares 0..1; Seconds = counted time
func FromAnalyses(as []workout.SessionAnalysis) (Mix, bool)  // false under 10800 s counted
func Targets(phase periodization.Phase) (low, high, anaerobic float64, ok bool) // ok false for taper/unknown
func ChooseZones(phase periodization.Phase, mix Mix, k int) []workout.Zone // cycling; nil if no targets or the tie rule applies
```

- `ChooseZones` returns exactly `k` zones when `k` ≤ the size of the allowed set, otherwise it cycles through the set. It returns nil when the tie rule applies, so the caller falls back.

- [ ] **Step 1: Write the failing tests:**
  - `TestBucketsFromPowerZoneSeconds`
  - `TestOnlyPowerScoredRidesCount`, covering HR-only, estimate, zero NP and nil arrays
  - `TestUnderThreeHoursIsNoMix`
  - `TestTargetsPerPhase`
  - `TestBuildWithHighOverAndAnaerobicShortPicksVO2First` (Review Focus 2)
  - `TestNoRepeatUntilExhausted`
  - `TestTiedGapsFallBack`
  - `TestTaperHasNoChoice`
- [ ] **Step 2: Run the tests and confirm they fail.**
- [ ] **Step 3: Implement.** Before writing the code, check that importing `periodization` from `intensity` creates no cycle with `scheduler`.
- [ ] **Step 4: Run the tests and confirm they pass.** `go test ./apps/api/internal/intensity/`
- [ ] **Step 5: Commit.** `git commit -m "intensity: 28-day mix and zone choice by shortfall"`

---

### Task 2: Scheduler — zone mix, third slot, recovery session

**Files:**
- Modify: `apps/api/internal/scheduler/demand.go` (add the option field and `WithZoneMix`)
- Modify: `apps/api/internal/scheduler/scheduler.go` (`assignSlots`, `structuredSlotsFor` call path, recovery branch)
- Test: `apps/api/internal/scheduler/zonemix_test.go`

**Interfaces:**
- Consumes: Task 1.
- Produces:

```go
func WithZoneMix(m intensity.Mix) Option
```

- **Cycling non-taper weeks with the option:** slot count = today's count, +1 when the third-slot rule holds. Zones come from `intensity.ChooseZones`, or today's table on nil. The level offset is `productiveOffset`.
- **Recovery weeks:** the recovery rule from the Global Constraints, with or without the option.

- [ ] **Step 1: Write the failing tests:**
  - `TestZoneMixChangesBuildZones`
  - `TestNoMixIsTodaysTable`, the existing behaviour
  - `TestThirdHardSessionNeedsSixDaysAndCTL60` (Review Focus 3, including no adjacency)
  - `TestRecoveryWeekHasOneShortStructuredSession` (Review Focus 4: level − 1, ≤ 3600 s, placement, no long ride)
  - `TestRouteDemandStillBiasesWithinTheChosenZone`
  - Update `TestRecoveryWeekIsAllEasy` to the new rule, and list it and any other changed test in the report.
  - `TestStructuredZonesMatchesTheScheduler` must stay green. If its recovery cases differ, note it.
  - `crewplan` `TestThePreviewRemovesExactlyTheDaysTheFillDrops` must stay green.
- [ ] **Step 2: Run the tests and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests and confirm they pass.** `go test ./apps/api/... -count=1`
- [ ] **Step 5: Commit.** `git commit -m "scheduler: zones from the intensity mix, a third hard day, a recovery-week session"`

---

### Task 3: API — compute the mix once, every builder uses it

**Files:**
- Modify: `apps/api/internal/api/training.go`
  - `seasonContext` gains a `mix *intensity.Mix`, computed in `seasonContext(...)` from `Training.ListAnalyses(ctx, rider, today−28d)`. A read error means no mix, logged as a Warn with rider and stage.
  - `weekOptions` appends `WithZoneMix`.
- Modify: `apps/api/internal/api/trainnow.go` (`weekZones` gets the same mix, via the season context or a shared helper) and `apps/api/internal/api/lifeevents.go` (`refillRequests` uses `weekOptions` or appends the mix the same way).
- Test: `apps/api/internal/api/zonemix_test.go`

**Interfaces:**
- Consumes: Tasks 1–2.

- [ ] **Step 1: Write the failing tests** (HTTP or season harness with seeded analyses):
  - `TestARiderWithHeavyThresholdHistoryGetsVO2InBuild`
  - `TestARiderWithNoPowerHistoryGetsTodaysPlan`
  - `TestTrainNowAndLifeEventRefillAgreeWithFill` (Review Focus 5)
  - `TestRefreshRederivesZonesFromTheMixAtRefreshTime`
- [ ] **Step 2: Run the tests and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests and confirm they pass.** `go test ./apps/api/... -count=1`, then `just docker-test`.
- [ ] **Step 5: Commit.** `git commit -m "api: plan weeks from the rider's intensity mix"`

---

### Task 4: Docs

- [ ] **Step 1:** In `AGENTS.md`, add one paragraph under "The whole season is planned" covering:
  - the 28-day mix and the counted-ride filter;
  - zone choice and the fallback;
  - the third hard session rule;
  - the recovery-week session;
  - that every builder gets the mix through `weekOptions`;
  - that `StructuredZones` stays today's table on purpose.

  Set the spec status to "5a implemented".
- [ ] **Step 2: Commit.** `git commit -m "docs: intensity mix"`
