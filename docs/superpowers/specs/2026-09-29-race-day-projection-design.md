# Race-day projection — design

Status: draft 2026-09-29. Builds on the season plan (`seasonplan.go`, #352), which now turns every
week up to a goal's event into real planned workouts.

## Why

The Fitness page shows CTL, ATL and TSB up to today, and the Plan page shows phases, but nothing
answers the question a rider asks six weeks out: "if I ride the plan, will I arrive fresh and fit?"
Competitors all project forward. TrainingPeaks' Performance Management Chart extends past today
using planned workouts' estimated TSS, and coaches read the race-day TSB off it. intervals.icu
draws the same future line from calendar workouts (fitness, fatigue and form for future dates).
TrainerRoad simulates the next 28 days and predicts FTP, a different model (adaptation, not
load). Domestique now has the ingredient the first two need, a fully planned season, and already
owns the math (`workout.RollFitness`) and a one-day roll-forward (`projectedTSB` in the
readiness forecast). This feature is the same roll, run to the event.

Deliberately read-only. It tells the rider what the plan does to their form; it changes nothing.

## Projection

Pure `projection.Roll(in Input) []Point`, on the same conventions as `workout.ComputeFitness`:
dates are `YYYY-MM-DD` strings, a snapshot's CTL/ATL are the values at the **start** of its date,
and TSB is CTL - ATL at that moment, so the race-day value is what the rider has at the start
line, before the event's own load.

- **Start:** the rider's latest fitness snapshot. Its date can be well before today (snapshots run
  to the last session, not to today), which is fine: the roll covers the gap.
- **Daily load, per day from the snapshot date to the day before the event:**
  - before today: the completed sessions' `TrainingLoad` summed for that date, 0 if none;
  - today: completed sessions if any have synced, else the planned workouts' estimate (the rule
    `todayTrainingLoad` already uses);
  - after today: the planned workouts' estimated TSS for that date, summed, **0 for a day with
    none**. A day that has a completed session never also counts its planned workout.
- **Roll:** `workout.RollFitness(ctl, atl, load)` per day, unchanged. No second copy of the 42-day
  and 7-day constants.
- **Which workouts:** every workout the rider owns dated in the window, not only the target
  goal's: tests, rider-built sessions and other goals' sessions all load the body. A B or C event
  is not itself a workout, so its own race load is not counted (stated in the assumptions).
