# Post-ride survey and a visible "Why?" — design

Status: draft 2026-09-29. Builds on progression levels (`2026-09-27-progression-levels-design.md`),
training adaptation (`2026-09-27-training-adaptation-design.md`), readiness
(`2026-09-28-readiness-design.md`, `2026-09-28-readiness-tomorrow-design.md`), threshold detection and
level recalibration (`2026-09-28-*`), and FTP tests (`2026-09-29-ftp-tests-design.md`).

## Why

Two gaps against the competition:

- **TrainerRoad and JOIN ask after every ride.** TrainerRoad's post-workout survey is a 1-5 effort
  scale (Easy, Moderate, Hard, Very Hard, All-out) and feeds Adaptive Training next to the power data;
  JOIN shows a feedback card after an activity (how hard did it feel, did you do what was planned).
  Domestique already has the effort tap (`FeelRating.vue`, `PUT /api/training/sessions/{id}/feel`), but
  it is nearly inert (below), and it never asks about legs or life stress, the two things a power file
  cannot show.
- **TrainerRoad and Garmin change the plan without saying why.** Domestique already writes a reason
  into a workout's description (`Adjusted automatically: ...`), but it is free text with the numbers
  baked in, only on a workout, and nothing explains a progression level or FTP moving. A rider
  looking at a swapped session or a lowered level should be able to see the rule, the inputs and
  the day, in one tap.

This feature extends the survey and makes every automatic change explain itself. It changes no
rule about *how much* to adapt, except the one survey signal below.

## What the feel rating does today

Checked in code, not assumed:

- `session_analyses.feel` (0 = unrated, 1-5) and `level_delta`. `handleSetSessionFeel` re-applies
  the ride's level change with the new feel; a re-rate replaces the stored delta.
- `progression.Delta` uses feel **only** to adjust a *nailed* bump (1-2: +0.2, 3: 0, 4: -0.1,
  5: -0.2 on a 0.3 base, floor 0.1). On a `completed`, `struggled` or `incomplete` ride it changes
  nothing.
- Feel reaches neither the adapter (`AdaptSessions`), fatigue detection, readiness nor load.
  Rating a ride "all-out" on a session that scored `nailed` leaves the plan exactly as it was.
- `struggled` comes from `rideanalysis` alone (`outcomeFrom`: under target on hard steps). The
  adapter turns it into a same-zone step-down (`stepDownTarget`), and two struggled key sessions
  in 14 days into a fatigue downgrade (`lastTwoStruggledKeySessions`).

## The survey

One card, about ten seconds, after an analysed ride. Effort keeps its existing tap; two optional
rows appear under it once effort is chosen.

| Input | Values | Stored | Notes |
|---|---|---|---|
| Effort (RPE) | 1 Easy, 2 Moderate, 3 Hard, 4 Very hard, 5 All-out | `feel` (exists) | Same column, same labels, so existing ratings stay valid |
| Legs | `fresh`, `normal`, `heavy` | `legs` (new) | Optional |
| Life stress | `low`, `normal`, `high` | `stress` (new) | Optional; never asked before a ride |

**Effort stays 1-5, not CR-10.** Foster's session-RPE uses the modified Borg CR-10 (2 easy,
4 somewhat hard, 6 hard, 8 very hard, 10 maximal), multiplied by minutes for a load in arbitrary
units. The five labels map onto CR-10 anchors (1 to 2, 2 to 4, 3 to 6, 4 to 8, 5 to 10), so the
tap keeps its resolution and existing data and the one-tap flow stay, at the cost of two-point
steps. Legs and stress are the "how do you feel" inputs coaches read alongside HRV and sleep; here
they are used only as below.

`PUT /api/training/sessions/{id}/feel` body becomes `{feel: 1..5, legs?: string, stress?: string}`.
It is a full replace: an omitted or empty `legs`/`stress` clears it. `feel` stays required (the
survey starts with it); an unknown `legs`/`stress` value is a 400. The analysis DTO gains `legs?`
and `stress?`. Owner-only, 404 for another rider's session, as today.

### How each input is used

1. **Effort 5 on a planned productive session is a struggle signal.** `SessionAnalysis`
   gains `EffectiveOutcome()`: `struggled` when `feel == 5`, the ride matched a planned workout and
   the stored outcome is `nailed` or `completed`; otherwise the stored outcome. It is the only place
   the rule lives, and these read it instead of `Outcome`: `stepDownTarget` and
   `lastTwoStruggledKeySessions` (the adapter), and `progression.Delta` (via the handler and
   `applyProgressionForAnalysis`). So an all-out "nailed" ride steps the next same-zone session
   down, counts toward the two-struggles fatigue swap, and earns no level bump (Delta 0 instead of
   the +0.1 floor); an under-target or cut-short ride was already `struggled`/`incomplete` and is
   untouched. Reason text says whose word it is: "Tuesday's threshold session felt all-out to you
   (5 of 5)". Efforts 1-4 change nothing new; the existing nailed-bump table stays.
