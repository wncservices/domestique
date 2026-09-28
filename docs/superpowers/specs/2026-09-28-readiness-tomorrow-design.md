# Readiness for tomorrow's plan — design

Status: draft 2026-09-28. Builds on `2026-09-28-readiness-design.md`
("Readiness"), which this spec calls **today's readiness** throughout to
keep the two apart. Explicitly out of scope there: "readiness for
tomorrow's plan" — this is that follow-up.

## Why

Today's readiness reacts to last night's HRV, sleep, Training Readiness and
resting HR — signals that only exist once the rider has already woken up.
That is enough to ease *today's* session, but it means a rider planning
their evening only finds out tomorrow's session got no easier at all — it
either was never at risk, or it is and nothing has looked ahead. Garmin's
Daily Suggested Workouts, WHOOP's next-day guidance, TrainerRoad's Adaptive
Training and intervals.icu's fitness chart all give a rider some form of
look-ahead:

- **Garmin DSW** recomputes its suggestion after every activity, from
  Training Load (acute vs. chronic), Training Status and **Recovery
  Time** — hours until the body is ready for another hard session,
  estimated from the EPOC of the most recently recorded activity.
- **TrainerRoad's Red Light Green Light** highlights a future day on the
  calendar yellow or red *before it arrives*, from a fatigue model of
  recent training stress, and Adaptive Training swaps a hard workout
  already sitting on a red day for an easier one.
- **intervals.icu** rolls CTL/ATL/TSB forward through the workouts already
  on the calendar, so a rider can see projected form on any future date,
  not only today's.

