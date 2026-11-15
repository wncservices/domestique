# Training Hours per Weekday Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A rider sets training hours per weekday; the planner caps each day's session at that day's hours, puts the long ride on the day with the most, and the week's cap is the sum.

**Architecture:** `workout.RiderProfile` gains `HoursByDay`, stored in a new `hours_by_day` column; the old `available_days` and `hours_per_available_day` columns keep being written (days from the map, figure = mean) so every `AvailableDays` reader and a rollback keep working. Readers of the single figure move to `HoursOn`/`WeekHours`. The scheduler picks the long day by hours and water-fills each day's share under its cap. Automatic moves (make-up, readiness swap) only use a day the session fits.

**Tech Stack:** Go (stdlib, SQLite + PostgreSQL), Vue 3 + Nuxt UI, node:test for web helpers.

**Spec:** `docs/superpowers/specs/2026-11-15-hours-per-day-design.md`

## Global Constraints

- Weekday keys are `mon`..`sun`, lowercase; Monday-first order wherever a list is produced.
- A day's hours: 0 < h <= 12 (`workout.MaxHoursPerDay = 12.0`); 0 means "off" and is dropped from the map; negative, NaN, Inf, > 12 or an unknown key is a 422 with nothing written.
- The map is authoritative only when non-empty. An empty map with `AvailableDays` set (days stated, no hours) keeps today's behaviour: days kept, no per-day cap.
- A day with no stated hours (`HoursOn(day) == 0`) is never capped.
- "Fits": planned seconds <= day hours x 3600 + 300 (`weekplan.FitSlackSeconds = 300`).
- Reconcile tolerance: 0.01 h (`workout.hoursTolerance`).
- Never log hours next to a rider. Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never commit `.claude/` or `.superpowers/`.
- `just check` passes before the PR.

## Review Focus

1. **Days stated, hours not** (old profile, figure 0): map empty, days kept, scheduler uncapped, nothing rebuilt — Task 1 test `TestDaysWithoutHoursKeepTheirDays`, Task 3 test `TestAProfileWithoutHoursIsNeverCapped`.
2. **A stale tab saving FTP only** (sends old fields equal to the stored derivation): per-day shape kept, no rebuild — Task 6 test `TestAnOldTabsSaveKeepsThePerDayHours`.
3. **Rollback round trip** (old image edits the old columns while a map is stored): old columns win on read — Task 1 test `TestOldColumnsWinWhenTheMapDisagrees`.
4. **Long day is mid-week** (Wednesday has most hours): no structured session on Tue/Thu, none on Wednesday — Task 3 test `TestHardDaysKeepClearOfAMidweekLongDay`.
5. **Every day full** (fixed rides or rungs leave more hours than the caps hold): no session exceeds its day, the remainder is dropped without a panic or a negative duration — Task 3 test `TestWaterFillDropsWhatNoDayHolds`.

---

### Task 1: Profile model and storage

**Files:**
- Create: `apps/api/internal/workout/hoursbyday.go`
- Create: `apps/api/internal/workout/hoursbyday_test.go` (pure), `apps/api/internal/workout/hoursbyday_db_test.go` (each engine)
- Modify: `apps/api/internal/workout/workout.go:143-148` (field + doc), `apps/api/internal/workout/db.go` (CREATE at ~54, ALTER list at ~853, `GetProfile` ~1464, `SaveProfileOn` upsert ~1577)

**Interfaces:**
- Produces:
  - `RiderProfile.HoursByDay map[string]float64` — days with hours > 0 only; nil/empty = not stated.
  - `const MaxHoursPerDay = 12.0`
  - `func (p RiderProfile) HoursOn(day string) float64` — the map's value; with an empty map, `HoursPerAvailableDay` when `day` is in `AvailableDays`; else 0.
  - `func (p RiderProfile) WeekHours() float64` — sum of `HoursOn` over the seven days.
  - `func (p RiderProfile) MaxDayHours() float64`
  - `func UniformHours(days []string, hours float64) map[string]float64` — nil when hours <= 0.
  - `func ValidateHoursByDay(m map[string]float64) (map[string]float64, error)` — returns the map with 0s dropped.
  - `func SameAvailability(stored RiderProfile, days []string, hours float64) bool` — same day set and |hours - stored.HoursPerAvailableDay| <= 0.01.
  - `func (p *RiderProfile) NormaliseAvailability()` — map non-empty: `AvailableDays` = its keys Monday-first, `HoursPerAvailableDay` = mean; map empty: map = `UniformHours(AvailableDays, HoursPerAvailableDay)`.

