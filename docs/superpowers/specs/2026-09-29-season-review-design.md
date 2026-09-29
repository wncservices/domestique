# Season review and best efforts — design

Status: draft 2026-09-29. Builds on ride analysis (`2026-09-27-ride-analysis.md`), threshold
detection (`2026-09-28-threshold-detection-design.md`) and FTP tests (`2026-09-29-ftp-tests-design.md`).
The history import (`2026-09-29-export-and-import-design.md`) and race-day projection
(`2026-09-29-race-day-projection-design.md`) are specs only in this tree; this design works
without either and says where each would plug in.

## Why

Riders look back. Strava's year in sport and PRs, intervals.icu's power curve, best efforts and
season comparison, and TrainingPeaks' season review all answer "was this a good year, and what
did I set?". Domestique already stores everything needed and shows none of it: `session_analyses`
holds per-ride NP, TSS, the power curve, max HR and best speeds; `fitness_snapshots` holds CTL;
goals hold event dates. Today a rider who rides a personal-best 20 minutes is told nothing, and
after a history import (years of rides) there is no place to see what those years amounted to.

Two features, one data set: **best efforts** (a running record book with a "new best" moment)
and a **season review** (one period, summarised). No social or sharing features, no export in v1.

## What exists

- `rideanalysis.PowerCurve` returns best average power over `powerCurveWindows = {5, 60, 300, 480,
  1200, 3600}` seconds (5 s, 1, 5, 8, 20, 60 min), stored as `session_analyses.power_curve` JSON
  (`map[string]float64`, keys are the seconds). A window longer than the ride is absent.
- `session_analyses` also has `max_hr`, `best_hr_1200`, `best_speed_1200`, `best_speed_1800`
  (m/s, meaningful for running rides), `normalized_power`, `tss`, zone seconds. It has no date:
  the date comes from `completed_sessions` (`date` is a `YYYY-MM-DD` local-date string, plus
  `sport`, `duration_seconds`, `distance_m`, `training_load`).
- `fitness_snapshots (rider, date, ctl, atl, tsb)`; goals (`event_date`, `priority`, `created_at`);
  `threshold_suggestions` (status `accepted` carries `source_date`, `value`, `previous`).
- **Gaps found:** there is no best-efforts or PR logic anywhere. `completed_sessions` has **no
  elevation**. **FTP changes are not recorded**: an auto-applied detection or test result writes
  the profile and leaves nothing behind (only an accepted suggestion leaves a row), and a manual
  edit leaves nothing at all. Both gaps need small additions below.

## Best efforts

### Metrics

| Metric | Source | Sport | Direction |
|---|---|---|---|
| `power_5`, `power_60`, `power_300`, `power_480`, `power_1200`, `power_3600` | `power_curve` | cycling | higher |
| `speed_1200`, `speed_1800` | `best_speed_*` | running | higher |
| `max_hr` | `max_hr` | any | higher |

`best_hr_1200`, NP and cycling speeds are not records (NP is not a peak; speed on the road mostly
measures wind and gradient). A zero or absent value is not an effort.

### Scopes

For each metric the page shows three bests, each with the ride's date and a link to that ride:
**all time**, **this season** (the current calendar year), and **last 42 days** (the window
threshold detection already uses, so "recent form" means one thing). The season of a ride is the
year in its date string, no timezone maths: dates are already local-date strings. Records always
use calendar years so "this season vs last season" is unambiguous; the review can also use a goal
season (below). Ties: the earliest ride keeps the record.

### "New best" rule

A ride sets a **season best** or an **all-time best** for a metric when its value beats every
other stored value in that scope by at least **1 W** (power) or **1 %** (speed and heart rate):
`value >= prev + 1` for power, `value >= prev * 1.01` for speed and HR.

- The scope needs **at least 3 prior values** for that metric. Without it a January ride, or a
  rider's first weeks, would beat an empty year and every effort would be "a best".
- All-time subsumes season: one marker, the stronger label.
- The comparison is against *all other stored values in scope*, not only earlier-dated ones, so a
  ride synced late (an older ride arriving after a newer one) cannot claim a record the newer ride
  already beats.
- **Never for imported history.** A ride earns an award only when the normal sync analyses it
  within 3 days of its date. Imported rides, and any ride analysed later than that, are stored as
  efforts with no award. This is the flood guard: importing five years must not toast 200 records.
