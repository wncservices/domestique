# Route-aware training and a race-day pacing plan — design

Status: draft 2026-09-29. Builds on the season plan (`seasonplan.go`), progression levels
(`2026-09-27-progression-levels-design.md`), FTP tests (`2026-09-29-ftp-tests-design.md`) and the race-day
projection (`2026-09-29-race-day-projection-design.md`, unmerged; its IF bands are reused, see "Target intensity").

## Why

A competitor analysis found nobody who trains for a *specific route's* climbs, or turns a real route library
into a pacing plan. Domestique has both halves and never joins them: "Train for this route" (#285) copies a
route's name, distance and ascent into a goal form and forgets the route, and the plan is built from the
event's date and duration alone. A rider preparing for a sportive with three 10-minute climbs gets the same
Build block as one preparing for a flat 100 km. On the day they have the route on the head unit and no
guidance on how hard to ride each part of it.

Two features, one spec, because they share climb extraction:

- **(a) Route-aware training.** Extract the goal route's sustained climbs, say how long each takes at the
  rider's target power, compare that with what the plan trains, and in Build and Peak pick workout rungs whose
  effort lengths match the climbs.
- **(b) A pacing plan.** Target watts (and HR as a fallback) per climb and segment, expected time from a simple
  physics model, shown on the goal and the route, exported as a FIT course pushed with the existing course push.

(a) ships first and stands alone. (b) reuses (a)'s climbs, physics and intensity.

## What exists

- `fitcourse.DeriveClimbs` already finds climbs (50 m elevation smoothing, 1.5 % "climbing" grade, 200 m merge
  gap, 500 m and 3 % minimum, Strava's length x gradient score) and `Options.ClimbCues` already writes a
  category course point at each start and a summit point. Tuned to Garmin ClimbPro's own thresholds, for
  device cues. Its distances and smoothing are private to `fitcourse`.
- `gpx.ParsePoints` gives points with elevation; routes without it are flagged by `gpx.NeedsElevation` and
  fixed by `POST /api/routes/{slug}/recalculate-elevation`. No new elevation source is needed.
- A goal stores **no route**. `usePlanGoals.startGoalFromRoute` reads `?goalFromRoute=<slug>` and pre-fills the
  form; nothing persists the slug.
- `rider_profiles` has FTP, LTHR, max HR, but **no weight**.
- `scheduler.WeekWorkouts` picks a rung per structured slot with `workoutlib.Pick(ladder, level, capSeconds)`;
  `refreshWeek` rebuilds only untouched generated sessions (`untouchedPlanSession`) when a week comes within
  reach. Both are the only places sessions are generated.
- The course push (`targets.Garmin`, `targets.Wahoo`) builds a FIT per library route, keyed on the route's
  content hash; `Courses.ImportCourse` / `WahooRoutes.CreateRoute` are the provider calls.

## Climb detection

New pure package `internal/climbs`: `Detect(points []gpx.Point, cfg Config) []Climb`. The algorithm moves out
of `fitcourse` unchanged; `fitcourse.DeriveClimbs` becomes a wrapper calling it with its current config, and a
test pins its output on synthetic tracks before and after, so device cues do not move.

Two configs over one algorithm:

| | `DeviceConfig` (fitcourse, as today) | `TrainingConfig` (this feature) |
|---|---|---|
| Minimum length | 500 m | **1000 m** |
| Minimum average gradient | 3 % | **3 %** |
| Smoothing radius | 50 m | 50 m |
| Point counts as climbing at | 1.5 % | 1.5 % |
| Merge gap (a dip or flat that may sit inside one climb) | 200 m | 200 m |

Rules, in order: smooth elevation over 50 m; mark points climbing at 1.5 % or more; grow a climb until the
last climbing point is more than 200 m behind, so a short dip, false flat or hairpin does not split it; end the
climb at its **highest** smoothed point, so a trailing descent is not counted; keep it if length and average
gradient clear the config. Every point must carry elevation, else nothing is returned (a climb from half-real
elevation is worse than none). Category is Strava's length x gradient score (>= 8 000 is category 4, upward as
`fitcourse` has it); the score is also the rank for "biggest climbs". Strava rates only climbs of at least
500 m and 3 %; Garmin ClimbPro shows climbs from 500 m and 3 % (per Garmin's documentation and forum threads,
not verified on a device here). The 1 km bar for training is ours: a 700 m ramp is a sprint, not something a
rung of intervals rehearses.

`Climb` gains what neither consumer had: `StartM`, `EndM` (distance along the track), `Index`. It carries **no
coordinates**; a consumer needing a point looks it up from the index.

## Physics model

New pure package `internal/pacing`, file `physics.go`. Steady-state power balance per segment, the model Best
Bike Split, Kreuzotter and every "watts to speed" calculator share:

`P x eta = (m g (sin t + Crr cos t) + 0.5 rho CdA v^2) v`, solved for `v` by bisection (monotonic in `v`).

Defaults, all overridable per call, none per rider yet:

| Input | Default | Note |
|---|---|---|
| Rider mass | `rider_profiles.weight_kg`, else **75 kg** flagged as assumed | new profile field |
| Bike and kit | 8 kg | added to the rider |
| CdA | 0.32 m^2 | road bike, hands on the hoods; drops 0.28, upright 0.38 |
| Crr | 0.005 | decent road tyre on good asphalt; rougher roads read slow |
| Air density | 1.20 kg/m^3 | about 20 C at low altitude |
| Drivetrain efficiency | 0.975 | |
| Wind | none | no weather in this version |
| Descents | speed capped at 60 km/h | braking and corners; a real descent is not free-running |

Segments: the smoothed profile cut into pieces of at least 100 m and at most 500 m, constant grade each.
Sample: a 6 % climb at 250 W for 83 kg comes out near 15 km/h, so 1.9 km takes about 7.5 minutes. This is a
planning aid; it will be off by several per cent on a real day (wind, drafting, road surface) and the UI says
"estimated" wherever a time appears. It never sees a coordinate, only a list of (length, grade).

## Target intensity

Race average intensity comes from the event's duration, the same three bands the race-day projection uses so
the two features agree: IF **0.95** under 2 h, **0.85** for 2 to 4 h, **0.75** over 4 h. `pacing.EventIF(hours)`
is the single implementation (if the projection feature has merged, it imports this or the reverse; one copy
either way). Duration is unknown before pacing, so: pass 1 at IF 0.85, take the band of that duration, pass 2
at the band's IF, report pass 2. The band is not re-evaluated after pass 2 (no flip-flop at a boundary).
The rider may override on the goal: `goals.pacing_if` (0 = derived), 0.60 to 1.05, shown as a slider with the
derived value as its default. Target NP is `FTP x IF`.

## Climbs vs the plan (feature a)

`GET /api/training/goals/{id}/route-demands` (owner-only, the rider from the session, 404 for anyone else's
goal). Response:

```
{ available, reason?, route: {slug, name}, assumptions[], climbs: [{index, startM, endM, lengthM, gainM,
  avgGradient, category, durationSec, watts, pctFtp, kind}], coverage: {longestSustainedSec, uncovered, message},
  bias: {active, phase} }
```

- `available` is false with a `reason` for: no route on the goal, route no longer visible or deleted, route has no
  elevation (with the recalculate hint), not cycling, no FTP.
- `durationSec` and `watts`: the climb ridden at `FTP x IF x factor(duration)` through the physics model, where
  the climb factor is 1.10 under 5 min, 1.05 for 5 to 20 min, 1.00 beyond (see the pacing plan; one function).
- `kind` by duration: `short` under 4 min, `medium` 4 to 8, `sustained` 8 to 20, `long` over 20.
- **Coverage.** The plan's longest *sustained effort* is the longest single work step, at sweet-spot intensity
  or above, in a planned or completed workout of this goal from today to the event (planned steps come from
  `workout.Steps`; sweet-spot-or-above is the workout's zone in `sweet_spot`, `threshold`, `vo2max`,
  `anaerobic`). A climb is **covered** when that length is at least 80 % of the climb's duration. `message`
  is generated: "Your route has 3 climbs over 8 minutes; your plan's longest threshold effort is 6 minutes."
  when some are uncovered, "Your plan trains for your route's climbs." when all are, nothing when no climbs.
  Phrased against the goal's *current* plan, so it changes as the plan bias below takes hold.

## Biasing the plan (feature a)

Only in **Build and Peak** weeks, never recovery weeks, taper, or Base. Only **structured slots** and the long
day, and only when generating: `scheduler.WeekWorkouts` gains a variadic option `WithRouteDemand(d)`. Both
callers that generate sessions (`fillWeek`, `refreshWeek`) get it from `seasonContext`, which computes the
demand once per goal. `refreshWeek` already rebuilds only `untouchedPlanSession`s dated after today; the bias
adds nothing to that gate, so **rider-touched, moved, adjusted, ridden and test sessions are never rewritten**,
and a week already filled is not rebuilt merely because a route was linked (it changes when it next refreshes).

The demand reduces to a wanted effort length per zone:

| Zone (slot) | Wanted work length | Source |
|---|---|---|
| `threshold`, `sweet_spot` | the longest climb of 4 to 30 min, capped at 20 min | sustained and medium climbs |
| `vo2max` | median duration of climbs under 6 min | short climbs |
| `anaerobic` | duration of the steepest climb under 2 min | punchy ramps |

No qualifying climb gives no wanted length and the slot is generated exactly as today. With one, a new
`workoutlib.PickNear(ladder, level, capSeconds, wantWork)` chooses among the rungs one level either side of
what `Pick` returns that fit the cap, the one whose `WorkSeconds` is closest to the wanted length, ties to
`Pick`'s own. So the progression level still decides difficulty (never more than one rung above it) and
the route only chooses *which shape* of that difficulty, "3 x 12 min" rather than "4 x 8 min".

**Zones are not changed.** The phase's zone mix belongs to periodization and each zone has its own
progression level; swapping a rider's Build vo2max slot for a threshold one on the strength of a route would
move a level they have not earned. A route dominated by short punchy climbs therefore trains them in the
existing vo2max and anaerobic slots.

**Climbing long ride.** When the route ascends at 10 m/km or more, or at least 1 500 m in total, the long
endurance ride in a Build or Peak week is named "Long ride, with climbing" instead of the plain name. Its
steps and hours are unchanged; the name tells the rider to choose a hilly route, since the app cannot
schedule a route. Name is part of `sameContent`, so a refresh applies it once.

## Pacing plan (feature b)

Pure `pacing.Build(in Input) Plan`. Input: the route's smoothed segments, FTP, IF, physics inputs, `LTHR` /
max HR for the fallback. Cycling routes with elevation and an FTP only; otherwise unavailable with a reason.

**Gradient rule.** Relative to a flat-level power `Pf`:

| Segment | Target |
|---|---|
| Climb under 5 min (from `climbs.Detect` at `DeviceConfig`, so short ramps are covered too) | `Pf x 1.10` |
| Climb 5 to 20 min | `Pf x 1.05` |
| Climb over 20 min | `Pf x 1.00` |
| Flat and rolling (not inside a climb, grade above -3 %) | `Pf` |
| Descent steeper than -3 % | `0.55 x FTP` ("soft pedal, recover") |

Climb watts are also capped at `FTP x` the same factor (1.10, 1.05, 1.00), so a low `Pf` never pushes a long
climb over threshold. `Pf` is solved by bisection so the plan's normalised power (30 s rolling, fourth-power
mean, over the physics timeline) equals `FTP x IF`. That is the honest meaning of a variable plan: the *same*
NP a rider would hold on a flat course, spent where it buys time. Constant power is the wrong answer on a
hilly course for a physical reason (speed is low on a climb, so an extra watt buys more seconds there than on
a flat, and coasting a descent loses almost nothing), and the 105 to 110 % figure is the practitioner's cap:
Roadman's hill guidance caps power at about 10 to 12 % over target on climbs beyond two minutes, Friel and
Coggan both stress a low variability index. Best Bike Split does the same with a full model and weather.

