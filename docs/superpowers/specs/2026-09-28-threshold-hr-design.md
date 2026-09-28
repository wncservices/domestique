# Threshold heart rate (LTHR) — design

Status: draft 2026-09-28. Extends
`2026-09-28-threshold-detection-design.md`, which explicitly named lactate
threshold HR "out of scope" while FTP, max HR and threshold pace shipped.

## Why

Every HR-only target this app produces today — `workoutlib.target`'s and
`scheduler.enduranceZoneTarget`'s HR fallback, the Fitness page's HR zone
chart — is a percentage of **max HR**. Max HR is the wrong anchor for
training intensity: it says nothing about where the aerobic/anaerobic transition actually sits.
Every serious HR-based method (Friel's zone system, TrainingPeaks,
intervals.icu, Garmin's own "Lactate Threshold" guided test) anchors zones
to **LTHR** instead. A runner or HR-only cyclist — no power meter, no
Stryd — is training off the least useful number this app has.

Joe Friel's own method: 30 minutes as hard as you can hold, alone; average
heart rate over the **last 20 minutes** is LTHR. His shorter cycling
variant is a 20-minute all-out test with LTHR = 0.95 × its average HR.
Garmin watches auto-detect a *running* LTHR from a sustained hard effort
and Connect exposes it through `latestLactateThreshold`, the undocumented
endpoint `internal/garmin/biometrics.go` already calls for
`ThresholdPaceSecPerKM` (its `speed` field). Its heart-rate field has never
been read; that is the only "Garmin's own detected LTHR in data this app
already fetches" — worth reading, not worth a new endpoint, running-only
(Connect has no cycling equivalent, so a cyclist's LTHR comes from ride
detection, as FTP mostly does).

## What is detected (42-day window)

**Best sustained 20-minute HR** per analysed ride (any sport):
`rideanalysis.BestHR`, the highest 20-minute rolling mean of the ride's HR
samples, built with the same `bestRollingMean` that builds `PowerCurve`'s
20-minute power and `BestSpeeds`' windows. Spike-robust by construction —
an average over 1200 s, unlike a peak (`MaxHR` already records that) — and
unlike a whole-ride average it is not dragged down by warmup or coasting.

**LTHR estimate = round(0.95 × the highest best-20 in the window).** An
organic best-20 effort resembles Friel's 20-minute all-out test more than
the last 20 of a 30-minute test, so his 0.95 correction applies. It also
errs the safe way: an underestimated LTHR only makes zones easier.
Needs at least one analysed ride with ≥ 20 minutes of HR samples in the
window; a shorter ride contributes nothing (`PowerCurve` likewise leaves
out a window longer than the ride rather than reporting a low 0).

Stored as `session_analyses.best_hr_1200 INTEGER NOT NULL DEFAULT 0`
(idempotent add-column in `UseDB`, like `max_hr` / `best_speed_1200`), filled
by `rideanalysis.Analyze`. The 0.95 lives in `internal/thresholds`, not in
storage, so it can change without a migration.

## When a value changes

| | Up | Down |
|---|---|---|
| Threshold HR | estimate ≥ current + 1 bpm | never |

- **Up delta is flat +1 bpm**, not a percentage: same as max HR's
  `maxHRUpDelta`.
- **No down rule**, like max HR. A 90-day stretch of endurance-only riding
  never contains a near-threshold 20-minute effort, so a "nothing reached
  95 %" rule would fire false suggestions at exactly the riders doing base
  training, and LTHR moves little with fitness anyway. A rider who really
  has a lower LTHR edits the field.
- **Empty or estimated field:** applied automatically through
  `autoprofile.Apply` (`workout.FieldThresholdHR = "threshold_hr"`, added to
  the `Estimated` list like `FieldMaxHR` / `FieldThresholdPace`, no separate
  bool). The sync result lists it and the UI toasts it.
- **Garmin biometric:** `Biometrics.ThresholdHR` fills the same field under
  the same rules — only when empty or estimated, never a rider-typed value —
  in the same pass as the pace. Ride detection runs after biometrics, so a
  ride-derived estimate ≥ 1 bpm higher wins that sync, as max HR already
  behaves.
- **Rider-typed field:** never overwritten; a suggestion is stored and shown
  like the other three: "New threshold heart rate detected: 162 bpm (was
  158) — from Saturday's ride. Update?" → Update / Dismiss. A dismissed
  suggestion reappears only after a further ≥ 1 bpm move.