- [ ] **Step 1: Write failing pure tests** in `hoursbyday_test.go`:
  - `TestNormaliseDerivesDaysAndMeanFromTheMap`: `{sat:3, mon:1, sun:3}` -> `AvailableDays == [mon sat sun]`, `HoursPerAvailableDay ≈ 7/3`.
  - `TestNormaliseSpreadsTheOldFigure`: days `[tue sat]`, figure 1.5 -> map `{tue:1.5, sat:1.5}`.
  - `TestDaysWithoutHoursKeepTheirDays`: days `[tue sat]`, figure 0 -> map empty, `AvailableDays == [tue sat]`, `HoursOn("tue") == 0`, `WeekHours() == 0`.
  - `TestWeekHoursIsTheSum`: `{mon:1,tue:1,wed:1,thu:1,fri:1,sat:3,sun:3}` -> 11.
  - `TestValidateHoursByDay`: `{"xyz":1}`, `{"mon":-1}`, `{"mon":NaN}`, `{"mon":12.5}` error; `{"mon":0,"sat":3}` -> `{"sat":3}`; `{"mon":12}` ok.
  - `TestSameAvailability`: stored `{mon:1,sat:3}` (mean 2) vs `([sat mon], 2.005)` true; `([mon], 2)` false; `([mon sat], 2.1)` false.
- [ ] **Step 2: Run** `go test ./apps/api/internal/workout -run 'Normalise|DaysWithoutHours|WeekHours|ValidateHours|SameAvailability'` — FAIL (undefined).
- [ ] **Step 3: Implement** `hoursbyday.go` with the signatures above. Doc comments say why the old columns are still written (readers, rollback).
- [ ] **Step 4: Write failing engine test** `TestHoursByDayEachEngine` (sqlite + postgres, shape of `TestHRZonesEachEngine`):
  - "per-day hours round trip": save `HoursByDay {mon:1,sat:3,sun:3}` -> read map equal, `AvailableDays [mon sat sun]`, `HoursPerAvailableDay ≈ 7/3`.
  - "an old row reads as its figure on each day": insert via `SaveProfile` with only days `[tue thu]` and figure 1.5, then `UPDATE rider_profiles SET hours_by_day = ''` -> map `{tue:1.5, thu:1.5}`.
  - `TestOldColumnsWinWhenTheMapDisagrees`: save map `{mon:1,sat:3}`, then `UPDATE rider_profiles SET available_days='tue', hours_per_available_day=2` -> read map `{tue:2}`.
  - "an older reader sees the same weekly sum": after the per-day save, raw `available_days` count x `hours_per_available_day` == 7.
- [ ] **Step 5: Run** `go test ./apps/api/internal/workout -run HoursByDay` (with `DOMESTIQUE_TEST_POSTGRES` set if available) — FAIL.
- [ ] **Step 6: Implement storage**: column `hours_by_day TEXT NOT NULL DEFAULT ''` in CREATE and the ALTER list; JSON object encoding (`encoding/json`, '' for empty). `GetProfile` decodes, then reconciles (map non-empty and day set differs from `available_days` or mean differs > 0.01 -> discard map), then `NormaliseAvailability`. `SaveProfileOn` calls `NormaliseAvailability` before the upsert and writes `hours_by_day` (empty map -> '').
- [ ] **Step 7: Run** `go test ./apps/api/internal/workout/...` — PASS.
- [ ] **Step 8: Commit** `feat(profile): store training hours per weekday`.

### Task 2: Weekly cap is the sum of the days

**Files:**
- Modify: `apps/api/internal/periodization/load.go:84`, `periodization/periodization.go:252,376` (and the doc lines 75-76, 185), `apps/api/internal/api/loadinput.go:19`, `apps/api/internal/api/goalcheck.go:84`
- Test: `apps/api/internal/periodization/load_test.go`

**Interfaces:** Consumes `RiderProfile.WeekHours()`.

