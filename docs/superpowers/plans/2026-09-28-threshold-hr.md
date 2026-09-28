# Threshold heart rate (LTHR) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect lactate threshold heart rate (LTHR) from analysed rides,
apply it to empty/estimated profiles and suggest it to rider-typed ones —
same machinery as FTP/max HR/threshold pace — then switch HR-based zones
(workout targets and the Fitness page chart) to LTHR wherever it's known.

**Architecture:** A new per-ride metric (`rideanalysis.BestHR`) alongside
`MaxHR`/`BestSpeeds`; a fourth rule in the existing `internal/thresholds`
package (0.95 × best 20-minute HR, up only); a `threshold_hr` profile column and `Estimated` entry; the existing
sync/suggestion/API path already handles any field name; HR targets
(`workoutlib`/`scheduler`) and the zone display (`fitnessMath.ts`'s
`hrZones`, `rideanalysis.HRZoneSeconds`) switch from %MaxHR to one Friel
%LTHR table per sport when LTHR is known; %MaxHR stays otherwise.

**Tech Stack:** Go stdlib, `internal/dbx`; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-28-threshold-hr-design.md` —
formulas, thresholds, windows, zone table and copy are binding.

## Global Constraints

- Read `AGENTS.md` first. No new dependencies. `gofmt`, `go vet`,
  `cd apps/api && golangci-lint run ./...` (0 issues) and `just check`
  green, run under `TZ=UTC`.
- Every new test uses a fixed clock with an explicit zone and passes under
  both `TZ=UTC` and `TZ=Europe/Brussels` — run the suite once under each
  before calling a task done.
- A rider-typed profile value is never overwritten automatically; only
  empty or estimated fields update on their own (`autoprofile`'s existing
  rule, unchanged by this plan).
- New SQL through `dbx`, `TestEachEngine` (SQLite + PostgreSQL), idempotent
  schema in `UseDB`. Owner-only everywhere a rider identity is involved.
- Health values (HR, LTHR, any bpm figure) are never logged next to a rider
  name — log the field and the number, not who it belongs to, matching how
  `detectThresholds`'s existing `s.logger().Info("threshold auto-applied",
  "rider", rider, "field", f.Field)` already omits the value itself; do not
  add a `"value"` key to that line or any new one like it.
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together.
  Frontend: tokens only, typed inline handler params, CI-equivalent
  typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts`
  aside, `npx vue-tsc --noEmit`, move back).