2. **Heavy legs on consecutive days is a readiness caution.** `readiness.Assess` gains the survey
   days (date, legs, stress) of the last three days. Caution reason when legs were `heavy` on two
   consecutive days ending today or yesterday ("you reported heavy legs on Monday and Tuesday"), or
   when the latest day is heavy and stress was `high` on both days ("heavy legs after two
   high-stress days"). High stress on those days is added to the wording when present. It is a
   caution reason only: it never produces `rest`, and on its own only steps today's generated hard
   session down one rung, exactly like an HRV caution. A single heavy report, or stress alone,
   does nothing.
3. **Session-RPE replaces the flat load guess when there is no power or HR.** `TrainingLoad` falls
   back to `hours x 50` with neither power nor HR. With an effort rating that fallback becomes
   `hours x 100 x IF^2` using IF 0.55, 0.65, 0.78, 0.90, 1.00 for efforts 1-5 (a TSS-shaped estimate,
   so it sits on the same fitness chart), and the load basis reads "session RPE". It applies at sync
   (looking up the ride's feel) and when a ride is rated. A ride with power or HR keeps its measured
   load; effort never overrides a measurement.
4. **None of it ever makes a day harder.** Effort 5, heavy legs and high stress can only ease:
   step-down, downgrade, a caution reason, or a smaller level move. Effort and legs never raise a
   session, unlock a harder one or clear a readiness caution. (The existing +0.2 nailed bump for an
   easy effort predates this and moves a level, not a day; unchanged.)
5. **One automatic change per workout still holds.** The survey adds no new writer. Its signals
   enter through `AdaptSessions` (struggle) and `readiness.Assess` (caution), which already produce
   at most one `Change` per workout, guarded again by `adaptRider`'s `appliedFor` and by
   `scheduler.IsGenerated` (the `AdjustedMarker` in the description). Rating a ride does not trigger
   an immediate change; the next scheduling pass (every half hour) reads it, as it reads everything.

Not used: stress or legs on a single day, on unplanned rides, or in the tomorrow forecast (its
inputs are load-based by design; a heavy report today is the natural extension, out of scope).

## Why? on every automatic change

### Data: a structured record, not structured text

A new table, `adjustments`, one row per automatic change, written where the change is applied:

| Column | Meaning |
|---|---|
| `id` | random hex |
| `rider` | owner; every read filters on it |
| `subject_kind` | `workout` or `level` |
| `subject_id` | workout id, or `sport:zone` for a level |
| `rule` | stable id, below |
| `inputs` | JSON, per rule (typed struct in `internal/why`) |
| `text` | the sentence shown, produced at write time (what `Change.Reason` is today) |
| `day` | the clock's calendar day when it happened |
| `created_at` | timestamp |

Unique on `(subject_kind, subject_id, day, rule)` with an upsert, so a retried pass is idempotent.
The workout id is `subject_id` with kind `workout`; the kind lets a progression level carry its own
reason without a second table. A workout shows its latest row by `created_at`.

Why a table over text: the description note has to stay (it is the marker that enforces one change
per workout, and the fallback), but it cannot hold typed inputs, cannot attach to a level, and the
frontend would have to parse numbers back out of prose, which `adjustmentNote` already only shows
verbatim. Why `text` next to `inputs`: the sentence is the headline and is already generated at
each site; storing it avoids a renderer that must reproduce every historical wording.
`internal/why` (pure) owns the rule ids, the input structs and `Facts(rule, inputs)`, which turns
inputs into the label/value rows the popover lists; the frontend renders what it is given.

Rules recorded, each from the code that already decides it:

| `rule` | Written by | Inputs |
|---|---|---|
| `readiness_rest` | `AdaptSessions` rest downgrade | verdict, signals |
| `readiness_caution` | `AdaptSessions` caution step-down | verdict, signals |
| `missed_moved` | missed key session moved | from date, to date, replaced easy workout? |
| `fatigue_struggles` | two struggled key sessions (includes effort-5 rides) | the two sessions (date, zone, outcome, hard steps hit, felt-all-out flag) |
| `fatigue_overload` | 7-day load over plan | analysed TSS, planned TSS, ratio |
| `struggle_step_down` | same-zone step-down | source session (date, zone), level from/to |
| `ftp_test_eve` | `easeBeforeFTPTests` | test date, protocol |
| `threshold_auto` | detection auto-applies a field | field, from, to, the finding's reason |
| `level_recalibration` | `recalibrateLevelsForFTP` | FTP from/to, zone, level from/to, trigger rule |
| `season_refresh` | `refreshWeek` (a week rebuilt as it comes within reach) | level or FTP the session was built from, and now; name from/to |

**The season refresh** (`seasonplan.go`) rebuilds a far week's untouched sessions in place when it
becomes next week, and leaves the description as the generated one, so unlike every other
change it carries no text note and the rider today cannot tell it happened. It records a row only
when the session's name, steps or level actually changed ("Rebuilt Monday from your current
levels: Threshold 4.6, was 4.2"). Because the description stays generated, the session can
still take one adaptation later; the popover then shows that newer row.

Readiness signals carry the numbers. `readiness.Assessment` gains `Signals []Signal` beside
`Reasons` (kind `hrv`, `sleep`, `readiness`, `resting_hr`, `form`, `load`, `survey_legs`; a label
and its values), and `readiness.Day` gains `HRVLastNight` and `HRVWeeklyAvg` (already in
`daily_wellness`). That is what makes "HRV low two nights (38, 41 ms vs usual 52)" possible;
`Reasons` and every existing caller are unchanged.

### Privacy

- `inputs` and `text` hold the rider's own health values (HRV, sleep, resting HR, load, legs,
  stress). They are returned only inside DTOs the owner already gets (a workout on their own week,
  their own levels), behind the same `isOwnTraining` check; there is no endpoint that lists
  adjustments.
- **Never logged next to a rider name.** Today `adaptRider` logs `"rider", rider, "reason",
  c.Reason`, and reasons contain sleep scores and HRV status; the feel handler logs the effort
  value beside the rider. Both change: the adaptation log carries rider, workout id, change kind
  and **rule id** only; the feel log carries rider and session id only.
- Deleted with their subject: `DeleteWorkout` removes its adjustment rows, `riderdelete` removes
  all a rider's, and level rows keep the latest five per subject.

### API and UI

- `workoutDTO` gains `why?: {rule, title, text, day, facts: [{label, value}]}`, filled by one
  batched read per response (never per workout). `progressionLevelDTO` gains the same `why?` from
  the latest `level` row. Types change together with `apps/web/src/api/types.ts`.
- `WhyPopover.vue` (Nuxt UI `UPopover`): a small "Why?" trigger; content is the title (the rule's
  name), the text, a facts list and the day. Example: "Eased one level on Tue: HRV low two nights
  (38, 41 ms vs usual 52); readiness caution".
- It replaces the plain adjustment line on the day card and in the workout slide-over, and sits on
  each Progression card zone that has a `why`. The week strip keeps its tooltip (a tile is too
  small for a popover).
- **Fallback.** A workout with `Adjusted automatically:` text and no row (anything adjusted before
  this ships, or a failed record write) shows the same popover with the text alone, from
  `adjustmentNote`, and no facts. A level with only `reason` shows the reason as today.
- `FeelRating.vue` is extended, not duplicated: effort row as now (saves on tap), then legs and
  stress rows that save on tap, each re-sending the full state; toast copy unchanged.

## Data

Idempotent in `UseDB`: `CREATE TABLE IF NOT EXISTS adjustments` plus its index (on
`rider, subject_kind, subject_id`), and add-column for `session_analyses.legs TEXT NOT NULL DEFAULT ''`
and `session_analyses.stress TEXT NOT NULL DEFAULT ''`. `SaveAnalysis` leaves them out of its
conflict update, like `feel`.

## Testing

- `why`: `Facts` for every rule; inputs round-trip through JSON; an unknown rule yields no facts.
- `workout` (`TestEachEngine`): adjustments upsert idempotent on the unique key, owner filter,
  batched read, latest row wins, delete with subject, level pruning to five; the new columns exist
  on an old DB and survive a re-analysis.
- `SessionAnalysis.EffectiveOutcome`: effort 5 turns nailed/completed into struggled; not
  unplanned, not effort 4, not incomplete.
- `adapter`: an effort-5 nailed session steps the next same-zone session down and counts toward the
  two-struggles swap; still one `Change` per workout; each `Change` carries its rule.
- `readiness`: heavy legs on consecutive days is caution (never rest); one day, a gap day, or
  stress alone is nothing; heavy plus two high-stress days is caution; `Signals` carry the HRV
  numbers.
- `progression`: effort-5 nailed gives 0; feel 1-4 rows unchanged.
- `workout.TrainingLoad`: power and HR untouched; the no-data fallback uses RPE per effort.
- `refreshWeek`: a changed session records `season_refresh` once; an unchanged one records nothing.
- API: feel body validation and full replace; owner-only 404; `why` on workouts and levels for each
  rule; fallback when no row; no health value in any captured log line.
- Fixed clocks with an explicit zone; tests pass under UTC and Europe/Brussels.

## Out of scope

Survey before a ride, per-step RPE, a CR-10 slider, free-text notes, a mood or illness input, using
legs or stress in the tomorrow forecast, a history page of adjustments, `why` for rider-typed
changes (the rider knows), and telling anyone else (crews see none of this).