- [ ] **Step 1: Failing test** `TestUnevenDaysCapTheWeekAtTheirSum` in `load_test.go`: profile `HoursByDay {mon:1,tue:1,wed:1,thu:1,fri:1,sat:3,sun:3}` (normalised), a CTL load high enough to hit the cap -> every week's `TargetHours <= 11` and the peak week's `== 11` (within 0.01). Same profile with `{mon:2,sun:2}` vs uniform `[mon sun]` at 2 gives identical plans.
- [ ] **Step 2: Run** `go test ./apps/api/internal/periodization -run UnevenDays`. It may already pass, because the stored mean x days equals the sum; keep it as the guard for the refactor.
- [ ] **Step 3: Replace** each `HoursPerAvailableDay * float64(len(AvailableDays))` with `profile.WeekHours()`.
- [ ] **Step 4: Run** `go test ./apps/api/internal/periodization ./apps/api/internal/api -run 'Load|GoalCheck|Periodization'` — PASS.
- [ ] **Step 5: Commit** `refactor(plan): the week's hours cap reads WeekHours`.

### Task 3: Scheduler — long day, per-day caps, water-fill

**Files:**
- Modify: `apps/api/internal/scheduler/scheduler.go` (`buildWeek` ~236-385, `assignSlots` ~466, `placeStructuredDays*`/`placeStructured` ~559-625, mini-taper ~861-905)
- Create: `apps/api/internal/scheduler/dayhours.go`, `apps/api/internal/scheduler/dayhours_test.go`

**Interfaces:**
- Consumes: `RiderProfile.HoursOn`.
- Produces (unexported): `func longDayIndex(days []string, profile workout.RiderProfile) int` (most `HoursOn`, ties to the later day); `func waterFill(total float64, weights, caps []float64) []float64` (a cap <= 0 means uncapped; result[i] <= caps[i]; sum <= total); `placeStructured(days []string, k, limit, long int) []int`.

