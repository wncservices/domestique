# Threshold heart rate (LTHR) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect lactate threshold heart rate (LTHR) from analysed rides,
apply it to empty/estimated profiles and suggest it to rider-typed ones —
same machinery as FTP/max HR/threshold pace — then switch HR-based zones
(workout targets and the Fitness page chart) to LTHR wherever it's known.

**Architecture:** A new per-ride metric (`rideanalysis.BestHR`) alongside
`MaxHR`/`BestSpeeds`; a fourth rule in the existing `internal/thresholds`
package; a `threshold_hr` profile column and `Estimated` entry; the existing
sync/suggestion/API path already handles any field name; two call sites
(`workoutlib`/`scheduler`'s HR target, `fitnessMath.ts`'s `hrZones` +
`rideanalysis.HRZoneSeconds`) switch basis from max HR to LTHR when known.

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
2. **The down rule fires for threshold HR** (unlike max HR's "never") —
   confirm the 90-day/95% check is wired to `detectThresholdHR`, not
   skipped by copy-pasting `detectMaxHR`'s shape wholesale. (Task 1)
3. **Zone basis switches per rider, not globally** — a rider with no
   `ThresholdHR` still gets max-HR zones exactly as before; nothing regresses
   for a rider this feature hasn't reached yet. (Task 3)
4. **`HRZoneSeconds`'s stored basis matches `hrZones`'s displayed basis** for
   the same rider — a mismatch would silently disagree with itself on the
   Fitness page. (Task 3)
5. **No health values in logs** — grep new/changed log lines for anything
   beyond a field name and a rider id. (Task 2)

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
  (no window, not a misleadingly-low number — mirrors `PowerCurve` leaving a
  short window out); storage round-trips `best_hr_1200` on both engines; an
  existing DB gains the column on `UseDB`. `thresholds.Ride` gains
  `BestHR1200 int`; `thresholds.Profile` gains `ThresholdHR int` /
  `ThresholdHREstimated bool`; `detectThresholdHR` — up: detected ≥ current
  + 1 bpm exactly (boundary case), `Auto` true only when
  empty/`ThresholdHREstimated` and up; down: 90-day history present and
  nothing in it reaches current × 0.95, suggestion only, never `Auto`
  (assert this explicitly — it is the one field mixing max HR's up shape
  with FTP/pace's down shape, easy to get backwards); reason wording
  matching the spec's example ("from Saturday's ride"); `Finding.Field ==
  "threshold_hr"`; `Detect`'s fixed output order gains threshold_hr as a
  fourth, deterministic slot.
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
  `latestLactateThreshold`'s heart-rate field (mind the reference's own
  "hearRate" spelling) the same call `thresholdPace` already makes, range-
  checked and failing closed to 0 on a missing/implausible reading, joined
  into `Biometrics`'s existing best-effort error-joining, not a second
  request; `detectThresholds` includes `threshold_hr` in `thresholdFields`
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

### Task 3: Switch HR-based zones to LTHR when known

**Files:** `apps/api/internal/workoutlib/library.go` (+ test),
`apps/api/internal/scheduler/scheduler.go` (+ test),
`apps/api/internal/rideanalysis/{metrics.go,analyze.go}` (+ test),
`apps/web/src/utils/fitnessMath.ts` (+ test).

- [ ] RED: `workoutlib.zoneHRRange`'s percentages (tempo 0.80–0.88,
  threshold 0.88–0.93, vo2max 0.90–0.97, anaerobic 0.93–1.00, easy
  0.50–0.60) apply to `profile.ThresholdHR` when it is > 0, to
  `profile.MaxHR` unchanged when it isn't — `target`/`enduranceZoneTarget`
  both covered, existing FTP/pace-first priority order untouched;
  `rideanalysis.HRZoneSeconds` takes whichever basis `Analyze` resolved
  (LTHR if set, else MaxHR) plus a matching edge set, so a ride's stored
  zone-seconds always agree with the table `hrZones` renders for that same
  rider — write the cross-check test that computes both from one fixture
  profile and asserts the same basis was used, not just that each function
  works in isolation; `fitnessMath.ts`'s `hrZones(profile)` (new signature:
  the whole profile, not just `maxHr`) returns the five-band %LTHR table
  from the spec when `profile.thresholdHr` is set, today's %MaxHR table
  otherwise; `TrainingZones.vue`'s one call site updated for the new
  signature (covered here since it's a one-line change the type system
  already forces, full UI wiring is Task 4).
- [ ] GREEN; `just check`; CI-equivalent web typecheck (move
  `components.d.ts`/`auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move
  back); commit
  `"Use lactate threshold heart rate for HR-based zones when it's known"`.

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
  `TrainingZones.vue` shows a short caption naming which basis is in
  effect ("zones based on threshold heart rate" / "estimated from max
  heart rate") next to the HR zone list.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with seeded
  rows (removed afterwards) covering both a rider with and without a
  threshold HR, light/dark, 375px; commit
  `"Show threshold heart rate in the profile and zone display"`.
