# FTP tests — design

Status: draft 2026-09-29. Builds on threshold detection (`2026-09-28-threshold-detection-design.md`)
and level recalibration (`2026-09-28-level-recalibration-design.md`).

## Why

Detection (eFTP from the power curve) only learns FTP when a rider happens to ride a hard
20-minute effort. Riders who train mostly at endurance or sweet-spot never trigger it, so their
FTP, and every target and progression level derived from it, goes stale. Today the only test is
`POST /api/training/tests/ftp`, a single open-target 20-minute workout with no timing advice
and no result capture: the rider reads their own average, multiplies by 0.95 and types it in.
TrainerRoad schedules a Ramp Test at the start of each block (roughly every 4 to 6 weeks) and
advises testing after a recovery week, when fresh; intervals.icu and Xert instead detect
passively and never ask. This feature does both: detection stays the default, and a test is
*suggested* when detection has gone quiet or the plan reaches a fresh moment.

The rider asked for three things: several kinds of test, advice on *when* in the plan, and a
regular nudge so FTP stays current. Separately, workouts must be doable on an indoor trainer
(its own backlog item, not designed here; see "Smart trainers").

## Test menu

Cycling only. All need a power meter or smart trainer. FTP is computed from the ride's power
curve (`rideanalysis.PowerCurve` gains an 8-minute window, key `480`).

| Protocol | id | Duration | Formula | Needs | For |
|---|---|---|---|---|---|
| Ramp | `ramp` | ~35 min | 0.75 x best 1-minute power | FTP guess (profile, or typed at scheduling) | Default. First test, or anyone who paces badly; short, nothing to pace |
| 20-minute | `twenty_minute` | ~60 min | 0.95 x best 20-minute power | nothing | Experienced riders who can pace an all-out effort; needs no FTP guess |
| 2 x 8-minute | `two_by_eight` | ~65 min | 0.90 x the higher 8-minute power | nothing | Riders who fade in a 20-minute effort; the second effort checks the first |

Protocols:

- **Ramp:** 5 min easy, then 1-minute power steps from 50 % of the estimated FTP, +6 % of the
  estimate each minute (steps capped at 170 %), ridden to failure, then 5 min easy. Absolute
  watts rounded to 5 W. (TrainerRoad's ramp steps by 6 % of the estimate and takes 75 % of the
  best minute; Zwift's steps a fixed 20 W from 100 W with the same 75 %.) The best minute comes
  from a rolling window over the whole ride, so it can sit a few seconds off a step boundary
  and read marginally high; accepted.
- **20-minute:** the existing workout, kept (10 min warmup, 3 x 1 min openers, 5 min primer at
  hard-not-max, 10 min easy, 20 min all-out steady, cooldown).
- **2 x 8-minute:** 15 min warmup with openers, 8 min all-out, 10 min easy, 8 min all-out,
  cooldown. The best 8-minute is the higher of the two by construction.

Steps are `TargetOpen` except the ramp's power steps, and each protocol's description carries
its pacing advice and the formula, so a rider without the app still knows the result.
`fitnesstest.FTPFromTest(protocol, curve) (watts, ok)` is the single implementation of the
formulas; `ok` is false when the needed curve key is absent.

## Result capture

A test is a workout with `test_protocol` set. Ride analysis already links a ride to its planned
workout (`session_analyses.workout_id`). When such an analysis exists:

1. The sync computes `FTPFromTest` from that ride's power curve and stores it on the workout
   (`test_result_watts`, once; a second sync finds it set and does nothing).
2. `thresholds.Ride` gains `TestProtocol`; for that ride the FTP candidate **is the test
   value** (reason "from Tuesday's ramp test (312 W best minute)") instead of the eFTP
   candidates, so the ride does not also produce a competing eFTP suggestion.
3. Then the existing path: an empty or estimated field auto-applies, a rider-typed one becomes
   a pending suggestion, and accepting or auto-applying triggers recalibration exactly as today.
4. `ftp_verified_at` on the profile moves to the test date whether or not FTP changed.

**A test outranks eFTP** in three ways, all in `thresholds`:

- A test value fires on a change of at least 1 % in either direction. Up is not held to eFTP's
  3 %, and down needs neither the 90-day history nor the 95 % rule: a rider who tested lower
  is told at once.
- While a test ride is inside the 42-day window, an eFTP candidate wins over it only when it
  is at least 3 % higher (a real breakthrough); eFTP alone never proposes lowering an FTP that
  a recent test set.
- A test finding never auto-applies when it moves FTP by more than 25 % either way (the
  recalibration cap): it becomes a suggestion whose reason says so.

A ride with no usable value (e.g. no power) records nothing and toasts "Couldn't read a result
from that ride". The sync result gains `ftpTests: [{workoutId, protocol, ftpWatts, date,
outcome}]` with outcome `applied` | `suggested` | `confirmed` (within 1 %) | `unreadable`; the
UI toasts "FTP updated to 268 W, from Tuesday's ramp test", "Your ramp test suggests 268 W,
review it on the Fitness page" or "Your FTP test confirms 255 W".

## When a test is suggested

Computed on read; nothing is stored except a snooze. Pure `testschedule.Suggest(in) *Suggestion`
over the focus goal's periodization plan, the rider's profile, upcoming workouts and goals,
and `now`. A suggestion needs: a cycling rider with power evidence (a completed session with
average power in the last 90 days, or an FTP on file), no test already scheduled today or
later (scheduling a test ends the nagging), no active snooze, and no test ridden in the last 14
days. Only a ridden test silences it: `ftp_verified_at` cannot tell a rider's own save from the
deploy backfill or an auto-estimate, and counting it silenced every rider for two weeks after
deploy and any brand-new plan for two weeks after an estimate. It still drives `stale`.

A **candidate week** is a Base or Build week that is not a recovery week and not within 14
days before any A-priority event. Never in taper, never in peak. Reasons, first match wins,
looking at the current and next week:

| Reason | When | Message |
|---|---|---|
| `no_ftp` | FTP not set | "Set your FTP with a test" |
| `plan_start` | plan week 1 or 2 (`Week.Number`, anchored on the goal's creation Monday), and no test ridden in the last 42 days | "Start your plan with an FTP test. Tuesday would be ideal" |
| `estimated_ftp` | FTP is only an estimate from rides (`FTPEstimated`) and no test has ever been ridden | "Your FTP is an estimate from your rides. A test would pin it down" |
| `after_recovery` | first week after a recovery week | "Time for an FTP test. Tuesday after your recovery week would be ideal" |
| `block_start` | first week of the Build phase | "A new block starts. Test now so its targets are right" |
| `stale` | `ftp_verified_at` more than 42 days ago | "It's been N weeks since your FTP was checked" |

A stale rider in a recovery week is pointed at the following week rather than tested tired.
A rider with no plan gets `no_ftp` and `stale` only, on the next available day.

**The day:** the first of the profile's available days, from tomorrow, in the candidate week,
whose previous day holds no key session (`scheduler.IsKeySession`) and that carries no
rider-built or already-ridden workout; only a generated session (or nothing) is replaced.

**Recommended protocol:** the protocol of the rider's last test (like with like), else `ramp`
when FTP is known, else `twenty_minute`. The rider can pick another.

`ftp_verified_at` is the latest of: FTP last changed (any path, set in `SaveProfile` when the
value differs), a test result, and a ride whose eFTP is within 3 % of the current FTP (recent
effort confirms it). Monotonic. Existing profiles with an FTP are backfilled to the deploy day
once, so nobody is nagged the moment this ships. That backfill (and an auto-estimate) sets the
date but does not silence anything: `stale` counts from it, nothing else reads it.

**Snooze:** dismissing stores `ftp_test_snoozed_until` = today + 28 days on the profile. The
banner returns afterwards only if the rules still fire.

## Scheduling a test

`POST /api/training/tests/ftp` gains a body `{protocol?, date?, estimatedFtp?}`; an empty body
keeps today's behaviour (an unscheduled 20-minute workout). With `date`: the workout is
created on that day linked to the focus goal (so scheduling treats the day as taken, and replan
leaves it because its description is not the generated one), and that day's plan-made session,
if any, is deleted (its Garmin copy removed as replan does). 409 when the day is in the past,
already ridden, or `ramp` has no FTP and no `estimatedFtp`.

**Test-day prep:** `adaptRider` gains one rule: a generated, unadjusted hard session dated the
day before a scheduled test becomes `scheduler.EasyVariant` with the `AdjustedMarker` and the
reason "eased the day before your FTP test". It runs after every schedule and replan, so it
survives both. A test is never auto-eased itself; on the day, if readiness is low the day card
says testing tired under-reads FTP and offers the existing Move.

## Smart trainers (interaction with the indoor backlog item)

- **Ramp** is meant for ERG: each 1-minute step is an absolute power target the trainer holds.
- **20-minute and 2 x 8-minute efforts must stay open-target.** ERG locks watts, so a rider
  cannot exceed the target and the test measures the target, not the rider; use resistance
  (level or slope) mode. Warmups and easy steps may be ERG.
- Each protocol carries `trainerMode` (`erg` | `resistance`), shown on its card and in its
  description. The indoor work must not convert these efforts to ERG targets, and must keep
  test workouts out of any "scale to an indoor FTP" pass.
- An FTP tested indoors often differs from outdoors; the result does not record where it was
  ridden (out of scope), the copy only says which mode to use.

## Data

Idempotent add-column in `UseDB`, no new tables:

- `workouts`: `test_protocol TEXT NOT NULL DEFAULT ''`, `test_result_watts DOUBLE PRECISION NOT NULL DEFAULT 0`.
- `rider_profiles`: `ftp_verified_at TEXT NOT NULL DEFAULT ''`, `ftp_test_snoozed_until TEXT NOT NULL DEFAULT ''`.

## API and UI

- `GET /api/training/tests` -> `{ protocols, suggestion?, scheduled, lastTest?, ftpVerifiedAt }`;
  `POST /api/training/tests/ftp/snooze` -> 204. `workoutDTO` gains `testProtocol` and
  `testResultWatts`. Owner-only.
- Plan page: an `FtpTestBanner` above the week strip when `suggestion` exists, with the message,
  a protocol picker, "Schedule it on Tuesday" and "Not now". The picker is `FtpTestPicker.vue`,
  a card per protocol (duration, difficulty, formula, trainer mode, "Recommended"), also
  opened from an "FTP test" action for manual scheduling on any date.
- The week strip and day card show a scheduled test with a test badge; after the result the day
  card shows "Result: 268 W". Result toast from the sync as above.

## Testing

- `fitnesstest`: each formula, missing curve key, ramp steps for a given FTP (count, start,
  increment, cap, 5 W rounding), workout shapes.
- `thresholds`: a test ride replaces its eFTP; 1 % boundary both ways; down without history;
  eFTP beats a test only at +3 %; over 25 % never Auto.
- `testschedule`: every reason; taper, peak, recovery and A-event-within-14-days exclusions;
  stale-in-recovery deferral; day choice; a scheduled test and a snooze silence it.
- Storage under `TestEachEngine`; sync captures once (idempotent), marks verified, ftpTests
  outcomes; scheduling replaces the generated session and 409s; the day-before easing survives
  replan; owner-only 404s.
- Fixed clocks with an explicit zone; tests pass under UTC and Europe/Brussels.

## Out of scope

Running tests (threshold pace), max-HR test timing, auto-inserting tests, indoor trainer
conversion, an Xert-style breakthrough model, recording indoor vs outdoor, manually pointing a
test at an arbitrary ride.