- [ ] **Step 1: Failing tests** in `dayhours_test.go` (build weeks with `WeekWorkouts`, as `scheduler_test.go` does):
  - `TestLongRideGoesOnTheDayWithMostHours`: `{tue:1,thu:1,sat:3,sun:2}` -> the long session (`IsLongRideName`) is on Saturday.
  - `TestTiesKeepTheLongRideOnTheLaterDay`: `{tue:1,sat:3,sun:3}` -> Sunday.
  - `TestNoSessionExceedsItsDay`: `{mon:1,tue:1,wed:1,thu:1,fri:1,sat:3,sun:3}`, build week, `TargetHours` 11 -> every session's `PlannedSeconds <= HoursOn(day)*3600 + 1`.
  - `TestWaterFillSendsTheExcessToDaysWithRoom`: `waterFill(6, [1,1,1.8], [1,3,3])` == `[1,2,3]` (within 1e-9); `waterFill(4, [1,1.8], [0,0])` == `[4/2.8, 7.2/2.8]` (uncapped = today's split).
  - `TestWaterFillDropsWhatNoDayHolds`: `waterFill(10, [1,1], [1,2])` sum 3, no negative; a week whose fixed ride leaves more than the caps hold builds without error and within caps.
  - `TestHardDaysKeepClearOfAMidweekLongDay`: `{mon:1,tue:1,wed:4,thu:1,fri:1,sat:1}`, a build week -> long on Wednesday, no structured session on Tue, Wed or Thu.
  - `TestAProfileWithoutHoursIsNeverCapped`: `AvailableDays [tue thu sat sun]`, no hours -> total duration equals `TargetHours` as in `TestWeekWorkoutsTotalDurationMatchesTargetHours`, long on Sunday.
  - `TestAnUncappedOldProfilePlacesAsBefore`: uniform 4 h on `[tue thu sat sun]` with `TargetHours` 8 -> same dates and kinds as with no hours stated (the cap does not bind).
  - `TestMiniTaperDayIsCappedByItsHours`: a B event week with `MiniTaperDayHours` 2 on a day of 1 h -> that day's session <= 1 h.
- [ ] **Step 2: Run** `go test ./apps/api/internal/scheduler -run 'LongRide|Ties|Exceeds|WaterFill|MidweekLong|WithoutHours|UncappedOld|MiniTaperDayIsCapped'` — FAIL.
- [ ] **Step 3: Implement**:
  - `assignSlots` sets `kinds[longDayIndex(days, profile)] = slotLong` instead of `n-1`; recovery-week rule unchanged.
  - `placeStructured` takes `long`: candidates skip `i == long`, the first pass skips days with `circularDays(days[i], days[long]) == 1`, `spreadScore` uses `days[long]`; `limit` still bounds candidates (mini-taper). Update the doc comment that says "slot len(days)-1 is always the long day".
  - Structured `capSeconds = min(shares[i]*targetHours*3600, HoursOn(day)*3600)` when `HoursOn(day) > 0`.
  - Rest split: `hours := waterFill(remainingHours, restWeights, caps)[i]` with `caps[i] = HoursOn(days[i])`.
  - Mini-taper: `min(mini.perDay, HoursOn(day))` when `HoursOn(day) > 0`.

  `waterFill` algorithm (not determined by the tests' exact numbers):
  ```
  free = all i with weights[i] > 0; left = total
  loop: share_i = left * w_i / Σ_free w; over = {i in free : caps[i] > 0 && share_i > caps[i]}
        if over empty: assign shares, done
        for i in over: out[i] = caps[i]; left -= caps[i]; remove i from free
        if free empty: done (the rest is dropped)
  ```
- [ ] **Step 4: Run** `go test ./apps/api/internal/scheduler/...` — PASS, existing tests included.
- [ ] **Step 5: Commit** `feat(scheduler): cap each day at its hours and put the long ride on the longest day`.

### Task 4: Automatic moves respect the day's hours

**Files:**
- Modify: `apps/api/internal/weekplan/weekplan.go` (`SwapInput`, `PlanSwap` ~131), `apps/api/internal/adapter/session.go:~230`, `apps/api/internal/api/movenext.go:~125`, `apps/api/internal/api/reoptimise.go:~131`
- Test: `apps/api/internal/weekplan/weekplan_test.go` (or a new `dayhours_test.go` there), `apps/api/internal/adapter/session_test.go`, `apps/api/internal/api/movenext_test.go`

**Interfaces:**
- Produces: `const FitSlackSeconds = 300`; `func Fits(profile workout.RiderProfile, date string, seconds float64) bool` (true when the weekday has no stated hours); `SwapInput.Fits func(date string, seconds float64) bool` (nil = always fits).
- `NextFreeDay` is unchanged: callers fold the fit into its `skip` predicate.

- [ ] **Step 1: Failing tests**:
  - `TestFits`: `{tue:1}` -> 3600+300 fits, 3601+300 does not; a weekday without hours always fits.
  - `TestSwapSkipsAPairThatDoesNotFit`: hard 90 min today (a day of 2 h); candidate easy session on a 1 h day -> no swap; a second candidate on a 2 h day -> swap to that one.
  - `TestMakeUpSkipsADayTooShort` (adapter): missed 2 h session, next free days a 1 h Thursday and a 3 h Saturday -> made up on Saturday; with only the Thursday, not made up.
  - `TestNotTodayPrefersAFittingDay` (api): today's 2 h session, free Thursday (1 h) and Saturday (3 h) -> moves to Saturday; with only Thursday free -> moves to Thursday (fallback, 200).
- [ ] **Step 2: Run** those tests — FAIL.
- [ ] **Step 3: Implement**: `PlanSwap` candidate loop also requires `fits(w.Date, seconds(h)) && fits(in.Today, seconds(w))`; `reoptimise.go` passes `Fits: func(d string, s float64) bool { return weekplan.Fits(profile, d, s) }`. The adapter's make-up `skip` adds `!weekplan.Fits(profile, d, seconds(missed))`. `movenext` calls `NextFreeDay` first with a skip that rejects non-fitting days, then without it.
- [ ] **Step 4: Run** `go test ./apps/api/internal/weekplan/... ./apps/api/internal/adapter/... ./apps/api/internal/api -run 'Swap|MakeUp|NotToday|MoveNext|Reoptimise'` — PASS.
- [ ] **Step 5: Commit** `feat(plan): automatic moves only use a day the session fits`.

### Task 5: Ledger repayment and history inference

**Files:**
- Modify: `apps/api/internal/ledger/repay.go:172,226`, `apps/api/internal/autoprofile/autoprofile.go:~245-256`
- Test: `apps/api/internal/ledger/repay_test.go`, `apps/api/internal/autoprofile/autoprofile_test.go`

**Interfaces:** Produces (unexported in ledger) `func dayCapSeconds(p workout.RiderProfile, date string) float64` — `HoursOn(weekday)*3600`, else `MaxDayHours()*3600`, 0 = no cap.

- [ ] **Step 1: Failing tests**:
  - `TestLengthenStopsAtTheSessionsOwnDay`: `{tue:1, sun:3}`, a 50 min Tuesday endurance session -> lengthened to at most 60 min; the same on Sunday may reach 65 min (30%).
  - `TestLengthenOnADayWithoutHoursUsesTheLongestDay`: session on Wednesday (no hours) -> cap 3 h.
  - `TestInferenceFillsPerDayHours` (autoprofile): applying an inferred `[tue sat]` at 1.5 to an empty profile sets `HoursByDay {tue:1.5, sat:1.5}`; a profile whose hours were typed (not estimated) is untouched.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement**: replace both `in.Profile.HoursPerAvailableDay * 3600` with `dayCapSeconds(in.Profile, w.Date)`; in autoprofile, after days/hours change set `p.HoursByDay = workout.UniformHours(p.AvailableDays, p.HoursPerAvailableDay)`.
- [ ] **Step 4: Run** `go test ./apps/api/internal/ledger/... ./apps/api/internal/autoprofile/...` — PASS.
- [ ] **Step 5: Commit** `feat(plan): repayment and inferred profiles use per-day hours`.

### Task 6: API — DTO, validation, save rules, rebuild trigger

**Files:**
- Modify: `apps/api/internal/api/training.go` (`riderProfileDTO` ~160, `profileDTOFrom` ~210, save handler ~1490-1560, `availabilityChanged` ~1566)
- Test: `apps/api/internal/api/profiledays_test.go`

**Interfaces:**
- DTO field `HoursByDay map[string]float64 \`json:"hoursByDay,omitempty"\``; responses keep `availableDays` and `hoursPerAvailableDay` (derived). `profileProposalDTO` unchanged.
- Consumes `ValidateHoursByDay`, `SameAvailability`, `NormaliseAvailability`.

- [ ] **Step 1: Failing tests** in `profiledays_test.go` (use `seasonHarness`):
  - `TestPerDayHoursRoundTrip`: PUT `{"hoursByDay":{"mon":1,"sat":3,"sun":3}}` -> GET has the map, `availableDays ["mon","sat","sun"]`.
  - `TestBadPerDayHoursAre422`: `{"hoursByDay":{"mon":13}}`, `{"hoursByDay":{"xyz":1}}`, `{"hoursByDay":{"mon":-1}}` -> 422, profile unchanged.
  - `TestChangingOneDaysHoursRebuilds`: profile `{tue:1,thu:1,sat:3,sun:3}`, goal, backdate, PUT the same with `sat:2` -> untouched future Saturday sessions are rebuilt (each <= 2 h + slack); PUT identical again -> no session id changes.
  - `TestAnOldTabsSaveKeepsThePerDayHours`: stored `{mon:1,sat:3}`; PUT `{"ftpWatts":260,"availableDays":["sat","mon"],"hoursPerAvailableDay":2}` -> map still `{mon:1,sat:3}`, no session rebuilt.
  - `TestOldFieldsStillSave`: no stored map; PUT `{"availableDays":["tue","sat"],"hoursPerAvailableDay":1.5}` -> map `{tue:1.5,sat:1.5}` (existing `TestChangingAvailableDaysMovesUntouchedSessionsToTheNewDays` keeps passing).
- [ ] **Step 2: Run** `go test ./apps/api/internal/api -run 'PerDayHours|BadPerDay|OneDaysHours|OldTab|OldFieldsStill|AvailableDays|Availability'` — FAIL.
- [ ] **Step 3: Implement**: validate `body.HoursByDay` when non-nil (422 `{"error": ...}`); when nil and `len(before.HoursByDay) > 0 && SameAvailability(before, body.AvailableDays, body.HoursPerAvailableDay)`, use `before.HoursByDay`. `availabilityChanged` compares the two normalised `HoursByDay` maps (keys and values within 0.01) and keeps the "before had no days" early return and the existing day-set comparison for map-less profiles.
- [ ] **Step 4: Run** `go test ./apps/api/internal/api/...` — PASS (sharded locally is fine; full package before commit).
- [ ] **Step 5: Commit** `feat(api): profile takes hours per weekday`.

### Task 7: Web — types, form, page

**Files:**
- Create: `apps/web/src/utils/hoursByDay.ts`, `apps/web/tests/hoursByDay.test.mjs`
- Modify: `apps/web/src/api/types.ts:~887` (RiderProfile `hoursByDay?: Partial<Record<string, number>>`), `apps/web/src/components/fitness/ProfileForm.vue` (~88 `toggleDay`, ~350-400 availability block), `apps/web/src/pages/FitnessPage.vue` (~122 normalise, ~159 needsSetup, ~271 proposal apply)

**Interfaces:** `hoursByDay.ts` exports:
- `WEEKDAYS: readonly string[]` (Monday-first)
- `withDayToggled(p: RiderProfile, day: string): Partial<RiderProfile>` — on: hours of the nearest switched-on day (by weekday distance, earlier on a tie) or 1; off: removes it. Returns `{hoursByDay, availableDays, hoursPerAvailableDay}` all consistent.
- `withDayHours(p: RiderProfile, day: string, hours: number | undefined): Partial<RiderProfile>` — same three fields.
- `hoursMapOf(p: RiderProfile): Record<string, number>` — the map, or the old figure spread over `availableDays`.
- `weekTotal(p: RiderProfile): number`

- [ ] **Step 1: Failing node tests** `hoursByDay.test.mjs`: toggling `sat` on with `{fri:1, sun:3}` gives 1 (Friday is nearer-earlier on a tie of distance 1); toggling on with nothing on gives 1; toggling off removes the key and the day; `hoursMapOf({availableDays:['tue'],hoursPerAvailableDay:1.5})` is `{tue:1.5}`; `weekTotal` of the 1/1/1/1/1/3/3 week is 11.
- [ ] **Step 2: Run** `npm --workspace @domestique/web test` — FAIL.
- [ ] **Step 3: Implement** the helpers; `ProfileForm.vue`: remove the "Hours per available day" field; keep the day buttons (now `withDayToggled`); below them, one row per switched-on day (Monday first): label + `UInput type="number" step="0.25" min="0" max="12"` bound through `withDayHours`; a muted line "`{total}` h a week"; the "auto-filled" badge on the Availability label when `available_days` or `hours_per_available_day` is estimated. Rows in one column (`flex flex-col gap-2`, input `w-24`). `FitnessPage.vue`: `normalise` sorts `hoursByDay` keys too (already sorted at every depth; keep availableDays sort); `needsSetup` = `weekTotal(profile) === 0`; applying a proposal sets `hoursByDay` to the proposal's figure on each proposed day (via a helper `uniformHours(days, h)` exported from `hoursByDay.ts`).
- [ ] **Step 4: Run** `npm --workspace @domestique/web test` and `npm --workspace @domestique/web run typecheck` (also with `components.d.ts` moved aside, per the project's typecheck note) — PASS.
- [ ] **Step 5: Check in a browser** at 375 px (`just api` + `just web`, Fitness page): toggling days adds/removes rows, total updates, Save bar appears only on a change, no horizontal scroll.
- [ ] **Step 6: Commit** `feat(web): hours per weekday on the profile form`.

### Task 8: AGENTS.md, checks, PR

**Files:** Modify `AGENTS.md` ("Available days or hours changed" bullet in *The whole season is planned*; the long-slot sentence "there is no per-day-hours cap on it, as there never was" in *Progression levels...*; the *Weeks are sized in TSS* paragraph on the cap; a short note on the three profile fields, the mean kept for rollback, and that automatic moves respect a day's hours).

- [ ] **Step 1: Edit AGENTS.md** as listed.
- [ ] **Step 2: Run** `just check` — PASS (gofmt, vet, go test, web typecheck); run `DOMESTIQUE_TEST_POSTGRES` engine tests if a database is reachable (`just docker-test` otherwise).
- [ ] **Step 3: Commit** `docs: hours per weekday in AGENTS.md`, push, open a PR against main whose body summarises the behaviour change (old profiles' long rides capped in newly built or refreshed weeks) and ends with the Claude Code line.