- The award is written once, at first analysis, and never moved or removed: it records "was a
  best when it was set". Later rides beating it do not revoke it. A re-analysis (FTP changed,
  re-sync) updates the stored value and keeps the award as it was.

### Where it shows

- The ride analysis view (`RecentRides` row and the analysis header) shows a "New best!" chip per
  award ("All-time 20-min power", "Season 5-min power"). `sessionAnalysisDTO` gains
  `awards: [{metric, scope}]`.
- The sync result gains `bestEfforts: [{sessionId, metric, scope, value, previous}]`; the UI
  toasts "New all-time best: 20-min power, 268 W". Several awards in one sync give one toast
  listing the top two and "and N more".
- A **Best efforts** card on the Fitness page: a table of every metric with three columns (all
  time, this season, 42 days), each cell "312 W, 14 Aug", and a Cycling / Running switch.
- A **power-duration curve** chart: X is duration on a log scale (5 s to 60 min), Y is watts,
  three lines: this season, last season, all time (the upper envelope, dashed). Inline SVG like
  `FitnessChart.vue`: **no chart library**, that is the convention. Points are the six standard
  windows joined by straight segments; hover shows watts at a window for each line.

### Storage: a `best_efforts` table

Computing on read means parsing `power_curve` JSON for every analysed ride on every page view,
and the "is this a new best" check would run the same scan at every sync. A rider with three
years of imported rides has roughly 1,500 to 3,000 analyses: a few MB of JSON per request. It
would work, but it is the wrong shape. So a small derived table, one row per ride per metric the
ride has a value for:

```sql
CREATE TABLE IF NOT EXISTS best_efforts (
    rider      TEXT NOT NULL,
    session_id TEXT NOT NULL,
    metric     TEXT NOT NULL,              -- power_480, speed_1200, max_hr, ...
    sport      TEXT NOT NULL,
    date       TEXT NOT NULL,              -- the ride's local date, copied for the index
    value      DOUBLE PRECISION NOT NULL,
    award      TEXT NOT NULL DEFAULT '',   -- '' | 'season' | 'all_time'
    PRIMARY KEY (session_id, metric)
);
CREATE INDEX IF NOT EXISTS best_efforts_rider_metric_idx ON best_efforts (rider, metric, value);
CREATE INDEX IF NOT EXISTS best_efforts_rider_date_idx ON best_efforts (rider, date);
```

- About 8 rows per ride, so ~25,000 rows for 3,000 rides. Each scope's best is one indexed
  `ORDER BY value DESC LIMIT 1` with a date bound; no JSON parsing on read.
- Written by the sync after `SaveAnalysis`: upsert on `(session_id, metric)` updating `value`,
  `date` and `sport`, never `award`; the award is decided by `bestefforts.Award` once, at first
  insert.
- **Derived and rebuildable.** `session_analyses` stays the source of truth. `RebuildBestEfforts`
  regenerates every row from `session_analyses` joined to `completed_sessions` (award empty: it
  cannot know what was new at the time). It runs at startup only when the table is empty and the
  analyses table is not, so a second start does nothing. Deleting a session removes its rows by
  `session_id`.
- Raw rows, not a "current record" table: seasons, 42-day windows and last-year comparison all
  fall out of the same rows, and a record table would need re-deriving whenever a ride is deleted.

## Season review

