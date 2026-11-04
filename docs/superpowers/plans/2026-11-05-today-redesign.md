# Interactive Today page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The Today page acts on today, without copying Plan:
- act on the session;
- readiness you can open;
- feedback on your last ride;
- a week load ring.

**Architecture:** Two small backend additions:
- a "not today" move, where the server picks the day and enforces the rules;
- the week's ridden TSS and debt on the week response.

The rest is Today-page UI that reuses existing pieces: AlternatesMenu, IndoorConvertModal/useIndoor, ReadinessChip, FeelRating and SessionExplanation.

**Tech Stack:** Go stdlib, Vue 3 / Nuxt UI.

**Spec:** `docs/superpowers/specs/2026-10-26-explainer-and-today-design.md`, part 2.

## Global Constraints

- **Plan stays the place for the week.** Today shows no day-by-day list beyond the existing compact "This week" tiles.
- **Move ("Not today").** `POST /api/training/workouts/{id}/move-next` with body `{}`.
  - **Choosing the day.** The server moves the session to the first available day after today, up to and including Sunday of this week.
    - "Available" means the rider profile's `availableDays`, a day without a planned workout, and a day outside a life-event blackout (`lifeevents.NoTestDays`).
    - Use the same rule as `adapter.nextFreeDay`. Export a small shared helper rather than copying it.
  - **Refusals, all 409 with a short rider-facing message:**
    - a crew ride;
    - an FTP test (`test_protocol` set);
    - a session already ridden (a completed session matched to it today);
    - a date other than today (only today's session can be "not today");
    - no free day this week: "No free day left this week. Open the plan to move it."
  - **Ownership.** Owner-only, 404 otherwise.
  - **Response.** `{workout, from}`.
  - **Undo.** Use the existing `PATCH /api/training/workouts/{id}` with `{date: from}`.
  - **Records.** Record the move the way `recordManualMove` does (moved by the rider).
- **Week figures.** `WeekFocus` gains:
  - `riddenTss`: the sum of `trainingLoad` of completed sessions in the week;
  - `debtTss`: the ledger debt still to make up. Use the same `weekDebt` the ledger pass uses, and the effective target (`ledger.EffectiveTarget`).
  - These are for the current week only, and both are absent (not 0) when the week has no CTL-based ledger row.
  - DTO changes go in `server.go`/`trainingweek.go` and `types.ts` together.
- **Today UI.** Nuxt UI components and semantic tokens. Card actions are `size="sm"`, with at most one solid primary per card. Use the shared `InlineLink`. Light and dark. At 375 px, no overflow.
  - **Order:** greeting, morning message, check-in, session card (with actions), last ride feedback, week ring, then the existing tiles.
- **Readiness popover.** Extend the existing `ReadinessChip`. Keep the reasons list, and add:
  - what it changed today, the today session's `why` chip if any (WhyPopover);
  - an `InlineLink` "See your fitness" to `/fitness`.
  - No raw HRV or sleep numbers.
- **Last ride feedback.** Show it for the most recent completed session in the last 3 days (today inclusive) that has an analysis and no `feel` yet.
  - Take it from the data Today already loads, `api.fitness()` sessions. Check its shape includes `analysis` with `feel`; otherwise use the week's completed sessions plus the previous week's.
  - It shows the session's name or the matched workout's name, the date ("Mon 26 Oct"), the duration, and the outcome as a UBadge (nailed, completed, struggled, incomplete).
  - It contains `FeelRating`. On `rated`, it folds to "Thanks — logged." and is gone after a reload.
- **Week ring.** A small ring (SVG, tokens) showing `riddenTss` against the effective target, with one line under it: "65 of 410 TSS · 30 to make up" ("· all made up" when the debt is 0).
  - It links to Plan with `InlineLink` "Open the plan".
  - It is hidden when `targetTss` or `riddenTss` is absent.
- Never commit `.superpowers/`. `gofmt`, `go vet`, typecheck clean.

## Review Focus

1. **"Not today" on the last available day of the week.** The rider sees the 409 message with the Plan link, and nothing moves. Test in Task 1.
2. **Undo after "Not today" when the original day was taken in the meantime.** The PATCH still restores it, which is a rider action like Plan's move. The toast Undo must work once; test in Task 3's browser check.
3. **A week where the ledger has no row (no FTP, or an hours basis).** The ring is hidden, and it never shows "0 of 0". Test in Task 2 (fields absent) and Task 4.
4. **The last ride was rated on the Plan page already.** The card does not show. Test in Task 4's helper test.
5. **The page at 375 px with every card present.** No overflow, and the session card's actions wrap cleanly. Browser check.

---

### Task 1: "Not today" move endpoint

**Files:**
- Create: `apps/api/internal/api/movenext.go`, `movenext_test.go`.
- Modify:
  - `apps/api/internal/adapter/session.go`: export or relocate the next-free-day helper, for example to `internal/weekplan` as `NextFreeDay`, keeping the adapter's behaviour.
  - `server.go`: the route.

**Interfaces (produces):**

```go
// weekplan (or the chosen package)
func NextFreeDay(from time.Time, weekEnd string, available, taken map[string]bool, skip func(date string) bool) (string, bool)
// HTTP: POST /api/training/workouts/{id}/move-next → 200 {workout, from} | 404 | 409 {error}
```

- [ ] **Step 1: Write the failing tests.**
  - Moves to the next available free day.
  - Skips taken days, unavailable weekdays and blackout days.
  - Last day of the week gives a 409 with the message.
  - Crew ride, FTP test, ridden, and not-today each give a 409.
  - Another rider's workout gives a 404.
  - The move is recorded (description note and flags parity).
  - The adapter's existing make-up tests still pass.
- [ ] **Step 2: Run them and confirm they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and confirm they pass.** Run `go test ./apps/api/... -count=1`, `go vet` and `gofmt`.
- [ ] **Step 5: Commit.** `api: not today — move today's session to the next free day`

### Task 2: Week ridden TSS and debt

**Files:**
- Modify:
  - `apps/api/internal/api/trainingweek.go`: `weekFocusDTO.RiddenTSS *float64` and `DebtTSS *float64`, filled for the current week in or next to `fillLedgerFigures`. Reuse the ledger pass's debt computation; do not copy it.
  - `apps/web/src/api/types.ts`: `riddenTss?` and `debtTss?`.
- **Tests:**
  - With a ledger row: ridden is the sum of the week's loads, and debt equals the ledger pass's figure.
  - With no row: both fields are absent from the JSON.
  - A past or future week: both are absent.
- [ ] **Steps:** failing tests, then implement, then pass, then commit `api: the week's ridden load and debt for Today`.

### Task 3: Session card actions on Today

**Files:**
- Modify:
  - `apps/web/src/pages/TodayPage.vue`;
  - `apps/web/src/api/client.ts`: `moveWorkoutNext(id)`.
- Possibly create `components/today/SessionActions.vue` if TodayPage grows too large.
- **Actions, only when not ridden:**
  - Send to Garmin (existing).
  - Swap (existing AlternatesMenu).
  - **Indoor version / Undo outdoor:** reuse `useIndoor` and `IndoorConvertModal` exactly as PlanPage does, with the same `canChangeIndoor` rules (cycling, not a crew ride, not ridden, date ≥ today).
  - **Not today:** hidden for crew rides and FTP tests. It calls `moveWorkoutNext`. On 200 it shows a toast "Moved to Thu 29 Oct" with an Undo action (PATCH back to `from`) and reloads the week. On 409 it shows a toast with the server message and an "Open the plan" action.
- [ ] **Steps:** implement, then run typecheck (also with `components.d.ts` moved aside), `npm test` and `vite build`, then commit `web: act on today's session from Today`.

### Task 4: Readiness popover, last ride feedback, week ring

**Files:**
- Modify:
  - `components/plan/ReadinessChip.vue`: optional `changes` slot or prop, and the Fitness link.
  - `pages/TodayPage.vue`.
  - `utils/today.ts`: `lastUnratedRide(sessions, today)` and `weekRing(focus)`, both pure and tested.
- Create:
  - `components/today/LastRideCard.vue`;
  - `components/today/WeekRing.vue`.
- **Tests (`tests/today.test.mjs`):**
  - `lastUnratedRide` picks the most recent within 3 days and skips rated, unanalysed and older sessions.
  - `weekRing` returns null when either figure is absent, and builds the line text, including "all made up".
- [ ] **Steps:** failing helper tests, then implement, then typecheck, test and build, then commit `web: readiness you can open, last ride feedback, week ring`.

### Task 5: Docs

- `AGENTS.md`: a short paragraph on the Today page covering:
  - what lives on Today and what lives on Plan;
  - the move-next endpoint and its rules;
  - `riddenTss`/`debtTss`;
  - last ride feedback.
- Spec status: "Part 2 implemented <date>".
- [ ] **Commit.** `docs: the interactive Today page`
