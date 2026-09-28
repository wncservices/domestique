# Threshold detection — design

Status: approved 2026-09-28. Sub-project 4 of
`2026-09-27-training-adaptation-design.md`; builds on ride analysis (#1).

## Why

Every target in the plan is a percentage of FTP, max HR or threshold pace,
so stale thresholds make every workout wrong. Today FTP is guessed from a
whole ride's *average* power (`fitnesstest.EstimateFTP`: 0.95 × a 15–25′
ride, or a 50–70′ ride), which misses almost every real effort, and max HR
and threshold pace are never learned from rides. Ride analysis now stores a
best-power curve per ride, the input intervals.icu (eFTP) and TrainerRoad
(AI FTP detection) use.

## What is detected (42-day window of analysed rides)

- **FTP (cycling):** eFTP = the highest of
  - 0.95 × best 20-minute power,
  - best 60-minute power,
  - critical power from the best 5- and 20-minute powers:
    `CP = (P1200·1200 − P300·300) / (1200 − 300)`, used only when both
    exist and `P300 > P1200`;
  rounded to the nearest watt. Needs at least one analysed ride with a
  20- or 60-minute best in the window.
- **Max HR:** the highest per-ride peak heart rate in the window (a new
  `max_hr` on each analysis, from the FIT records; peaks above 230 bpm are
  ignored as sensor spikes).
- **Threshold pace (running):** best 30-minute average speed of analysed
  runs; if a run has only a 20-minute best, 0.97 × that. Stored as
  sec/km (`1000 / speed`), rounded to the second. Analysed runs gain
  `best_speed_1200` / `best_speed_1800` (m/s).

## When a value changes

| | Up | Down |
|---|---|---|
| FTP | estimate ≥ current × 1.03 | only when the 90-day history exists and no eFTP in the window reaches current × 0.95 → suggestion only |
| Max HR | detected peak ≥ current + 1 | never |
| Threshold pace | estimate faster by ≥ 3 % (speed ≥ current speed × 1.03) | as FTP (90 days, < 95 %) → suggestion only |

- **Empty or estimated field** (`RiderProfile.IsEstimated` / `FTPEstimated`):
  applied automatically through `autoprofile.Apply`, stays marked
  estimated, and the sync result lists it so the UI toasts
  "FTP updated to 268 W from Saturday's 20-minute effort".
- **Rider-typed field:** never overwritten. A **suggestion** is stored and
  shown: "New FTP detected: 268 W (was 255) — from Saturday's ride.
  Update?" → **Update** (saves the profile, clears the estimated flag,
  offers "Replan the rest of this week") / **Dismiss**. A dismissed
  suggestion reappears only if a later estimate moves at least a further
  3 % beyond the dismissed value (1 bpm for max HR).
- `fitnesstest.EstimateFTP` stays only as the fallback when a rider has no
  analysed ride with a power curve yet.

## Data

- `session_analyses` gains `max_hr INTEGER NOT NULL DEFAULT 0`,
  `best_speed_1200 DOUBLE PRECISION NOT NULL DEFAULT 0`,
  `best_speed_1800 DOUBLE PRECISION NOT NULL DEFAULT 0` (idempotent
  add-column in `UseDB`); `rideanalysis.Analyze` fills them.
- New table `threshold_suggestions (id, rider, field, value, previous,
  source_session_id, source_date, reason, status, created_at, updated_at)`
  with status `pending` | `accepted` | `dismissed`; at most one `pending`
  per (rider, field) — a newer estimate replaces the pending one.

## Package

`internal/thresholds` (pure): `Detect(analyses []Ride, profile Profile,
now time.Time) []Finding` where `Finding{Field, Value, Previous,
Direction (up|down), SourceSessionID, SourceDate, Reason, Auto bool}` —
`Auto` true when the field is empty or estimated and the direction is up.
Reason wording e.g. "from Saturday's 20-minute effort (282 W)",
"peak heart rate 191 on Tuesday's ride", "30-minute best on Sunday's run".

## API and UI

- Sync: after analysing new rides, run `Detect`; apply Auto findings via
  `autoprofile.Apply` (sync result gains `detected: [{field, value,
  reason}]`); upsert suggestions for the rest.
- `GET /api/training/thresholds` → `{ suggestions: [...pending] }`;
  `POST /api/training/thresholds/{id}` `{ action: "accept" | "dismiss" }`
  → accept writes the profile (rider-typed from then on), dismiss marks it.
  Owner-only; 404 for another rider's suggestion; 409 if no longer pending.
- Fitness page: a suggestion banner per pending suggestion (above the
  status card) with Update / Dismiss, and after Update a "Replan the rest
  of this week" action; estimated profile values show "detected from
  <ride date>" provenance. The sync toast lists auto-applied changes.

## Testing

- `thresholds`: eFTP from each source and the max rule; CP only when valid;
  window 42 days; up thresholds exactly at 3 % / +1 bpm; down only with 90
  days and < 95 %; max-HR spike filter; pace from 30 vs 20 minutes;
  Auto vs suggestion by field state.
- `rideanalysis`: max HR and best speeds from synthetic FIT activities.
- Storage under `TestEachEngine`; sync applies/suggests; dismissed not
  re-suggested unless 3 % further; API owner-only, accept/dismiss, 409.
- Fixed clocks with an explicit zone; tests pass under UTC and
  Europe/Brussels.

## Out of scope

Recalibrating progression levels after an FTP change; lactate threshold HR;
running power; Wahoo-specific threshold APIs.
