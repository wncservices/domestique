# Readiness for tomorrow's plan — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Forecast whether tomorrow's generated hard session is at risk,
from today's own readiness verdict, projected form and load — and let the
rider ease it now with one click, without ever changing tomorrow on its
own.

**Architecture:** A pure `readiness.ForecastTomorrow` alongside today's
`Assess`; an API-layer builder next to `assessReadiness` that gathers its
inputs (projected TSB, ACWR-with-today, consecutive hard days); the
existing `GET /api/training/readiness` grows a `tomorrow` field and a new
`POST .../tomorrow/ease` reuses today's readiness's own Downgrade/StepDown
application path, targeted at tomorrow's workout; the sync tick logs the
same forecast at Info, changing nothing; a Plan-page banner with an Ease
button.

**Tech Stack:** Go stdlib, `internal/dbx` (no new schema — nothing new is
persisted); Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-28-readiness-tomorrow-design.md`
— thresholds, reason wording, timezone handling and the double-easing
guard are binding.

## Global Constraints

- Read `AGENTS.md` first (Conventions, Tests, Observability incl. the
  outbound-client/new-guard checklist, Security).
- No new dependencies, no new Garmin endpoint, no new table. gofmt / go vet
  clean; `just check` green.
- Every new test uses a fixed clock (`Server.Clock` or explicit dates) —
  nothing depends on today's date. Every date-window test (TSB freshness,
  the 7/28-day ACWR windows, consecutive-hard-days) must pass run under
  both `TZ=UTC` and `TZ=Europe/Brussels` (`TZ=Europe/Brussels go test
  ./apps/api/internal/readiness/... ./apps/api/internal/api/...`) — this
  logic must only ever read the `?today=` parameter or an explicit date
  argument, never the process's local timezone, and running under both
  zones is what actually proves that rather than assumes it.
- Health data is personal and this feature does not add any new health
  reads — it reads training load only. Still: never log a rider's name
  alongside a numeric verdict/score at Info or above beyond what today's
  readiness already logs (counts and verdicts, not raw wellness values —
  there are none here anyway).
- Owner-only on both the extended GET and the new POST, same gate as every
  other training endpoint (`auth.PermManageTraining`).
- Only tomorrow's generated (`scheduler.IsGenerated`), hard
  (`scheduler.IsHardSession`), untouched, not-yet-done workout may ever
  change, at most once, via the ease action only — never automatically,
  never today's session, never more than one workout per click.
- Any new SQL: none expected (nothing new is persisted). If a task turns
  out to need a query it does not already have available through
  `s.Training`, route it through `dbx` and cover it with `TestEachEngine`
  — flag this as a plan deviation before writing it.
- DTOs in `apps/api/internal/api/readiness.go` and
  `apps/web/src/api/types.ts` change together.
- Frontend: tokens only, no hex, no `dark:`, semantic colours only for
  status, typed inline handler params. CI-equivalent typecheck: move
  `apps/web/components.d.ts` and `apps/web/auto-imports.d.ts` aside,
  `npx vue-tsc --noEmit` from `apps/web`, then move both back — a green
  local typecheck with the generated `.d.ts` files in place proves
  nothing, since they paper over exactly the prop-typing mistakes CI would
  catch without them.
- Never `git stash`. Commit trailer `Co-Authored-By: Claude Sonnet 5
  <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **The double-easing guard is not new code.** Confirm the ease action's
   `Downgrade`/`StepDown` application appends `scheduler.AdjustedMarker`
   through the exact same path `applyStepDown`/the existing
   Downgrade-application code in `adaptRider` already use — if a task adds
   a second, separate marker check instead of routing through that
   existing code, it has built a guard the codebase already provides for
   free and now has two ways to get it wrong. (Task 2)
2. **`ProjectedTSB`/`ACWR` degrade to "no reason," never to a wrong one.**
   No FTP, no snapshot, a stale snapshot, or fewer than 21 days of load
   coverage must each suppress their own reason without affecting the
   others — same shape as today's readiness's own partial-data handling.
   (Task 1–2)
