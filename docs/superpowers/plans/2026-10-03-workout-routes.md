# Routes for workouts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generate a loop route sized and shaped for a planned ride, let a rider schedule a library route as a ride, link both to the workout, and put the course on the head unit with today's workout.

**Architecture:** The suggest handler's generator moves into `internal/loops` with a time objective; a pure `internal/routefit` estimates a route's time and scores terrain by session family; `internal/ridestart` stores an opt-in start point; two new `workouts` columns link a route; candidate, link and schedule endpoints; today's push also sends the course; a slide-over, a settings card and a schedule modal.

**Tech Stack:** Go stdlib, `internal/dbx`, `internal/routing`; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-10-03-workout-routes-design.md` — families, thresholds, the 15 % and 20 % windows, the privacy rules and copy are binding.

**Prerequisite:** the route-training stack (`claude/route-1-climbs` through `route-7-pacing-ui`: `internal/climbs`, `internal/pacing`, `goals.route_slug`, `rider_profiles.weight_kg`) is merged to `main`. Branch from `origin/main` after that; if it is not, stack on `claude/route-7-pacing-ui` instead.

## Global Constraints

- Read `AGENTS.md` first, especially route coordinates as personal location data, the outbound-client checklist and the "not configured" guard logging. No new dependencies. gofmt, go vet, `cd apps/api && golangci-lint run ./...` (0 issues) and `just check` green, run under `TZ=UTC` (seven scheduling tests are timezone-sensitive under Europe/Brussels; pre-existing, don't touch).
- Every new test uses a fixed clock with an explicit zone and passes under both `TZ=UTC` and `TZ=Europe/Brussels`. Fixtures are synthetic (invented coordinates and a fake routing engine); never a real route or place.
- The routing engine is tested with `httptest` (or the `routing.Client` fake the suite already substitutes), never the network. No new outbound client: generation reuses `routing.Client`, and every call takes the request's `ctx` (`r.Context()`), never `context.Background()`.
- New SQL through `dbx`, `TestEachEngine` (SQLite + PostgreSQL), idempotent schema in `UseDB` (`CREATE TABLE IF NOT EXISTS`, add-column that tolerates a second run). Owner-only everywhere; the rider comes from the session, never the body; someone else's workout, route or candidate is a 404.
- Every new "not configured" or refusal guard logs: Warn for 412 (no engine), Info for 409 (no start point), Error when the request fails (every seed failed, nothing within 15 %). **No coordinates, place labels, FTP, watts or weight in any log line**, only rider, workout id, counts, outcome.
- New rider-keyed tables (`ride_start_points`) are registered in `riderTables` (purge and rename) in the same task that creates them; the in-memory candidates are dropped in `purgeRiderSteps`.
- Responses never carry lat/lon beyond a candidate returned to its owner (as `routebuilder/suggest` does). The workout, schedule and start-point responses carry none.
- A generated route is never shared: created through `Source.Create` with `Targets nil` (not the HTTP handlers that apply a crew's auto-share), tagged `wroute:<workoutId>`.
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together. Frontend: Nuxt UI semantic tokens only, typed inline template handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back).
- Never `git stash` (shared across worktrees). Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **The suggest handler behaves exactly as before the extraction**: its existing tests pass unmoved. (Task 1)
2. **A route's time is one function** (`routefit.EstimateSeconds`) used by generation, scheduling and the stored `route_seconds`. (Task 2)
3. **The start point is stored at three decimals, opt-in, and never returned by any endpoint.** (Task 3)
4. **A generated route is owner-only: no auto-share, share links and crew targets refused until the tag is removed.** (Task 5)
5. **A routed session survives refresh, trim and replan, and readiness easing still applies to it.** (Task 5)
6. **`adjust` edits in place and drops the generated prefix; `link` changes nothing but the route.** (Task 6)
7. **Today's push sends the course to the rider's own Garmin accounts only, idempotently, deleting nothing.** (Task 7)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1-2 shared generator, route fit | `claude/wroute-1-loops` |
| 2 | 3 start point | `claude/wroute-2-start` |
| 3 | 4-5 candidates, save, link | `claude/wroute-3-generate` |
| 4 | 6 schedule a route | `claude/wroute-4-schedule` |
| 5 | 7 course with today's push | `claude/wroute-5-push` |
| 6 | 8-9 UI | `claude/wroute-6-ui` |

Restack right after each squash merge.

---

### Task 1: Extract the loop generator

**Files:** create `apps/api/internal/loops/{loops.go,loops_test.go}`; modify `apps/api/internal/api/server.go` (`handleRouteBuilderSuggest` calls it).

- [ ] Move `fireRoundTripAttempts`, `averageOvershootRatio`, `summarizePath`-independent pool selection, `filterLowBacktrack` and the seed helpers verbatim; give `loops.Generate(ctx, client, Request{Start, Profile, Hilliness, Seeds, Rounds, Objective}) ([]Loop, Stats, error)` an `Objective` interface (`TargetLength() float64`, `Refine(round1 []Loop) float64`, `Keep(Loop) bool`, `Score(Loop) float64`) so the suggest handler passes its distance objective (10 % filter, hilliness rank) and Task 4 passes a time one.
- [ ] RED first: a table test pinning the suggest objective's selection on a fixed pool (same output as `selectSuggestCandidates` today); then the move. `routebuilder_test.go`, `suggestselection_test.go` and the acceptance tests must pass **unmodified**.
- [ ] `just check`; commit `"Move the route builder's loop generation into its own package"`.

### Task 2: Route fit

**Files:** create `apps/api/internal/routefit/{routefit.go,family.go,routefit_test.go}`.

**Produces:**

```go
type Rider struct { FTP, WeightKg float64 }
func AverageFraction(steps []workout.WorkoutStep) float64
func FlatSpeed(r Rider, fraction float64) (mps float64, assumed bool)
func EstimateSeconds(points []gpx.Point, r Rider, fraction float64) (sec float64, assumed bool)
func FamilyOf(wk workout.Workout) Family
func Fit(f Family, wk workout.Workout, points []gpx.Point, r Rider) (score float64, note string)
```

- [ ] RED: `AverageFraction` (power steps by midpoint, others 0.50/0.65, repeat blocks); `FlatSpeed` and `EstimateSeconds` for a fixed flat and a fixed hilly synthetic track (hilly strictly slower at equal length), no-FTP fallback 25 km/h plus 1.4 s per metre ascent and `assumed` true; `FamilyOf` for each row of the spec table including the 75 and 150 minute boundaries and the "Long ride, with climbing" name; `Fit` at each family's boundaries (recovery 4 m/km and the 1 km climb penalty, endurance 5 to 10 band, long 10 m/km, steady coverage and the climb alternative, climb 0.8 x work step, no climb halves rolling) and its notes.
- [ ] GREEN; `just check`; commit `"Estimate a route's time for a rider and judge its terrain against a session"`.

### Task 3: Start point

**Files:** create `apps/api/internal/ridestart/{ridestart.go,ridestart_test.go}`; modify `apps/api/internal/api/{riderdata.go,riderdelete.go,server.go}` (+ `ridestart.go` handlers and tests), `apps/web/src/api/{types.ts,client.ts}`.

- [ ] RED: under `TestEachEngine`, `Set` rounds to three decimals and reduces the label to a town (same rule as `weather.TownLabel`), rejects out-of-range and NaN, upserts; `Get` / `Delete`; schema idempotent on a second `UseDB`. `GET /api/training/ride-start` returns `{set, place}` and **never lat or lon** (assert the raw body has neither key), `PUT` / `DELETE` 204, owner-only. `riderTables["ride_start_points"]` registered (`Purged`, rename `rider` unique): the purge and rename registry tests go green, and a purge removes the row.
- [ ] GREEN; `just check`; commit `"Let a rider save where their rides start, privately"`.

### Task 4: Route candidates for a workout

**Files:** create `apps/api/internal/loops/time.go` (the time objective), `apps/api/internal/api/workoutroute.go` (+ test), `apps/api/internal/api/candidates.go` (in-memory store, + test); modify `server.go` routes.

- [ ] RED (routing engine via `httptest` or the existing fake): `POST /api/training/workouts/{id}/route-candidates` builds round 1 at flat speed x planned seconds (3 seeds) and round 2 at planned / seconds-per-metre / overshoot (7 seeds), profile and `steepness_difficulty` per family; keeps loops within 15 % of planned time, drops backtrackers, ranks `0.5 x timeFit + 0.5 x terrainFit`, returns at most 3 that differ (3 % distance and 10 % ascent); one candidate per distinct shape on a fixed pool. 412 + Warn without an engine (and no engine call); 409 `no_start_point`; 409 for a past, ridden, indoor, test or non-cycling workout; 429 from the builder limiter; 502 + Error when every seed fails or nothing is within 15 %; partial failure = Warn and the rest returned; not-your-workout 404; ctx cancelled stops the calls. The start comes from `ride_start_points`, never the body. Logs hold no coordinates, place, FTP or watts (capture the log and assert).
- [ ] Candidates held `(rider, id)`, 30 min, at most 12 per rider, bound to the workout id; expiry under a fixed clock; `Forget(rider)` wired into `purgeRiderSteps`.
- [ ] GREEN; `just check`; commit `"Generate loop candidates sized and shaped for a planned ride"`.

### Task 5: Save, link, unlink

**Files:** `apps/api/internal/workout/{workout.go,db.go}` (`route_slug`, `route_seconds`, `UnlinkRoute`), `apps/api/internal/api/{workoutroute.go,training.go,seasonplan.go,replan.go,server.go}`, `apps/api/internal/alternates/alternates.go`, `apps/web/src/api/types.ts`.

- [ ] RED: columns round-trip on both engines and an existing DB gains them; `POST .../route {candidateId}` creates a route via `Source.Create` (owner = session rider, `Targets nil`, tag `wroute:<id>`, GPX from the held path with elevation, no crew auto-share even with an auto-share crew set), sets slug and `route_seconds`; 410 for an unknown or expired id, 404 for another rider's; choosing again deletes only the previous route carrying this workout's tag and no other link; `DELETE .../route` unlinks and deletes a `wroute:` route but only unlinks a library route. Creating a share link or a crew target on a `wroute:` route is 409, and works once the tag is removed. `workoutDTO.route` has slug, name, distance, ascent, seconds, `generated` and no coordinates; a route the rider cannot see is omitted. Deleting a library route clears every workout's link. `untouchedPlanSession` and `removePlanMadeWorkouts` skip a routed session (refresh, trim and replan leave it, parity test extended); `scheduler.IsGenerated` is unchanged so readiness easing still eases it and the DTO shows the route/ride mismatch; `alternates.Plannable` is false when routed; indoor conversion keeps the link but the DTO marks it inactive.
- [ ] GREEN; `just check`; commit `"Link a generated route to its workout and keep it private"`.

### Task 6: Schedule a route

**Files:** `apps/api/internal/api/routeschedule.go` (+ test), `server.go`, `apps/web/src/api/{types.ts,client.ts}`.

- [ ] RED: `GET /api/routes/{slug}/schedule?date=` reports the day's unridden outdoor cycling sessions, the route estimate at 0.65 x FTP (fallback flagged) and the offered choices per the spec table, including the 20 % boundary and "more than one session needs `workoutId`"; `POST` `link` sets only `route_slug` / `route_seconds` (name, steps, description untouched); `adjust` rewrites that session in place (same id, goal kept, one endurance step of the route time, description without the generated prefix, so replan and refresh skip it) and its Garmin copy is repushed once, the key-session replacement flagged in the response; `new` creates a rider-built endurance ride with the focus goal id and refuses a day that already has an unridden outdoor ride; 409 past day or already ridden; 422 non-cycling route; a route the rider cannot see is 404; indoor, running and ridden sessions on the day do not block `new`. Fixed clock, both zones, "today" from the rider's local day.
- [ ] GREEN; `just check`; commit `"Schedule a library route as a ride"`.

### Task 7: The course with today's workout

**Files:** `apps/api/internal/api/{workoutpush.go,training.go,server.go}` (+ tests).

- [ ] RED: `pushWorkoutsForRider` also pushes a routed, non-indoor today session's route to the rider's own **Garmin** accounts through `applyPush` selected to that `PlanKey`: created once, a second pass is a noop (hash), nothing deleted, other riders' accounts and Wahoo untouched, a future day sends nothing extra, an indoor or unrouted session sends no course, a course failure is a Warn and today's workout is still pushed. `handlePushWorkoutToGarmin` sends workout and course together with a `course` outcome and a Warn on failure; a "send course to devices" action pushes it to all the rider's own accounts. Logs carry no coordinates.
- [ ] Device check, recorded in the PR (not a test): on a real Edge, does a pushed course and a pushed workout run together? The day card copy waits on the answer.
- [ ] GREEN; `just check`; commit `"Send a routed ride's course with today's workout"`.

### Task 8: Route for this ride UI

**Files:** create `apps/web/src/components/plan/{RouteForRideSlideover.vue,RideStartCard.vue}`; modify `TodayCard.vue`, `WorkoutSlideover.vue`, `WeekStrip.vue`, `SettingsPage.vue`, `apps/web/src/api/{types.ts,client.ts}`.

- [ ] "Route for this ride" on the day card and slideover, hidden when `routingConfigured` is false, for an indoor, ridden, past or test session. Slideover: inline start picker when none (search or browser geolocation, copy about using a nearby corner), three candidate cards (map via `RouteMap`'s dynamic import only, distance, ascent, estimated vs planned time, family note, "Best fit" chip), "Use this route"; 409/410/412/429/502 each get plain copy. With a route: name, distance, estimated time, mismatch note, Change / Remove, "Send to devices" on today. Settings card "Where do your rides start?" shows the label only.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser against a fake routing engine with a seeded plan (rows removed afterwards), light/dark, 375px; commit `"Offer a route for a planned ride on the Plan page"`.

### Task 9: Schedule ride UI

**Files:** create `apps/web/src/components/ScheduleRideModal.vue`; modify `RouteDetailModal.vue`, `RouteCard.vue`, `apps/web/src/api/{types.ts,client.ts}`.

- [ ] "Schedule ride" on the detail modal and the card (cycling, visible routes). Date picker (today onward), what is on the day, the choices with their consequences in words (the key-session warning included), estimated time marked as an estimate, success toast linking to the Plan page. "Train for this route" is untouched.
- [ ] Verify as Task 8; commit `"Schedule a route as a ride from the library"`.
