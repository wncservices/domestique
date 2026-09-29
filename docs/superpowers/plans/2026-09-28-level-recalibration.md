# Level recalibration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After a rider's FTP rises by 3–25%, lower their existing cycling progression levels so the absolute watts the next workouts ask for stays close to what they'd already adapted to — never on a decrease, never for a rise over 25% (likely a typo), capped at -2.0 levels, and never twice for the same rise.

**Architecture:** Two pure functions added to `internal/progression`; a new `rider_profiles.ftp_levels_calibrated_watts` marker that follows FTP in both directions; one shared API-layer helper called from the three places FTP is persisted (sync auto-apply, threshold-suggestion accept, manual profile save); the existing Progression card shows the result via the existing per-zone `Reason` string, plus one new optional toast field for the background-sync case.

**Tech Stack:** Go stdlib (`math.Log2`), `internal/dbx`; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-28-level-recalibration-design.md` — the formula, the 3–25% trigger band, the -2.0 cap, the never-on-decrease rule, the marker semantics and the copy are binding.

## Global Constraints

- Read `AGENTS.md` first. No new dependencies. gofmt, go vet, `cd apps/api && golangci-lint run ./...` (0 issues) and `just check` green — run `just check` under `TZ=UTC` (pre-existing timezone-sensitive scheduling tests; don't touch).
- Every new test uses a fixed clock where a clock is involved at all, and passes under both `TZ=UTC` and `TZ=Europe/Brussels`.
- New SQL through `dbx`, `TestEachEngine` (SQLite + PostgreSQL), idempotent schema in `UseDB`. Owner-only everywhere — the rider always comes from the session, never a request body.
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together. Frontend: tokens only, typed inline handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back).
- Never `git stash` (shared across worktrees). Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **Never fires on an FTP decrease**, even for an estimated field — mirrors the existing threshold-detection asymmetry. (Tasks 1–2)
2. **The typo case:** a rise over 25% (255 → 2550) changes no level, logs Info with the field only, and still moves the marker; a single recalibration never exceeds -2.0 levels. (Tasks 1–2)
3. **The marker follows FTP in both directions:** after 255 → 2550 → 255, a later 255 → 268 still recalibrates; a drop moves the marker down and no level. (Task 2)
4. **Idempotent:** the same FTP written twice recalibrates once (ratio 1 is under the trigger). (Task 2)
5. **Only touches cycling structured zones the rider already has a saved level for** — never seeds a new zone, never touches running. (Task 2)
6. **All three trigger sites use the one shared helper** — no fourth copy of the gate logic drifting out of sync. (Task 2)
7. **The formula's own clamping** — `Apply` still floors a level at 1.0. (Task 1)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1 pure formula + profile column | `claude/recalibration-1-formula` |
| 2 | 2 shared helper wired into all three trigger sites | `claude/recalibration-2-wire-up` |
| 3 | 3 toast + replan affordance | `claude/recalibration-3-ui` |

Restack right after each squash merge.

---

### Task 1: `progression.RecalibrationDelta` / `RecalibrationReason`, and the marker column

**Files:** `apps/api/internal/progression/{progression.go,progression_test.go}`, `apps/api/internal/workout/{db.go,training_db.go or wherever RiderProfile is scanned}` (add `FTPLevelsCalibratedAt`).

**Produces:**

```go
const (
    RecalibrationUpFactor          = 1.03
    RecalibrationMaxFactor         = 1.25
    RecalibrationLevelsPerDoubling = 10 // the tunable
    RecalibrationMaxDrop           = 2.0
)