## Zones: Friel %LTHR when known, %MaxHR otherwise

There is **one** Friel %LTHR table per sport, used for both workout
generation and the Fitness-page display; the existing %MaxHR tables stay
exactly as they are for a rider with no LTHR.

| Zone | Cycling | Running |
|---|---|---|
| Z1 | < 81 % | < 85 % |
| Z2 | 81–89 % | 85–89 % |
| Z3 | 90–93 % | 90–94 % |
| Z4 | 94–99 % | 95–99 % |
| Z5a | 100–102 % | 100–102 % |
| Z5b | 103–106 % | 103–106 % |
| Z5c | > 106 % | > 106 % |

**Workout generation** (`workoutlib.zoneHRRange` / `target` and
`scheduler.enduranceZoneTarget`). Today's HR percentages are %MaxHR bands
(`tempo` 0.80–0.88 of max HR is far below Friel's tempo, which is 90–93 % of
*LTHR*) and are **not** reused. When `profile.ThresholdHR > 0`, HR targets
are `ThresholdHR ×` these ranges (a Z1 floor of 0.60 is used only to define
"top half"):

| Workout zone | Cycling | Running |
|---|---|---|
| recovery / warmup / cooldown / rest step (`""`) | Z1 top half: 0.70–0.81 | 0.72–0.85 |
| endurance (`enduranceZoneTarget`) | Z2: 0.81–0.89 | 0.85–0.89 |
| `tempo` | Z3: 0.90–0.93 | 0.90–0.94 |
| `sweet_spot` | upper Z3–low Z4: 0.92–0.96 | 0.92–0.96 |
| `threshold` | Z4: 0.94–0.99 | 0.95–0.99 |
| `vo2max`, `intervals` | Z5b: 1.03–1.06 | 1.03–1.06 |
| `anaerobic` | Z5c: 1.07–1.10 | 1.07–1.10 |

Any resulting HR target is capped at `MaxHR` when it is known (low and high
both `min(x, MaxHR)`), so Z5c never asks for a heart rate above the rider's
ceiling. Verified against the code: `zoneHRRange` maps exactly the zone
names `tempo`/`sweet_spot`/`threshold`/`vo2max`/`intervals`/`anaerobic` plus a
default easy bucket (warmup, cooldown, rest, currently 0.50–0.60 of max HR);
`Instantiate` calls it for the rung's work step and hard-codes `0.50, 0.60`
for the rest step; `Warmup`/`Cooldown` call `zoneHRRange("")`;
`scheduler.enduranceZoneTarget` hard-codes 0.60–0.75 of max HR. The FTP/pace
branches of `target` and the max-HR fallback numbers are untouched.
`target` takes the zone name instead of `hrLow/hrHigh` so it can choose
either table; the rest step uses the easy zone.

**Fitness page display.** The HR block shows 5 bands, so Z5a–c collapse to
one Z5 (≥ 100 %). Cycling: < 81, 81–89, 90–93, 94–99, ≥ 100. Running:
< 85, 85–89, 90–94, 95–99, ≥ 100. `hrZones(profile)` takes the whole
profile and returns this table when `thresholdHr` is set, today's %MaxHR
table otherwise. The profile has no sport, so the display uses the running
table only for a rider with a threshold pace and no FTP, else cycling.
Z1's data `low` is 0 (Friel gives no floor) and renders "below N bpm" like
`paceZones`' Z1; the bar sizes it from a 0.65 floor.

