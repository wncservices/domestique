# Crew-aware planning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a rider say "I'm going" to a crew ride so it becomes a fixed session the plan fills around, and propose lining up two crew mates' long rides on a shared day, each confirmed by its owner.

**Architecture:** Pure `internal/crewplan` (estimate, constraints, preview diff, alignment); "going", together flag and proposals stored in `schedule`; the going ride is a `workouts` row with `crew_ride_id` (the FTP-test precedent); `fillWeek`/`adaptRider` honour it; readiness gets advice, never changes it; Plan page card and a minimal crew page change.

**Tech Stack:** Go stdlib, `internal/dbx`; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-29-crew-planning-design.md` — estimate formula, classification thresholds, rules, exposure table and copy are binding.

## Global Constraints

- Read `AGENTS.md` first (users, riders and accounts; crews; `config.VisibleTo`/`TargetsFor`). No new dependencies. gofmt, go vet, `cd apps/api && golangci-lint run ./...` (0 issues) and `just check` green, run under `TZ=UTC` (seven scheduling tests are timezone-sensitive under Europe/Brussels; pre-existing, don't touch).
- Every new test uses a fixed clock with an explicit zone and passes under both `TZ=UTC` and `TZ=Europe/Brussels`.
- New SQL through `dbx`, `TestEachEngine` (SQLite + PostgreSQL), idempotent schema in `UseDB`.
- **Rider data is owner-only.** The rider comes from the session, never the body. A cross-rider write happens only on the owner's own confirmation and only to the owner's own rows: going, together, accept and decline each write the caller's rows; a crew action (delete ride, remove member) never touches a rider's workouts (it orphans them; the rider confirms). Peers receive only the spec's "What crosses" table.
- No health values (watts, FTP, HR, readiness) in log lines next to a rider name: log rider, ride id, outcome only.
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together. Frontend: Nuxt UI semantic tokens only, typed inline template handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back).
- The fixed row is never `IsGenerated`; nothing automatic may move, ease, skip or delete it. Preview then apply: the server recomputes the diff, never trusts the client's.
- Life events are unmerged: consult the blackout set through one small function that returns empty until they land; don't import their code.
- Never `git stash` (shared across worktrees). Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **A rider can only ever write their own rows**; a crew delete or member removal orphans, never edits, a plan. (Tasks 1, 2, 5)
2. **The fixed row is untouchable by automation** (`IsGenerated` false, push skips it) and its day is taken even with no goal. (Tasks 1, 2)
3. **Preview writes nothing; apply recomputes and honours `skip`**, under the scheduling lock. (Task 2)
4. **Readiness advises and never changes** the ride; "Ease tomorrow" refuses it. (Task 3)
5. **Nothing beyond the exposure table reaches a peer**: no dates, durations, TSS, FTP, health, available days. (Tasks 4, 5)
6. **A proposal needs a shared free day and a route visible to everyone**; otherwise none. (Task 4)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1 estimate, going state, fixed session | `claude/crew-plan-1-going` |
| 2 | 2 fill rules, adaptation, join/leave preview | `claude/crew-plan-2-fill` |
| 3 | 3 readiness advice, forecast and FTP-test guards | `claude/crew-plan-3-readiness` |
| 4 | 4-5 ride-together flag, proposals, confirm | `claude/crew-plan-4-together` |
| 5 | 6 UI | `claude/crew-plan-5-ui` |

Restack right after each squash merge.

---

### Task 1: Estimate, "I'm going" and the fixed session

**Files:** create `apps/api/internal/crewplan/{crewplan.go,crewplan_test.go}`; `apps/api/internal/schedule/schedule.go` (`crew_ride_going`, `Go`/`Leave`/`GoingFor`/`ListGoing`; delete going rows with a ride) (+ test); `apps/api/internal/workout/{workout.go,db.go}` (`CrewRideID`, column, unique partial index); `apps/api/internal/scheduler/adapt.go` (`IsKeySession`); `apps/api/internal/api/{crewrides.go,workoutpush.go,server.go}`; `apps/web/src/api/{types.ts,client.ts}`.

**Produces:**

```go
type Estimate struct { Km, AscentM, Hours, TSS float64; Kind Kind } // Kind: Long | Endurance | Short
func EstimateRoute(stats model.RouteStats) Estimate
```

- [ ] RED: estimate (100 km/1000 m about 4.35 h, cap 8 h, TSS = h x 0.4225 x 100, no FTP input); kind at 59/60/119/120 minutes; going rules (approved member only, own row, today or later, route exists, idempotent second call); `PUT .../going` creates one workout row with the spec's name, description, one open step, focus goal link, `crew_ride_id`; unique per rider and ride; `IsGenerated` false; `IsKeySession` true for a long row and false for endurance; workout auto-push skips it; column exists after `UseDB` twice and on both engines; a ride delete removes going rows but leaves workouts, and reads mark such a row `orphaned` (`cancelled`, `left` when the rider is no longer approved).
- [ ] GREEN; `just check`; commit `"Let a rider join a crew ride and plan it as a fixed session"`.

### Task 2: The plan fills around it

**Files:** `apps/api/internal/scheduler/scheduler.go` (fixed input to `WeekWorkouts`: hours reduced with the 50 % floor, day removed, long slot dropped) (+ test); `apps/api/internal/api/training.go` (`fillWeek` day-taken counts `crew_ride_id` rows), `seasonplan.go` (`refreshWeek`), `adaptation.go` (`adaptRider` rules 4 and 5); `apps/api/internal/crewplan/preview.go` (+ test); `apps/api/internal/api/crewrides.go` (`dryRun`, `skip`, lock, `add` on leaving).

- [ ] RED: fill for a week with a long fixed ride (no generated long slot, reduced hours, floor, day taken with and without goal), an endurance one, a short one, a recovery week; refresh keeps a joined ride; day-before hard becomes `EasyVariant` with the marker, day-after a long ride capped at 60 minutes, the rider-built and already-adjusted left alone, both survive replan and the tick; `Preview` ops (`remove` only generated/unadjusted/unridden, `ease`, `shorten` with the 45-minute floor, `add` on leaving, leaves-alone list, blackout warning); `dryRun` writes nothing; apply recomputes, honours `skip`, ignores a vanished id; 409 while the tick holds the lock; failure midway leaves the rider unchanged; leaving does not restore eased sessions.
- [ ] GREEN; `just check`; commit `"Fill the week around a joined crew ride and preview join and leave"`.

### Task 3: Readiness advice and guards

**Files:** `apps/api/internal/api/{readiness.go,tomorrowforecast.go}` (+ tests); `apps/api/internal/api/ftptests.go` (409 on a crew-ride day); `apps/web/src/api/types.ts`.

- [ ] RED: caution and rest produce `crewRide` advice for today and, from the forecast, tomorrow, with the wattage only when FTP is known and only for the owner; ready produces none; no session is changed, moved or swapped in any verdict; "Ease tomorrow" on a crew ride is 409; `AdaptSessions` leaves it; scheduling an FTP test onto a crew-ride day is 409 and the test suggestion never picks it or the day after a long one.
- [ ] GREEN; `just check`; commit `"Advise easy riding on a crew-ride day instead of changing it"`.

### Task 4: Ride-together flag and proposals

**Files:** `apps/api/internal/schedule/together.go` (`crew_ride_together`, `ride_together_proposals`, `ride_together_members`) (+ test); `apps/api/internal/crew/crew.go` (`RemoveRiderEverywhere` and rider delete clear the flag); `apps/api/internal/crewplan/align.go` (+ test); `apps/api/internal/api/together.go` (`PUT /api/crews/{id}/together`, `GET /api/training/ride-together`), `crews.go` (`togetherDays` on members); `apps/web/src/api/{types.ts,client.ts}`.

**Produces:**

```go
type Candidate struct { Rider string; Session workout.Workout; Days []string }
type Proposal struct { CrewID, WeekStart, Day, RouteSlug string; Riders []string }
func Align(in AlignInput) []Proposal // pure: candidates per crew and week, visible routes, blackout, now
```

- [ ] RED: only opted-in approved members; own row only (a second rider's `PUT` cannot write another's); removing a member clears the flag; `Align`: qualifying session (long or endurance >= 60 minutes, generated, unadjusted, unridden, tomorrow or later), a rider with a crew ride that week excluded and one-sided means none, Saturday then Sunday then chronological, every rider's day free (taken, `MovedFrom`, blackout, hard or key the day before), route visible to all and within 15 % over the shortest session, no route or day means none; proposals stored once per crew and week, both riders read the same one, a premise change makes it `stale` and hidden; **a peer's response contains only the exposure table** (assert on the JSON keys); storage under `TestEachEngine`, idempotent schema.
- [ ] GREEN; `just check`; commit `"Propose a shared day and route when two crew mates both have a long ride"`.

### Task 5: Confirming a proposal

**Files:** `apps/api/internal/api/together.go` (`accept`, `decline`) (+ test); `apps/api/internal/crewplan/align.go` (revalidate).

- [ ] RED: accept moves only the caller's qualifying session (`MovedFrom`, `AdjustedMarker`, "Ride together" note), records their member row with `workout_id`, runs under the scheduling lock; accepting for another rider is impossible (no such parameter, and a second rider's call touches only their own row); it does not need the peer to have accepted; the last acceptance makes it `agreed`; any decline ends it for the week; a stale premise is 409 with the fresh proposal; every write endpoint called as a second rider changes none of the first rider's workouts; logs carry no health values.
- [ ] GREEN; `just check`; commit `"Let each rider confirm a shared ride for their own plan"`.

### Task 6: UI

**Files:** create `apps/web/src/components/plan/RideTogetherCard.vue`, `apps/web/src/components/CrewRideGoing.vue`; modify `TrainingPlanPage.vue`, `WeekStrip.vue`, `TodayCard.vue`, `CrewsPage.vue`.

- [ ] Week strip and day card: crew icon, "Long crew ride", estimate, going names, readiness advice line, orphaned note with "Update my plan"; Plan page `RideTogetherCard` (spec copy, "Move mine to <day>", "No thanks", who has accepted); Crew page: "I'm going" toggle and going names per ride opening a preview modal (changes with skip checkboxes, leaves-alone list, blackout warning) before applying, and an "Open to riding together" days selector on your own member row. Nuxt UI tokens only, typed inline handler params.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with two seeded riders in one crew (rows removed afterwards), light/dark, 375px; commit `"Show crew rides in the week, ride-together suggestions and the going toggle"`.
