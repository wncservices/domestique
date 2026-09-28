# Threshold detection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect FTP, max HR and threshold pace from analysed rides; update empty/estimated profile fields automatically and suggest changes to rider-typed ones.

**Architecture:** New per-ride metrics in `rideanalysis` + `session_analyses`; a pure `internal/thresholds` package; a `threshold_suggestions` table; the sync applies/suggests after analysis; two endpoints; a Fitness page banner.

**Tech Stack:** Go stdlib, `internal/dbx`; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-28-threshold-detection-design.md` — formulas, thresholds, windows and copy are binding.

## Global Constraints

- Read `AGENTS.md` first. No new dependencies. gofmt, go vet, `cd apps/api && golangci-lint run ./...` (0 issues) and `just check` green — `just check` under `TZ=UTC` (seven scheduling tests are timezone-sensitive under Europe/Brussels; pre-existing, a separate fix is pending — don't touch).
- Every new test uses a fixed clock with an explicit zone and passes under both `TZ=UTC` and `TZ=Europe/Brussels`.
- A rider-typed profile value is never overwritten automatically; only empty or estimated fields update on their own (the existing `autoprofile` rule).
- New SQL through `dbx`, `TestEachEngine` (SQLite + PostgreSQL), idempotent schema in `UseDB`. Owner-only everywhere.
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together. Frontend: tokens only, typed inline handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back).
- Never `git stash` (shared across worktrees). Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **A rider-typed FTP** is never changed by a sync, only suggested. (Task 3)
2. **Dismissed suggestions** don't come back unless the estimate moves ≥ 3 % further. (Task 3)
3. **Sensor spikes / a single weird ride** — max HR ignores > 230 bpm; CP only when P300 > P1200. (Tasks 1–2)
4. **No analysed power rides yet** — the old `EstimateFTP` fallback still works. (Task 3)
5. **Down direction** never auto-applies, even for estimated fields. (Task 2)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1–2 per-ride metrics + `internal/thresholds` | `claude/thresholds-1-detect` |
| 2 | 3 storage, sync, API | `claude/thresholds-2-apply` |
| 3 | 4 UI | `claude/thresholds-3-ui` |

Restack right after each squash merge.

---

### Task 1: Per-ride max HR and best running speeds

**Files:** `apps/api/internal/rideanalysis/{metrics.go,analyze.go}` (+ tests), `apps/api/internal/workout/analysis_db.go` + `db.go` (columns), `apps/api/internal/api/rideanalysis.go` (copy into storage).

- [ ] RED: synthetic FIT rides → `Analysis.MaxHR` = highest HR sample ≤ 230 (a 250 spike ignored); `BestSpeed1200`/`BestSpeed1800` from speed samples (0 when the ride is shorter); storage round-trips the three columns on both engines; an existing DB gains them on `UseDB`.
- [ ] GREEN; `just check`; commit `"Record each ride's peak heart rate and best 20/30-minute speed"`.

### Task 2: `internal/thresholds`

**Files:** create `apps/api/internal/thresholds/{thresholds.go,thresholds_test.go}`.

**Produces:**

```go
type Ride struct { SessionID, Date, Sport string; PowerCurve map[int]float64; MaxHR int; BestSpeed1200, BestSpeed1800 float64 }
type Profile struct { FTPWatts float64; FTPEstimated bool; MaxHR int; MaxHREstimated bool; ThresholdPaceSecPerKM float64; PaceEstimated bool }
type Finding struct { Field string /* "ftp" | "max_hr" | "threshold_pace" */; Value, Previous float64; Direction string /* "up"|"down" */; SourceSessionID, SourceDate, Reason string; Auto bool }
func Detect(rides []Ride, p Profile, now time.Time) []Finding
```

- [ ] RED: every rule in the spec's "What is detected" and "When a value changes" (incl. exact 3 % / +1 bpm boundaries, 42-day window, 90-day down rule, CP validity, pace from 30 vs 20 minutes, Auto only for empty/estimated + up, reason wording per spec).
- [ ] GREEN (pure; no store imports); commit `"Detect FTP, max heart rate and threshold pace from analysed rides"`.

### Task 3: Suggestions, sync and API

**Files:** `apps/api/internal/workout/threshold_db.go` (+ test), `db.go`; `apps/api/internal/api/metricssync.go`, new `apps/api/internal/api/thresholds.go` (+ test), `server.go`, `apps/web/src/api/{types.ts,client.ts}`.

- [ ] RED: store upserts one pending suggestion per (rider, field), lists pending, marks accepted/dismissed (both engines); sync runs `Detect` after analysis — Auto findings update the profile via `autoprofile.Apply` (still estimated) and appear in the sync result `detected`; rider-typed fields get a suggestion instead and the profile is unchanged; a dismissed suggestion isn't re-created unless the new value is ≥ 3 % (1 bpm) further; no analysed power rides → the old `EstimateFTP` path still sets an estimate; `GET /api/training/thresholds`, `POST /api/training/thresholds/{id}` accept (profile saved, estimated flag cleared) / dismiss, owner-only 404, 409 when not pending.
- [ ] GREEN; `just check`; commit `"Apply detected thresholds to estimated fields and suggest the rest"`.

### Task 4: UI

**Files:** create `apps/web/src/components/fitness/ThresholdSuggestions.vue`; modify `TrainingFitnessPage.vue`, `ProfileForm.vue`.

- [ ] Banner per pending suggestion above the status card (spec copy), Update / Dismiss, then a "Replan the rest of this week" action after Update (calls `api.replan()`, handles its 409 like the Plan page). Sync toast lists auto-applied changes ("FTP updated to 268 W — from Saturday's 20-minute effort"). Estimated values in the profile show "detected from <date>" when the latest detection has a source.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with seeded rows (removed afterwards), light/dark, 375px; commit `"Show detected thresholds and let riders accept or dismiss them"`.
