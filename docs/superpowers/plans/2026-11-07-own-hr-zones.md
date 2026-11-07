# Own heart-rate zones Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Riders choose their heart-rate zones per sport and see each ride's time in zone, recomputed on read.

**Architecture:** Settings are JSON columns on `rider_profiles` with their own writer. A pure package `internal/hrzones` turns a profile and sport into four bpm boundaries and a histogram into zone seconds. Analyses store a per-bpm histogram; the analysis DTO computes zones on read. A bounded backfill in the metrics sync fills histograms for older synced rides.

**Tech Stack:** Go (stdlib, SQLite + PostgreSQL through `dbx`), Vue 3 + Nuxt UI.

**Spec:** `docs/superpowers/specs/2026-11-07-own-hr-zones-design.md`

## Global Constraints

- Both engines: every new query runs on SQLite and PostgreSQL (`TestEachEngine` pattern in `internal/workout`).
- New columns go through the idempotent `ALTER TABLE ... ADD COLUMN` list in `workout/db.go` (`addEstimatedColumns`) and the `CREATE TABLE` text.
- No bpm, histogram or zone value in a log line next to a rider. Counts only.
- `SaveProfile`/`SaveProfileOn`/`SaveProfileFrom` must not write the new profile columns.
- DTOs in `apps/api/internal/api/*.go` and `apps/web/src/api/types.ts` change together.
- Comments explain why; match the surrounding code's density. `gofmt`, `go vet` clean.
- Commit after each task; messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never commit `.claude/` or `.superpowers/`.

## Review Focus

1. A rider with no setting (auto) sees exactly today's zones and HR targets.
2. Reserve method with resting >= max: no zones, not a division into nonsense.
3. A histogram with no HR seconds: zones absent, not five zeros.
4. A re-analysis (`SaveAnalysis` upsert) replaces the histogram; the backfill never overwrites a Feel/Legs/Stress.
5. Imports are never fetched by the backfill.

---

### Task 1: `internal/hrzones` (pure)

**Files:** Create `apps/api/internal/hrzones/hrzones.go`, `hrzones_test.go`. Modify `apps/api/internal/workout/workout.go` (types only).

**Interfaces — Produces:**
- In `workout`: `type HRZoneSetting struct { Method string `json:"method"`; Bounds []int `json:"bounds,omitempty"` }`; constants `HRZonesAuto = ""`, `HRZonesThreshold = "threshold"`, `HRZonesMax = "max"`, `HRZonesReserve = "reserve"`, `HRZonesCustom = "custom"`; `type HRHistogram struct { Min int `json:"min"`; Seconds []int `json:"s"`; NoHR int `json:"n"` }`; `RiderProfile` gains `HRZonesCycling, HRZonesRunning HRZoneSetting`.
- `hrzones.Setting(p workout.RiderProfile, sport string) workout.HRZoneSetting` (running for "running", cycling otherwise).
- `hrzones.Bounds(p workout.RiderProfile, sport string) (b [4]float64, ok bool)` — the spec's table; auto = threshold if `ThresholdHR > 0` else max.
- `hrzones.Valid(s workout.HRZoneSetting) error` — method known; custom: exactly 4 strictly ascending ints in 40..230; other methods: Bounds empty.
- `hrzones.Seconds(h workout.HRHistogram, b [4]float64) (z [5]int, ok bool)` — bin `Min+i` counts in zone where `bpm < b[k]`; ok false when no HR seconds. `NoHR` not counted.
- `hrzones.Range(p workout.RiderProfile, sport, zone string) (low, high float64, ok bool)` — only for an explicit method (returns ok=false for auto so the caller keeps its table); the spec's zone mapping. Zones: "recovery", "endurance", "tempo", "sweet_spot", "threshold", "vo2max", "intervals", "anaerobic".