Per segment output: `startM`, `endM`, `kind` (`climb`, `flat`, `descent`), `gradient`, `wattsLow` / `wattsHigh`
(target -3 % / +3 %, rounded to 5 W), `hrLow` / `hrHigh`, `speedKph`, `seconds`. Plan totals: `seconds`,
`normalizedWatts`, `if`, `avgWatts`, `avgSpeedKph`, `variabilityIndex`. Adjacent segments of one kind merge
so a rider reads 6 to 12 lines, not 200.

**HR fallback.** For every segment, the zone whose percent-of-FTP range contains the target, through
`workoutlib.HRRange` (LTHR when known, else max HR), so the rider without a power meter still has a number.
A rider with neither gets watts only, and vice versa: no FTP means no plan (HR alone cannot drive the model).
The HR range is labelled "steady-state; HR lags the first minutes of a climb".

**Weight.** New `rider_profiles.weight_kg REAL NOT NULL DEFAULT 0` (0 = unset). Typed by the rider on the
profile form, never auto-estimated (no provider read; no guess worth showing). Unset uses 75 kg and the plan
says so: "Assumed 75 kg; add your weight for a better time".

`GET /api/routes/{slug}/pacing?goal=<id>` returns the plan for the signed-in rider with their profile and,
when `goal` is given (and is theirs), that goal's IF override and event date. Route visibility is
`config.VisibleTo`; the rider from the session; 404 otherwise. **The response carries distances, never
coordinates.** Assumptions travel with it (`massKg`, `massAssumed`, `cdA`, `crr`, `if`, `ifSource`
`derived|goal`, `wind: none`).

