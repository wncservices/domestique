# Threshold heart rate (LTHR) — design

Status: draft 2026-09-28. Extends
`2026-09-28-threshold-detection-design.md`, which explicitly named lactate
threshold HR "out of scope" while FTP, max HR and threshold pace shipped.

## Why

Every HR-only target this app produces today — `workoutlib.target`'s and
`scheduler.enduranceZoneTarget`'s HR fallback, the Fitness page's HR zone
chart — is a percentage of **max HR**. Max HR is the wrong anchor for
training intensity: it barely moves rider to rider relative to fitness and
says nothing about where the aerobic/anaerobic transition actually sits.
Every serious HR-based method (Friel's zone system, TrainingPeaks,
intervals.icu, Garmin's own "Lactate Threshold" guided test) anchors zones
to **LTHR** instead. A runner or HR-only cyclist — no power meter, no
Stryd — is training off the least useful number this app has.

Joe Friel's own method: ride (or run) 30 minutes as hard as you can hold
alone, no coasting; average heart rate over the **last 20 minutes** is
LTHR, no scaling factor (unlike his power/pace version of the same test,
which uses a 20-minute all-out and multiplies by 0.95 because power decays
less linearly than HR does across the extra 10 minutes). Garmin's watches
auto-detect a running LTHR the same way — a sustained hard effort's
steady-state HR — and expose it through `latestLactateThreshold`, the same
undocumented endpoint `internal/garmin/biometrics.go` already calls for
`ThresholdPaceSecPerKM` (its "speed" field). The reference implementation
this package already cites notes the response's heart-rate field carries a
"hearRate" typo — nobody has read it because nothing needed it before.
That is this project's one closest thing to "Garmin's own detected LTHR
in data this app already fetches": worth reading, not worth a new
endpoint, and running-only (Connect has no equivalent cycling biometric —
a cyclist's LTHR here comes from ride detection, same as FTP).

## What is detected (42-day window, mirrors FTP/max-HR exactly)

**Best sustained 20-minute HR** per analysed ride (any sport): the highest
20-minute rolling average of the ride's HR samples — `rideanalysis.BestHR`,
built the same way `bestRollingMean` already builds `PowerCurve`'s 20-minute
power window and `BestSpeeds`' 1200/1800 windows. This is the spike-robust
choice: an instantaneous max-HR-style peak (already captured separately as
`MaxHR`) says nothing about a *sustained* effort, and a plain ride average
is dragged down by warmup, coasting and traffic lights. A rolling 20-minute
average is exactly what Friel's own test reads off a chart, just taken from
an ordinary hard ride instead of a scheduled test — the same relationship
`FTP`'s eFTP already has to a formal 20-minute test. No 0.95-style scaling:
Friel's own last-20-of-30 reading *is* LTHR, not an input to a formula (the
0.95 factor belongs to his power/pace variant, not the HR one). Needs at
least one analysed ride ≥ 20 minutes in the window; a ride with < 20 minutes
of samples contributes no candidate, exactly like `PowerCurve` leaving a
window out rather than reporting a misleadingly low 0.

Stored as `session_analyses.best_hr_1200 INTEGER NOT NULL DEFAULT 0`
(idempotent add-column in `UseDB`, same shape as `max_hr` /
`best_speed_1200`), filled by `rideanalysis.Analyze`.

## When a value changes

| | Up | Down |
|---|---|---|
| Threshold HR | detected ≥ current + 1 bpm | 90-day history exists and nothing in it reaches current × 0.95 → suggestion only |

- **Up delta is flat +1 bpm**, not a percentage — same reasoning as max HR's
  `maxHRUpDelta`: a percentage doesn't mean anything for a value this size
  and already reported as a whole number.
- **Down is a suggestion, not "never"** — unlike max HR, which only ever
  reads high because a lower observed peak just means the rider didn't go
  that hard that day. LTHR moves with fitness the same way FTP and
  threshold pace do (it *is* a fitness marker, not an anatomical ceiling),
  so a real detraining drop is worth surfacing exactly like FTP's own
  90-day/95% down rule — see "Ruling" below for the alternative considered
  and rejected.
- **Empty or estimated field**: applied automatically through
  `autoprofile.Apply` (`workout.FieldThresholdHR = "threshold_hr"`, added to
  the `Estimated` list the same way `FieldMaxHR` and `FieldThresholdPace`
  already are — no separate `*Estimated` bool, matching those two rather
  than `FTPEstimated`, which predates the list). Garmin's `Biometrics` also
  best-effort-fills it the same pass it fills `ThresholdPaceSecPerKM`, from
  the same `latestLactateThreshold` call — for a runner this can arrive
  before any ride is even analysed.
- **Rider-typed field**: never overwritten; a suggestion is stored and shown
  exactly like the other three — "New threshold heart rate detected: 162
  bpm (was 158) — from Saturday's ride. Update?" → Update / Dismiss.
  Dismissal re-suggests only on a further ≥ 1 bpm move, same rule as max HR.

## Zones: LTHR when known, max HR otherwise

Both places a heart-rate target already exists switch to LTHR the moment
`profile.ThresholdHR > 0`, and keep reading max HR only as the fallback for
a rider detection hasn't reached yet:

- **`workoutlib.target` / `workoutlib.zoneHRRange` and
  `scheduler.enduranceZoneTarget`'s HR case.** Their existing zone-name
  percentages — `tempo` 0.80–0.88, `threshold` 0.88–0.93, `vo2max` 0.90–0.97,
  `anaerobic` 0.93–1.00 — are, unlabelled, Friel's own %LTHR zone
  boundaries; they were only ever applied to max HR because nothing better
  existed. **Reuse them unchanged as %LTHR** when `ThresholdHR` is set;
  fall back to applying the same numbers to `MaxHR` exactly as today when it
  isn't. No new zone-percentage table for workout generation, no rider-
  visible change in a step's target *shape* — only which number it is a
  percentage of.
- **Fitness page HR zone chart** (`fitnessMath.ts`'s `hrZones` /
  `HR_ZONE_EDGES`, `HRZoneSeconds`/`hrZoneEdges` in
  `rideanalysis/metrics.go`). These are a *separate*, coarser 5-band display
  table already independent of `workoutlib`'s zone-name percentages — the
  same way the Coggan 7-zone power *display* table
  (`POWER_ZONE_EDGES`) is already independent of `workoutlib`'s FTP
  zone-name percentages. Consistent with that precedent, add a second,
  LTHR-based 5-band table rather than trying to unify the two existing
  %MaxHR tables (backend `[0.60, 0.70, 0.80, 0.90]`, frontend
  `[0.5, 0.6, 0.7, 0.8, 0.9, 1.0]`, already not identical to each other):

  | Zone | %LTHR |
  |---|---|
  | Z1 Recovery | < 85 % |
  | Z2 Aerobic | 85–89 % |
  | Z3 Tempo | 90–94 % |
  | Z4 Threshold | 95–99 % |
  | Z5 Anaerobic | ≥ 100 % (open top) |

  `hrZones(profile)` takes the whole profile (not just `maxHr`) and returns
  the LTHR table + names above when `thresholdHr` is set, else today's
  max-HR table unchanged. `rideanalysis.HRZoneSeconds` takes the same
  "which basis" decision once in `Analyze` (LTHR if the rider has one, else
  `MaxHR`) so a ride's stored time-in-zone always matches the table the
  Fitness page renders it against — it would be a visible bug for the
  chart's bands and the ride's own zone-seconds bar to disagree about what
  "Z3" means for the same rider.

Running keeps the same %LTHR table as cycling — Friel publishes the same
breakpoints for both; the split that matters is power vs. pace vs. HR, not
sport, which `workoutlib`/`scheduler`'s existing sport-first, HR-fallback
priority order already encodes.

## Data

- `session_analyses.best_hr_1200 INTEGER NOT NULL DEFAULT 0` (idempotent
  add-column in `UseDB`, alongside the existing `max_hr` /
  `best_speed_1200/1800` columns it was added next to).
- `rider_profiles.threshold_hr INTEGER NOT NULL DEFAULT 0`, alongside the
  existing `max_hr`/`resting_hr` int columns — idempotent add-column,
  `RiderProfile.ThresholdHR`, no separate estimated flag (see above).
- `threshold_suggestions.field` gains a fourth value, `"threshold_hr"` — no
  schema change, the column is already a free-text field name.
- `garmin.Biometrics` gains `ThresholdHR int`, read from
  `latestLactateThreshold`'s heart-rate field the same call that already
  reads its `speed` field for `ThresholdPaceSecPerKM`, range-checked and
  failing closed to 0 like every other biometric here.

## Package

`internal/thresholds.Ride` gains `BestHR1200 int` (mirrors `BestSpeed1200`);
`Profile` gains `ThresholdHR int` / `ThresholdHREstimated bool`; `Detect`
gains a fourth rule, `detectThresholdHR`, alongside `detectFTP`/
`detectMaxHR`/`detectPace`, following `detectMaxHR`'s flat-delta shape for
the up side and `detectFTP`/`detectPace`'s 90-day/95%-down shape for the
down side — the only one of the four fields that mixes an up rule from one
sibling with a down rule from the others. `Finding.Field` gains
`"threshold_hr"`.

## API and UI

- No new endpoints. `GET /api/training/thresholds` /
  `POST /api/training/thresholds/{id}` already handle any field name;
  `thresholdSuggestionDTO`/`detectedThresholdDTO` need no shape change,
  only a new possible `field` value, mirrored in
  `apps/web/src/api/types.ts`'s `ThresholdSuggestion`/`DetectedThreshold`
  `field` union.
- `ThresholdSuggestions.vue` needs no new logic — `thresholdFieldLabel`/
  `thresholdFieldTitle`/`formatThresholdValue`/`formatThresholdNumber` in
  `fitnessMath.ts` gain a `'threshold_hr'` case each (label "threshold heart
  rate", value formatted as bpm like max HR).
- `ProfileForm.vue` gains a Threshold HR field next to Max HR and Resting
  HR, with the same "detected from `<date>`" provenance line estimated
  fields already show.
- `TrainingZones.vue` needs no structural change — `hrZones(props.profile)`
  already returns whichever table applies; the zone list it renders is
  unaware of which basis produced it. A caption line ("zones based on
  threshold heart rate" vs. "estimated from max heart rate") tells the
  rider which one is in effect, the same distinction the profile page
  already draws with "estimated" provenance text.

## Rulings (judgement calls, for review)

1. **Best-sustained-20-minute HR, no scaling factor**, as the per-ride
   candidate — mirrors Friel's own last-20-of-30 reading directly rather
   than inventing a multiplier the way eFTP's 0.95×20-minute-power does.
   Cost if wrong: an organically-detected LTHR from a hard training ride
   (not a dedicated test) may run a few bpm hot or cold versus a real
   30-minute test: cheap to fix later with a calibration constant, no
   schema change.
2. **Down rule borrowed from FTP/pace, up rule borrowed from max HR** — the
   only field mixing the two existing shapes, because LTHR is a fitness
   marker (can decline) reported in an absolute unit (bpm, so no percentage
   makes sense going up). Cost if wrong: if this reads as "LTHR never
   really drops in practice," the down rule is straightforward to disable
   later exactly like max HR's; nothing else depends on it firing.
3. **No cycling-specific Garmin LTHR biometric** — Connect's documented
   surface has no cycling equivalent to `latestLactateThreshold`, so a
   cyclist's threshold HR only ever comes from ride detection, never from
   Garmin sync, unlike FTP and max HR which both have a Garmin path. Cost
   if wrong: if Garmin does expose one under an undocumented path nobody
   has found yet, it is a pure addition later — `Biometrics.ThresholdHR`
   already exists as a field, and adding a second source that also writes
   it is not a breaking change.
4. **Display zones (5-band, Friel-collapsed) stay separate from
   `workoutlib`'s zone-name percentages**, matching the FTP precedent
   (Coggan display vs. tempo/threshold/vo2max/anaerobic generation
   percentages already disagree). Cost if wrong: a rider comparing the
   Fitness page's "Z3 Tempo 90–94%" band against a workout step generated
   at 80–88% could read the two numbers as contradictory; a caption
   ("workout zones use a wider band for pacing headroom") is the cheap fix
   if this comes up in practice.
5. **Running and cycling share one %LTHR table** — Friel publishes the same
   breakpoints for both, and the codebase's existing sport-first priority
   order already handles the actual sport difference (pace vs. power vs.
   HR) upstream of the zone percentages. Cost if wrong: splitting the table
   per sport later is a pure data change, no structural rework.

## Out of scope

`workout.TrainingLoad`'s HR-based case keeps using `avgHR/MaxHR` — switching
it to an LTHR-based intensity factor (closer to true TRIMP) is a real
improvement but a separate change with its own review surface; recalibrating
progression levels after an LTHR change; a dedicated cycling LTHR test
workout (mirrors `MaxHRTestWorkout` but does not exist for FTP either, so
not this feature's gap to close); splitting the %LTHR zone table per sport.