- [ ] Tests first (table tests): each method's bounds with exact numbers (LTHR 160 cycling → 129.6, 144, 150.4, 160; max 190 → 114, 133, 152, 171; reserve rest 50 max 190 → 134, 148, 162, 176; custom passthrough); missing inputs → ok false; reserve with resting >= max → ok false; `Valid` cases; `Seconds` on a hand-made histogram including a value exactly on a boundary (goes to the higher zone) and an all-NoHR histogram (ok false); `Range` for an explicit custom setting [120,140,155,170] with MaxHR 190: endurance 120-140, sweet_spot 147.5-162.5, vo2max 170-190, and with MaxHR 0: vo2max 170-185; auto → ok false.
- [ ] A test that the LTHR edges equal `LTHR_CYCLING_EDGES` / `LTHR_RUNNING_EDGES` in `apps/web/src/utils/fitnessMath.ts` (see how `rideanalysis` tests read that file; mirror it) and that `max`/`reserve` fractions are `[0.6,0.7,0.8,0.9]`.
- [ ] Implement, `go test ./apps/api/internal/hrzones/`, commit `hrzones: one place for a rider's heart-rate zone boundaries`.

### Task 2: store

**Files:** Modify `apps/api/internal/workout/db.go`, `analysis_db.go`; tests beside them.

**Interfaces — Consumes:** Task 1 types. **Produces:**
- Columns `rider_profiles.hr_zones_cycling TEXT NOT NULL DEFAULT ''`, `hr_zones_running` (same), `session_analyses.hr_histogram TEXT NOT NULL DEFAULT ''`.
- `GetProfile` fills `HRZonesCycling/Running` ('' → zero value). `SaveProfile*` untouched.
- `(d *DB) SetHRZones(ctx, rider, sport string, s HRZoneSetting) error` — upsert like `SetWeight`; sport "cycling" or "running", else error.
- `SessionAnalysis.HRHistogram *HRHistogram` (nil = '' stored). `SaveAnalysis` writes it (insert and conflict update); every reader that scans analyses fills it.
- `(d *DB) SetHRHistogram(ctx, sessionID string, h HRHistogram) error` — updates only that column.
- `(d *DB) SessionsMissingHRHistogram(ctx, rider string, limit int) ([]CompletedSession, error)` — sessions with an analysis whose `hr_histogram = ''`, provider not `"import"`, newest first, at most limit.
- [ ] Tests on both engines: round trip of zone settings; `SaveProfile` after `SetHRZones` keeps them; histogram round trip through `SaveAnalysis`/`GetAnalysis`/`ListAnalyses`; `SetHRHistogram` leaves Feel/Legs/Stress; `SessionsMissingHRHistogram` excludes import and filled rows and respects limit.
- [ ] Implement, test, commit `workout: store heart-rate zone settings and a ride's histogram`.

### Task 3: histogram in analysis

**Files:** Modify `apps/api/internal/rideanalysis/metrics.go`, `analyze.go`; `apps/api/internal/api/rideanalysis.go` (`analyseNewSessions` sets `sa.HRHistogram`); the import path that builds a `workout.SessionAnalysis` (`internal/rideimport` / `api/rideimport.go`, find it with grep for `HRZoneSeconds:`).

**Produces:** `rideanalysis.HRHistogramOf(s []Sample) workout.HRHistogram` (rounded bpm, `HeartRate <= 0` counts in NoHR); `Analysis.HRHistogram *workout.HRHistogram`, set when samples are readable.
- [ ] Tests: histogram of a small sample slice; `Analyze` with an activity sets it, without one leaves it nil.
- [ ] Implement, test, commit `rideanalysis: keep a ride's seconds per heart rate`.

### Task 4: API

**Files:** `apps/api/internal/api/training.go`, `workoutlib/library.go`, tests in `internal/api` and `internal/workoutlib`.

