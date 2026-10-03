# Routes for workouts — design

Status: draft 2026-10-03. Builds on the route builder's suggested loops (`internal/routing`, `handleRouteBuilderSuggest`),
route-aware training and pacing (`2026-09-29-route-training-and-pacing-design.md`: `internal/climbs`, `internal/pacing`,
`goals.route_slug`, `rider_profiles.weight_kg`, all unmerged when this was written; the plan is stacked on them) and
the season plan's guarantees (fill-once, `untouchedPlanSession`, readiness adaptation).

## Why

A planned ride says "2 h endurance" or "3 x 12 min sweet spot" and stops there. The rider then opens the route
builder, guesses a distance, scrolls loops that ignore what the session is for, and saves one. Two requests, one
link between a planned workout and a route:

1. "For a workout be able to generate a route for that specific workout."
2. "From the routes add a button to schedule the route."

(1) goes workout to route: a loop whose length and terrain suit the session. (2) goes route to workout: put a
library route on a day. Both end in the same state, `workouts.route_slug`, and both reuse the same estimate of how
long a rider takes on a route, so they share a spec. The route-training spec went goal to route ("Train for this
route"); that stays separate, see "Goals".

## What exists

- `routing.Client.RoundTrip(ctx, start, distanceM, seed, profile, hilliness)` is OpenRouteService (ORS)
  `round_trip`: one loop per call, varied by seed, `cycling-regular|road|mountain|electric`, `steepness_difficulty`
  0 to 3, elevation and surface on every path. `Route` snaps waypoints. No avoid-hills or "find me a climb": hilliness
  is the only terrain lever, and it biases loops weakly (ascent per km, not total, shows it). Off unless configured
  (`s.Routing != nil`, `routingConfigured` in the config DTO), key from `DOMESTIQUE_ROUTING_API_KEY`, shared free-tier
  quota, response cache, outbound spans already in place.
- `handleRouteBuilderSuggest` does the generation inline in `api/server.go`: two rounds (5 calibration seeds at the raw
  length to measure ORS's systematic overshoot, 10 refinement seeds at the corrected length), a 10 % distance filter,
  a backtrack filter, ranking by ascent per km. Shaped for "about N km"; it knows nothing of time or session type.
- `internal/geocoding` (Nominatim, no key) backs `GET /api/geocode`; the route builder uses it, or the browser's
  geolocation, to pick a start. Nothing is stored: the start is per request.
- `weather_locations` is the one stored rider location: opt-in by row, rounded to two decimals (about 1 km), never
  returned by the API. A town is the right precision for a forecast and the wrong one for a loop that must start on a
  real road.
- `internal/climbs` (`Detect`, `TrainingConfig`: 1 km and 3 %), `internal/pacing` (`Physics.Speed`, `Segments`,
  `TotalSeconds`, `ClimbSeconds`, `DefaultPhysics(weightKg)`) give a route's climbs and a time at a given power.
- A route is a library row owned by `Owner`; `Source.Create` takes GPX with elevation, `Targets nil` means
  owner-only push, and only the HTTP save handlers apply a crew's auto-share. A route created straight through
  `Source.Create` is never auto-shared.
- Sessions: `workouts` rows; `untouchedPlanSession` (generated, unedited, not indoor, no snapshot, never updated) gates
  refresh, trim and replan; readiness adaptation changes `scheduler.IsGenerated` sessions; the FTP test scheduler
  already sets the precedent for "rider-chosen change to a day": link to the focus goal, drop the description prefix.
- Pushes: workouts go to Garmin only (`syncWorkoutToGarmin`, idempotent on a content hash); `pushWorkoutsForRider`
  sends **today's** session only, for riders who set `AutoPushWorkouts`. Courses go through the library push
  (`applyPush`, selectable by `PlanKey{account, slug}`, state in `sync_state`) to Garmin and Wahoo.

## Generating a route for a workout (feature 1)

Cycling, outdoor, unridden, dated today or later, owned by the caller, not an FTP test. "Route for this ride" on
the day card and in the workout slideover. Indoor sessions never show it: an indoor session needs no route, and
converting a routed session to indoor keeps the link but stops the day card and push from using it (reverting
restores both).

### Length: planned time, not distance

Planned seconds are `workout.PlannedSeconds(steps)`. Average power is `FTP x f`, where `f` is the time-weighted
mean fraction of FTP over the steps: power-target steps use their midpoint, other steps default to 0.50 (warmup,
cooldown, recovery, rest) or 0.65 (active, open). Speed on flat road comes from `pacing.Physics.Speed(watts, 0)`
with the rider's weight (75 kg flagged as assumed, as in pacing). No FTP: **25 km/h**, flagged `speedAssumed`, and
a route's time is its distance over that speed plus 1.4 s per metre of ascent. First guess at the loop length is
flat speed x planned seconds.

The route's real time is estimated through the same physics over its smoothed segments at the same average
power (`pacing.TotalSeconds`), so a hilly loop honestly takes longer than a flat one of equal length. That is
the one function (`routefit.EstimateSeconds`) feature 2 also uses.

### Terrain: five families, two levers

The session maps to a family; a family sets ORS's profile and `steepness_difficulty` (the only levers) and says
how a finished loop is judged (the part ORS cannot do):

| Family | Which sessions | Profile, hilliness | A loop fits when |
|---|---|---|---|
| `recovery` | endurance zone, planned under 75 min | `cycling-regular`, 0 | at most about 4 m/km; any 1 km climb at 3 % costs 30 % |
| `endurance` | endurance zone, 75 to 150 min | `cycling-road`, 1 | 5 to 10 m/km, tapering to nothing 8 m/km outside |
| `long` | endurance zone over 150 min, or named "Long ride, with climbing" | `cycling-road`, 3 | 10 m/km or more (route-training's own "climbing" bar) |
| `steady` | tempo, sweet spot, threshold | `cycling-road`, 1 | longest run of road within 2 % grade covers the longest work step, or one climb lasts 80 % of it |
| `climb` | vo2max, anaerobic, intervals | `cycling-road`, 3 | a climb (`TrainingConfig`) lasts at least 0.8 x the work step at its power; none: rolling counts half |

Work step length is the longest work step in `Steps`; its distance and a climb's duration are taken at that
step's own power through the physics. Fit is 0 to 1; a loop below 0.5 still shows, with a plain note
("No climb long enough for 6-minute intervals; do them on the flatter stretches"). The family table is a
heuristic, not a coaching claim; the copy says "suited to", never "perfect for".

### Candidates: generate wide, score, keep three

The generator is lifted out of the suggest handler into `internal/loops` unchanged in behaviour, then given a time
objective instead of a distance one:

1. Round 1 (3 seeds) at the first-guess length. For each loop that came back: estimated seconds, and ORS's overshoot.
2. Round 2 (7 seeds) at planned seconds / the average seconds-per-metre of round 1 / the overshoot ratio.
3. Drop loops whose estimated time is more than **15 %** off planned and loops that mostly backtrack
   (the existing filter). All fail: 502, Error log, "could not find a loop close enough to the length of that ride".
4. Rank by `0.5 x timeFit + 0.5 x terrainFit`, `timeFit = 1 - |est - planned| / (0.15 x planned)`.
5. Keep the top 3 that differ (skip one whose distance is within 3 % and ascent within 10 % of one already kept).

Ten ORS calls, not fifteen: the quota is shared and a rider may plan a week. Same per-rider limiter as the builder
(`rateLimitRouteBuilder`). Calls are concurrent within a round, with `r.Context()` so every call is a child span.

A candidate returns `{id, points, distanceM, ascentM, surface, elevationProfile, estimatedSeconds, family, fit,
note}` plus, on the response, `plannedSeconds`, `speedKph`, `speedAssumed`. `points` are the owner's own generated
loop, the same data `routebuilder/suggest` already returns to its caller. Candidates stay **in memory** keyed
`(rider, id)` for 30 minutes, at most 12 per rider: the save step builds the GPX from the server's own path (with its
elevation, which the browser-resend path of the builder drops and then pays an external elevation lookup to
recreate) instead of trusting posted coordinates. A lost candidate (restart, another replica) is 410
"generate again".

### Start point

A new rider-keyed `ride_start_points` table, one row, **opt-in by row** like `weather_locations`: the rider picks
it in Settings (and inline the first time they press "Route for this ride") with the route builder's location
chooser (search or browser geolocation). Stored to **three decimals** (about 110 m): ORS snaps to a road anyway,
and the exact doorstep is never needed or kept; the copy suggests the corner or the first junction. The label is
reduced to a town the way `weather.TownLabel` does. **The API never returns the coordinates**: `GET` answers
`{set, place}` only, `PUT {place, lat, lon}` and `DELETE` write. Only `POST .../route-candidates` reads them, and
the loops it returns start and end there, as any route does.

Rejected: reusing the weather town (about 1 km of error is a loop that starts in someone else's street);
inferring home from where the rider's most-used routes start (compiles a location nobody gave, and quietly goes
wrong after a move); asking each time (nobody types a start for every ride).

### Save, link, replace

`POST /api/training/workouts/{id}/route {candidateId}` creates a library route through `Source.Create` (owner = the
session rider, `Targets nil`, so **no crew auto-share**), name "<workout name> loop, 62 km", tag `wroute:<workoutId>`,
and sets `workouts.route_slug` and `workouts.route_seconds` (the estimate when chosen, so week responses read no GPX).
Choosing again replaces: the previous route is deleted only if it carries that same `wroute:` tag and no other
workout links it. `DELETE .../route` unlinks and, for a `wroute:` route, deletes it; a library route (feature 2) is
only unlinked. The `wroute:` tag is the privacy guard: creating a share link or a crew target on a route that
carries it is a 409 ("generated for a ride from your start point; remove the tag to share it"). Removing the tag
is the rider's own, deliberate way to make it an ordinary route.

## Scheduling a route (feature 2)

"Schedule ride" in `RouteDetailModal` and on `RouteCard`: pick a date (today or later, the rider's local day),
see what is on it, choose. `GET /api/routes/{slug}/schedule?date=` returns the situation; `POST` with
`{date, choice, workoutId?}` does it. Cycling routes only, visible to the caller (`config.VisibleTo`).

The route's time is `routefit.EstimateSeconds` at **endurance power** (0.65 x FTP; 25 km/h flat fallback), flagged
as an estimate wherever shown.

| That day has | Choices |
|---|---|
| nothing, or only indoor, running or ridden sessions | `new`: a rider-built endurance ride sized to the route time |
| an unridden outdoor cycling session whose route time is within 20 % of its planned time (the planned time is the base: `abs(route - planned) <= 0.2 x planned`) | `link` (default) |
| the same, differing by more than 20 % of the planned time | `link` (ride is as planned, the route just rides longer or shorter) or `adjust` |
| more than one such session | the list, `workoutId` required |

- **`link`**: sets `route_slug` and `route_seconds`. Name, steps and description are untouched.
- **`adjust`**: rewrites that session to a single endurance step of the route time, zone endurance, level 0, name
  "Endurance ride: <route>", description "Sized to <route>." The generated prefix is dropped, so it is the rider's
  own session from now on (replan and refresh leave it), and the goal id is kept so scheduling counts the day as
  taken, the FTP-test precedent. When the replaced session was a key session
  (`scheduler.IsKeySession`) the modal says "This replaces your threshold session; the plan will not add another
  this week." Done in place (same id), so an existing Garmin copy is updated in place by the normal push (the content hash changes) rather than
  duplicated; adjust itself pushes nothing.
- **`new`**: `CreateWorkout`, cycling, endurance, goal id = the focus goal when there is one, description as above.
  Never a second session on a day with an unridden outdoor ride: that is `link` or `adjust`.
- 409 for a past day, for a `link` or `adjust` that names a ride that is already ridden (or otherwise cannot take a route), and for a choice the day does not offer; 422 for a non-cycling route. A ridden ride on the day does **not** block `new`: ridden sessions are ignored when working out the choices, as the table says.

### What a linked route does to the plan's guarantees

**Ruling: a route link freezes the session against refresh, trim and replan, whether or not its length changed.**
`untouchedPlanSession` and `removePlanMadeWorkouts` gain `wk.RouteSlug == ""`, stated like the other guards even
though updating the row already moves `UpdatedAt`. Otherwise a rebuilt week silently drops the link, or swaps the
content under a route chosen for the old one. Everything else is unchanged:

- Fill-once and auto-scheduling see a taken day exactly as before; nothing is re-added.
- Readiness adaptation still eases a routed generated session (`IsGenerated` is untouched): protecting the rider
  outranks the route. The day card then shows "The route takes about 2 h; today's ride is now 1 h" from
  `route_seconds` against `plannedSeconds`, and offers "Remove route".
- Alternates are not offered on a routed session (`alternates.Plannable` false): a swap changes what the route
  was chosen for. Remove the route first.
- Move keeps the link; the route follows the session. Deleting the library route clears every workout's link
  (`Training.UnlinkRoute(slug)`), which also stops a recreated slug attaching a stranger's route; the DTO shows a
  route only if it is visible to the rider.

### Goals

`goals.route_slug` ("Train for this route", route-training) and `workouts.route_slug` are independent: one says
"this event is on that route", the other "ride this route on that day". Riding the goal route as a recon is just
feature 2. No coupling, no pacing-card changes here.

## Pushing the course

A course and a workout are separate Garmin objects: the workout lives on the calendar, the course in Courses, and
the head unit starts them independently. Whether an Edge follows a course while running a workout was not checked
on a device here; the day card does not claim it, and a device test is part of the plan.

- A routed ride's route is an ordinary library route, so the existing library push already carries it to the
  rider's own accounts (Garmin and Wahoo); nothing new for a future day.
- **Today**: `pushWorkoutsForRider`, after syncing today's workout, also pushes that session's route
  (`wk.RouteSlug != ""`, not indoor) to the rider's own **Garmin** accounts with `applyPush` selected to
  `PlanKey{account, slug}`: idempotent on the route's content hash, nothing deleted. Garmin only, because the
  standing `AutoPushWorkouts` permission names Garmin; Wahoo gets the course through the library push or the
  manual button. A failure is a Warn and never fails the workout push.
- The manual "Send to Garmin" on the day card sends workout and course together (course outcome in the response, a
  failed course a Warn), and "Send to devices" there pushes the course to all the rider's own accounts, since the
  rider asked for that.
- No new push destination: coordinates leave only to the rider's own consented accounts, as for any route.

## Privacy

Routes are personal location data, and these start at the rider's saved start point.

- Generated loops are owned by, visible to and pushed to the rider only. Created through `Source.Create` with
  `Targets nil` (no auto-share), tagged `wroute:`, share and crew targeting refused until the tag is removed.
- The start is stored at about 110 m, opt-in, deletable, never returned. It and `ride_start_points` are in
  `riderTables` (`Purged: true`, rename on `rider`, unique), so removing or renaming a rider carries it, and
  `TestEveryRegisteredTableHasARenameRule` fails until they are. Generated routes are the rider's `Owner` rows and go
  with the existing route purge; `workouts.route_slug` goes with the workouts. The in-memory candidates are dropped
  for the rider in `purgeRiderSteps`.
- The only coordinates sent out are a start point and a distance to the configured routing engine, on the
  rider's own press of the button, the same as the route builder; no new third party. Geocoding is the existing
  rider-typed search.
- Logs carry rider, workout id, counts and outcomes; never coordinates, the place label, FTP, watts or weight.
  Responses carry coordinates only in a candidate returned to its owner (as `routebuilder/suggest` does) and
  never in the workout, schedule or start-point responses.

## Not configured and failing

- No routing engine: 412 on candidates with a **Warn** log, and the UI hides "Route for this ride" (the existing
  `routingConfigured`); feature 2 needs no engine and stays available.
- No start point: 409 `{code: "no_start_point"}`, an Info log; the panel shows the picker inline.
- Engine fails for every seed: 502, **Error** log (the request fails). Some seeds fail: Warn, the rest are used.
  Nothing within 15 %: 502, Error. Rate limited: 429. Candidate gone: 410.
- The routing quota and each call's failure use the builder's existing `recordRouteBuilderError`.

## Data

Idempotent add-column in `UseDB`, one new table:

- `workouts`: `route_slug TEXT NOT NULL DEFAULT ''`, `route_seconds DOUBLE PRECISION NOT NULL DEFAULT 0`.
- `ride_start_points(rider TEXT PRIMARY KEY, place TEXT NOT NULL, lat DOUBLE PRECISION NOT NULL, lon DOUBLE PRECISION NOT NULL, updated_at TEXT NOT NULL)`.

## API and UI

- `GET|PUT|DELETE /api/training/ride-start`; `POST /api/training/workouts/{id}/route-candidates`;
  `POST|DELETE /api/training/workouts/{id}/route`; `GET|POST /api/routes/{slug}/schedule`. Owner-only; the rider
  comes from the session; someone else's workout or route is a 404.
- `workoutDTO` gains `route {slug, name, distanceM, ascentM, estimatedSeconds, generated}`; no coordinates.
- Day card and slideover: "Route for this ride" opens `RouteForRideSlideover` (start picker if unset, three
  candidate cards with the route builder's inline SVG preview (not a mounted map per card), distance, ascent, estimated vs planned time, family note and "Use this route").
  With a route: name, distance, estimated time, a mismatch note, "Change", "Remove", and on today "Send to devices".
  Settings gets a "Where do your rides start?" card. `RouteDetailModal` and `RouteCard` get "Schedule ride" and
  `ScheduleRideModal` (date, what is on it, choices with the consequences in words).

## Testing

- `loops`: the suggest handler's existing tests pass unmoved; time objective: refinement length, 15 % filter, top-3
  diversity, all-fail and partial-fail.
- `routefit`: speed and estimated time for fixed profiles, no-FTP fallback, each family's fit at its boundaries,
  work-step and climb-coverage, steady-stretch finding.
- `ridestart` under `TestEachEngine`: opt-in by row, three-decimal rounding, label reduction, never read back by
  the API, purge and rename (registry tests).
- Candidates via `httptest` for the routing engine: request shape (profile, hilliness, length), 412 plus Warn, 409,
  410, 429, ownership 404, no coordinates in the workout DTO; save creates an owner-only untargeted tagged route,
  replace deletes only its own tagged route; share and crew targeting refused until the tag is removed.
- Scheduling: each row of the table; `adjust` in place keeps the id and goal and drops the generated prefix;
  `new`; 409 and 422; refresh, trim and replan leave a routed session; readiness still eases it; alternates hidden.
- Push: today's course goes to own Garmin only, idempotent, nothing deleted, a failure does not fail the workout
  push; a future day pushes nothing extra; route delete unlinks.
- Fixed clocks with an explicit zone; tests pass under UTC and Europe/Brussels. Synthetic fixtures only.

## Out of scope

Generating a week of routes at once, point-to-point or out-and-back routes, running routes, wind, traffic and
surface preferences, several loops per workout, re-generating when FTP changes, inferring the start from history,
deleting a generated route when its workout is deleted (the rider deletes it from the library), anything that
claims a head unit follows a course during a workout until a device confirms it.
