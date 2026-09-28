# Training adaptation from ride data — design

Status: approved 2026-09-27 (roadmap + sub-project 1 in detail).

## Why

Every planned workout should be adapted from what the rider actually did,
as recorded by their Garmin or Wahoo. Today it is not:

- **Data**: sync reads summaries only — duration, distance, average HR,
  average power (plus Garmin's resting HR). Training load is
  `hours × (avgPower/FTP)² × 100`, which undercounts interval sessions
  (average power hides the efforts), and there is no way to tell whether a
  session's intervals were hit.
- **Plans** (`internal/scheduler`): four session types, at most one hard
  session a week, a fixed 3′/2′ interval shape, the same workout every week
  of a phase, open-ended warmup/cooldown.
- **Adaptation** (`internal/adapter`): a weekly hours multiplier from
  compliance, a missed key session made up once, and hard → easy when
  TSB < −30. "Done" means ≥ 50 % of the planned time; nothing judges how a
  session went.

## What the leaders do

- **TrainerRoad Adaptive Training** analyses every completed workout,
  keeps a 1–10 Progression Level per power zone, rates every workout on the
  same scale (Workout Levels), asks a 1–5 "how did it feel" survey, and
  makes the next workout in that zone harder or easier.
- **Garmin Daily Suggested Workouts** pick each day's session from HRV
  status, sleep, Training Readiness and acute:chronic load.
- **intervals.icu / Xert** keep thresholds current by estimating FTP from
  the power curve, without formal tests.
- **TrainingPeaks** scores planned-vs-completed (compliance) from TSS and
  duration.

The common foundation is **reading the ride itself**. Both providers expose
the full ride: Wahoo's workout summary carries a FIT file URL plus
normalized power and TSS; Garmin Connect serves each activity's original
FIT file, and its activity summary carries normalized power, TSS,
intensity factor and best-power figures.

## Roadmap

| # | Sub-project | Outcome | Status |
|---|---|---|---|
| 1 | **Ride analysis** | Every synced ride analysed from its FIT file (summary fallback): accurate load, time in zones, power curve, and each planned step scored hit/under/over; an outcome per session that the adapter and UI use | shipped (#296–#300) |
| 2 | Progression levels + workout library | Per-zone 1–10 levels moved by #1's outcomes and an optional 1–5 feel rating; a workout library (recovery, endurance, tempo, sweet spot, threshold, VO2max, anaerobic) with levels; weekly progression through Build; structured warmup/cooldown | shipped (#301–#305) |
| 3 | Readiness | Garmin HRV status, sleep score, Training Readiness and resting-HR trend adjust today's/tomorrow's session; Wahoo riders fall back to load-only readiness | shipped (#306–#310) |
| 4 | Threshold detection | FTP from the power curve (#1), threshold HR / pace from data; suggest profile updates (never silent) | shipped (#312–#315) |

Each sub-project has its own spec, plan and PR stack: #1 is this spec;
#2 is `docs/superpowers/specs/2026-09-27-progression-levels-design.md`;
#3 is `docs/superpowers/specs/2026-09-28-readiness-design.md`;
#4 is `docs/superpowers/specs/2026-09-28-threshold-detection-design.md`.
(The Replan button, #311, shipped between #3 and #4 and isn't one of the
four sub-projects above.)

## Sub-project 1: ride analysis

### Privacy

A FIT file contains GPS — personal location data (see AGENTS.md). The file
is decoded **in memory** and discarded. Only derived aggregates are stored:
load metrics, time in zones, power-curve points, per-step results. No
track, no second-by-second streams, no file.

### Fetching

- `garmin.Client.ActivityFIT(ctx, activityID) ([]byte, error)` —
  `GET {APIBase}/download-service/files/activity/{id}` with the bearer
  token; the response is a zip holding one `.fit`; unzip in memory
  (`archive/zip`, cap the uncompressed size at 32 MiB).
- `wahoo.Client.WorkoutFIT(ctx, fileURL) ([]byte, error)` — the URL from
  `workout_summary.file.url`; same size cap.
- Summary fallback fields, parsed alongside the existing ones:
  Garmin activity list `normPower`, `trainingStressScore`,
  `intensityFactor`, `maxAvgPower_5/60/300/1200/3600`; Wahoo summary
  `power_bike_np_last`, `power_bike_tss_last`. Absent fields stay 0.
- Both clients' HTTP goes through their existing `otelhttp` transport
  with the request context (AGENTS.md observability checklist).
- A download failure logs **Warn** (the sync still succeeds with summary
  data) and never fails the sync.
- Per sync, at most 20 FIT downloads per rider per provider; sessions
  from the last 42 days without an analysis are processed newest first,
  so a first sync backfills over a few ticks instead of hammering an
  unofficial API.

### Analysis — `internal/rideanalysis` (pure)

`Analyze(in Input) Analysis`, where `Input` holds the decoded
`*filedef.Activity` (nil when only a summary exists), the summary numbers,
the matched planned `*workout.Workout` (nil if none) and the
`workout.RiderProfile`.

Metrics:

- **Normalized power**: 30-second rolling average of per-second power
  (records resampled to 1 Hz, gaps ≤ 5 s filled with the previous value,
  longer gaps as 0), fourth power mean, fourth root. **IF** = NP / FTP.
  **TSS** = seconds × NP × IF / (FTP × 3600) × 100.
- **HR load** when there is no power or no FTP: threshold HR =
  0.9 × max HR; per-second IF_hr = (HR − rest) / (threshold − rest)
  clamped to [0, 1.5], with rest = resting HR or 0.5 × max HR; load =
  Σ IF_hr² / 3600 × 100 (so an hour at threshold ≈ 100).
- **Fallback order** for the session's training load: FIT power TSS →
  provider TSS (summary) → FIT HR load → the existing
  `workout.TrainingLoad` estimate. `LoadSource` records which was used.
- **Time in zones**: seconds per power zone (the Fitness page's Coggan 7)
  and per HR zone (5), from the same percentages as
  `apps/web/src/utils/fitnessMath.ts`.
- **Power curve**: best average power over 5 s, 60 s, 300 s, 1200 s,
  3600 s (0 when the ride is shorter or has no power).

Scoring against the plan:

- **Matching**: the planned workout for the same date and sport
  (`compliance` rules); with several, the one whose planned duration is
  closest to the ride's.
- **Step mapping**: planned steps are flattened in the same order
  `internal/fitworkout` encodes them (a repeat block's children, then its
  repeat marker). FIT laps carrying `wkt_step_index` map to those steps;
  every lap mapped to a step with a target is scored.
- **Per-step result**: the lap's average of the step's target metric
  (power W, HR bpm, speed m/s) against `[low × 0.95, high × 1.05]` →
  `hit`, `under` or `over`. Steps with an open target are not scored.
- **No step laps** (free ride, or a device that did not run the workout):
  score the planned workout's main targeted step by time in its target
  range across the ride; ≥ 70 % counts as hit.
- **Outcome** (duration ratio = ride timer time / planned seconds):
  `unplanned` — no planned workout;
  `incomplete` — ratio < 0.5;
  `struggled` — fewer than 75 % of hard steps hit (hard = intensity
  `interval`/`active` with a target), or ratio < 0.8;
  `nailed` — every hard step hit and ratio ≥ 0.9 (a workout with no hard
  steps is nailed when ratio ≥ 0.9 and its main step is hit);
  `completed` — otherwise.

### Storage

New table `session_analyses` (both engines, created in `workout.UseDB`):
`session_id` (PK, FK to `completed_sessions.id`), `rider`, `workout_id`
(nullable), `outcome`, `load_source`, `normalized_power`,
`intensity_factor`, `tss`, `duration_ratio`, `power_zone_seconds` and
`hr_zone_seconds` (JSON arrays), `power_curve` (JSON object), `steps`
(JSON array of `{index, name, target, low, high, actual, result}`),
`analysed_at`. Store methods: `SaveAnalysis`, `GetAnalysis`,
`ListAnalyses(rider, since)`.

The session's `training_load` is updated to the analysed load, and
`RecomputeFitnessSnapshots` runs once per sync when any load changed.

### Adaptation (uses #1 now; progression is #2)

- `adapter.AdaptSessions`' "was the key session done?" uses the outcome
  when an analysis exists: `incomplete` or no ride = missed;
  `struggled` = done but hard; otherwise done. The ≥ 50 % time rule
  remains the fallback without an analysis.
- Fatigue trigger, in addition to TSB < −30: the last two analysed key
  sessions both `struggled`, or the last 7 days' analysed TSS ≥ 1.3 × the
  planned TSS of the same days. It uses the existing swap-hard-for-easy
  path.
- Every adjustment reason names the ride, e.g. "Tuesday's threshold
  intervals were under target (2 of 4)".

### API

`completedSessionDTO` gains `analysis?: { outcome, loadSource, np?, if?,
tss?, durationRatio?, steps?: [...] }` — returned by
`GET /api/training/fitness` and inside `GET /api/training/week`'s days.
TS types in `apps/web/src/api/types.ts` mirror it.

### UI

- Week strip tiles and the today card: an outcome chip — Nailed it
  (success), Completed (neutral), Struggled (warning), Incomplete
  (error), Unplanned (info) — icon + text, never colour alone.
- Recent rides: NP, IF and TSS columns when present.
- Opening a completed planned workout shows a per-step table: step, target,
  actual, result.

### Testing

- `rideanalysis`: table tests on synthetic FIT activities built in the test
  with the repo's own FIT encoder (no real rides committed): NP of a
  constant-power ride equals that power; a 1-hour ride at FTP ≈ 100 TSS;
  HR load of an hour at threshold ≈ 100; lap/step mapping including a
  repeat block; each outcome boundary; no-laps fallback; nil activity →
  summary fallback.
- `garmin` / `wahoo`: `httptest` servers for FIT download (zip and plain),
  oversize rejection, error statuses.
- `workout` store: `TestEachEngine` covers `session_analyses`.
- `api`: sync analyses new sessions, respects the per-sync cap, falls
  back to summary on download failure and still returns 200.
- `adapter`: outcome-driven missed/struggled/fatigue cases.

### Out of scope for #1

Progression levels, workout library, feel survey (#2); HRV / sleep /
readiness (#3); threshold detection (#4); storing any raw ride data.