- **Series out:** one point per day, `{date, ctl, atl, tsb, load}`, from today to the event
  (history stays the snapshots' job). Capped at 400 days; a goal further out reports
  `unavailable: "too far"`.

### Planned TSS estimate

`adapter.EstimatePlannedTSS` (used by readiness and overload) takes the **first** power target's
midpoint as the whole workout's intensity, and workouts open with a power-targeted warm-up, so
for structured sessions it reads low. Fine for "is today hard", wrong for a season sum. New
`adapter.PlannedTSS(w, ftp) (tss float64, ok bool)` sizes the workout step by step:

- Per step (descending into repeats): watts = midpoint of a power target; otherwise a fixed
  fraction of FTP by step intensity (warm-up/cool-down 0.55, rest 0.45, active 0.65, the same
  0.65 default `defaultPlannedIF` already uses).
- Normalised power by Coggan's fourth-power mean: `NP = (sum(t * P^4) / sum(t))^(1/4)`,
  `IF = NP / FTP`, `TSS = hours * IF^2 * 100`. The 30-second smoothing is ignored (steps are
  minutes long).
- Distance-based steps carry no duration and count 0; `ok` is false when nothing is sized (no
  FTP, or no time steps), and that workout contributes nothing.
- Cycling only. A run or a swim has no FTP-based estimate and contributes 0; the assumptions
  say so when the rider has non-cycling planned workouts.

`EstimatePlannedTSS` is left alone; moving readiness onto `PlannedTSS` is a follow-up.

### Cost and caching

A season is about 130 to 200 workouts of some 20 steps: about 4000 step visits plus at most 400
daily rolls, microseconds. Inputs are reads the Plan and Fitness handlers already make
(`ListWorkouts`, `ListGoals`, `ListFitnessSnapshots`, `ListSessions`, `GetProfile`,
`ScheduledWeeks`). **Computed on every read, nothing cached, no table.** A cache would need
invalidating on every reschedule, replan, ride sync, FTP change and goal edit, all of which
change the answer, for a computation cheaper than the JSON encode of the response.

## Race-day readout

`GET /api/training/projection` returns the projection for the **primary event**: the nearest
future A-priority goal, else the nearest future goal of any priority (then with no verdict, see
below). Optional `?goal=<id>` picks another goal.

- Race day: projected CTL, ATL and TSB at the event date.
- **Target TSB band** by the event's estimated duration (below), and a **target CTL**.
- A verdict, a ramp warning and at most one suggestion.
- `events`: every future goal within the horizon with its projected CTL and TSB on its date
  (B and C are info only: no verdict, no band colouring).

### Event duration and target bands

Estimated duration: `distanceKm / 28 + elevationM / 1000` hours, from the goal's
`TargetDistanceM` / `TargetElevationM` (a 100 km, 1500 m gran fondo comes out at 5.1 h). A goal
with neither gets the medium band; one with distance and no elevation uses distance alone.

| Duration | Example | TSB band on race day |
|---|---|---|
| under 2 h | criterium, time trial, short race | +10 to +25 |
| 2 to 4 h | road race, medium fondo | +5 to +20 |
| over 4 h | long gran fondo, sportive | +5 to +15 |

Sources: TrainingPeaks and its coaches put the "freshness" zone at +5 to +25 and most riders'
race day at +5 to +15, coaches aiming higher (+15 to +25) for short, punchy events; time
trialists and climbers +10 to +20; beyond about +25 is usually an over-taper. No source validates
one universal number, and long events are ridden at lower intensity, so they tolerate less
freshness and reward more fitness. The bands are a default, not a finding, and the UI says
"typical", not "required".

**Target CTL** is a rule of thumb, `eventTSS / 4` clamped to [30, 120], with
`eventTSS = hours * IF^2 * 100` and IF 0.95 / 0.85 / 0.75 for the three bands (a 5 h fondo:
0.75, about 280 TSS, target CTL 70). It exists only so "undertrained" has a yardstick; it is
labelled "typical for this event" and not a goal the rider set.

### Verdict rules

First match wins; numbers rounded to whole points, minus rendered as "−".

| Key | When | Message | Tone |
|---|---|---|---|
| `unavailable` | no FTP, no snapshot or session ever, event today or past, over 400 days | reason ("Set your FTP to project your form") | none |
| `incomplete` | any plan week up to the event is not in `scheduled_weeks` | "Plan is still being built, projection covers N of M weeks" | info |
| `undertrained` | CTL < 85 % of target CTL | "Undertrained: fitness 48 vs 60 target" | warning |
| `fatigued` | TSB < band low | "Too fatigued: form −6, consider a longer taper" | warning |
| `fresh` | TSB > band high | "Very fresh: form +31, a shorter taper keeps more fitness" | info |
| `on_track` | otherwise | "On track: form +12 on race day" | success |

`undertrained` outranks `fatigued`: a taper cannot fix missing fitness, so it is the bigger
message, and the form number is appended ("... and form is −6"). The 15 % tolerance stops a
rule-of-thumb target from nagging a rider who is close. B and C events get no verdict, only
"Projected form +8 on 12 Jun". `incomplete` still returns the series it has.

### Ramp warning

Weekly ramp = CTL at a Monday start minus CTL seven days earlier, over each Monday from the
current week to the event. Warn when it exceeds **8**: Friel and Coggan/Allen put a workable ramp
at 5 to 8 CTL per week for most riders, with 3 to 5 the conservative long-run figure. Result
`ramp: {maxPerWeek, warnings: [{weekStart, perWeek, excessTss}]}`, where `excessTss` is
`(perWeek - 8) * 42`, the weekly load that ramp takes (about 42 TSS of load per CTL point per
week at steady state). Shown as "Week of 3 Nov ramps +11 CTL, about 130 TSS over a sustainable
build". Not shown for a rider whose CTL is under 20 (from a standing start, a few TSS/day is
mathematically a big weekly ramp).

### Suggestions (offered, never applied)

At most one, only when the event is 7 or more days away:

- `fatigued`: **what-if taper.** Re-run the tail with the planned load of the last D days
  scaled to 60 % for D in 7, 10, 14, 21 and report the first that reaches the band's low edge:
  "Starting your taper 10 days out instead would land form at about +6". None reaches: say so.
- Ramp warning: the `excessTss` line above (a suggestion to trim, not a change).
- `fresh`: none; the message already says a shorter taper. `undertrained`: no fix exists inside
  the plan; none.