A **Season** tab on the Fitness page (`?tab=season`), owner-only. A period picker offers each
calendar year with any completed session, and each goal with an event date (its **goal season**:
from the goal's `created_at` date, the anchor, to `event_date`). Default: the current calendar
year. The response is computed on read for the chosen period; no summary is stored (the inputs
are indexed range scans, and a stale summary is worse than a fast one).

| Section | Contents | Source |
|---|---|---|
| Volume | rides, hours, distance, elevation, TSS | `completed_sessions` in range: count, sum of `duration_seconds`, `distance_m`, new `ascent_m`; TSS is the sum of `training_load` |
| Fitness | CTL at start, peak (value and date), at end (today for the current period) | `fitness_snapshots`; start is the snapshot nearest on or before the period start |
| FTP | start and end watts, each change with date and source (`test`, `detection`, `manual`) | new `ftp_changes` table, plus accepted `threshold_suggestions` for older history |
| Plan | sessions planned vs completed, compliance % | `workouts` in range with `compliance.Day`; past days only |
| Best efforts | awards dated in the period | `best_efforts.award` |
| Goals | each goal with an event date in the period: priority, CTL and form (TSB) on event day, the event-day ride if found | goals, snapshots, `completed_sessions` |

Rules:

- **Elevation** is not stored today. `completed_sessions` gains `ascent_m DOUBLE PRECISION NOT
  NULL DEFAULT 0` (idempotent add-column), filled at sync when the provider reports an ascent. A
  ride with 0 means "no ascent recorded", not "flat": the total reads "n/a" when no ride in the
  period has one, and "from N of M rides" when only some do.
- **FTP history** needs a record it lacks. `ftp_changes (rider, changed_at, date, from_watts,
  to_watts, source)` is appended by the one place that writes FTP (`SaveProfile` and the
  threshold auto-apply both go through it) whenever the value differs; `source` comes from the
  caller. It is append-only. Existing accepted suggestions are copied in once (idempotent: only
  where no row has that suggestion's date and value), source `detection`, or `test` when the
  reason names a test ride. Before the first recorded change the review says "FTP history starts
  <date>" rather than inventing a start. Start and end FTP for a period are the last change on or
  before each boundary; with none, the profile value only if there has never been a change.
- **Plan compliance:** a workout counts as planned when it is dated in the period and not after
  today; done when `compliance.Day` says so. The percentage shows only with at least 4 planned.
- **Event-day ride:** the completed session on the event date with the longest duration, shown
  with distance, time and TSS; none found says so. **Projected vs actual:** the projection spec
  persists nothing, so v1 shows the actual CTL and form on event day only. If projection later
  stores its race-day value, the goal row gains a "projected" figure beside the actual; the DTO
  reserves `projectedCtl` (omitted) so the UI needs no reshuffle.
- **History import:** imported sessions are ordinary `completed_sessions` and analyses, so a past
  period is reviewable as soon as the import lands, with volume and best efforts (no awards) but
  no FTP history and no plan compliance. Missing data shows an empty section, never a zero.
- A print stylesheet (`@media print`) hides the tabs and controls and flattens the cards. That is
  the only "export" in v1.

## API

Owner-only, rider from the session; another rider's data is unreachable (404, not 403, as the FTP
tests endpoints do):

- `GET /api/training/best-efforts?sport=cycling|running` -> `{ metrics: [{metric, label, unit,
  allTime, season, last42}], curve: {season, lastSeason, allTime} }`; each best is `{value, date,
  sessionId}`, each curve `[{seconds, watts}]`.
- `GET /api/training/season?year=2026` or `?goal=<id>` -> `{ period, volume, fitness, ftp, plan,
  bestEfforts, goals }`. 400 for a malformed period; 404 for another rider's goal.
- `sessionAnalysisDTO.awards` and the sync result's `bestEfforts` as above.

DTOs in `internal/api` and `apps/web/src/api/types.ts` change together.

## Privacy and logging

Owner-only: no sharing, no leaderboard, nothing visible to another rider or a crew. **Logs carry
rider, metric name and scope, never a value:** "new best recorded (rider, metric, scope)", never
watts, heart rate or CTL next to a rider. Rebuild logs a row count only. Deleting a session
removes its `best_efforts` rows; any rider-data purge must include the two new tables.

## Testing

- `bestefforts`: the award at exactly 1 W and exactly 1 % (in and out), fewer than 3 prior values,
  ties, a late-synced older ride not claiming a beaten record, season vs all-time label, a late
  (imported) analysis never awarded, re-analysis keeps the award.
- Storage under `TestEachEngine` (SQLite and PostgreSQL): upsert idempotence, scope queries,
  rebuild complete and idempotent, delete by session; every migration run twice.
- Season aggregation: a year, a goal season, inclusive boundaries, an empty period, missing
  elevation, FTP changes and sources, the compliance floor of 4.
- Sync: a new season best returns `bestEfforts` once, a second sync returns none.
- Owner-only 404s for both endpoints.
- Fixed clocks with an explicit zone; season logic uses date strings, and tests pass under UTC and
  Europe/Brussels (a ride dated 31 December belongs to that year in both).

## Out of scope

Sharing, social comparison, a year-in-review poster, PDF/CSV export, segment or route records,
records for cycling speed, cadence or work, weekly or monthly record views, comparing two
arbitrary periods, and reconstructing what was projected for past goals.