func RecalibrationDelta(oldFTP, newFTP float64) float64
func RecalibrationReason(oldFTP, newFTP, from, to float64) string
```

- [ ] RED: `RecalibrationDelta(255, 268)` ≈ `-0.718` (assert to 3 decimals); `RecalibrationDelta(255, 255)` is `0`; the cap: `RecalibrationDelta(255, 306)` is exactly `-2.0` (raw -2.63) while `RecalibrationDelta(255, 280.5)` ≈ `-1.375` is uncapped; a decrease (`RecalibrationDelta(255, 240)`) returns a *positive* delta — the function itself is unconditional, so it must not panic, it is the caller's job never to call it (Task 2's gate test); `progression.Apply(5.3, RecalibrationDelta(255, 268))` rounds to `4.6`; `Apply` still clamps at 1.0 (a level of 1.4 with delta -2.0 lands on 1.0); `RecalibrationReason(255, 268, 5.3, 4.6)` renders exactly `"FTP 255 → 268 W — threshold 5.3 → 4.6"` (pass the zone name in, matching `progression.Reason`'s own explicit-zone-parameter shape). `rider_profiles` gains the column on a pre-existing SQLite and PostgreSQL database (`UseDB` idempotent add-column, `TestEachEngine`); `RiderProfile.FTPLevelsCalibratedAt` round-trips through `SaveProfile`/`GetProfile` on both engines, defaulting to `0`.
- [ ] GREEN; `just check`; commit `"Add the level-recalibration formula and its idempotency marker"`.

### Task 2: Shared helper wired into all three trigger sites

**Files:** create `apps/api/internal/api/recalibration.go` (+ `recalibration_test.go`); modify `apps/api/internal/api/metricssync.go`, `apps/api/internal/api/thresholds.go`, `apps/api/internal/api/training.go`.

**Produces:**

```go
type levelsRecalibratedDTO struct {
    FromFTPWatts float64 `json:"fromFtpWatts"`
    ToFTPWatts   float64 `json:"toFtpWatts"`
}

func (s *Server) recalibrateLevelsForFTP(ctx context.Context, rider string, before workout.RiderProfile) (levelsRecalibratedDTO, bool, error)
```

`recalibrateLevelsForFTP` re-reads the just-saved profile (never trusts a
caller-passed "after"), compares its `FTPWatts` against `before`'s
`FTPLevelsCalibratedAt` (not `before.FTPWatts` — the marker is what makes
this idempotent across dismissed/re-detected findings). It always sets the
marker to the new FTP (both directions, first save included). It moves
levels only when the new value is between `RecalibrationUpFactor` and
`RecalibrationMaxFactor` times the old marker; above the max it logs Info
(field name only, no watt values) and stops. On a qualifying rise: loads
`ListLevels`, filters to cycling's `workout.StructuredZones` minus running's
three, computes the delta once, `SaveLevel`s each affected zone with
`RecalibrationReason`.

- [ ] RED: fires exactly once for a qualifying rise and is a no-op on an immediate second call at the same FTP; never moves levels on a decrease or a rise under 3%, but a decrease moves the marker down; 255→2550 changes no level (typo guard, Info logged) yet moves the marker; 255→2550→255 then 255→268 still recalibrates; a 20% rise (255→306) moves levels by exactly -2.0; exactly 1.25× recalibrates, just over does not; only updates zones with an existing row (a rider with no cycling levels yet: no-op, no rows created); leaves running zones alone even when the rider has them; a brand-new profile's first save (`FTPLevelsCalibratedAt == 0`) seeds the marker to the saved FTP and recalibrates nothing; wired correctly into `syncRiderMetrics` (an FTP auto-applied via Garmin, threshold detection, or `EstimateFTP` all reach the same helper — assert with each source individually), `handleResolveThreshold`'s accept branch (a rider-typed FTP accepted via a threshold suggestion), and `handleSaveRiderProfile` (a rider typing a new FTP by hand); storage assertions under `TestEachEngine`.
- [ ] GREEN; `just check`; commit `"Recalibrate progression levels after a qualifying FTP rise"`.

### Task 3: Toast and replan affordance

**Files:** `apps/api/internal/api/metricssync.go` (`syncMetricsResultDTO`), `apps/api/internal/api/thresholds.go` (`handleResolveThreshold`'s response), `apps/api/internal/api/training.go` (`handleSaveRiderProfile`'s response), `apps/web/src/api/types.ts`, `apps/web/src/api/client.ts`, `apps/web/src/components/fitness/{ThresholdSuggestions.vue,ProfileForm.vue}`, wherever the sync toast is rendered.

- [ ] `syncMetricsResultDTO`, the threshold-accept response and the manual-save response each gain an optional `levelsRecalibrated?: {fromFtpWatts, toFtpWatts}` (mirrored in `types.ts`, same shape all three places — one struct on the Go side, reused). Sync toast adds a line: "Levels adjusted for your new FTP (255 → 268 W)." The threshold-accept banner's existing "Replan the rest of this week" action (already shown after Update) is unchanged in shape; `ProfileForm.vue`'s manual save and the sync toast both gain the same "Replan the rest of this week" action when `levelsRecalibrated` is present, calling the existing `api.replan()` and handling its 409 the same way the threshold banner already does. No changes to `ProgressionCard.vue` — it already renders `Reason` per zone, so the recalibration text shows there for free.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser check with a seeded FTP rise (removed afterwards) showing the toast/banner and a successful replan, light/dark, 375px; commit `"Tell riders when their levels were adjusted for a new FTP"`.
