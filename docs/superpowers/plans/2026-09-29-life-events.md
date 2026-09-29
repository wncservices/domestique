# Life events and natural-language plan edits Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a rider record travel, illness or a busy stretch and confirm a deterministic preview of the plan changes it implies, and, with an API key, type the same request in a sentence.

**Architecture:** A pure `internal/lifeevents` (blackout, placement, illness ramp, preview diff); a `life_events` table on `workout.DB`; the blackout consulted by every plan-making path and by adaptation; preview/apply endpoints that recompute the diff server-side; `narration.ProposePlanEdits` producing strict tool-use intents that go through the same rules; a Plan page modal, preview and event bands.

**Tech Stack:** Go stdlib, `internal/dbx`, hand-rolled Anthropic client; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-29-life-events-design.md` — rules, ramp table, limits, schema and copy are binding.

## Global Constraints

- Read `AGENTS.md` first. No new dependencies. gofmt, go vet, `cd apps/api && golangci-lint run ./...` (0 issues) and `just check` green, run under `TZ=UTC` (seven scheduling tests are timezone-sensitive under Europe/Brussels; pre-existing, don't touch).
- Every new test uses a fixed clock with an explicit zone and passes under both `TZ=UTC` and `TZ=Europe/Brussels`. No `time.Now()` in rules; dates are `YYYY-MM-DD` strings parsed in UTC.
- New SQL through `dbx` with `TestEachEngine` (SQLite + PostgreSQL) and idempotent schema in `UseDB` (`CREATE TABLE IF NOT EXISTS`; an existing DB gains the table). Owner-only everywhere; the rider comes from the session, never the body; another rider's id is a 404.
- No health values (watts, FTP, HR, wellness) or the event `note` in log lines next to a rider name: log rider, kind, counts and outcome only.
- LLM calls: send the minimum (spec, "What the model sees"): no rider name, FTP, watts, HR, wellness, goal or event names, coordinates, database ids. Never auto-apply: a proposal only ever produces a preview. Tests use a fake `narration` server and assert the request body. Follow AGENTS.md's outbound checklist: `otelhttp` transport, `http.NewRequestWithContext` with the request's ctx, a Warn log when the deployment has no key (never silent), `NarrationLimiter`. Do not use forced `tool_choice` and do not change the `model` constant.
- A rider-built or already-adjusted session is never rewritten; one automatic change per workout (`scheduler.AdjustedMarker`); the client never supplies the diff, the server recomputes it.
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together. Frontend: Nuxt UI semantic tokens only, typed inline template handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back).
- Never `git stash` (shared across worktrees). Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **The blackout holds everywhere plans are made**: fillWeek, refreshWeek, scheduleGoal/Replan, the tick, and adaptation targets. A removal that comes back is the bug. (Task 3)
2. **Placement never adds load and never puts two hard days in a row**, same week only, key session first. (Task 1)
3. **Apply recomputes**: a stale preview cannot be half-applied; `skip` is by deterministic id. (Task 4)
4. **The model is untrusted**: strict schema, every field re-validated, no path to a write, request body contains none of the withheld data. (Task 5)
5. **Illness ramp table matches the spec** to the day, and rider-touched sessions are left alone. (Task 2)
6. **Capability gating**: `gym`, `indoor` and `swap_alternate` exist only when indoor conversion and alternates have landed. (Tasks 1, 5)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1 storage and travel/busy rules | `claude/life-1-rules` |
| 2 | 2-3 illness ramp, blackout everywhere | `claude/life-2-illness-blackout` |
| 3 | 4 preview/apply/edit/end API | `claude/life-3-api` |
| 4 | 5 natural-language intents | `claude/life-4-narration` |
| 5 | 6-7 UI: form, preview, bands, NL box | `claude/life-5-ui` |

Restack right after each squash merge.

---

### Task 1: Storage and travel/busy rules

**Files:** create `apps/api/internal/lifeevents/{lifeevents.go,place.go}` (+ tests); `apps/api/internal/workout/{lifeevents.go,db.go}` (+ `TestEachEngine` cases).

**Produces:**

```go
type Event struct { ID, Rider, Kind, Start, End, Option, Note string }
type Change struct { ID, Op, WorkoutID, ToDate, Reason string; Default bool }
type Diff struct { Changes []Change; LeftAlone []Note }
func Blackout(events []Event) map[string]bool
func Validate(e Event, now time.Time) error
func Preview(in Input) Diff
```

- [ ] RED: `Validate` limits (7 days back, 42-day span, same-kind overlap rejected, different kind allowed, legal kind/option pairs); `Blackout` union; store CRUD, owner-only, table idempotent on an existing DB, both engines; placement: nearest free day, later on ties, key first, one per day, never two hard in a row, same week only, never more planned seconds, no free day removes lowest priority first with the reason, vacated `MovedFrom` days and other events' days are not free, an FTP test moves like a session, rider-built sessions are "left alone" with an unticked remove, past and ridden sessions untouched; `gym` keeps cycling sessions as indoor endurance <= 60 min and leaves running alone, behind a capability flag (400 when off).
- [ ] GREEN; `just check`; commit `"Add life events storage and travel/busy placement rules"`.

### Task 2: Illness and the return ramp

**Files:** `apps/api/internal/lifeevents/{illness.go,illness_test.go}`, `apps/api/internal/scheduler/adapt.go` (a reusable one-rung ease, if `EasyVariant`/alternates do not already give one) (+ tests).

- [ ] RED: `proper` removes every plan-made session in range with no compensation; `mild` removes hard/structured and caps endurance at 45 min; the ramp table row by row (`mild`; `proper` at d = 4, 5, 9, 10, 14: easy days, rung window, caps 60/90 min); rider-built and adjusted sessions untouched; a session already changed by an event is not changed twice; d >= 14 adds the clinician note; an FTP test in `end+1..end+7` moves to the first eligible day on or after `end+8`, else removed; a retroactive event (`end` in the past) yields only the ramp.
- [ ] GREEN; `just check`; commit `"Add illness rules and a return-to-training ramp"`.

### Task 3: The blackout everywhere plans are made

**Files:** `apps/api/internal/api/{training.go,seasonplan.go,replan.go,autoschedule.go,adaptation.go}`, `apps/api/internal/adapter/session.go` (+ tests).

- [ ] RED: `fillWeek`, `refreshWeek`, `scheduleGoal` (Replan and the tick) create nothing on a blackout date and add it to the day-taken set; a session removed for an event is not regenerated by replan, a refresh or a season pass; an event created before its weeks are filled keeps them clear; `adaptRider` gains the ramp rule for sessions of weeks filled later (`IsGenerated` only, marker, one change per workout, survives replan); `AdaptSessions` never targets an event day and never counts a removed session as missed; readiness chip and tomorrow advisory are suppressed on event days; a life-event move is not re-created on its vacated day.
- [ ] GREEN; `just check`; commit `"Respect life events wherever the plan is built or adapted"`.

### Task 4: Preview, apply, edit, end

**Files:** create `apps/api/internal/api/lifeevents.go` (+ `lifeevents_test.go`, acceptance cases), `server.go` routes, `apps/web/src/api/{types.ts,client.ts}`.

- [ ] RED: `POST` with `dryRun` writes nothing and returns the diff; without it, creates the event and applies the recomputed diff minus `skip`; a plan changed between preview and apply applies only what is still valid; runs under the `autoScheduleLockKey` lock and 409s like replan when held; markers written (`MovedFrom` form, `AdjustedMarker`, event note); `PUT` lengthening runs the rules on the new days only; shortening or `DELETE` refills freed days from `scheduler.WeekWorkouts` (not blackout, not taken, not a vacated day) as `add` changes and leaves `scheduled_weeks` untouched; ending early sets `end` to yesterday, an `end` before `start` deletes; owner-only 404s; Garmin copies removed as replan does; Info logs carry rider, kind and counts only.
- [ ] GREEN; `just check`; commit `"Preview and apply life events, with edit and end-early"`.

### Task 5: Natural-language intents

**Files:** `apps/api/internal/narration/{planedits.go,planedits_test.go}`, `apps/api/internal/api/planedit.go` (+ test), `server.go` route.

- [ ] RED (fake Anthropic server): the request body holds the note, today and weekday, available days and the 14-day plan with opaque handles, and **none of** the rider name, FTP, watts, HR, wellness, goal names or database ids (assert by string search on fixtures that contain them elsewhere); one `propose_plan_edits` tool with `strict: true`, `tool_choice` auto, no forced choice; well-formed reply becomes items with diffs via the same `Preview`/placement code; rejected outright: unknown type, extra property, more than 5 intents, no tool block; dropped with a reason: bad or out-of-range date, foreign or ridden or past handle, illegal kind/option pair, illegal `to_date`; `unsupported` rendered as plain text <= 140 chars; the schema omits `swap_alternate` / `convert_indoor` when those capabilities are absent; model error and timeout return 502 with a Warn log; no key logs and returns the guard; `NarrationLimiter` applied; nothing is written by any path; the note is never logged.
- [ ] GREEN; `just check`; commit `"Turn a sentence into validated plan-edit proposals"`.

### Task 6: Life event form, preview and bands

**Files:** create `apps/web/src/components/plan/{LifeEventModal.vue,LifeEventPreview.vue,LifeEventBand.vue}`; modify `TrainingPlanPage.vue`, `WeekStrip.vue`, `SeasonTimeline.vue`, `TodayCard.vue`.

- [ ] "Life event" button and modal (kind, dates, the kind's option, note); preview grouped by day with reasons, per-change checkboxes (rider-built removals unticked), "Left alone", clinician line, Apply/Cancel; bands on the week strip and season timeline; event day card with Edit and "I'm back" (illness asks the neck-check question first, "Not yet" keeps the event); readiness chip hidden on event days.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with a seeded plan (rows removed afterwards): create travel, illness (mild and proper), end early, edit, light/dark, 375px; commit `"Add a life event flow with a confirmable preview to the Plan page"`.

### Task 7: Natural-language box

**Files:** create `apps/web/src/components/plan/PlanEditBox.vue`; modify `TrainingPlanPage.vue`, `LifeEventPreview.vue`.

- [ ] One-line box shown only when `narrationEnabled`, with the note that the sentence is sent to Anthropic; submit opens the same preview modal seeded with each proposed item (individually skippable) and dropped-intent reasons; loading, error (502) and "couldn't understand that, use the form" states; nothing applies before Apply.
- [ ] Verify: typecheck, browser against a fake narration server (real key never used), hidden without a key, 375px; commit `"Add a natural-language plan edit box to the Plan page"`.