There is no "apply". Changing a plan stays where it is today (goal edit, replan); a later
one-click "extend the taper" is out of scope. The response carries the suggestion as text plus
the numbers, so the UI can show it without re-deriving anything.

## Assumptions, shown in the UI

Returned as `assumptions: string[]` and shown behind an "About this projection" disclosure:

- "Assumes you ride the plan as written." Always.
- "Uses your FTP of N W today; targets will move if your FTP does."
- "Planned load is estimated from each workout's power targets; cycling sessions only." when the
  rider has other planned sports.
- "Days with no planned workout count as rest."
- "Does not include the load of B and C events." when any fall before the target event.
- "Event length estimated at N h from distance and elevation." or "No distance stated: assuming
  a medium-length event."

Not modelled: compliance (a rider who skips 30 % of sessions reads better than they are). A
follow-up could scale future load by trailing compliance; excluded here because a fudge factor
the rider cannot see is worse than a stated assumption.

## API and UI

- `GET /api/training/projection` -> `{ available, reason?, goal, points, raceDay, band, targetCtl,
  verdict, ramp, suggestion?, events, assumptions }`. Owner-only under `PermManageTraining`;
  the rider comes from the session; 404 for a `goal` that is not theirs, never 403.
  No health value (CTL, TSB, watts) is written to a log line beside a rider name; the handler
  logs rider, goal id and verdict key only. Failure of one input degrades to `unavailable`, not a
  500, the way `analysesSince` degrades.
- `apps/web/src/utils/fitnessMath.ts` gains the pure display helpers (extend series, band
  clamp); DTOs mirror by hand in `types.ts`.
- **Fitness chart:** `FitnessChart` takes an optional `projection`. The x-domain extends to the
  event; history stays solid, the future CTL, ATL and TSB are dashed, the event is a vertical
  marker with its name, the target band is a shaded strip on the TSB axis (a success-token
  tint, not raw Tailwind), the crosshair reads "Projected" beside future values. A new "To
  race" range key shows six weeks back to the event; the other ranges stay history-only and add
  the future only when the event falls within them. With no projection the chart is exactly as
  today.
- **Fitness page:** below the status card, a short race-day card: verdict, "Fitness 62,
  form +12, typical +5 to +15", the ramp warning and the suggestion, and the disclosure.
- **Plan header:** a `RaceDayChip` beside the priority badge for an A goal (verdict text, tone
  colour, `title` carrying the numbers); for a B or C goal a neutral "Form +8 projected".
  `SeasonTimeline` gets a marker per future event, coloured by its info-only tone (no verdict
  text).

## Data

None. No table, no column, no stored projection: everything is derived from rows that already
exist, so there is no schema to migrate. The invariant that matters is that the endpoint writes
nothing, which a test asserts.

## Testing

- `projection`: a flat plan converges toward its daily load; a hand-computed 3-day roll matches
  `workout.RollFitness` exactly; the gap between snapshot and today uses actual loads with 0
  for empty days; a completed session replaces that day's planned workout; today uses actual
  else planned; TSB at the event is start-of-day (excludes the event's own load); the taper
  what-if; ramp weeks, the 8 boundary (8.0 no warning, 8.01 warning), the CTL-under-20 exemption.
- Verdicts: every key, band edges inclusive (+5, +15 on_track), the 85 % boundary,
  undertrained outranking fatigued, no-distance default band, over 400 days.
- `adapter.PlannedTSS`: hand-computed NP and TSS for a warm-up plus repeat block plus
  cool-down; open and HR targets use the intensity fractions; a distance-only step counts 0;
  no FTP is `ok=false`; the result exceeds `EstimatePlannedTSS` for a structured session.
- API under `TestEachEngine` (the workout reads go through `dbx`): 200 shape, owner-only 404,
  incomplete plan, B/C events listed without verdicts, no write occurs, no health values in logs.
- Frontend: `fitnessMath` helpers.
- Every clock is fixed with an explicit zone; tests pass under UTC and Europe/Brussels,
  including a `now` at 23:30 UTC, which is already tomorrow in Brussels.

## Out of scope

Auto-changing the plan, an "apply" button, compliance scaling, a rider-set target CTL or TSB
band on the goal, running and swimming loads, FTP or progression drift over the season,
multi-day events, projecting past the event, TrainerRoad-style FTP prediction, notifications.