**Stored time-in-zone.** `rideanalysis.HRZoneSeconds` follows the same
choice, made once in `Analyze`: LTHR with the ride's sport's edges
(`Input` gains `Sport`) when the rider has one, else `MaxHR` with today's
`hrZoneEdges`. A ride's stored zone-seconds must mean the same "Z3" as the
table the page draws.

## Data

- `session_analyses.best_hr_1200 INTEGER NOT NULL DEFAULT 0`.
- `rider_profiles.threshold_hr INTEGER NOT NULL DEFAULT 0` beside
  `max_hr`/`resting_hr`; `RiderProfile.ThresholdHR`; estimated state via the
  `Estimated` list.
- `threshold_suggestions.field` gains the value `"threshold_hr"` — the
  column is free text, no schema change.
- `garmin.Biometrics.ThresholdHR int`, from `latestLactateThreshold`. The
  reference's heart-rate key spelling (`hearRate` vs `heartRate`) is
  **unverified against a live response**, so the parser accepts both
  (`number(o, "heartRate", "hearRate")`) and takes it from the same object
  that supplied the accepted speed; range-checked (about 90–220 bpm) and
  failing closed to 0.

## Package

`internal/thresholds.Ride` gains `BestHR1200 int`; `Profile` gains
`ThresholdHR int` / `ThresholdHREstimated bool`; `Detect` gains
`detectThresholdHR` after `detectPace` (fixed order: ftp, max_hr,
threshold_pace, threshold_hr). It is `detectMaxHR`'s shape — flat +1 bpm up,
no down — with the 0.95 factor; `Finding.Field` gains `"threshold_hr"`.
Reason wording: "from Saturday's best 20-minute heart rate (171 bpm)".

## API and UI

- No new endpoints; DTOs unchanged except the new `field` value, mirrored
  in `apps/web/src/api/types.ts` (`ThresholdSuggestion`, `DetectedThreshold`,
  `ThresholdField`).
- `fitnessMath.ts`: `thresholdFieldLabel` ("threshold heart rate"),
  `thresholdFieldTitle`, `formatThresholdValue`/`Number` (bpm) gain a case.
  `ThresholdSuggestions.vue` needs no logic change.
- `ProfileForm.vue`: a Threshold HR field beside Max HR / Resting HR with the
  usual "detected from <date>" provenance.
- `TrainingZones.vue`: the HR header reads "Threshold HR 162 bpm" (or "Max
  HR …" when LTHR is unknown) so the basis is visible.

## Rulings (judgement calls, for review)

1. **0.95 × best 20-minute HR.** Cost if wrong: an organic effort that was
   really a steady 30-minute one is underestimated by ~5 %, so zones run
   slightly easy until a higher effort arrives; the factor is one constant.
2. **No down rule.** Cost if wrong: a rider whose LTHR truly fell keeps
   easy-side-wrong zones until they edit the field; no false suggestions for
   base-training riders is judged the better trade.
3. **A Friel table for workout targets, not the old %MaxHR bands.** Cost if
   wrong: HR-target steps change for riders with LTHR (Z2 endurance is now
   81–89 % of a higher anchor); riders without LTHR see no change.
4. **Display sport heuristic** (running table only for pace-without-FTP).
   Cost if wrong: a rider doing both sees the cycling bands 4 points off on
   Z1/Z2 for running; per-sport display is a small later addition.
5. **Garmin LTHR from the running endpoint fills the shared field.** Cost if
   wrong: a running LTHR (typically higher than cycling's) lands on a
   cyclist's profile as an estimate; ride detection overrides it once a
   ≥ 1 bpm higher effort is analysed, and the rider can edit it.

## Out of scope

`workout.TrainingLoad`'s HR case keeps `avgHR / MaxHR` (an LTHR-based
intensity is a separate change); recalibrating progression levels; a
cycling LTHR test workout; Z5a/b/c granularity on the Fitness page.