- `riderProfileDTO.HRZones *hrZonesDTO` (`json:"hrZones,omitempty"`), `hrZonesDTO{Cycling, Running *workout.HRZoneSetting}`. GET returns both (always present). PUT: each non-nil one validated with `hrzones.Valid` (422 `{"error": ...}`), then `SetHRZones`; nil leaves it.
- `sessionAnalysisDTO.HRZones []hrZoneSecondsDTO` (`json:"hrZones,omitempty"`), `hrZoneSecondsDTO{Zone int; Low, High float64 (high 0 = open); Seconds int}` with json `zone, low, high, seconds`. Filled from `hrzones.Bounds` + `hrzones.Seconds` for the ride's sport with the rider's current profile, by every response that builds `sessionAnalysisDTOFrom` (load the profile once per request). Z1 low 0, Z5 high 0.
- `workoutlib.HRRange`: first `hrzones.Range`; when ok, return it (still capped at MaxHR when MaxHR > 0), else today's code.
- [ ] Tests: PUT then GET round trip; 422 on bad custom bounds; a ride with a histogram returns hrZones that change after a PUT of a custom setting (recalculated, no re-analysis); auto rider's HRRange unchanged; custom rider's endurance HR target follows the custom bounds.
- [ ] Journey test `internal/api/hrzonesjourney_test.go` over real HTTP: auto zones; custom cycling zones re-cut an analysed ride with no re-analysis while running rides keep theirs; reserve without a resting HR shows no zones, with one it does; back to auto; a generated endurance session's HR target follows custom bounds.
- [ ] Commit `api: riders set their heart-rate zones; rides report time in zone`.

### Task 5: backfill

- [ ] Journey: a rider's older synced ride without a histogram shows time in zone after the next metrics pass.


**Files:** `apps/api/internal/api/rideanalysis.go` (or a new `hrhistogrambackfill.go`), the metrics sync call site after `analyseNewSessions`.

- `const hrHistogramBackfillPerProvider = 10`. For the rider, `SessionsMissingHRHistogram(ctx, rider, 50)`, at most 10 per provider: `fetchSessionFIT` (same args `analyseNewSessions` gets), `guardedDecode`, `rideanalysis.Resample(act.Records)`, then `SetHRHistogram` with `HRHistogramOf` (empty samples → empty histogram). Download failure: skip (retried next pass), Warn once per pass with a count. Log Info `hr histogram backfill` with rider and count only.
- [ ] Test with the fake providers the metrics-sync tests already use: an analysed ride without histogram gets one after a pass; an import is never fetched; a failed download stays ''.
- [ ] Commit `metrics sync: fill the heart-rate histogram of older rides`.

### Task 6: web

**Files:** `apps/web/src/api/types.ts`, `utils/fitnessMath.ts`, `components/fitness/ProfileForm.vue`, `components/fitness/TrainingZones.vue`, `components/plan/StepResultsTable.vue` (or beside it where the analysis shows).

- Types mirroring Task 4. `hrZonesFor(profile, sport): Zone[] | null` mirroring `hrzones.Bounds` (keep `hrZones()` for callers, or replace its two callers).
- ProfileForm: section per the spec; method labels: "Auto", "% of threshold HR", "% of max HR", "Heart-rate reserve", "My own"; a missing input says which number is needed; saved with the profile (`hrZones` in the PUT body).
- TrainingZones: HR zones for cycling and running.
- Time in zone: five bars with minutes, only when `analysis.hrZones` is present.
- [ ] Browser journey in `apps/e2e` (it gates the production rollout, so it must leave prod as it found it): the test rider opens Fitness, picks "My own" for cycling, enters four bounds, saves, sees them in the training zones card, then sets Auto again and saves.
- [ ] `npm run -w @domestique/web typecheck` passes; check in the browser at desktop and 375 px.
- [ ] Commit `web: choose your heart-rate zones; see time in zone per ride`.

### Task 7: docs

- AGENTS.md: a short "Heart-rate zones" section (settings, own writer, on-read computation, backfill, privacy). Commit.
