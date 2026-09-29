# Indoor trainer versions and weather-aware suggestions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Any cycling session can be converted to a trainer-friendly indoor version, and when the forecast for a planned ride is bad, the rider is offered that conversion (or a move) for their OK.

**Architecture:** A pure `internal/indoor` conversion; an `indoor` flag on workouts and a `smart_trainer` profile preference, with adaptation preserving the flag; FIT and Garmin push carry it. A new outbound `internal/weather` client (Open-Meteo) with a pure `Assess`, an opt-in rounded-location table, a suggestions endpoint, and UI banners and chips.

**Tech Stack:** Go stdlib, `internal/dbx`, `otelhttp` (already a dependency); Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-29-indoor-and-weather-design.md` — rules, thresholds, defaults, request parameters, copy and icons are binding.

## Global Constraints

- Read `AGENTS.md` first (Security incl. GPX and coordinates as personal data, Observability incl. the outbound-client checklist and "not configured" guards, Conventions incl. the dependency budget, Tests).
- No new dependencies (stdlib `net/http`, `encoding/json`; `otelhttp` already in use). gofmt / go vet clean; `just check` green.
- Every test uses a fixed clock (`Server.Clock`, the weather client's injected clock) and explicit dates; forecast times stay strings. Time-dependent tests pass under `TZ=UTC` and `TZ=Europe/Brussels` (run both).
- Open-Meteo is only ever hit through `httptest` in tests, never the network.
- New SQL through `dbx`, covered by `TestEachEngine` (SQLite + PostgreSQL); schema added idempotently in `UseDB` and run twice in a test.
- Owner-only everywhere: the rider comes from the session, never the body; another rider's workout or weather is a 404.
- Weather location is opt-in. Only lat/lon rounded to 2 decimals and no other rider data is ever sent to Open-Meteo, and never route, GPX or ride coordinates. **Never log coordinates or place names, and never a rider name next to a weather or health value** (counts and error class only, Info and above).
- New outbound client: `otelhttp.NewTransport`, a 10 s timeout, `NewRequestWithContext` with the caller's ctx. A new "not configured" guard logs Warn with the reason. Failure degrades to no suggestion, never a failed request.
- Only generated, untouched workouts change automatically, each at most once (the `AdjustedMarker` guarantee is unchanged). Converting to indoor is rider-initiated, adds no marker, and adaptation preserves `indoor`.
- FTP test rules hold: the ramp stays ERG, 20-minute and 2 x 8 tests stay open (resistance) and are never scaled or retargeted. Nothing for Zwift is built.
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together. Frontend: Nuxt UI semantic tokens only, no hex, no `dark:`, typed inline template-handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back). Generated d.ts can hide CI errors, so test with them moved.
- Never `git stash`. Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push until told.

## Review Focus

1. **Conversion never changes the work.** Intervals are not scaled, TSS is unchanged, an FTP test's steps are byte-identical. (Task 1)
2. **HR-only rider** keeps HR targets and is told ERG is unavailable; no invented FTP. (Task 1)
3. **Idempotent conversion, reversible, and adaptation keeping `indoor`.** A second click shortens nothing and keeps the first `outdoor_steps`; revert restores them exactly; easing an indoor session yields an indoor session whose `outdoor_steps` is the eased outdoor form; no marker added by conversion or revert. (Task 2)
4. **Privacy.** No request without a saved location; the query has only the listed params and 2-decimal coordinates; nothing logged. (Tasks 5, 6)
5. **Degradation.** Open-Meteo down: 200 with `unavailable`, Plan page unaffected, no stale data. (Tasks 5, 7)
6. **Only opted-in smart-trainer riders get the switch;** others get info and an optional move. (Task 7)

## Stack

Indoor PRs first, then weather. Weather PRs do not depend on the UI of the indoor ones.

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1 conversion rules | `claude/indoor-1-convert` |
| 2 | 2 storage, endpoint, adaptation | `claude/indoor-2-apply` |
| 3 | 3 FIT and push | `claude/indoor-3-export` |
| 4 | 4 indoor UI | `claude/indoor-4-ui` |
| 5 | 5 weather client and rules | `claude/indoor-5-weather-client` |
| 6 | 6 location preferences and sync warm-up | `claude/indoor-6-weather-prefs` |
| 7 | 7 suggestions endpoint | `claude/indoor-7-weather-suggest` |
| 8 | 8 weather UI | `claude/indoor-8-weather-ui` |

Restack right after each squash merge (`git rebase --onto origin/main <old parent tip> <branch>`, retarget, `--force-with-lease`).

**FTP tests dependency.** `origin/claude/ftp-tests-plan` adds `workouts.test_protocol`. Task 1 ships `isTest(w) bool` returning false with a `// wired when FTP tests land` note and a test using a package-level hook; whichever of the two stacks merges second changes it to `w.TestProtocol != ""` and adds the test-workout case to the acceptance test. If the FTP tests merge first, do it in Task 1.