## Export as a FIT course

What a FIT course can carry to a device, as far as could be established:

- A `course` file has records (position, altitude, distance), a lap, and `course_point` messages. The
  `course_point` message in `muktihari/fit` v0.28.3 has **`Name`, `Type`, `Distance`, position, timestamp and
  `Favorite`. No notes or description field.** Notes exist in GPX and TCX, not FIT.
- Garmin Edge shows a course point's **name** as the on-map label and pop-up when the rider reaches it. Forum
  reports: older Edges display 10 characters, newer ones such as the 1040 about 15. So a name must be short
  and ASCII; the sentence "Climb 3: 6.2 km @ 5.1%, hold 250-265 W" **cannot** be carried. That text lives in the
  UI.
- FIT has no per-segment power or HR target in a *course*. Targets belong to a **workout**; Garmin's own
  pacing features (PacePro for running, ClimbPro for climbs) are computed on the device from the course's
  elevation, not read from a file we could write. A course plus a workout cannot be linked in one file, and
  a workout with per-climb steps has no idea where the rider is on the road.
- Whether Wahoo ELEMNT displays course point names from an imported FIT route is **not established** here; the
  Wahoo adapter today writes no course points at all. Verify on a device before promising it.

So the export is a **separate FIT course** named "<route> pacing" (device names truncate; the name is short),
containing the route track and, per climb, two course points:

- at the climb start, `Type` = the category value (`fitcourse` already maps score to category) and `Name` =
  `C3 250-265W` (`C` + climb number + target range; `C3 148-156bpm` for HR only), at most 15 characters;
- at the summit, `Type` = `Summit`, `Name` = `Top C3`.

Sized by construction: names are built by `pacing.CueName`, which truncates to 15 ASCII characters and
replaces an en dash with a hyphen (the one place this could drift). `fitcourse.Options` gains a generic
`CoursePoints []CoursePoint{DistanceM float64, Type typedef.CoursePoint, Name string}`; the encoder maps each
distance to the nearest track point for position and timestamp. It is generic on purpose: pacing is the first
user but not the only shape a caller could want.

**Push.** A separate course, not a change to the library route's course: the library push is keyed on route
content and shared by everyone; a pacing course is one rider's, built from their FTP and a goal. Explicit
action, never automatic: `POST /api/routes/{slug}/pacing/push` body `{provider, goalId?}`, owner-only, to the
rider's **own** linked account (rider from the session), through `Courses.ImportCourse` (Garmin) and
`WahooRoutes.CreateRoute` / `UpdateRoute` (Wahoo, `external_id` = `pacing:<goalId or slug>`). A small table
records `(rider, provider, key) -> remote id`, so a second push deletes the previous Garmin copy or updates the
Wahoo one instead of accumulating courses. `GET .../pacing.fit` downloads the same file. 412 with a log line
when no account is linked. The Garmin/Wahoo push is the one place route coordinates already leave the app with
the rider's consent; this adds no destination.

## Privacy