3. **Tomorrow, never today.** The ease action must be structurally unable
   to touch today's workout even when today's own readiness has already
   eased something in the same pass — these are two different workouts on
   two different dates by construction, but the test needs to actually
   exercise a day where both fire. (Task 2)
4. **`?today=` drives both endpoints**, not `s.now()` — a request from the
   app's own frontend near local midnight must get the browser's tomorrow,
   not the server's. The sync tick's Info log is the one place `s.now()`
   is correct to use instead. (Task 2)
5. **Re-read, don't trust.** `POST .../tomorrow/ease` recomputes the
   forecast before applying anything — a stale `ready` banner must not be
   actionable, and a banner that was `caution` when the page loaded but is
   `rest` by the time of the click must apply `rest`'s treatment, not
   `caution`'s. (Task 2)
6. **The ease endpoint refuses rather than improvises.** 409 with the
   server message and zero changes when the fresh forecast is `ready`, or
   tomorrow's workout is no longer generated/untouched/hard/undone or is
   gone; the UI shows that message as a warning toast (like the replan
   409) and refetches. (Tasks 2–3)
7. **Log hygiene.** The sync-tick Info line carries rider + verdict word
   only — no TSB, ACWR or load value next to the rider name. (Task 2)
8. **Today's `rest` maps to tomorrow `caution`**, never `rest`. (Task 1)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1 Forecast rules | `claude/tomorrow-1-forecast` |
| 2 | 2 Builder, sync log, API | `claude/tomorrow-2-apply` |
| 3 | 3 UI | `claude/tomorrow-3-ui` |

Restack right after each squash merge (`git rebase --onto origin/main <old
parent tip> <branch>`, retarget, `--force-with-lease`).

---

### Task 1: `readiness.ForecastTomorrow`

**Files:** `apps/api/internal/readiness/readiness.go` (or a new
`tomorrow.go` in the same package — either is fine, keep `Verdict`/
`Assessment` shared); `apps/api/internal/readiness/tomorrow_test.go`.

**Produces:**

```go
// TomorrowInput is what ForecastTomorrow needs — gathered by the caller
// (internal/api), same division of labour assessReadiness already draws
// for today's Assess: this package stays plain values in, Assessment out.
type TomorrowInput struct {
    TodayVerdict        Verdict
    ProjectedTSB        float64
    HaveProjectedTSB    bool
    ACWR                float64
    HaveACWR            bool
    ConsecutiveHardDays int
}
func ForecastTomorrow(in TomorrowInput) Assessment
```

- [ ] RED: `TodayVerdict: Rest` alone gives `Caution` (never `Rest`) with
  its own reason, every other field zero/false; `Rest` comes only from
  `ProjectedTSB < −30`, and wins when both fire, with the today-rest
  reason still listed after it; `ProjectedTSB` −29
  (no reason) vs −30 (rest reason, `formatSigned` reused — assert the
  exact string, e.g. "tomorrow's form is projected at −34"); `HaveProjectedTSB:
  false` with `ProjectedTSB: -50` set anyway produces no TSB reason (the
  caller's "I couldn't compute this" must be trusted over a leftover zero
  value); `ACWR` 1.49 (no reason) vs 1.5 (caution reason) with `HaveACWR`
  gating the same way; `ConsecutiveHardDays` 1 (no reason), 2 (caution
  reason "tomorrow would be your third hard day in a row"); rest reasons
  before caution reasons when both a rest and a caution rule fire
  together (mirrors today's `Assess` ordering); `Ready` (zero value in,
  all flags false) returns `Assessment{Verdict: Ready}` with no reasons.
- [ ] GREEN; `just check`; commit `"Forecast whether tomorrow's hard session is at risk"`.

### Task 2: Builder, sync observability, API