---

### Task 1: `internal/indoor` conversion

**Files:** create `apps/api/internal/indoor/{indoor.go,indoor_test.go}`.

**Produces:**

```go
var ErrNotCycling = errors.New("indoor: only cycling workouts have an indoor version")
type Result struct { Steps []workout.WorkoutStep; Note string; ERG bool; Changed bool }
func Convert(w workout.Workout, p workout.RiderProfile, smartTrainer bool) (Result, error)
// constants: DistanceSpeedMS = 7.8, OtherOpenSeconds = 300, EnduranceFactor = 0.75,
// MaxMainSeconds = 7200, MinMainSeconds = 1800, RoundSeconds = 300
```

- [ ] RED per spec rules 1 to 6: distance to time at 7.8 m/s; open warmup/cooldown 10 min and other open 5 min; nested repeat converted; HR to power for endurance (55 to 75 %) and each structured zone from `workoutlib.LadderFor`, warmup 50 to 65 %, cooldown 45 to 55 %; no FTP keeps HR and `ERG=false` with the "Ride by heart rate" note; cadence and open targets untouched; midpoint collapse only when `smartTrainer`; endurance/long scaled 0.75, rounded to 5 min, floor 30 min, cap 2 h, never longer than the original, warmup and cooldown kept, 4 h ride ends about 2h20; hard sessions (each structured zone and the legacy name fallback) never scaled; `isTest` workout returns identical steps with `Changed=false` on steps; running returns `ErrNotCycling`; converting the result again is a no-op.
- [ ] GREEN (imports `workout`, `workoutlib`, `scheduler` only, no store); `just check`; commit `"Convert a cycling workout to a trainer-friendly indoor version"`.

### Task 2: Storage, convert and revert endpoints, adaptation keeps `indoor`

**Files:** `workout/db.go` (schema, scan/insert, `Workout.Indoor`, `RiderProfile.SmartTrainer`, `Workout.OutdoorSteps *[]WorkoutStep`, `UpdateWorkoutRequest.Indoor *bool` and `OutdoorSteps` (a set-to-nil clear)), `api/training.go` (+ new `api/indoor.go`, test), `api/adaptation.go`, the tomorrow-apply path if merged (`applyChange`), `adapter/session.go` only if it builds replacements; `apps/web/src/api/{types.ts,client.ts}`.

