# Route-aware training and pacing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Link a goal to its route, extract the route's climbs, bias Build and Peak sessions toward them, show what the route demands against the plan, then produce a race-day pacing plan for the route and push it as a FIT course.

**Architecture:** Pure `internal/climbs` (moved out of `fitcourse`) and `internal/pacing` (physics, intensity, plan); `goals.route_slug` links; `scheduler.WithRouteDemand` and `workoutlib.PickNear` bias generation; read-only demands and pacing endpoints; `fitcourse.Options.CoursePoints` plus a separate pacing course push.

**Tech Stack:** Go stdlib, `internal/dbx`, `muktihari/fit`; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-29-route-training-and-pacing-design.md` — thresholds, factors, defaults, names and copy are binding.

## Global Constraints

- Read `AGENTS.md` first, especially that GPX and route coordinates are personal location data. No new dependencies. gofmt, go vet, `cd apps/api && golangci-lint run ./...` (0 issues) and `just check` green, run under `TZ=UTC` (seven scheduling tests are timezone-sensitive under Europe/Brussels; pre-existing, don't touch).
- Every new test uses a fixed clock with an explicit zone and passes under both `TZ=UTC` and `TZ=Europe/Brussels`.
- New SQL through `dbx`, `TestEachEngine` (SQLite + PostgreSQL), idempotent schema in `UseDB` (add-column and `CREATE TABLE IF NOT EXISTS`, safe to run twice). Owner-only everywhere; the rider comes from the session, never the body.
- Route visibility is `config.VisibleTo(route, rider, crews)`: linking a goal, reading demands or pacing, and pushing all check it; an invisible or deleted route answers `available: false` or 404, never its details.
- Never log coordinates, and never log health values (watts, FTP, weight, HR) next to a rider name: log rider, goal id, route slug, outcome only.
- Response bodies carry distances and elevations, never latitude or longitude; route coordinates leave the app only inside the pacing FIT to the rider's own linked Garmin or Wahoo account. No third-party calls.
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together. Frontend: Nuxt UI semantic tokens only (no raw Tailwind colours), typed inline template handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back).
- Test fixtures are synthetic routes built in code; never commit a real GPX file or a real coordinate trail.
- `fitcourse.DeriveClimbs` output must not change (device cues); a test pins it.
- Never `git stash` (shared across worktrees). Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **`fitcourse.DeriveClimbs` is byte-for-byte unchanged in behaviour** after the algorithm moves; training uses 1 km and 3 %. (Task 1)
2. **Linking a goal to a route needs `VisibleTo`; a vanished route leaks nothing.** (Task 3)
3. **Bias only in Build and Peak, only generated and untouched sessions, never more than one rung above progression, zones unchanged.** (Tasks 5-6)
4. **Plan NP equals `FTP x IF`**; climb watts capped at 1.10 / 1.05 / 1.00 x FTP. (Task 8)
5. **No coordinate in any response or log line.** (Tasks 4, 9)
6. **Course point names are at most 15 ASCII characters**; the pacing course is separate from the library route's course and re-push replaces, not duplicates. (Tasks 10-11)

## Stack

(a) first, (b) after; restack right after each squash merge.

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1-2 climbs, physics, intensity | `claude/route-1-climbs` |
| 2 | 3-4 goal-route link, weight, route demands | `claude/route-2-goal-route` |
| 3 | 5-6 bias generation | `claude/route-3-bias` |
| 4 | 7 UI for (a) | `claude/route-4-demands-ui` |
| 5 | 8-9 pacing plan and API | `claude/route-5-pacing` |
| 6 | 10-11 FIT course points and push | `claude/route-6-fit-push` |
| 7 | 12 UI for (b) | `claude/route-7-pacing-ui` |

---

### Task 1: Climb detection

**Files:** create `apps/api/internal/climbs/{climbs.go,climbs_test.go}`; modify `apps/api/internal/fitcourse/fitcourse.go` (+ test).

**Produces:**

```go
type Config struct { MinLengthM, MinGradient, ClimbingGrade, MergeGapM, SmoothRadiusM float64 }
var DeviceConfig, TrainingConfig Config   // 500 m / 1000 m, both 3 %
type Climb struct { Index, StartIdx, EndIdx int; StartM, EndM, LengthM, GainM, AvgGradient, Score float64 }
func Detect(points []gpx.Point, cfg Config) []Climb
func Category(score float64) (cat int, ok bool)
```

- [ ] RED first: pin `fitcourse.DeriveClimbs` on three synthetic tracks (record current output). Then `Detect`: a steady 6 % ramp; a 150 m false flat inside one climb (merged) and a 250 m one (split); a trailing descent excluded; 900 m rejected and 1 100 m kept under `TrainingConfig`; 2.9 % rejected and 3.1 % kept; no elevation on any point returns nil; `Score` and `Category` match `fitcourse`'s thresholds. Tracks are generated in code, no GPX.
- [ ] GREEN: move the algorithm and smoothing from `fitcourse` into `climbs`; `fitcourse.DeriveClimbs` calls `Detect(..., DeviceConfig)` and maps to its own `Climb`. The pinned test must pass unmodified. `just check`; commit `"Move climb detection into its own package with a training config"`.

### Task 2: Physics and intensity

**Files:** create `apps/api/internal/pacing/{physics.go,intensity.go}` (+ tests).

**Produces:** `Physics{MassKg, BikeKg, CdA, Crr, Rho, Eta, MaxDescentKph}` with `DefaultPhysics(massKg)`; `(Physics) Speed(watts, grade float64) float64` (m/s, bisection); `Segments(points, minM, maxM)` -> `[]Seg{StartM, EndM, Grade}` (distance and grade only, no coordinates); `EventIF(hours) float64` (0.95 / 0.85 / 0.75 at exactly 2 h and 4 h edges, spec bands); `ClimbFactor(durationSec) float64` (1.10 / 1.05 / 1.00 at 5 and 20 min); `ClimbSeconds(c climbs.Climb, seg []Seg, ph Physics, watts float64) float64`.

- [ ] RED: hand-computed flat case (250 W, 83 kg, defaults: speed within 0.5 % of the value worked out in the test comment) and a 6 % climb; speed strictly increases with power and falls with grade; descent capped at 60 km/h; zero power on a descent gives the cap or coasting speed, never a negative or NaN; `EventIF` and `ClimbFactor` edges; `Segments` respects min and max length and sums to the track length.
- [ ] GREEN; `just check`; commit `"Add the pacing physics model and target-intensity rules"`.

### Task 3: Goal-route link and weight

**Files:** `apps/api/internal/workout/{workout.go,db.go}` (+ tests), `apps/api/internal/api/training.go` (goal create/update/DTO), `apps/web/src/api/types.ts`.

- [ ] RED (under `TestEachEngine`): `goals.route_slug`, `goals.pacing_if`, `rider_profiles.weight_kg` round-trip; opening an existing database adds the columns and a second `UseDB` changes nothing; create and update a goal with a `routeSlug` the rider can see; a slug the rider cannot see (another rider's private route, unknown slug) is 422 with no detail about the route; an update that omits `routeSlug` keeps it, empty clears it; `pacingIf` outside 0.60 to 1.05 (and not 0) is 422; a non-cycling goal cannot carry a route; profile save accepts 30 to 250 kg or 0, rejects the rest, and `SaveProfile` from other forms never zeroes weight; `riderdelete` still removes everything.
- [ ] GREEN; `just check`; commit `"Let a goal name its route and a rider record their weight"`.

### Task 4: Route demands endpoint

**Files:** create `apps/api/internal/api/routedemands.go` (+ tests); `server.go` route; `apps/web/src/api/{types.ts,client.ts}`.

- [ ] RED: `GET /api/training/goals/{id}/route-demands` shape per spec; climbs with `durationSec` and `watts` from `FTP x IF x ClimbFactor` through the physics model, and `kind` boundaries at 4, 8 and 20 minutes; `available:false` with the right `reason` for no route, invisible or deleted route, no elevation, non-cycling, no FTP; coverage counts the longest single work step at sweet-spot-or-above in this goal's planned workouts from today, covered at exactly 80 %, and generates the spec's message for uncovered, covered and no-climb cases; another rider's goal is a 404; **the JSON contains neither the fixture's latitude nor longitude** (scan the body); the handler writes nothing; the log line has slug and outcome only.
- [ ] GREEN; `just check`; commit `"Show what a goal's route demands against the plan"`.

### Task 5: Rung matching

**Files:** `apps/api/internal/workoutlib/library.go`, `apps/api/internal/scheduler/{demand.go,scheduler.go}` (+ tests).

**Produces:**

```go
func PickNear(l Ladder, targetLevel, maxSeconds float64, wantWorkSeconds int) (Rung, bool)
type RouteDemand struct { Sustained, Short, Punchy int /* wanted work seconds, 0 = none */; Climbing bool }
func WithRouteDemand(d *RouteDemand) Option        // variadic on WeekWorkouts and NextWorkouts
func DemandFromClimbs(cs []climbs.Climb, durations []float64, ascentM, distanceM float64) *RouteDemand
```

- [ ] RED: `PickNear` never returns a level more than one above `Pick`'s, respects the cap, picks the work length nearest the wanted one, ties to `Pick`'s own, and with `wantWorkSeconds == 0` equals `Pick`. `DemandFromClimbs`: wanted lengths per the spec table (longest 4 to 30 min climb capped at 20 min; median of under-6-minute climbs; steepest under-2-minute climb), `Climbing` at 10 m/km or 1 500 m. Scheduler: a Build week's threshold slot and a Peak week's vo2max slot change rung toward the demand; **Base, Taper and recovery weeks are identical to no demand**; the long ride is renamed "Long ride, with climbing" only in Build or Peak; no `WithRouteDemand` is byte-identical output to today; zones never change.
- [ ] GREEN; `just check`; commit `"Pick workout rungs whose effort lengths match the route's climbs"`.

### Task 6: Wire the bias into generation

**Files:** `apps/api/internal/api/seasonplan.go` (`seasonContext`, `fillWeek`, `refreshWeek`) and `replan.go` (+ tests).

- [ ] RED: `seasonContext` computes the demand once per goal (no route, invisible route, no elevation or no FTP all yield nil, never an error that blocks planning); a new week is filled with matched rungs; `refreshWeek` rewrites an untouched generated session to the matched rung; **a moved, adjusted, edited, ridden or FTP-test session is left alone**; linking a route does not rebuild a week already filled (only the next refresh does); a second `planSeason` pass creates and changes nothing; replan uses the same demand; log lines carry goal id and outcome only.
- [ ] GREEN; `just check`; commit `"Bias generated Build and Peak sessions toward the goal route's climbs"`.

### Task 7: UI for training

**Files:** create `apps/web/src/components/plan/RouteDemandsCard.vue`; modify `GoalSlideover.vue`, `usePlanGoals.ts`, `TrainingPlanPage.vue`, the profile form, `ElevationProfile.vue`.

- [ ] Route picker in the slideover (visible cycling routes, "None") and the shortcut passes the slug on save; profile Weight field with the assumed-75-kg hint; the demands card (climbs table, per-row covered mark, coverage message, biasing note, unavailable reasons with their fix); `ElevationProfile` shades climbs from `startM`/`endM` and labels C1..Cn.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with a seeded goal and synthetic route (rows removed afterwards), light/dark, 375px; commit `"Show route demands and let a goal name its route"`.

### Task 8: Pacing plan

**Files:** `apps/api/internal/pacing/{plan.go,hr.go}` (+ tests).

**Produces:** `Build(in Input) Plan`; `Input{Segs []Seg, Climbs []climbs.Climb, FTP float64, IF float64, Phys Physics, Profile workout.RiderProfile}`; `Plan{Segments []Segment, Seconds, NormalizedW, AvgW, IF, AvgKph, VI float64}`; `CueName(n int, low, high int, hr bool) string`.

- [ ] RED: normalised power of the produced timeline equals `FTP x IF` within 0.5 %; climb watts follow 1.10 / 1.05 / 1.00 and are capped at that multiple of FTP; descents steeper than -3 % get `0.55 x FTP`; adjacent same-kind segments merge; targets are +/-3 % rounded to 5 W; a flat course gives constant power (VI 1.0); HR ranges come from `workoutlib.HRRange` and are absent without LTHR or max HR; no FTP is unavailable; `CueName` is at most 15 ASCII characters for one- and three-digit watts and HR, and never contains an en dash.
- [ ] GREEN; `just check`; commit `"Build a gradient-aware pacing plan from FTP and the route profile"`.

### Task 9: Pacing endpoint

**Files:** create `apps/api/internal/api/pacing.go` (+ tests); `server.go`; `apps/web/src/api/{types.ts,client.ts}`; update `routedemands.go` to take climb watts from the plan.

- [ ] RED: `GET /api/routes/{slug}/pacing?goal=<id>` returns plan, totals and assumptions (`massAssumed` true when weight is 0); the goal's `pacingIf` overrides the derived IF and `ifSource` says so; a goal that is not the rider's is 404; an invisible route is 404; no elevation, non-cycling or no FTP is `available:false` with a reason; the two-pass IF picks the band of pass 1 and does not flip; the demands endpoint now reads the same climb watts; **no latitude or longitude in the body**; nothing is written; log line has slug and outcome only.
- [ ] GREEN; `just check`; commit `"Serve the pacing plan for a route"`.

### Task 10: Course points in the FIT

**Files:** `apps/api/internal/fitcourse/fitcourse.go` (+ test), `apps/api/internal/pacing/course.go`.

- [ ] RED: `Options.CoursePoints` writes a `course_point` per entry at the nearest track point (position, timestamp, distance), with the given type and name; the file round-trips through `Decode` and the course points keep their names and types; `pacing.CoursePoints(plan, climbs)` yields, per climb, a category point named `C3 250-265W` (or `C3 148-156bpm`) and a `Top C3` summit; all names at most 15 ASCII characters; existing `TurnCues`/`ClimbCues` output unchanged.
- [ ] GREEN; `just check`; commit `"Let a FIT course carry named course points for climb targets"`.

### Task 11: Push the pacing course

**Files:** create `apps/api/internal/api/pacingpush.go`, `apps/api/internal/api/pacingpushes.go` (store) (+ tests); `targets` glue; `riderdelete.go`.

- [ ] RED (under `TestEachEngine` for the store): `pacing_pushes` created idempotently; `GET .../pacing.fit` returns an `application/vnd.ant.fit` attachment named from the slug; `POST /api/routes/{slug}/pacing/push` imports to the rider's **own** linked account (Garmin via `ImportCourse`, Wahoo via `CreateRoute`/`UpdateRoute`), records the remote id, and a second push deletes the previous Garmin course or updates the Wahoo route; 412 with a warn log and no coordinates in it when no account is linked; another rider's account or an invisible route is 404; the library route's own course and its sync state are untouched; a rider delete removes the rows.
- [ ] GREEN; `just check`; commit `"Push a pacing course to the rider's Garmin or Wahoo account"`.

### Task 12: UI for pacing

**Files:** create `apps/web/src/components/PacingCard.vue`; modify `RouteDetailModal.vue`, `TrainingPlanPage.vue`, `GoalSlideover.vue`.

- [ ] Pacing card on the goal and in the route modal: merged segment table (km range, kind, gradient, watts and HR, time), expected time, NP and average speed, assumptions behind a disclosure, the "Assumed 75 kg" hint, "Export FIT" and "Send to Garmin / Wahoo" only for linked accounts with the "Sending your route to your own device" wording the library push uses; the slideover's Pacing intensity slider defaulting to the derived value.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with a seeded goal and synthetic route (rows removed afterwards), light/dark, 375px; commit `"Show the pacing plan on the goal and the route"`.