**Files:** `apps/api/internal/adapter/session.go` (export
`estimatePlannedTSS` as `EstimatePlannedTSS`, thin wrapper, keep the
unexported one used internally — or rename in place, caller's choice, but
keep `overloadedWeek`'s own call site working); new
`apps/api/internal/api/tomorrowforecast.go` (+ test);
`apps/api/internal/api/readiness.go` (+ test) for the extended GET and new
POST; `apps/api/internal/api/adaptation.go` for the sync-tick log line;
`server.go` route registration; `apps/web/src/api/{types.ts,client.ts}`.

**Produces:**

```go
// internal/api/tomorrowforecast.go

// forecastTomorrow builds today's readiness.TomorrowInput and returns the
// forecast plus the eligible workout (or ok=false when tomorrow has
// nothing generated/hard/untouched to ease) — the one call site that knows
// how to gather what readiness.ForecastTomorrow needs, same role
// assessReadiness already plays for today's Assess.
func (s *Server) forecastTomorrow(ctx context.Context, rider string, today time.Time, workouts []workout.Workout, sessions []workout.CompletedSession, latest *workout.FitnessSnapshot, todayAssessment readiness.Assessment, profile workout.RiderProfile) (readiness.Assessment, workout.Workout, bool)

// projectedTSB rolls latest's CTL/ATL forward one day through today's own
// load — actual TrainingLoad summed from any session already dated today,
// else adapter.EstimatePlannedTSS over today's own generated hard
// workout(s) when an FTP is set, else 0 (an easy or rest day truly
// contributes ~0, which is the realistic input here, not a missing value).
// ok is false with no snapshot, or one more than 2 days stale (same
// freshness gate readiness.tsbFresh already applies to today's own rule).
func projectedTSB(latest *workout.FitnessSnapshot, todayLoad float64, today time.Time) (tsb float64, ok bool)

// acwrThroughToday is readiness's own acute:chronic ratio, recomputed with
// today's own load appended to the daily loads used for today's Assess —
// "as of the end of today" rather than today's Assess's own "as of
// yesterday" (today's session may not be logged yet when Assess runs at
// sync time). ok mirrors readiness's own 21-day-coverage gate.
func acwrThroughToday(loads []readiness.Load, todayLoad float64, today time.Time) (ratio float64, ok bool)

// consecutiveHardDays counts the unbroken run of calendar days, ending at
// and including today, that have a generated hard/key workout on them
// (scheduler.IsHardSession) — a day with none breaks the run. Capped at 7:
// nothing in ForecastTomorrow's own rule needs more.
func consecutiveHardDays(workouts []workout.Workout, today time.Time) int
```

```go
// internal/api/readiness.go changes

type tomorrowForecastDTO struct {
    Date        string   `json:"date"`
    Risk        string   `json:"risk"`
    Reasons     []string `json:"reasons,omitempty"`
    WorkoutID   string   `json:"workoutId"`
    WorkoutName string   `json:"workoutName"`
}
// readinessResponseDTO grows: Tomorrow *tomorrowForecastDTO `json:"tomorrow,omitempty"`

// handleGetReadiness: reads ?today=, defaults to s.now() UTC exactly like
// handleUpcomingRides's own `from` param; builds Tomorrow via
// forecastTomorrow, nil when !ok or the forecast is Ready.

// handleEaseTomorrow: POST /api/training/readiness/tomorrow/ease?today=...
// Recomputes forecastTomorrow fresh (never the GET response's own cached
// shape); 409 with an explanatory error if now Ready or !ok; otherwise
// applies Downgrade (Rest) or StepDown (Caution) to the eligible workout
// through the exact same application code adaptRider already uses for
// today's own readiness-driven changes (factor that application out of
// adaptRider into a shared helper if it is not already reachable outside
// the per-rider loop — do not duplicate the Downgrade/StepDown-and-
// AdjustedMarker logic here), reason "Eased ahead of time — " + reasons,
// StepDownSourceID "readiness-forecast:<tomorrow date>" for the Caution
// case. 200 with the applied reason on success.
```

```go
// internal/api/adaptation.go — inside AutoScheduleTick's per-rider loop,
// after AdaptWorkouts's own per-rider work, using inputs already in hand:
if f, _, ok := s.forecastTomorrow(ctx, rider, s.now(), workouts, sessions, latest, assessment, profile); ok && f.Verdict != readiness.Ready {
    s.logger().Info("tomorrow's session may need easing", "rider", rider, "risk", string(f.Verdict))
}
```

- [ ] RED:
  - `projectedTSB`: uses actual today-dated session load over the
    estimate when both exist; falls back to the estimate with no session
    yet; no FTP and no session today → `ok=false`; snapshot dated 3+ days
    before today → `ok=false`; matches `readiness.ForecastTomorrow`'s own
    −30 boundary end to end through `forecastTomorrow`.
  - `acwrThroughToday`: today's own load changes the ratio relative to
    `dailyLoadsForReadiness`'s own retrospective figure; < 21 days
    coverage → `ok=false`.
  - `consecutiveHardDays`: a rest/easy day breaks the run; a run of 3
    caps correctly; a gap in the workouts slice (no workout that date at
    all) counts as broken, same as an explicit easy day.
  - `forecastTomorrow`: no eligible tomorrow workout (none generated, not
    hard, already done, or already `AdjustedMarker`-touched) →
    `ok=false`; eligible workout present but every input clean → `Ready`.
  - `handleGetReadiness`: `tomorrow` omitted (`nil`) when `Ready` or
    `!ok`; present with the right shape otherwise; `?today=` changes which
    calendar day is treated as tomorrow; owner-only.
  - `handleEaseTomorrow`: applies `Downgrade`/`StepDown` correctly per
    verdict; 409 when recomputation comes back `Ready` or `!ok`; a second
    call (or a direct second `AdaptWorkouts`/sync-tick run) makes no
    further change because the workout is no longer `IsGenerated` —
    assert this by checking the workout's own `IsGenerated` result
    post-ease, not by asserting on a separately-invented marker; today's
    own workout is provably untouched by an ease call made on a day where
    today's own readiness *also* eased a (different) workout; owner-only.
  - Ease 409s, each its own test: fresh forecast `Ready`; workout already
    adjusted (`AdjustedMarker`); workout rider-authored; workout done;
    workout deleted. Response carries a plain message, nothing changes.
  - Sync tick: logs at Info exactly when the recomputed forecast is not
    `Ready`, attributes are rider + verdict word only (spy logger asserts
    no TSB/ACWR/load value), and never mutates a workout by itself — assert via a spy
    logger and via workouts being byte-for-byte unchanged after a tick
    that only logs.
  - All of the above run under `TZ=UTC` and `TZ=Europe/Brussels`.
- [ ] GREEN; `just check` + CI-equivalent typecheck; commit `"Warn about tomorrow's hard session when recovery is at risk"`.

### Task 3: UI

**Files:** create `apps/web/src/components/plan/TomorrowForecastBanner.vue`;
modify `TrainingPlanPage.vue` (or wherever tomorrow's workout row already
renders) to mount it, and `apps/web/src/api/client.ts` for the new
`ease` call.

- [ ] Banner copy exactly per the spec's API-and-UI section (forecast
  wording, "Ease it now, or wait for tomorrow's readiness check."). A 409
  from the ease call shows a warning toast with the server message (same
  handling as the replan 409) and refetches.
- [ ] Banner on tomorrow's workout row, shown only when the API's
  `tomorrow` field is non-null: "Tomorrow may be too much" (`caution`,
  warning colour/icon) or "Tomorrow is likely too much" (`rest`, error
  colour/icon) — icon + label, never colour alone, same convention as
  today's readiness chip; reasons in a `UPopover`, keyboard reachable; an
  **Ease tomorrow** button that calls the ease endpoint and refetches the
  readiness response afterwards (the banner disappears on its own once
  `tomorrow` comes back `null`, no separate "eased" state to track).
- [ ] Verify: `just check`, CI-equivalent typecheck, browser test with a
  seeded fitness snapshot and completed sessions that push `ProjectedTSB`
  below −30 or `ACWR` above 1.5 for tomorrow (removed afterwards), light/dark,
  375px; commit `"Show a Plan-page warning when tomorrow's session is at risk"`.