- [ ] RED: idempotent `ALTER TABLE` for all three columns (incl. nullable `outdoor_steps`) run twice; `outdoor_steps` round trips nested repeat blocks and NULL; round trips under `TestEachEngine`; profile save keeps `smart_trainer`. `POST /api/training/workouts/{id}/indoor`: converts in place (same id, date, goal, zone, level), sets `indoor`, appends the note, does **not** add `AdjustedMarker`; second call 200, unchanged and still holding the **first** original in `outdoor_steps`; `?preview=1` returns the `Result` and changes nothing; 409 for a ridden or past-date workout, 422 for running, 404 for another rider's. `DELETE /api/training/workouts/{id}/indoor` restores the original steps exactly (deep equality, including nested repeats), clears `indoor` and `outdoor_steps`, appends the note, adds no marker; a second call 200 and unchanged; on a non-indoor workout 200 unchanged; 409 ridden or past, 404 another rider's; convert, revert, convert again round-trips; the Garmin copy re-pushes after revert (Task 3 test). Adaptation: readiness rest (downgrade), caution (step-down) and, when merged, the tomorrow apply on an `indoor` workout return an indoor workout (`indoor=true`, time-based steps, ERG shape); the same on a non-indoor workout is unchanged; ease-then-convert and convert-then-ease both end indoor with one marker; after an automatic easing of an indoor workout `outdoor_steps` equals the eased **outdoor** replacement and reverting yields that eased session, not the pre-easing one.
- [ ] GREEN: one helper `keepIndoor(req, wk, profile)` (stores the outdoor replacement's steps as the new `outdoor_steps`, then converts) called from every place a replacement is built (`applyStepDown`, the `Downgrade` branch, `applyChange` when present) so a new replacement path cannot forget it; Info log with the workout id only; `just check` + CI-equivalent typecheck; commit `"Store an indoor flag, make it reversible, and keep it through automatic easing"`.

### Task 3: FIT export and Garmin push

**Files:** `fitworkout/fitworkout.go` (+ test), `api/workoutpush.go` (+ test), the FIT download handler and `cmd/domestique` `fit-workout`.

- [ ] RED: `Options.Indoor` sets `sub_sport = indoor_cycling` on the workout message (round trip through the library; default unchanged); export and push use one `deviceName(wk)` helper adding " (indoor)"; the stored name is untouched; an already-pushed workout re-pushes after conversion and again after revert (content hash changed) under the same remote id; an unchanged indoor workout does not re-push. Verify `mesgdef.Workout` exposes `SubSport` (muktihari/fit v0.28.3 does).
- [ ] GREEN; `just check`; commit `"Mark indoor workouts in FIT export and on the head unit"`. Note in the PR that whether a device switches to trainer control is a real-device check (Edge and ELEMNT), like `domestique fit`.

### Task 4: Indoor UI

**Files:** create `apps/web/src/components/plan/IndoorBadge.vue`, `IndoorConvertModal.vue`; modify `TodayCard.vue`, `WeekStrip.vue`, `WorkoutSlideover.vue`, the profile form, `client.ts` (`revertIndoor`), `types.ts`, `client.ts`.

- [ ] Profile form: "I have a smart trainer" toggle (`smartTrainer`). Day card: "Indoor version" action (cycling, not indoor, not ridden, not past) opening `IndoorConvertModal` with the preview (new duration, "Trainer control (ERG)" or "Ride by feel", the shortening and HR-only notes) and Confirm. `IndoorBadge` (`i-lucide-house` + "Indoor") on the day card and week strip; tooltip with the conversion note. On an indoor, unridden, future workout the day card shows "Back to outdoor version" (confirm shows the restored duration) calling the revert endpoint, then reloads.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with a seeded cycling workout in light, dark and 375 px; typed handlers, tokens only; commit `"Offer an indoor version of a session from the plan"`.

### Task 5: `internal/weather` client and rules

**Files:** create `apps/api/internal/weather/{client.go,rules.go,client_test.go,rules_test.go}`; `config` gains `weather.enabled` (default true), `weather.base_url`.

**Produces:**

```go
type Location struct{ Lat, Lon float64 }              // callers pass already-rounded values; Client rounds again defensively
type Hour struct{ Date string; Hour int; Temp, Rain, RainProb, Wind, Gust float64; Code int }
type Forecast struct{ Hours []Hour; UTCOffsetSeconds int; FetchedAt time.Time }
type Client struct{ /* base URL, api key, otelhttp client, cache, clock */ }
func New(baseURL, apiKey string, clock func() time.Time) *Client
func (c *Client) Forecast(ctx context.Context, loc Location) (Forecast, error) // 4 days, cached 60 min per rounded location
type Window struct{ Date string; StartHour, EndHour int }
type Verdict struct{ Bad bool; Reasons []string; Codes []string; TempMin, TempMax, RainProbMax, GustMax float64 }
func Assess(f Forecast, w Window) Verdict
func WindowFor(start, end int, plannedSeconds float64) (endHour int)   // spec: max(end, start+ceil(hours)), capped at 6 h and the day
```

- [ ] RED: httptest server: exact path and query keys (`latitude`, `longitude`, `hourly=...`, `forecast_days=4`, `timezone=auto`, `wind_speed_unit=kmh`, `apikey` only when configured), coordinates never more than 2 decimals even when given 5, no other params; fixture decode into `Hour`s keeping local time strings; 500, malformed JSON and a slow server (timeout, ctx cancel) return errors; cache: second call inside 60 min makes no request, after 60 min (fake clock) refetches, an error is not cached, entries bounded (256). `Assess`: every rule and boundary from the spec table (59/60 %, 0.19/0.2 mm, 0.9/1.0 mm/h, 29/30 and 49/50 km/h, 2.9/3 C, 33/33.1 C, WMO 95/96/99, 56/57/66/67/71 to 77/85/86, and codes just outside), reasons text and order, window extension by duration, today skips past hours, an empty window is not bad.
- [ ] GREEN (`rules.go` holds each threshold as a named constant with its reasoning comment); guard "weather disabled" logs Warn at the call sites in Task 6; `just check`; commit `"Add an Open-Meteo forecast client and the bad-weather rules"`.

### Task 6: Location preferences, endpoints, sync warm-up

**Files:** create `apps/api/internal/weather/store.go` (+ test) or `workout/weather_db.go`, `api/weather.go`, `server.go` wiring, `api/metricssync.go` (warm-up), `docs/` note on Open-Meteo terms; `types.ts`, `client.ts`.

- [ ] RED: table under `TestEachEngine`, idempotent create run twice; `PUT /api/training/weather/location {place, lat, lon}` stores lat/lon rounded to 2 decimals and place truncated to 80 characters, validates ranges; `PUT .../window {start, end}` (0 to 23, start < end); `GET` never returns coordinates; `DELETE` removes the row and the next `GET` reports `configured: false`; owner-only; the rider from the session only. With no row, no Open-Meteo request is made (httptest counts zero). Warm-up: the sync fetches once per distinct rounded location among opted-in riders (two riders in one town, one request), a fetch failure logs Warn and the sync still succeeds, a rider with no location causes no request; skipped when the metrics-sync branch is not merged. Logs contain no coordinates, place names, or rider next to a value (assert on captured log output).
- [ ] GREEN: place search reuses the existing `GET /api/geocode` (no second geocoder); `weather.enabled: false` returns 412 and logs Warn with the reason; `just check` + CI-equivalent typecheck; commit `"Let a rider opt in to weather with a rounded town location"`.

### Task 7: Suggestions endpoint

**Files:** `api/weather.go` (+ test), `weather/suggest.go` (+ test), `types.ts`, `client.ts`.

**Produces:** `GET /api/training/weather` per the spec's shape; pure `Suggest(days []Day, workouts []workout.Workout, smartTrainer bool, availableDays []string, window, today) []Suggestion`.

- [ ] RED: candidates are planned, unridden, cycling, non-indoor workouts dated today to +3; the window per workout is `WindowFor(start, end, plannedSeconds)`; today past its window gives none; a good day gives none; a bad day gives a suggestion with reasons; `canSwitch` true only with `smartTrainer`; `altDate` is the first later forecast day that is good, has no workout and (when set) is an available day, only for riders without the preference; an FTP test workout gets a suggestion whose conversion leaves steps intact; another rider's workouts never appear; Open-Meteo error returns 200 with `configured: true, unavailable: true` and empty lists; not configured returns `configured: false` and makes no request; attribution string present; Warn log on unavailable with no coordinates. Fixed clock; passes under both zones.
- [ ] GREEN; `just check`; commit `"Suggest an indoor session or another day when the forecast is bad"`.

### Task 8: Weather UI

**Files:** create `apps/web/src/components/plan/{WeatherBanner.vue,WeatherChip.vue,WeatherSettings.vue}`, `apps/web/src/composables/useWeather.ts`; modify `TodayCard.vue`, `WeekStrip.vue`, `TrainingPlanPage.vue`, profile/settings surface, `types.ts`, `client.ts`.

- [ ] `WeatherSettings`: town search (existing geocode), pick a result, ride-window hours, the privacy copy from the spec verbatim, "Stop using weather". `WeatherBanner` on the affected day card: reasons, "Switch to indoor version" (opens `IndoorConvertModal` from Task 4 with the shortened duration, then the convert call and a reload) and "Keep outdoors" (per-viewer `localStorage`, try/catch, keyed by workout id plus reasons hash) for `canSwitch`; info-only with "Move to <day>" (existing move action) otherwise. `WeatherChip` (`i-lucide-cloud-rain`, `wind`, `snowflake`, `cloud-lightning` by worst reason; icon plus text) with the summary and the Open-Meteo attribution link in a `UPopover`, keyboard reachable. Weather is optional: `useWeather` failing or `unavailable` renders nothing.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser against a local `httptest`-style stub of Open-Meteo (`weather.base_url`), cases rain, wind, storm, dry, non-smart-trainer, unavailable; light, dark, 375 px; check the network panel shows only the stub with 2-decimal coordinates; commit `"Show weather warnings and the indoor switch on the plan"`.
