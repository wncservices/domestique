# Training load model — design (sub-project 1)

Status: implemented 2026-10-18. Detailed design for section 1 of
`2026-10-18-adaptive-training-builder-design.md`.

## Why

A plan week is sized in hours from available time and fixed phase fractions.
Your fitness never sets the load, so a rider who has become fitter is never asked
for more, and a rider coming back is asked for the same hours as before. This
sub-project sizes each week in **TSS from current CTL** and a ramp per phase,
keeps available hours as a hard cap, and shows the target and the planned CTL
on the Plan page.

## Decisions

| Question | Decision |
|---|---|
| Where TSS becomes sessions | In `periodization`: TSS target → hours through the rider's typical IF, capped by available hours. The scheduler keeps sizing by hours; one new option steps rungs up when the hours cap bites. |
| UI | The Plan page shows the TSS target and hours per week, plus the planned CTL line. |
| A dated goal whose target is easy to reach | Ramps are never scaled *below* the phase defaults; load stays high. Ramps are scaled up, capped at the rider's maximum ramp, only when the target needs it. |
| Without fitness data | Exactly today's hours model (no FTP, no snapshot, or no available hours). |

## Model

All of this is pure, in `internal/periodization`.

**Inputs.** `LoadInput`:

| Field | Meaning |
|---|---|
| `CTL`, `ATL` | today's values (latest snapshot, rolled forward to today with zero load) |
| `TypicalIF` | `√(Σload ÷ (Σhours × 100))` over the last 42 days, clamped 0.55–0.85, 0.70 below 3 h of history |
| `HistoryWeeks` | whole weeks from the first completed session to today |
| `BestRecentWeekTSS` | highest Monday–Sunday load sum over the last 4 complete weeks |
| `MaxRamp` | 4 beginner, 6 intermediate or unset, 8 advanced (`ExperienceLevel`) |
| `TargetCTL` | dated goal: `projection.TargetCTL(EventDuration(distance, elevation))`; undated: 0 |