The common thread, and the one piece none of them can dodge: **none of them
have tomorrow's HRV or sleep either** — those signals genuinely do not
exist until tomorrow morning. What they look ahead with instead is training
load already committed to the calendar (today's planned or completed
session folded into tomorrow's projected form) and load ratios that already
include today. That is exactly what this feature reads — nothing invented,
nothing that pretends to know how tomorrow's body will feel.

This is a **forecast**, not a second automatic easing pass. Today's
readiness already eases the day it actually applies to, using the same
morning-of signals every rider's watch has by then; tomorrow morning's own
readiness pass will run exactly as it does today and is the one that acts
on HRV/sleep/RHR once they exist. This feature's job is narrower: warn a
rider *tonight* that tomorrow's hard session is at risk, and offer to ease
it right now rather than wait and be surprised in the morning. It never
changes tomorrow's plan on its own.

## Data — nothing new is fetched

No new Garmin endpoint. `Activity` (`internal/garmin/activities.go`)
already carries `TrainingStressScore`; it does not carry Garmin's own
Recovery Time, and today's sync does not read or store it. Adding that
field would need a new endpoint and a schema change for one more Garmin
signal, on top of what `2026-09-28-readiness-design.md` already ships —
not justified for a first version when the load-based signals below cover
the same "was today too much" question using data already synced. See
"Out of scope."

Everything this feature reads already exists:

| Signal | Source |
|---|---|
| CTL/ATL as of the start of today | latest `workout.FitnessSnapshot` (`ListFitnessSnapshots`) |
| Today's own load | today's completed sessions' `TrainingLoad` if any exist yet, else an estimate from today's planned hard workout's power targets and the rider's FTP (`adapter`'s existing `estimatePlannedTSS`, exported for reuse — see plan) |
| Daily loads for the 7/28-day windows | completed sessions' `TrainingLoad`, same aggregation `assessReadiness` already does for today's ACWR |
| Consecutive hard days | generated workouts' own dates and `scheduler.IsHardSession` — no new field |
| Tomorrow's session | `ListWorkouts` — the same generated/hard/untouched checks today's readiness already uses |
| Today's own verdict | `readiness.Assess`'s result, already computed once per pass — not recomputed |

## Forecast rules — `internal/readiness.ForecastTomorrow` (pure)

Only evaluated when tomorrow has a workout that is generated
(`scheduler.IsGenerated`), hard (`scheduler.IsHardSession`) and not yet
done — the same three gates today's readiness applies to *today's*
session. No such workout, no forecast: there is nothing to warn about and
nothing an "ease tomorrow" click could do.

```go
type TomorrowInput struct {
    TodayVerdict        Verdict // today's own Assess result
    ProjectedTSB        float64 // CTL/ATL rolled forward one day through today's own load
    HaveProjectedTSB    bool    // false with no snapshot, or one more than 2 days stale
    ACWR                float64 // 7-day / 28-day mean load, with today's own load counted
    HaveACWR            bool    // same 21-day-coverage gate as today's readiness
    ConsecutiveHardDays int     // hard/key generated sessions today and the unbroken run before it
}
func ForecastTomorrow(in TomorrowInput) Assessment
```

`Assessment` is the same `{Verdict, Reasons}` shape today's readiness
returns — `rest` and `caution` mean exactly what they mean today (swap for
easy, step down one rung), just decided a day in advance and never applied
without the rider clicking "Ease tomorrow."

- **rest** if:
  - `HaveProjectedTSB` and `ProjectedTSB < −30` — the same threshold
    today's readiness uses for form, one day rolled forward; reason:
    "tomorrow's form is projected at −34" (same `formatSigned` as today).
- **caution** if not rest and any of:
  - `TodayVerdict` is `rest` — reason: "you needed to rest today, and
    tomorrow is a hard session too". This is `caution`, not `rest`:
    tonight's sleep and HRV can still recover, and a rest day today
    already addresses the cause, so this signal alone asks for a one-rung
    step-down at most, never a swap for an easy session;
  - `HaveACWR` and `ACWR ≥ 1.5` — the same threshold and the same ratio,
    now counting today's own load; reason: "with today's session counted,
    your load this week is 1.6× your usual";
  - `ConsecutiveHardDays ≥ 2` (today plus yesterday both hard, making
    tomorrow the third) — reason: "tomorrow would be your third hard day
    in a row".
- **ready** otherwise — no banner.

Two deliberately reused numbers, not new ones: −30 TSB and 1.5 ACWR are
today's readiness's own thresholds, rolled one day forward rather than
invented fresh. A rider who has internalised what those numbers mean today
does not need to learn a second set for tomorrow.

`ConsecutiveHardDays` and the ACWR/TSB inputs are gathered by a new
API-layer builder next to `assessReadiness` (see the plan) — `readiness`
itself stays pure and unaware of `workout.Workout`, `garmin`, or the store,
same as today.

## What it may change

Only **tomorrow's** generated, untouched, hard session — never today's
(today's own readiness pass owns that), never a rider-authored workout,
never more than the one session. Nothing changes on its own: the forecast
is read-only until a rider clicks **Ease tomorrow**.

That click re-runs the forecast fresh (never trusts what the banner showed
when the page loaded — training data can change between the two) and, if
still `rest` or `caution`, applies exactly the same two paths today's
readiness already uses, just targeted at tomorrow's workout instead of
today's:

- `rest` (projected form below −30) → `Downgrade` (swap for the easy variant), reason "Eased ahead of
  time — " + reasons.
- `caution` → `StepDown` (one rung down its own ladder), reason "Eased
  ahead of time — " + reasons, `StepDownSourceID`
  `"readiness-forecast:<tomorrow's date>"` — a distinct marker from
  today's own `"readiness:<date>"`, so the two are told apart in a
  workout's description if anyone ever reads it, though nothing here
  actually depends on that: `scheduler.AdjustedMarker`, already appended
  by the same `Downgrade`/`StepDown` application path, is what makes the
  workout no longer `IsGenerated` — which is also what stops tomorrow
  morning's own readiness pass from ever touching it again. **This is the
  entire double-easing guard, and it is not new code**: it is the same
  one-change-per-workout rule today's readiness already relies on, applied
  to a workout one day sooner. Tomorrow's morning pass sees an
  already-adjusted workout and skips it exactly as it would skip any other
  workout today's readiness already eased.

If the rider does nothing, tomorrow's real readiness pass runs on schedule
and acts on whatever HRV, sleep, Training Readiness and resting HR actually
show that morning — this feature never pre-empts that pass, it only offers
to get ahead of it.

## Timezone

"Tomorrow" is the browser's local tomorrow, not the server's. Both the
forecast endpoint and the ease action take the caller's own idea of today
as a `?today=YYYY-MM-DD` query parameter — the exact precedent
`handleUpcomingRides` already sets (`apps/api/internal/api/rides.go`) for
the same reason: this deployment's riders are not necessarily in the
server process's timezone, but the browser always knows its own local day.
Omitting the parameter (any caller besides this app's own frontend) falls
back to the server's UTC today, same as `handleUpcomingRides`.

The background sync tick has no browser to ask, so its own advisory log
line (see Observability below) uses the server's UTC "tomorrow" — that is
allowed to be up to a few hours off near local midnight because it is
Info-level and observational only. The GET and POST paths a rider actually
sees and acts on are always anchored to the browser's date, never the
sync tick's.

## API and UI

- `GET /api/training/readiness?today=YYYY-MM-DD` — extends today's
  response with `tomorrow: { date, risk, reasons, workoutId, workoutName }
  | null` — `null` when tomorrow has no eligible workout or the forecast
  is `ready`. Same owner-only gate as the rest of the endpoint.
- `POST /api/training/readiness/tomorrow/ease?today=YYYY-MM-DD` —
  recomputes the forecast; 200 with the applied change's reason on
  `rest`/`caution`; **409 with a plain server message** (nothing is
  changed) when the fresh forecast is now `ready`, or tomorrow's workout
  is no longer eligible — no longer generated, already adjusted, already
  done, or gone. The endpoint never falls through to "ease something
  anyway." Owner-only.
- Plan page: a banner on tomorrow's workout row, shown only when
  `tomorrow` is non-null. It must read as a **forecast**, not a verdict,
  and say tomorrow morning's own check still runs. Exact copy (title +
  first reason inline, remaining reasons in the popover):
  - caution: "Tomorrow's {zone} session may be too much — {reason}. Ease
    it now, or wait for tomorrow's readiness check."
  - rest: "Tomorrow's {zone} session is likely too much — {reason}. Ease
    it now, or wait for tomorrow's readiness check."
  - e.g. "Tomorrow's threshold session may be too much — your form will be
    about −32. Ease it now, or wait for tomorrow's readiness check."
  Reason phrasing in the banner uses the future tense ("your form will be
  about −32"); the API's `reasons` carry the plain form from the rules
  above and the component maps the TSB reason to the banner wording.
  Icon + label per today's readiness chip's own convention, an **Ease
  tomorrow** button. A 409 from the ease call is handled like the replan
  409: a warning toast showing the server's message, then a refetch (the
  banner disappears if it no longer applies). Once eased, the workout carries the same "Adjusted
  automatically" note every other adaptation already renders on the Plan
  page, and the banner's own `tomorrow` field goes back to `null` on the
  next fetch (the workout is no longer `IsGenerated`) — no separate
  "already eased" UI state to build.

## Observability

The sync tick (`AutoScheduleTick`, after `AdaptWorkouts`) computes the same
forecast per rider it already has the inputs for and logs at **Info**
when it comes back `rest`/`caution` — "tomorrow's session may need
easing" with the rider and the verdict word only. **Counts only: no TSB,
ACWR or load value ever appears next to the rider name** (form and load
are health-adjacent; today's readiness logs the same way). This is
observation only; it changes nothing, per **What it may change** above. It
exists so an operator (and a future notification, if ever built — not
this feature, see Out of scope) has something to look at without waiting
for a rider to open the Plan page. No new metric: a per-tick log line
answers "did this fire," and nothing here aggregates across riders in a
way a dashboard would watch yet.

## Testing

- `readiness`: table tests for every rule and boundary (TSB −29/−30,
  ACWR 1.49/1.5, 1/2/3 consecutive hard days), `TodayVerdict: rest` alone
  giving `caution` (never `rest`), and `rest` only from projected TSB
  (which still wins when both fire), `HaveProjectedTSB`/
  `HaveACWR` false suppressing their rules, reason wording and order
  (rest reasons before caution reasons, same as today's `Assess`).
- The new API-layer builder: projected TSB uses today's actual load when a
  session already synced today, the estimate otherwise; no FTP means no
  TSB-based reason (same gate `estimatedWeek`'s own FTP check already
  uses), no snapshot or a stale one means no TSB-based reason either;
  consecutive-hard-days counts correctly across a rest day that breaks the
  run.
- Fixed clocks throughout (`Server.Clock` or explicit dates), and every
  date-window test passes under `TZ=UTC` and `TZ=Europe/Brussels` — none
  of this logic should ever read the process's local timezone, only the
  `?today=` parameter or explicit dates, and a test run under each TZ is
  what actually proves that rather than assumes it.
- `TestEachEngine` is not needed here: nothing new is persisted (see
  "What it may change" — no new table, the forecast is recomputed on
  every sync tick and every page load, the same choice today's own
  `assessReadiness` already makes rather than caching an Assessment).
- API: owner-only on both endpoints; `POST .../ease` re-reads fresh state
  rather than trusting the caller's idea of the forecast; 409 with the
  message and no change when the fresh forecast is `ready` or tomorrow's
  workout is no longer generated/untouched/hard/undone (each case tested
  separately); ease applied to tomorrow only, never today's session
  even when today's own readiness has already eased something.
- Sync-tick log: a spy logger sees no TSB/ACWR/load value in any attribute.
- UI: 409 shows a warning toast with the server message; banner copy
  matches the spec strings.
- Acceptance: forecast appears on the Plan page only with an eligible
  tomorrow workout; the "Adjusted automatically" note appears after
  clicking Ease; a second click (or the next morning's own pass) makes no
  further change.

## Out of scope

Garmin Recovery Time (would need a new endpoint and a new stored field —
today's load-based signals cover the same question without it); a second
automatic easing pass (tomorrow morning's own readiness pass already owns
acting on real HRV/sleep/RHR); notifications (push, email — nothing in
this app sends either today); forecasting further than one day ahead;
easing anything other than tomorrow's single hard session; a persisted
forecast history (recomputed fresh every time, like today's readiness
itself).