- Never `git stash` (shared across worktrees — use a WIP commit if you need
  to set work aside). Commit trailer
  `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **A rider-typed threshold HR** is never changed by a sync, only
   suggested — same guarantee as the other three fields. (Task 2)
2. **Threshold HR never suggests a decrease**, like max HR: no down finding
   even with 90+ days of history and only easy rides. (Task 1)
3. **Zone basis switches per rider, not globally** — a rider with no
   `ThresholdHR` gets today's %MaxHR targets and bands unchanged; with one,
   HR targets come from the Friel table, capped at `MaxHR`. (Task 3)
4. **`HRZoneSeconds`'s stored basis matches `hrZones`'s displayed basis** for
   and sport for the same rider — a mismatch would make the ride's zone bar
   and the zone list disagree about "Z3". (Task 3)
5. **No health values in logs** — grep new/changed log lines for anything
   beyond a field name and a rider id. (Task 2)
6. **The Garmin key typo is unverified** — the parser accepts `heartRate` and
   `hearRate`, and a Garmin value never overwrites a rider-typed LTHR. (Task 2)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1 per-ride metric + `internal/thresholds` rule | `claude/lthr-1-detect` |
| 2 | 2 storage, sync, API, Garmin biometric | `claude/lthr-2-apply` |
| 3 | 3 zone basis switch (backend + frontend) | `claude/lthr-3-zones` |
| 4 | 4 UI | `claude/lthr-4-ui` |

Restack right after each squash merge.

---

### Task 1: Per-ride best-20-minute HR and the detection rule

**Files:** `apps/api/internal/rideanalysis/{metrics.go,analyze.go}` (+
tests), `apps/api/internal/workout/analysis_db.go` + `db.go` (column),
`apps/api/internal/api/rideanalysis.go` (copy into storage),
`apps/api/internal/thresholds/{thresholds.go,thresholds_test.go}`.

- [ ] RED: `BestHR(s []Sample) int` — highest 20-minute rolling mean of HR
  samples via the existing `bestRollingMean`, 0 for a ride under 20 minutes
  and unaffected by a single 250 bpm spike sample; storage round-trips
  `best_hr_1200` on both engines; an existing DB gains the column on
  `UseDB`. `thresholds.Ride` gains `BestHR1200 int`; `Profile` gains
  `ThresholdHR int` / `ThresholdHREstimated bool`; `detectThresholdHR`:
  estimate = round(0.95 × the highest best-20 in the 42-day window); up
  exactly at current + 1 bpm (boundary both sides); `Auto` only when
  empty/estimated; **no down finding ever** (assert with 90+ days of
  history and only sub-threshold rides); reason wording per spec; Field
  `"threshold_hr"`; `Detect` output order ftp, max_hr, threshold_pace,
  threshold_hr.
- [ ] GREEN; `just check` under `TZ=UTC` and again under
  `TZ=Europe/Brussels`; commit
  `"Detect lactate threshold heart rate from analysed rides"`.

### Task 2: Storage, sync, API, Garmin biometric

**Files:** `apps/api/internal/workout/{db.go,workout.go}` (column,
`FieldThresholdHR`), `apps/api/internal/garmin/biometrics.go` (+ test),
`apps/api/internal/api/{metricssync.go,thresholds.go}` (+ tests),
`apps/web/src/api/types.ts`.

- [ ] RED: `rider_profiles.threshold_hr` round-trips on both engines,
  idempotent add-column on an existing DB; `workout.FieldThresholdHR =
  "threshold_hr"` added to the `Estimated`-field constants alongside
  `FieldMaxHR`/`FieldThresholdPace` (no separate bool, matching those two);
  `garmin.Biometrics` gains `ThresholdHR int`, read from
  `latestLactateThreshold` via `number(o, "heartRate", "hearRate")` — the
  spelling is unverified against a live response, so a test covers both —
  from the same object as the accepted speed, in the same call
  `thresholdPace` already makes (no second request), range-checked (about
  90–220) and failing closed to 0; it fills the profile only when the field
  is empty/estimated (test: a rider-typed LTHR is untouched); `detectThresholds` includes `threshold_hr` in `thresholdFields`
  and in the `tp := thresholds.Profile{...}` construction, so the fourth
  rule actually runs during a sync, both directions reachable end to end
  (Auto applies via `autoprofile.Apply` and appears in `detected`; a
  rider-typed value produces a suggestion instead and the profile is
  unchanged); Garmin-sourced `ThresholdHR` merges into the profile the same
  pass `CyclingFTPWatts`/`MaxHR`/`ThresholdPaceSecPerKM` already do;
  suggestion store/API round-trip `field: "threshold_hr"` with no code
  change needed beyond the constant existing (confirm this rather than
  assume it — `CreateSuggestion`/`GET /api/training/thresholds` are already
  field-name-agnostic, but prove it with a test using this field). No log
  line anywhere in this task's diff carries the bpm value next to `rider`.
- [ ] GREEN; `just check` under `TZ=UTC` and `TZ=Europe/Brussels`; commit
  `"Store, sync and suggest lactate threshold heart rate"`.

### Task 3: Friel %LTHR zones for HR targets and display

**Files:** `apps/api/internal/workoutlib/library.go` (`zoneHRRange`,
`target`, `Warmup`, `Cooldown`, `Instantiate`) (+ test),
`apps/api/internal/scheduler/scheduler.go` (`enduranceZoneTarget`) (+ test),
`apps/api/internal/rideanalysis/{metrics.go,analyze.go}` (`hrZoneEdges`,
`HRZoneSeconds`, `Input.Sport`, the `Analyze` call) (+ test),
`apps/api/internal/api/rideanalysis.go` (pass the session's sport),
`apps/web/src/utils/fitnessMath.ts` (`hrZones`) (+ test),
`apps/web/src/components/fitness/TrainingZones.vue` (call site).

- [ ] RED: with `ThresholdHR > 0`, HR targets are `ThresholdHR ×` the spec's
  Friel table per sport (recovery/warmup/cooldown/rest 0.70–0.81 cycling,
  0.72–0.85 running; endurance 0.81–0.89 / 0.85–0.89; tempo 0.90–0.93 /
  0.90–0.94; sweet_spot 0.92–0.96; threshold 0.94–0.99 / 0.95–0.99;
  vo2max and intervals 1.03–1.06; anaerobic 1.07–1.10), both `target` and
  `enduranceZoneTarget` covered, low and high each capped at `MaxHR` when
  known (e.g. LTHR 170 anaerobic with MaxHR 185 caps at 185); with only
  `MaxHR` every number is exactly today's (regression test on the old
  0.80–0.88 etc. and 0.60–0.75); FTP/pace branches untouched. `target`
  takes the zone name instead of `hrLow/hrHigh`.
  `HRZoneSeconds` uses LTHR + the sport's five edges (cycling
  0.81/0.90/0.94/1.00, running 0.85/0.90/0.95/1.00) when the rider has
  LTHR, else `MaxHR` + `hrZoneEdges` unchanged; a cross-check test asserts
  the Go edges and the TS `hrZones` band boundaries agree for the same
  profile. `hrZones(profile)` returns the five LTHR bands (cycling < 81,
  81–89, 90–93, 94–99, ≥ 100; running < 85, 85–89, 90–94, 95–99, ≥ 100;
  running only when the profile has a pace and no FTP), today's table
  otherwise.
- [ ] GREEN; `just check`; CI-equivalent web typecheck (move
  `components.d.ts`/`auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move
  back); commit
  `"Use Friel heart-rate zones from threshold heart rate when it is known"`.

### Task 4: UI

**Files:** `apps/web/src/utils/fitnessMath.ts` (label/format cases),
`apps/web/src/components/fitness/{ThresholdSuggestions.vue,ProfileForm.vue,TrainingZones.vue}`.

- [ ] `thresholdFieldLabel`/`thresholdFieldTitle`/`formatThresholdValue`/
  `formatThresholdNumber` gain a `'threshold_hr'` case (label "threshold
  heart rate", bpm formatting like max HR) — `ThresholdSuggestions.vue`
  needs no other change, confirm the banner renders correctly for this
  field with no per-field special-casing added there. `ProfileForm.vue`
  gains a Threshold HR input beside Max HR/Resting HR, with the same
  "detected from `<date>`" provenance line the other estimated fields show.
  `TrainingZones.vue`'s HR header reads "Threshold HR 162 bpm" (else "Max HR
  …") and Z1 renders "below N bpm".
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with seeded
  rows (removed afterwards) covering both a rider with and without a
  threshold HR, light/dark, 375px; commit
  `"Show threshold heart rate in the profile and zone display"`.