Route geometry is personal location data (`AGENTS.md`: a route usually starts at somebody's front door).

- The climbs, demands and pacing endpoints return distances, elevations, watts, times: no latitude or longitude.
  `climbs.Climb` and `pacing.Plan` hold none.
- Coordinates leave the app only inside the pacing FIT, to the rider's own consented Garmin or Wahoo
  account, exactly as the library push does. No third-party calls: the physics is local, no weather, no
  elevation service (the existing recalculate-elevation call is unchanged and not made by this feature).
- Linking a goal to a route requires the route to be `config.VisibleTo` the rider; a goal cannot expose a
  route someone else does not share. If the route later becomes invisible or is deleted, demands and pacing
  answer `available: false`, and nothing about the route is returned.
- Logs carry rider, goal id, route slug and outcome only; never watts, FTP, weight, HR or any coordinate beside
  a rider name.
- Tests use routes generated in code; no real GPX is committed.

## Data

Idempotent add-column in `UseDB`, one small table, all through `dbx`:

- `goals`: `route_slug TEXT NOT NULL DEFAULT ''`, `pacing_if DOUBLE PRECISION NOT NULL DEFAULT 0`.
- `rider_profiles`: `weight_kg DOUBLE PRECISION NOT NULL DEFAULT 0`.
- `pacing_pushes (rider, provider, key, remote_id, pushed_at, PRIMARY KEY (rider, provider, key))`.

`goalDTO`, `createGoalRequest`, the profile DTO and `types.ts` mirror by hand. Deleting a rider deletes their
pacing pushes (`riderdelete.go`).

## UI

- **GoalSlideover** gains a route picker (visible cycling routes, "None") and the "Train for this route"
  shortcut now passes the slug through, so the link is made when the goal is saved. Also a "Pacing intensity"
  slider showing the derived IF, edited only on demand.
- **Route demands card** on the goal (Plan page, beside the season timeline): a climbs table (start km, length,
  gradient, category, time at target, watts) with a covered/not covered mark per row, the coverage message, and a
  note when the plan is being biased ("Build and Peak sessions favour 12-minute efforts"). Unavailable reasons
  are shown with their fix ("Add your FTP", "Recalculate this route's elevation").
- **Pacing card** on the goal and in `RouteDetailModal`: the merged segment table, expected time, NP and average
  speed, the assumptions behind a disclosure, "Export FIT" and "Send to Garmin / Wahoo" (only for linked
  accounts). `ElevationProfile.vue` shades climbs and labels them C1..Cn from the response's `startM`/`endM`.
- **Profile form** gains Weight (kg), with the assumed-75-kg hint.

## Testing

- `climbs`: a hand-built profile (ramp, false flat inside 200 m, gap over 200 m, trailing descent, 900 m
  vs 1 100 m climb, 2.9 % vs 3.1 %), no-elevation returns nothing, both configs; `fitcourse.DeriveClimbs`
  output identical to before on the same synthetic tracks.
- `pacing`: physics against a hand-computed flat and climb case (speed within 0.5 %), monotonic in power,
  descent cap; `EventIF` band edges; NP of the plan equals `FTP x IF` within 0.5 %; climb caps; kinds merge;
  HR fallback; unavailable reasons; `CueName` never exceeds 15 characters or emits non-ASCII.
- `workoutlib.PickNear`: never more than one rung above `Pick`, respects the cap, ties to `Pick`, no wanted
  length is `Pick`.
- Scheduler: Build and Peak only; recovery, taper and Base identical to today; long-ride name; a touched
  session is not changed by `refreshWeek`; a second pass changes nothing (idempotent).
- Storage under `TestEachEngine`; an existing database gains the columns; goal route validation (invisible route
  is rejected), owner-only 404s for demands, pacing, push; no coordinate appears in any response body (a test
  scans the JSON for the fixture's latitude and longitude); the push records and replaces its remote id;
  the FIT round-trips through `fitcourse.Decode` and its course points carry the expected names.
- Every clock is fixed with an explicit zone; tests pass under UTC and Europe/Brussels.

## Out of scope

Weather and wind, drafting, per-rider CdA or Crr, altitude and heat, multi-route or multi-day events,
changing zones or the phase mix for a route, adjusting weekly hours, nutrition and fuelling plans, a
device-side workout with per-climb targets, live guidance, importing pacing back, running and swimming,
automatic push of a pacing course, HR-only plans without FTP, Strava segment lookups (a third-party call).