**Ramp per week** (CTL points per week; `posInBlock` as today's 3:1 cycle):

| Phase | Ramp |
|---|---|
| base | 3, 4, 5 by `posInBlock` 0, 1, 2 |
| build | 5, 6, 7 by `posInBlock` 0, 1, 2 |
| peak | 1 |
| recovery (base/build, every 4th) | none — the week is 0.65 × the previous non-recovery week's TSS |
| taper | solved, see below |

Each ramp is capped at `MaxRamp`.

**TSS for a ramp.** `WeeklyTSS(ctl, ramp) = 7 × (ctl + ramp ÷ (1 − e^(−7/42)))`: a constant
daily load that moves a 42-day EWMA by `ramp` in 7 days. `NextCTL` and `NextATL` roll a
week at constant daily load with `workout.RollFitness`'s constants, so the forecast and
the fitness history use one formula.

**Forecast.** Week 0 starts at today's CTL. Each later week starts at the CTL the
previous week would produce if ridden as planned (`PlannedCTL`). Far weeks are a
forecast; a week is re-sized from real CTL when `refreshWeek` rebuilds it as next week.

**Dated goal.**

- The default ramps run to the peak week's end.
- If the resulting CTL is below `TargetCTL`, every non-recovery base/build/peak ramp is
  multiplied by the smallest factor `s ≥ 1` (bisection, 1e-3) that reaches it. Each ramp
  is still capped at `MaxRamp`.
- If even `MaxRamp` everywhere falls short, `Plan.Shortfall = TargetCTL − reached`.

**Taper.**

- Taper weeks share one constant daily load `L`, solved by bisection. On the event date,
  projected TSB (`CTL − ATL`, rolled day by day) must land at the midpoint of the event's
  form band (`projection.BandFor`, passed in as `TaperTSB`).
- `L` is clamped to `[0.3, 1.0] ×` the last pre-taper week's daily load.

**Undated goal.**

- The ceiling is `capHours × TypicalIF² × 100 ÷ 7`, the CTL the available hours can hold.
- Non-recovery weeks ramp until `PlannedCTL` reaches the ceiling; a ramp is trimmed so it
  lands on it, not over. After that the ramp is 0 (hold), and recovery weeks continue.

**Spike guard.** When `HistoryWeeks < 6` and `BestRecentWeekTSS > 0`, plan week *i* is at
most `BestRecentWeekTSS × 1.15^(i+1)`, but never below 0.6 × the hours model's `TargetHours` for
that same week (capped by what the load itself asked for), so a small best week does not leave a new
rider with near-empty weeks for months; `TargetTSS` is recomputed from the floored hours.

**Hours.**

- `capHours = HoursPerAvailableDay × len(AvailableDays)`.
- `TargetHours = min(capHours, TargetTSS ÷ (TypicalIF² × 100))`.
- `LoadCapped` is set when the cap applied.

**Result fields.**

- `Week` gains `TargetTSS`, `PlannedCTL` (at the week's start), `Ramp` and `LoadCapped`.
- `Plan` gains `LoadBasis` (`"ctl"` or `"hours"`), `TargetCTL` and `Shortfall`.
- With `LoadInput == nil`, every new field is zero and `LoadBasis` is `"hours"`. The old
  fields are byte-identical to today.

## One TSS estimator

- `adapter.PlannedTSS` moves to `workout.PlannedTSS`, unchanged; `adapter.PlannedTSS`
  delegates to it. That breaks the scheduler → adapter import cycle.
- `estimatePlannedTSS` / `EstimatePlannedTSS` are removed. Their callers (the 7-day
  overload trigger, tomorrow's forecast) use `workout.PlannedTSS`, falling back to
  `hours × 0.65² × 100` when it is not ok.
- This is the one intended behaviour change outside sizing: a structured session's
  planned TSS is now counted step by step, so the overload trigger compares against a
  higher, truer plan.

## Scheduler

- `scheduler.WithTargetTSS(tss float64)` is passed only for a week with `LoadCapped`.
- After building, the scheduler sums `workout.PlannedTSS` of the cycling requests
  (FTP from the profile). Below `tss`, it rebuilds with every structured slot's target
  level raised by 0.5, then by 1.0, and keeps the first build that reaches `tss`, or the
  +1.0 one.
- No other effect. Running or no FTP is a no-op.

## API

- **`loadInput(ctx, g, rider, profile) (*periodization.LoadInput, error)`** returns
  `nil, nil` when the profile has no FTP, no available hours, or the rider has no
  fitness snapshot.
- **`reconciledPeriodizationPlan`** calls `periodization.BuildWithLoad`. It runs
  `adapter.Reconcile` only when `LoadBasis == "hours"`, because CTL already reflects what
  was ridden.
- **`fillWeek`, `refreshWeek`, `scheduleGoalWeek`** add `scheduler.WithTargetTSS(week.TargetTSS)`
  when `week.LoadCapped`.
- **DTOs:**
  - `periodizationWeekDTO` gains `targetTss`, `plannedCtl` and `loadCapped`.
  - `periodizationPlanDTO` gains `loadBasis`, `targetCtl` and `shortfall`.
  - `weekFocusDTO` gains `targetTss`.
  - `types.ts` mirrors all of these.
- The narration prompt names the first week's TSS target when `LoadBasis == "ctl"`.

## Web

- **SeasonTimeline:** with `loadBasis === 'ctl'`, bar heights come from `targetTss`, an
  SVG polyline draws `plannedCtl`, and the title and aria-label read "420 TSS · 7.5 h".
  A plan with `shortfall > 0` shows one line: "Target fitness out of reach by N CTL at
  your maximum ramp."
- **WeekStrip and Today:** with `targetTss`, the label reads "done / target TSS · h".

## Testing

- `periodization` table tests cover:
  - ramps per phase;
  - `WeeklyTSS` round-trips with `NextCTL`;
  - a reachable dated goal never ramps below the defaults;
  - an unreachable goal sets the shortfall;
  - the undated hold at the ceiling;
  - the spike guard;
  - the hours cap and `LoadCapped`;
  - the taper landing in the band within 1 TSB;
  - the nil path equal to `Build` for every existing fixture.
- `scheduler`: `WithTargetTSS` raises planned TSS and never exceeds +1.0 level; running
  and no-FTP are no-ops.
- `api`, over HTTP:
  - a rider with snapshots gets `loadBasis: "ctl"` and TSS-sized weeks;
  - a rider without FTP gets today's hours plan;
  - a refresh re-sizes next week after a ride raises CTL.
- Every existing hours test stays green unchanged.

## Out of scope

- The ledger and debt, readiness v2, the re-optimiser (sub-projects 2–4).
- Per-rider overrides of ramps or target CTL.
- Running-specific load: running uses the same TSS proxy from `TrainingLoad`.
