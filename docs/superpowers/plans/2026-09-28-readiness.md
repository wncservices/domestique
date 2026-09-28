# Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Read Garmin HRV, sleep, Training Readiness and resting HR each day, judge today ready / take care / rest, and ease today's hard session accordingly.

**Architecture:** Garmin client methods + a `daily_wellness` table; a pure `internal/readiness` package; the sync fetches wellness; the adapter takes today's assessment and uses its existing downgrade / step-down paths; an endpoint and two UI pieces show it.

**Tech Stack:** Go stdlib, `internal/dbx`; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-28-readiness-design.md` — paths, fields, thresholds, reason wording and icons are binding.

## Global Constraints

- Read `AGENTS.md` first (Conventions, Tests, Observability incl. the outbound-client checklist, Security).
- No new dependencies. gofmt / go vet clean; `just check` green (note: seven date-dependent schedule tests fail on `main` since 2026-09-28 — a separate fix is pending; don't mask or "fix" them here, and report them as pre-existing if still failing).
- Every new test uses a fixed clock (`Server.Clock` or explicit dates) — nothing depends on today's date.
- Health data is personal: store daily aggregates only; owner-only everywhere; never log values with the rider name at Info or above (counts only).
- New Garmin calls reuse the client's `do` helper, bearer token and headers, with the caller's ctx; a failure is Warn and never fails the sync.
- New SQL through `dbx`, covered by `TestEachEngine` (SQLite + PostgreSQL); schema added idempotently in `UseDB`.
- Only generated, untouched workouts change, each at most once, one rule per workout per pass (the existing guarantees).
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together. Frontend: tokens only, no hex, no `dark:`, semantic colours only for status, typed inline handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back).
- Never `git stash`. Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **Partial Garmin data** (HRV missing, sleep present) — the assessment uses what exists, never treats a missing reading as a bad one. (Tasks 1–2)
2. **Resting-HR baseline with too little history** (< 7 readings) — the RHR rule stays silent. (Task 2)
3. **Wahoo-only rider** — only load/form rules; no Garmin calls attempted. (Tasks 2–3)
4. **Readiness and other adapter rules on the same day** — one change per workout; a readiness step-down happens once per day across 30-minute passes. (Task 3)
5. **First sync backfill** — 28 days fetched at most once per rider per sync, not on every tick. (Task 3)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1 Garmin client + storage | `claude/readiness-1-data` |
| 2 | 2 readiness rules | `claude/readiness-2-rules` |
| 3 | 3 sync + adapter + API | `claude/readiness-3-apply` |
| 4 | 4 UI | `claude/readiness-4-ui` |

Restack right after each squash merge (`git rebase --onto origin/main <old parent tip> <branch>`, retarget, `--force-with-lease`).

---

### Task 1: Garmin wellness + `daily_wellness` storage

**Files:** `apps/api/internal/garmin/wellness.go` (+ test); `apps/api/internal/workout/wellness_db.go` (+ test), `db.go` schema.

**Produces:**

```go
// garmin
type Wellness struct { Date string; HRVLastNight, HRVWeeklyAvg float64; HRVStatus string; SleepSeconds int; SleepScore int; ReadinessScore int; ReadinessLevel string; RestingHR int }
func (c *Client) Wellness(ctx context.Context, date time.Time) (Wellness, error) // error only when nothing at all could be read (auth/transport); per-signal failures leave fields zero
// workout store
type DailyWellness struct { Rider, Date string; HRVLastNight, HRVWeeklyAvg float64; HRVStatus string; SleepSeconds, SleepScore, ReadinessScore int; ReadinessLevel string; RestingHR int; UpdatedAt string }
func (d *DB) SaveWellness(ctx, w DailyWellness) error                       // upsert on (rider, date)
func (d *DB) ListWellness(ctx, rider, sinceDate string) ([]DailyWellness, error) // ascending by date
```

- [ ] RED: httptest per endpoint with the spec's fields (paths + query exact); one endpoint 500 → others still filled; status strings upper-cased as received; no display name → error before any request (like `RestingHeartRate`); store round-trip, upsert, owner isolation under both engines.
- [ ] GREEN; `just check`; commit `"Read HRV, sleep and Training Readiness from Garmin and store them daily"`.

### Task 2: `internal/readiness`

**Files:** create `apps/api/internal/readiness/{readiness.go,readiness_test.go}`.

**Produces:**

```go
type Verdict string // "ready" | "caution" | "rest"
type Assessment struct { Verdict Verdict; Reasons []string }
type Day struct { Date string; HRVStatus string; SleepSeconds, SleepScore, ReadinessScore int; ReadinessLevel string; RestingHR int; Present bool } // Present=false: no Garmin row that day
type Load struct { Date string; Load float64 }
func Assess(today Day, history []Day /* previous days, any order */, tsb *float64, tsbDate string, loads []Load, now time.Time) Assessment
```

- [ ] RED: every rule and boundary in the spec's Testing section; missing fields never trigger a rule; `Present=false` → only form/ACWR rules; reason wording and order per spec ("HRV has been low for two nights", "you slept 5h10 (sleep score 38)", "resting heart rate is 8 above your usual 52", Garmin readiness "Garmin readiness is poor (18)", form "your form is −34", load "your load this week is 1.6× your usual").
- [ ] GREEN (no imports of workout/store — plain values); `just check`; commit `"Assess daily readiness from HRV, sleep, resting heart rate and load"`.

### Task 3: Sync, adapter, API

**Files:** `apps/api/internal/api/metricssync.go` (+ new `wellnesssync.go`), `adaptation.go`, `apps/api/internal/adapter/session.go` (+ tests), new `apps/api/internal/api/readiness.go` (+ test), `server.go`, `apps/web/src/api/{types.ts,client.ts}`.

- [ ] RED: sync fetches today + yesterday for Garmin-connected riders and saves them; a rider with no rows gets a 28-day backfill once (second sync fetches only 2 days); Garmin failure → Warn, sync 200; Wahoo-only rider → no Garmin calls. Adapter: given today's Assessment — rest → today's untouched generated hard workout gets a Downgrade (reason "Swapped for an easy ride — …"), caution → a StepDown one rung easier with source `readiness:<date>` (reason "Eased one level — …"), ready → none; a second pass the same day changes nothing; a workout already claimed by another rule isn't touched; rider-owned untouched; the TSB < −30 rule now lives in readiness (adapter's `detectFatigue` keeps the struggled/overload triggers — existing adapter tests updated with intent preserved). `GET /api/training/readiness` owner-only, shape per spec.
- [ ] GREEN: adaptation builds today's `readiness.Day` from `ListWellness`, the latest snapshot and daily loads, calls `readiness.Assess`, passes the Assessment into `AdaptSessions`.
- [ ] `just check` + CI-equivalent typecheck; commit `"Ease today's hard session when recovery is poor"`.

### Task 4: UI

**Files:** create `apps/web/src/components/plan/ReadinessChip.vue`, `apps/web/src/components/fitness/RecoveryCard.vue`; modify `TodayCard.vue`, `TrainingPlanPage.vue`, `TrainingFitnessPage.vue`.

- [ ] Readiness chip on the today card per spec (icon + label, reasons in a `UPopover`, keyboard reachable); hidden when the API returns no assessment.
- [ ] Recovery card after the Progression card: last 7 days (local dates), sleep "7h12 · 82", HRV "48 ms · Balanced", resting HR, readiness "71 · Moderate"; empty cells as "—"; hidden with no rows; mono numbers.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with seeded `daily_wellness` rows (removed afterwards), light/dark, 375px; commit `"Show today's readiness and a week of recovery data"`.
