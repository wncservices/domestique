# Indoor trainer versions and weather-aware suggestions — design

Status: draft 2026-09-29. Builds on the workout library and scheduler, readiness
(`2026-09-28-readiness-design.md`) and its tomorrow forecast, and touches the in-flight FTP
tests spec (`2026-09-29-ftp-tests-design.md`), whose "Smart trainers" rules are kept.

## Why

The rider asked for two things: "make sure that exercises can be done on an indoor trainer"
(a Zwift sync comes later and is **not** started here) and, for the weather, "suggest doing the
training indoor when bad weather, and update the training when OK to an indoor session".

Today a session is written for the road. Generated sessions are already time-based with power
targets when FTP is known, but they carry the road's assumptions: a long ride is three or four
hours, an endurance step is a power *range*, and a hand-built workout may use distance or open
steps that a trainer cannot follow. Nothing says "this one is indoors", and nothing looks at the
sky.

How others do it (research 2026-09-29):

- **ERG mode** (TrainerRoad, Zwift, Garmin, Wahoo) makes the trainer hold a prescribed wattage
  by adjusting resistance, so it needs an absolute **power** target and a **time** duration
  (the trainer cannot know when a distance is done, and cannot hold a heart rate).
  [TrainerRoad: Smart Trainer Modes](https://support.trainerroad.com/hc/en-us/articles/360024069532-Smart-Trainer-Modes-Explained).
- **Garmin Edge and Wahoo ELEMNT** drive a paired smart trainer over ANT+ FE-C from a
  structured workout's power steps; Edge offers set target power, set resistance and set grade,
  ELEMNT controls Wahoo trainers directly. [Garmin Edge manual: indoor trainer](https://www8.garmin.com/manuals/webhelp/GUID-17DE938E-466A-4746-BDBF-7A6FC1B3A32C/EN-US/GUID-8826CB17-DD0D-40F9-89BB-D93C1E8534CF.html),
  [Garmin forum thread](https://forums.garmin.com/sports-fitness/cycling/f/edge-830/306116/erg-mode-in-a-trainer-workout-edge-830-and-wahoo-kickr-2022-v5).
  Forum reports say ELEMNT shows the target for each step where Edge shows a range; how Edge
  picks the ERG value from a range is not documented, hence the midpoint rule below.
- **Indoor duration.** TrainerRoad's own comparison shows the same 90-minute threshold session
  taking far longer outdoors, the extra time being easy aerobic riding
  ([TrainerRoad blog](https://www.trainerroad.com/blog/riding-indoors-vs-riding-outside-a-comparison/)).
  The common rule of thumb is that an hour indoors is worth 1.25 to 1.5 hours outdoors because
  there is no coasting, no junctions and no descents (coach Matt Rowe via
  [BikeRadar](https://www.bikeradar.com/advice/fitness-and-training/double-up-your-training-riding-indoors-and-out)).
  **This is a heuristic, not a standard**; the factor below is a constant, easy to retune.

## Indoor

### What an indoor version is

`workouts.indoor` (boolean, default false). Converting a workout **edits it in place**: same id,
date, goal, zone and level; new steps; `indoor = true`; a note appended to the description,
"Indoor version of Long ride (3h00), 2h15 on the trainer". In place, not a new workout, because a
copy would leave two sessions on one day, break the scheduler's "day is taken" logic and orphan
the Garmin copy; the push path already updates a changed workout under its remote id.

Conversion is **rider-initiated or rider-approved, never silent**: the manual "Indoor version"
action, or the weather banner's button. It is idempotent: an already-indoor workout returns
unchanged, so a double click or a retried request cannot shorten a ride twice. The note keeps
the original name and duration so the change is legible.

**Conversion is reversible.** Converting stores the workout's pre-conversion steps in
`workouts.outdoor_steps` (nullable JSON, the same encoding as `steps`). Only the steps change on
conversion (the name, date, zone and level do not), so steps are all that needs keeping.
**Converting twice keeps the first original**, because an already-indoor workout is a no-op and
never overwrites `outdoor_steps`. "Back to outdoor version" restores `outdoor_steps` into
`steps`, sets `indoor = false`, clears `outdoor_steps` (NULL) and appends a note ("Back to the
outdoor version"). Revert is idempotent too: a workout that is not indoor, or has no
`outdoor_steps`, returns unchanged with 200. Like conversion it is rider-initiated, adds no
marker, and is refused (409) for a ridden or past-date workout.

**Adaptation and `outdoor_steps`.** When adaptation replaces an indoor workout's content, the
replacement is first built as the usual *outdoor* session (the easy variant or the lower
rung) and then converted. **That outdoor replacement becomes the new `outdoor_steps`**, so
"Back to outdoor" after an automatic easing gives the eased outdoor session, not the
pre-easing one (the easing was decided on today's recovery and still applies outdoors). This is
simpler than disabling revert after an automatic change, needs no extra state, and `keepIndoor`
already has the outdoor replacement in hand before it converts it.

**Changed-once markers.** Conversion does **not** add `scheduler.AdjustedMarker`, which stays
the guard for *automatic* adaptation. So an indoor session is still eligible for readiness
easing (a wet day and bad sleep is exactly when to ease). What matters is that easing does not
turn it back into a road session: every place adaptation replaces a workout's content
(the step-down, the downgrade to an easy variant, and the tomorrow-apply path) re-runs the
indoor conversion on the replacement when the original was indoor. The result keeps
`indoor = true`. One automatic change per workout still holds, because that guarantee lives in
the marker and the marker is untouched.

### Conversion rules — `internal/indoor` (pure)

`indoor.Convert(w workout.Workout, p workout.RiderProfile) (Result, error)`, with
`Result{Steps []workout.WorkoutStep, Note string, ERG bool}`. Cycling only; a running workout
returns `ErrNotCycling` (treadmill is out of scope).

1. **Every step becomes time-based.** Distance steps use 28 km/h (7.8 m/s), a heuristic for a
   moderate road pace. Open steps: warmup and cooldown steps get the library's own lengths
   (10 minutes each, `workoutlib.WarmupCooldownSeconds`); other open-duration steps 5 minutes.
   Repeat blocks are converted recursively.
2. **Targets are power where FTP is known** so ERG can drive them.
   - A power step keeps its watts.
   - An HR step becomes power only when the workout's zone is known (endurance uses the
     55 to 75 % band `enduranceZoneTarget` already uses; a structured zone uses its ladder's
     `LowPct` and `HighPct` from `workoutlib.LadderFor`); warmup, cooldown and rest steps use the
     library's own 50 to 65 % and 45 to 55 %.
   - **No FTP (HR-only rider): HR targets stay.** ERG cannot hold a heart rate, so the note says
     so: "No FTP set, so the trainer cannot control resistance. Ride by heart rate." `ERG` is
     false.
   - Cadence steps and steps whose target stays `open` are left as they are.
3. **Power ranges collapse to their midpoint** (`TargetLow == TargetHigh`) for a rider with the
   smart-trainer preference, because ERG holds one number and how a range is resolved differs by
   device. Riders without the preference keep ranges (a power-meter rider on rollers wants the
   zone, not a lock).
4. **Endurance and long rides are shortened.** Applies to a workout that is not a hard session
   (`scheduler.IsHardSession` false; so endurance, long and easy rides). The `active` steps are
   scaled by **0.75** (an outdoor hour becomes 45 minutes, the middle of the 1.25 to 1.5
   heuristic, since 1 / 1.33 = 0.75), rounded to 5 minutes, **capped at 2 hours** of main work
   and floored at 30 minutes (never longer than the original). Warmup and cooldown are kept.
   A 4-hour long ride becomes about 2h20 in all. The note says the rest can be ridden outside on
   another day. Structured intervals (tempo through anaerobic) are **never** scaled: the work
   is the work, and TSS should not change. Recovery steps between intervals are kept.
5. **FTP tests are trainer-ready and untouched.** A test workout (`test_protocol` set once the
   FTP tests land) is flagged indoor **without changing its steps**: the ramp's steps are already
   absolute power for ERG, and the 20-minute and 2 x 8-minute all-out efforts stay `open`
   (resistance mode; ERG would lock the watts and measure the target, not the rider). No
   scaling, no midpoint collapse, no HR to power. This is the FTP spec's rule, enforced here.
6. **`Result.ERG`** is true when every non-open, non-warmup step has a power target; the UI uses
   it to say "Trainer control (ERG)" or "Ride by feel".

Until the FTP tests land, `isTest(w)` in `internal/indoor` returns false; that PR's stack wires
`w.TestProtocol != ""` (see the plan's restack note).

### Preference

`rider_profiles.smart_trainer` (boolean, default false), "I have a smart trainer", in the
profile form. It gates: the weather switch offer, the midpoint collapse and the "ERG" wording.
The manual "Indoor version" action is available to **every** cycling rider (time-based,
power-target steps help a power-meter rider on rollers too). "Indoor days" on the calendar is
not built: available days already exist and the weather does the deciding.

### Export and push

- `fitworkout.Options` gains `Indoor bool`; when set the workout message carries
  `sub_sport = indoor_cycling`, and the file name and Garmin/Wahoo display name gain a
  " (indoor)" suffix at export/push time via one helper. The workout's stored **name is not
  changed**: `scheduler.IsKeySession`'s legacy fallback matches the exact name ("Long ride").
- The Garmin JSON push (`garmin.buildWorkout`) needs no new field: power steps with time
  durations are what Edge and ELEMNT turn into ERG. The changed steps change the content hash, so
  the existing push path re-sends an already-pushed workout under its remote id.
- **Zwift seam.** The invariant "an indoor workout is time-based, power targets in watts, FTP on
  the profile" is exactly what a `.zwo` writer needs (Zwift's XML uses `Duration` in seconds and
  `Power` as a fraction of FTP: `watts / FTP`). A future `internal/zwo` is then a pure
  function of `[]workout.WorkoutStep` and FTP. **Nothing is built for it, and no field is
  reserved.**

### Plan page

- `workoutDTO` gains `indoor`. Week strip and day card show an "Indoor" badge with
  `i-lucide-house` (icon and word, never colour alone).
- Day card actions gain "Indoor version" (hidden when already indoor, ridden, past, or not
  cycling) with a confirm showing the result: new duration, "Trainer control (ERG)" or "Ride by
  feel", the shortening note. The result comes from `POST .../indoor?preview=1`, which converts
  nothing.
- The Indoor badge is also the way back: on the day card an indoor workout shows "Back to
  outdoor version" (hidden when ridden or past), with a confirm showing the restored duration.
  It calls `DELETE /api/training/workouts/{id}/indoor` (200 with the workout, idempotent, 409
  ridden or past, 404 another rider's). The weather banner does not reappear for the restored
  workout until the forecast is bad again and the rider has not dismissed it.

## Weather

### Source and terms

**Open-Meteo**, forecast API, no key. Request: `latitude`, `longitude` (rounded, see Privacy),
`hourly=precipitation_probability,precipitation,temperature_2m,wind_speed_10m,wind_gusts_10m,weather_code`,
`forecast_days=4`, `timezone=auto`, `wind_speed_unit=kmh`. The response's local ISO times are
kept as strings and matched by date and hour; the server's own zone is never involved.

Terms ([open-meteo.com/en/terms](https://open-meteo.com/en/terms), checked 2026-09-29): the free
API is for **non-commercial use only**, capped at 10,000 calls a day, 5,000 an hour and 600 a
minute, and requires CC-BY 4.0 **attribution**. Commercial use (an ad-supported or subscription
product) needs a paid plan; the terms do not address self-hosting (the software itself is open
source). Domestique is two riders' free software: non-commercial. Consequences:

- The popover shows "Weather data by Open-Meteo.com" with a link.
- Config `weather.base_url` (default `https://api.open-meteo.com`) lets an operator point at a
  self-hosted instance or the commercial endpoint; `OPEN_METEO_API_KEY`, if set, is sent as
  `apikey` and never stored or logged. `docs/` gets a note that a commercial deployment needs a
  subscription.
- Volume is far under the caps: results are cached per rounded location for an hour
  (Open-Meteo refreshes its models roughly hourly), so N riders in one town cost one call an
  hour.

Alternatives considered: **MET Norway Locationforecast** (free, CC-BY, no key, requires an
identifying User-Agent, but no precipitation *probability*, which the rain rule wants);
**Bright Sky** (DWD data, Germany-centred); **OpenWeatherMap** and **WeatherAPI** (key
required, free tiers restrictive for anything shared). Open-Meteo has the needed hourly
variables, no key and global coverage.

### Privacy

Coordinates are personal data (AGENTS.md: a route starts at somebody's front door), so:

- **Opt-in.** Nothing is sent to Open-Meteo for a rider who has not chosen a weather location.
  No location, no request; the GET reports `configured: false`.
- **A town, not a place.** The rider searches by name through the existing geocoder
  (`GET /api/geocode`) and picks a result. The server stores **lat and lon rounded to 2
  decimals (about 1.1 km)** plus the place name (truncated to 80 characters), and sends only the
  rounded pair. The name is what the UI shows; the coordinates are never returned to the
  browser.
- **Never route or GPX coordinates.** The weather package takes a `Location{Lat, Lon}` from the
  preferences store and imports neither `gpx`, `source` nor route models; a test asserts the
  outgoing query has only the listed parameters and coordinates with at most 2 decimals.
- No rider identifier is in the request; Open-Meteo sees the deployment's IP. The rounded
  location is owner-only in the API and deleted with "Stop using weather" (row removed).
- **Logging:** never coordinates or place names; never a rider name next to any weather value.
  Counts and error class only ("weather fetch failed", locations fetched: n).
- UI copy under the location field: "Domestique sends only this town's approximate location
  (rounded to about 1 km) to Open-Meteo to fetch the forecast. Your routes and rides are never
  shared. Remove it any time."
- The location is stored in plain text like a profile field (a town-level value); encrypting it
  with `DOMESTIQUE_ENCRYPTION_KEY` was considered and left out.

### Preferences

New table `weather_locations (rider TEXT PRIMARY KEY, place TEXT, lat DOUBLE PRECISION, lon
DOUBLE PRECISION, window_start INTEGER, window_end INTEGER, updated_at TEXT)`; idempotent
create in `UseDB`, all SQL through `dbx`. A separate table (not profile columns) because
saving the profile form has "confirmed value" semantics that do not fit, and deleting the row
is the opt-out. `window_start` and `window_end` are local hours, default **9 and 12**
(a common weekend-morning ride; rider-editable, "I usually ride between 07:00 and 09:00").

### Bad-weather rule — `internal/weather.Assess` (pure)

Judged over the **ride window**: `[window_start, max(window_end, window_start +
ceil(planned outdoor hours))` hours of the workout's date, so a 4-hour long ride is judged on
four hours, not a 3-hour slice; clipped to the day and at most 6 hours. For today, hours already
past (by the forecast's own local time) are skipped; none left means no suggestion.
Without a workout (the day chip) the configured window is used.

Bad if **any** of these hold; thresholds are constants in `weather/rules.go` with the reasoning
beside them, defaults for a road rider:

| Rule | Default | Why |
|---|---|---|
| Rain | window max probability >= 60 % **and** window total >= 0.2 mm; **or** any hour >= 1.0 mm | Probability alone flags dry-but-uncertain cloud; amount alone ignores likelihood; 1 mm an hour is steady rain (the UK Met Office calls 0.5 to 4 mm/h moderate). |
| Wind | sustained 10 m >= 30 km/h, or gusts >= 50 km/h | Beaufort 5 (fresh breeze, 29 to 38 km/h) is where a long ride is a grind; gusts of 50 km/h and up (Beaufort 7) are a handling risk. |
| Cold | temperature < 3 C at any window hour | Ice on wet roads and numb hands start around here. |
| Heat | temperature > 33 C at any window hour | Heat stress on a long effort; the trainer is not cool either, but a fan is. |
| Thunder | WMO weather code 95, 96 or 99 | Never ride out a storm. |
| Wintry | WMO codes 56, 57, 66, 67 (freezing drizzle or rain), 71 to 77, 85, 86 (snow) | Ice and snow. |

Each rule contributes a plain reason ("Rain likely, 80% and 3.1 mm between 9 and 12", "Gusts up
to 58 km/h", "Thunderstorms forecast"), in the table order. The per-day summary for the chip:
temperature range, max rain probability, max gust, worst reason code.

### When and how

- **Horizon:** today and the next 3 days (`forecast_days=4`), for the rider's **planned,
  unridden, cycling, non-indoor** workouts only.
- **Refresh:** on page load (`GET /api/training/weather`, served from the hourly in-memory cache)
  and warmed by the fixed-time metrics sync on `origin/claude/daily-garmin-sync` (morning and
  evening): after wellness, the sync fetches once per distinct rounded location among opted-in
  riders, so the first page load after it is a cache hit. If that branch is not merged when this
  ships, warming is skipped and page load fills the cache; nothing else depends on it.
- **Failure degrades to no suggestion.** A timeout (10 s), non-200 or malformed body logs Warn
  and the endpoint returns `configured: true, days: [], suggestions: []` with `unavailable:
  true`; the Plan page renders without weather. No stale data is served after an error.
- **Suggest, never silently change.** Nothing here writes a workout.

### What the rider sees

`GET /api/training/weather` (owner-only) returns `{ configured, unavailable?, place?, window:
{start, end}, days: [{date, bad, summary: {tempMin, tempMax, rainProb, gustMax}, reasons}],
suggestions: [{workoutId, date, reasons, canSwitch, altDate?}], attribution }`.

- **Smart-trainer rider:** a banner on the affected day card: "Rain likely Saturday (80%, 9 to
  12h). Ride indoors? [Switch to indoor version] [Keep outdoors]", the button calling
  `POST /api/training/workouts/{id}/indoor`. The confirm shows the shortened duration first.
  Keep outdoors hides it for that workout and forecast day (browser storage, a per-viewer
  convenience only; it reappears if the reasons change).
- **Rider without the preference:** informational only ("Rain likely Saturday..."), plus a
  "Move to Monday" button when the next 3 days hold a good-weather day with no workout on it
  (`altDate`), using the existing move action. `canSwitch` is false.
- **Chip:** the day card and week strip get a small chip when a day is bad
  (`i-lucide-cloud-rain`, `i-lucide-wind`, `i-lucide-snowflake`, `i-lucide-cloud-lightning` by
  the worst reason) with the summary in a popover and the Open-Meteo attribution. No forecast
  charts, no good-weather chip (YAGNI).

### Interaction with readiness

Independent and composable. A wet day plus poor sleep: readiness eases the session (its own
marker, once), the weather banner separately offers indoor, and converting after easing works
from the eased steps; converting before easing keeps `indoor` through the easing (rule above).
The weather banner never appears for an already-indoor workout, so a converted session is not
nagged.

## Data

Idempotent in `UseDB`, through `dbx`:

- `workouts`: `indoor <boolean> NOT NULL DEFAULT FALSE`, and `outdoor_steps TEXT` (nullable
  JSON, the original steps; NULL unless the workout is indoor).
- `rider_profiles`: `smart_trainer <boolean> NOT NULL DEFAULT FALSE`.
- New `weather_locations` as above.

## Observability

`weather.Client` follows the AGENTS.md checklist: `http.Client{Transport:
otelhttp.NewTransport(http.DefaultTransport), Timeout: 10s}`, every call
`NewRequestWithContext` with the request's ctx (the sync warm-up uses the sync's own ctx).
"Weather not configured/disabled" guards (`weather.enabled: false`, or no location for the
rider) log at **Warn** when a request hits them with the reason and no coordinates; a fetch
failure logs Warn (the request still succeeds, degraded); a completed conversion logs Info with
the workout id only. No new metric: per-request spans already cover outbound calls.

## Testing

- `indoor`: table tests per rule and boundary: distance to time, open steps (warmup, cooldown,
  other), nested repeat, HR to power for endurance and each structured zone, no FTP keeps HR and
  sets `ERG` false, midpoint only with the preference, shortening (0.75, 5-minute rounding, the
  30-minute floor, the 2-hour cap, 4-hour ride, never longer than original, warmup and cooldown
  kept), hard sessions never scaled, test workout untouched, running rejected, idempotent.
- `weather`: httptest server for Open-Meteo: exact query parameters and 2-decimal coordinates, no
  extra params, key sent as `apikey` only when configured, decode of a fixture, 500 and bad
  JSON return errors, timeout honoured, cache hit inside the hour and refetch after (fake clock).
  `Assess` table tests for every rule and boundary (59/60 %, 0.19/0.2 mm, 0.9/1.0 mm/h, 29/30
  and 49/50 km/h, 2.9/3 C, 33/33.1 C, each WMO code), window extension by duration, past hours
  today, empty window, reasons order.
- Storage (`weather_locations`, `indoor`, `smart_trainer`) under `TestEachEngine`; schema
  migration run twice.
- Adaptation: readiness easing and step-down on an indoor workout keep `indoor` and the
  time-based shape; conversion does not add the marker; convert-then-ease and ease-then-convert.
- API: owner-only 404s; convert 409 for ridden or past, 422 for running; preview changes
  nothing; suggestions only for opted-in riders and never send a request without a location;
  `canSwitch` follows the preference; `altDate` rules; unavailable degrades to 200.
- FIT: `Indoor` sets sub_sport on a round trip; Garmin push content hash changes on conversion.
- All clocks fixed (`Server.Clock`, and the weather client's), tests pass under `TZ=UTC` and
  `TZ=Europe/Brussels`; forecast times are strings, no test depends on today's date.

## Out of scope

Zwift export (only the seam above), a treadmill or run indoor version, "indoor days" scheduling, reverting automatically when the forecast clears (revert is a rider
click), automatic conversion, notifications
or push alerts, hourly charts, alternative providers, using Garmin's or Wahoo's own weather,
recording where a ride was done, and moving a session to a drier day automatically.
