# Workout export and ride-history import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Download a workout as `.zwo`, `.mrc` or `.erg`, and import a rider's past rides from a Strava or Garmin export (or loose FIT files) into fitness history and threshold detection, without ever storing the uploaded files.

**Architecture:** Pure encoders in `internal/workoutexport`, fed by `indoor.Convert`; a sibling export endpoint plus a week zip; a pure `internal/rideimport` reader with hard limits; FIT-to-session through the existing `rideanalysis`; one small `ride_imports` table for job status; a background goroutine; a Fitness page card.

**Tech Stack:** Go stdlib (`encoding/xml`, `archive/zip`, `compress/gzip`), `internal/dbx`, the existing `muktihari/fit`; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-29-export-and-import-design.md` — mappings, limits, dedupe rules and copy are binding.

## Global Constraints

- Read `AGENTS.md` first. **No new dependencies** (the dependency budget): everything above is stdlib or already in `go.mod`. gofmt, go vet, `cd apps/api && golangci-lint run ./...` (0 issues) and `just check` green, run under `TZ=UTC` (seven scheduling tests are timezone-sensitive under Europe/Brussels; pre-existing, don't touch).
- Every new test uses a fixed clock with an explicit zone and passes under both `TZ=UTC` and `TZ=Europe/Brussels`.
- New SQL through `dbx`, `TestEachEngine` (SQLite + PostgreSQL), idempotent schema in `UseDB` (applies twice). Owner-only everywhere; the rider comes from the session, never the form.
- **Raw uploaded files are never persisted**: the only disk use is a 0600 temp spool removed on every path, size and zip-bomb limits from the spec enforced by counting bytes read, not by trusting headers. Entry names are never used as paths and never logged.
- No health values (watts, HR, FTP) and no file names in log lines next to a rider name: log rider, job or workout id, format, counts, outcome.
- **Synthetic test fixtures only**: build zips and FIT files in test code; no real ride, route or export ever enters the repo (AGENTS.md: rides are personal location data).
- Any new outbound HTTP client (none planned; the intervals.icu API is v2) would need the checklist in AGENTS.md's Observability section. Any new "not configured" guard logs at the right level.
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together. Frontend: Nuxt UI semantic tokens only, typed inline template handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back).
- Never `git stash` (shared across worktrees). Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **Only time-and-power exports.** HR/pace after `indoor.Convert` is refused, never guessed; an open step is `FreeRide` in `.zwo` and a refusal in `.mrc`/`.erg`. (Tasks 1-3)
2. **Repeats become `IntervalsT` only for exactly two time-and-power children**; otherwise unrolled, with identical total time. (Task 1)
3. **`.zwo` `PowerLow`/`PowerHigh` are start/end** (a cooldown runs high to low), checked in real Zwift before merge. (Task 1)
4. **ERG needs no FTP; ZWO and MRC do** (409). (Tasks 1-3)
5. **A hostile zip cannot exhaust memory or disk**: size, total, count and ratio caps hold against a bomb built in the test. (Task 5)
6. **No raw bytes reach the database or a log line.** (Tasks 5-7)
7. **Import is idempotent and dedupes against provider sessions** on local date +-1 day, duration and power tolerance; a re-upload adds nothing. (Task 6)
8. **Imports never move progression levels or match plans**; analyses exist for every imported ride, not only the last 42 days. (Task 6)

## Stack

Export PRs first, each independently useful.

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1-2 encoders (zwo, mrc, erg) | `claude/io-1-encoders` |
| 2 | 3 export endpoint and week zip | `claude/io-2-export-endpoint` |
| 3 | 4 export UI | `claude/io-3-export-ui` |
| 4 | 5-6 import reader, limits, session pipeline | `claude/io-4-import-core` |
| 5 | 7 job table, upload and status endpoints | `claude/io-5-import-api` |
| 6 | 8 import UI | `claude/io-6-import-ui` |

Restack right after each squash merge.

---

### Task 1: Timeline and `.zwo`

**Files:** create `apps/api/internal/workoutexport/{flatten.go,zwo.go}` (+ tests, `testdata/*.zwo` goldens).

**Produces:**

```go
type Meta struct{ Name, Description string }
func Zwo(steps []workout.WorkoutStep, ftp float64, m Meta) ([]byte, error)
var ErrHRTarget, ErrNoFTP error; type OpenStepError struct{ Format string }
```

- [ ] RED: goldens for an over-under workout (a two-child repeat -> `IntervalsT`), a sweet-spot block with warmup/cooldown ranges (`Warmup`/`Cooldown`, start/end power), a three-child repeat and a nested repeat (unrolled, same total seconds), an open step (`FreeRide`), an FTP ramp test (consecutive `SteadyState`), range midpoint, step name -> `textevent` (none inside `IntervalsT`), XML escaping of `& < " '` and non-ASCII names, HR/pace step -> `ErrHRTarget`, ftp <= 0 -> `ErrNoFTP`, power fractions to 3 decimals, whole seconds.
- [ ] GREEN; `just check`; commit `"Add the workout timeline and a .zwo encoder"`.
- [ ] Manual gate, recorded in the PR body: load one exported file in real Zwift and confirm the warmup/cooldown direction and the `IntervalsT` block. If the direction is wrong, flip the goldens, not the reading of the spec.

### Task 2: `.mrc` and `.erg`

**Files:** `apps/api/internal/workoutexport/{course.go,mrc.go,erg.go}` (+ tests, goldens).

- [ ] RED: goldens for header, `[COURSE DATA]` points and CRLF for both formats; a step is two points at one value, a ramp two different values, a step boundary repeats the minute; minutes are cumulative-seconds/60 to two decimals (a 10 s step does not drift over a 90 min workout); `.mrc` is watts/ftp*100 to one decimal; `.erg` whole watts; repeats always unrolled; any open step -> `OpenStepError`; `.mrc` with ftp <= 0 -> `ErrNoFTP`, `.erg` without FTP succeeds and omits `FTP =`.
- [ ] GREEN; `just check`; commit `"Add .mrc and .erg encoders"`.

### Task 3: Export endpoint and week zip

**Files:** create `apps/api/internal/api/workoutexport.go` (+ test); `server.go` routes; `apps/web/src/api/client.ts` (`workoutExportUrl`, `weekExportUrl`); acceptance case in `acceptance_test.go`.

- [ ] RED: `GET /api/training/workouts/{id}/export?format=zwo|mrc|erg` converts with `indoor.Convert` (the workout's own steps when `Indoor`), puts the converter's note in the description, 200 with `Content-Disposition` and the right type; 400 unknown or missing format; 404 unknown id; 403 for another rider's workout; 422 for a running workout, HR/pace after conversion, and an open step on mrc/erg (each with the spec's message); 409 for no FTP on zwo/mrc; the existing FIT route is byte-identical to before. `GET /api/training/weeks/{monday}/export?format=` returns a zip with one `<date>-<slug>.<ext>` per exportable workout and `SKIPPED.txt` naming the rest and why; 404 for an empty week; only the caller's own workouts. Log line carries rider, id, format, outcome and no watts.
- [ ] GREEN; `just check`; commit `"Serve a workout as .zwo, .mrc or .erg, and a week as a zip"`.

### Task 4: Export UI

**Files:** modify `apps/web/src/components/plan/TodayCard.vue`, `GoalsSection.vue`, the week download menu; create `apps/web/src/components/plan/ExportMenu.vue`.

- [ ] Replace the single download button with `ExportMenu` (`UDropdownMenu`): FIT (Garmin/Wahoo), Zwift (.zwo), TrainerRoad and others (.mrc), .erg; a refused export shows the server's message as a toast (fetch, then save the blob; no pre-judging in the menu). Week menu gains "Export this week (zip)". Typed template handler params, semantic tokens only.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with a seeded plan (rows removed afterwards): a power workout downloads all three, an FTP 20-minute test shows the `.mrc` refusal toast, light/dark, 375px; commit `"Offer Zwift, TrainerRoad and ERG downloads on the day card and week menu"`.

### Task 5: Import reader and limits

**Files:** create `apps/api/internal/rideimport/{read.go,limits.go}` (+ tests).

**Produces:**

```go
type Limits struct{ MaxEntryBytes, MaxTotalBytes int64; MaxEntries, MaxDepth int; MaxRatio int }
type Report struct{ Files, Unsupported, Oversize int; Err error }
func Read(spool *os.File, size int64, l Limits, emit func(fit []byte) error) Report
var DefaultLimits Limits // 64 MiB, 6 GiB, 20000, depth 2, 200x
```

- [ ] RED (all fixtures built in the test): Strava-shaped zip (`activities/*.fit.gz`, one `.fit`, a `.gpx`, a `.tcx`, `activities.csv`) emits the FIT bytes and counts gpx/tcx as `Unsupported`; a Garmin-shaped nested zip is read at depth 2 and a third level is ignored; a loose `.fit` and `.fit.gz` (not a zip) work; type is confirmed by the FIT header magic, a mislabelled entry is skipped; a bomb entry (megabytes of zeros in a few KiB) aborts by the ratio cap without allocating past the cap; an entry over `MaxEntryBytes` is `Oversize` and the job goes on (counting actual bytes, with a header that lies about its size); total, entry-count and depth caps stop the read with a typed error; entry names never appear in the returned `Report` or in any log call; an encrypted entry is skipped; memory stays bounded (one entry at a time, `io.LimitReader`).
- [ ] GREEN; `just check`; commit `"Read a ride export zip safely, one bounded entry at a time"`.

### Task 6: FIT to session, and dedupe

**Files:** create `apps/api/internal/rideimport/{ride.go,dedupe.go}` (+ tests); move `decodeFIT` from `apps/api/internal/api/rideanalysis.go` to `internal/rideanalysis` (export it; the api package calls it); `apps/api/internal/workout/` gains `FindSimilarSession(ctx, rider, sport, dates []string) ([]CompletedSession, error)` (+ `TestEachEngine` case).

**Produces:**

```go
type Ride struct{ Sport, Date, StartUTC string; Duration, Distance, AvgPower float64; AvgHR int; Analysis rideanalysis.Analysis }
func Parse(fit []byte, p workout.RiderProfile) (Ride, error)
func Duplicate(r Ride, candidates []workout.CompletedSession) bool
```

- [ ] RED: `Parse` reads sport (cycling, virtual and e-bike -> cycling; running; swim -> `ErrSport`), UTC start, local date from `local_timestamp - timestamp` (a 23:30 local ride lands on its own day under UTC and Europe/Brussels), duration, distance, average power/HR, and runs `Analyze` with no plan; a FIT with no records is `ErrUnreadable`. `Duplicate`: duration within max(60 s, 2 %) and power within 3 % when both have power, date +-1 day, same sport; boundaries just inside and just outside each tolerance; a different sport never matches; a provider row is never overwritten. Storage: `FindSimilarSession` on both engines. A ride is written as `provider = "import"`, `external_id` = UTC start unix seconds, and a second write of the same ride is a no-op; a `session_analyses` row exists for a ride 300 days old; `applyProgressionForAnalysis` and `MatchPlanned` are not called (a test with a planned workout on that day asserts no level move, no `workout_id`).
- [ ] GREEN; `just check`; commit `"Turn an imported FIT into a session and analysis, skipping rides already here"`.

### Task 7: Job table, upload and status

**Files:** `apps/api/internal/workout/db.go` (`ride_imports`, idempotent) + store methods (+ `TestEachEngine`); create `apps/api/internal/api/rideimport.go` (+ test); `server.go` routes; `riderdelete.go` drops `ride_imports`; `apps/web/src/api/{types.ts,client.ts}`.

- [ ] RED: `POST /api/training/import` streams the multipart body through `MaxBytesReader` (1 GiB) into a 0600 temp spool removed on every path (success, failure, panic-recover, cap breach), returns 202 and a job id, and runs the job in a goroutine with a context detached from the request and cancelled on shutdown; 413 over the cap; 409 while the rider has a running job; 429 inside the 10-minute cooldown (fixed clock); no file part -> 400; the rider is the session's, a `rider` form field is ignored (test posts one); owner-only. The job: read -> parse -> dedupe -> `UpsertSession` + `SaveAnalysis` (analyses for every ride) -> one `RecomputeFitnessSnapshots` -> `detectThresholdsFresh`; counts (`added`, `duplicate`, `skippedSport`, `unsupported`, `unreadable`) and phase are written to `ride_imports`; a cap breach ends `failed` with a short error class and keeps the rides already added; re-uploading the same zip ends `added 0`. `GET /api/training/import/status` returns the rider's latest job, a `running` row not updated for 2 minutes reads `interrupted`, none -> 204. A test scans every table for the fixture's marker bytes and finds none; log capture holds no file name and no watts beside the rider. `ride_imports` schema applies twice.
- [ ] GREEN; `just check`; commit `"Import ride history in the background, with a status endpoint"`.

### Task 8: Import UI

**Files:** create `apps/web/src/components/fitness/RideImportCard.vue`; modify the Fitness page.

- [ ] Card: two short lines on where to get each export, a picker (`.zip`, `.fit`, `.gz`) with an upload progress bar, then polling every 2 s while `running` (stopped on unmount), the result copy from the spec (added, already here, not supported (GPX/TCX), earliest date), and clear messages for 409, 413, 429 and `interrupted`. On `done` the fitness chart and thresholds refetch. Typed template handlers, semantic tokens only, no names or power shown.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser against `just demo` with a synthetic zip generated by a throwaway script under the scratchpad (never committed): upload, progress, result, re-upload shows 0 added; light/dark, 375px; commit `"Add the ride-history import card to the Fitness page"`.
