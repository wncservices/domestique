# Workout export and ride-history import — design

Status: draft 2026-09-29. Builds on the indoor conversion (`2026-09-29-indoor-and-weather-design.md`),
ride analysis and threshold detection (`2026-09-28-threshold-detection-design.md`).

## Why

A competitor analysis found intervals.icu exports workouts as ZWO/MRC/ERG and imports history
broadly. Two gaps follow for Domestique riders:

- **Export.** A workout can be downloaded as a FIT file for a Garmin or Wahoo, but a rider on
  Zwift, TrainerRoad, Golden Cheetah or another trainer app has nothing. A `.zwo` dropped in
  Zwift's workouts folder works today, with no Zwift API, and is the clean seam a future Zwift
  sync would reuse (that sync is parked and **not designed here**).
- **Import.** History starts the day a rider connects Garmin or Wahoo, and sync only reads the
  latest 50 (Garmin) or 30 (Wahoo) activities. A rider with years of data gets a cold-start
  fitness chart, and threshold detection's 90-day history rule (`thresholds.HistoryWindowDays`)
  has nothing older than the connection date to work with. An account export is the only source
  of the full past.

## Part A: export

### Formats and who reads them

| Format | Targets | Reads |
|---|---|---|
| `.zwo` | fractions of FTP | Zwift (Documents/Zwift/Workouts/<id>/); also read by several editors and some other apps |
| `.mrc` | % of FTP, minutes | TrainerRoad (Create Workout from an ERG or MRC file), Golden Cheetah, other trainer software |
| `.erg` | absolute watts, minutes | same apps; ERG carries everything needed to run without an FTP setting |

Sources: the Zwift tag reference (github.com/h4l/zwift-workout-file-reference), TrainerRoad's
support article "Creating a Workout from an ERG or MRC File", and the Golden Cheetah user
group's thread on ERG/MRC import. **Not verified by us against a real device or app**: which
other apps read which format. Only the three named above are claimed in the UI copy.

### Which workouts export

Only what a trainer can hold: **time and power**. The source is the *indoor form*:
`indoor.Convert(workout, profile)` on the fly (or the workout's own steps when `Indoor` is
set, which is what Convert returns for it). That already makes distance and open-duration steps
time-based, and turns HR steps into power when FTP and zone are known. After it:

- A step left as **HR or pace** (no FTP, or unknown zone): refused, 422, "This workout is
  paced by heart rate and can't be turned into power. Set your FTP first." Never guess.
- A **running or other non-cycling** workout: refused ("Only cycling workouts export to
  trainer files"), the same rule as `indoor.ErrNotCycling`.
- An **open-target step** (`TargetOpen`, e.g. an FTP test's 20-minute effort, which must stay
  in resistance mode): `.zwo` maps it to `FreeRide`; `.mrc` and `.erg` **refuse** the workout,
  because those formats can only express a power line and ERG would lock the effort, so the
  test would measure the target instead of the rider (see the FTP tests spec, "Smart
  trainers"). The message says `.zwo` can carry it.
- A **cadence-only** step is an open step (its cadence is kept in `.zwo` only).
- **FTP missing**: `.zwo` and `.mrc` need it (targets are stored as watts and written as
  fractions of FTP); `.erg` does not. 409 "Set your FTP to export this as .zwo/.mrc".

Export inherits `indoor.Convert`'s shortening of easy and long rides, and says so: the
converter's note goes into the file's description, so a rider sees why 3 hours became 2. The
download is otherwise a snapshot: a later FTP change does not touch a file already exported.

### Encoders

`internal/workoutexport/{zwo,mrc,erg}.go`, pure: `(workout.Workout steps, ftp float64,
meta Meta) ([]byte, error)`, stdlib only (`encoding/xml`, `bytes`). No store, no HTTP, no
`indoor` import; the handler converts first. A shared `flatten.go` turns the step tree into
the timeline all three need, so the mapping rules exist once.

**`.zwo`**

- Root `<workout_file>`: `author` (Domestique), `name`, `description`, `sportType` `bike`,
  empty `tags`, then `<workout>`. Durations in whole seconds, power a fraction of FTP with 3
  decimals (`0.850`).
- `SteadyState` (`Duration`, `Power`) for a constant-power step; a range collapses to its
  midpoint (a lock holds one number anyway; the indoor conversion already does this for smart
  trainers).
- `Warmup` / `Cooldown` (`Duration`, `PowerLow`, `PowerHigh`) for a warmup or cooldown step
  with a range. **`PowerLow` is the power at the start and `PowerHigh` the power at the
  end**, in both elements: a cooldown reads high to low (`PowerLow="0.65" PowerHigh="0.40"`).
  Cited from the tag reference; **golden-file-checked only against our reading of it. Load one
  in real Zwift before shipping** (PR 3's manual gate).
- `IntervalsT` (`Repeat`, `OnDuration`, `OffDuration`, `OnPower`, `OffPower`) for a repeat
  block that is exactly two time-and-power children (on, then off) with no nested repeat.
  Anything else in a repeat block (three children, a nested repeat, an open step) is **unrolled**
  into flat elements: correct on every app, merely longer.
- `FreeRide` (`Duration`) for an open step.
- A `textevent` (`timeoffset="0"`, `message` = step name) is the child of a flat element whose
  step has a name. None inside `IntervalsT` (its text is relative to the block, a wrong
  message every cycle is worse than none).
- An FTP test's ramp (consecutive 1-minute power steps) stays as consecutive `SteadyState`s,
  not a `Ramp` element: same watts, exactly the shape the protocol defined.

**`.mrc` and `.erg`** share one writer, differing in the unit line and value column:

```
[COURSE HEADER]
VERSION = 2
UNITS = ENGLISH
DESCRIPTION = <name>
FILE NAME = <slug>
FTP = <ftp>            (.mrc and .erg, written when known)
MINUTES PERCENT        (.mrc)   |   MINUTES WATTS   (.erg)
[END COURSE HEADER]
[COURSE DATA]
0.00	50.0
5.00	50.0
5.00	88.0
...
[END COURSE DATA]
```

Each step becomes two points, at its start and end (`minutes` computed from cumulative
seconds, two decimals, so rounding never accumulates); two points at one target are a step,
a ramp is a start and end at different targets, and a step boundary repeats the minute with
the next step's start value. `.mrc` value is `watts / ftp * 100`, one decimal; `.erg` is
whole watts. CRLF line endings, tab separators. `[COURSE TEXT]` (per-step cues) is **not
written** in v1; step names are a `.zwo`-only nicety. Repeat blocks are always unrolled here
(no repeat concept).

### Endpoint and UI

`GET /api/training/workouts/{id}/fit` keeps its route; a new sibling `GET
/api/training/workouts/{id}/export?format=zwo|mrc|erg` (the existing FIT route is untouched
and gains no `format`, so nothing that links to it can break; `api.workoutFitUrl` gets a sibling
`api.workoutExportUrl(id, format)`). Same guards as `handleDownloadWorkoutFIT`:
`PermManageTraining`, `trainingAvailable`, `isOwnTraining`. Errors above are JSON 4xx with the
message, so the UI can toast them. `Content-Disposition: attachment`, filename from the slugged
workout name (`workout.DeviceName(...)` style: no rider name in it), types
`application/xml`, `text/plain`. Unknown `format`: 400.

- **Day card:** the existing download button becomes a small menu (`UDropdownMenu`): FIT
  (Garmin/Wahoo), Zwift (.zwo), TrainerRoad and others (.mrc), .erg. A rider whose export is
  refused sees the server's message in a toast; the menu does not pre-judge.
- **Week download menu:** the same menu per day, plus **"Export this week (zip)"**.
- **Zip of the week: in.** One request, `GET /api/training/weeks/{monday}/export?format=`,
  `archive/zip` (stdlib), one entry per exportable workout named `<date>-<slug>.<ext>`,
  workouts that refuse are skipped and listed in a `SKIPPED.txt` entry with the reason, so
  a rider with one HR-only day still gets the other six. It is a loop over the single
  encoder, about 40 lines. 404 when the week has no workouts.

Log: rider, workout id, format, outcome. No file contents, no watts.

## Part B: import

### Sources and what they contain

- **Strava bulk export** (account settings, "Download or Delete Your Account"): a zip with an
  `activities/` folder of one file per activity, mostly `.fit.gz`, some `.gpx`, `.tcx`, plus
  `activities.csv`.
- **Garmin account export** (Account Data Management): a zip whose FIT files sit in a *nested*
  zip, `DI_CONNECT/.../UploadedFiles_*.zip`, alongside JSON summaries. The inner FIT names carry
  no reliable activity id.
- **A set of `.fit` / `.fit.gz` files** picked directly (Wahoo, a head unit's own folder).

Sources: cubetrek.com's bulk-download guide and the Strava export write-ups it cites. The
Garmin layout is the one part **we could not confirm from an official page**; the reader
therefore walks *any* zip depth up to 2 for FIT entries instead of hardcoding a path.

### v1 scope

**Upload a zip, or several files. FIT only** (`.fit`, `.fit.gz`). GPX and TCX are **out**:
they carry no reliable power and no Garmin-style summary, most Strava rows that are GPX/TCX are
the old or phone-recorded ones a rider cares least about for FTP, and a second parser is a
dependency-budget question with a poor payoff. They are counted in the report as
`unsupported`, so a rider is told, not silently short. `activities.csv` is ignored (FIT holds
sport, time and power itself).

**intervals.icu API import is out of v1 and out of this plan.** It needs an outbound client,
an API key (Basic auth, user `API_KEY`; athlete id `0` is "me") sealed in the settings or
provider-link store, the outbound-client checklist (otelhttp transport, context threading),
paging and gzip file downloads, and **their API terms have not been read by us**. Activities
sourced from Strava are also not downloadable there. A rider with intervals.icu history but no
Garmin/Strava export is the case it would serve; see Ruling in the plan. Recorded as v2, with
this seam: the FIT-to-session path below takes bytes, not a zip, so a client only has to
produce bytes.

**Garmin/Wahoo backfill past the sync window: out.** Garmin's activity endpoint takes `start`
and `limit`, so deeper paging looks possible, but it is the same unofficial, breakable client
the AGENTS.md warns about, on a rider's real login, for a one-off need the export already
serves. Wahoo pages are 1-based with `perPage` up to 100 and would be simpler, but Wahoo's own
files are not fetched back past sync (`wahoo` file URLs are never stored). Not attempted.

### Pipeline

`internal/rideimport` (pure, no HTTP): `Read(src, limits, emit func(File) error) Report` walks
the upload and emits each FIT as bytes; `Session(fitBytes, profile) (Ride, error)` decodes with
the same `decoder`/`filedef.Activity` `api.decodeFIT` uses (moved to a shared spot in
`rideanalysis`, not copied) and runs `rideanalysis.Analyze` with `Planned: nil`.

Per accepted ride: a `completed_sessions` row (`provider = "import"`, `external_id` = the
ride's UTC start as unix seconds, so **re-uploading the same file is idempotent through the
existing `UNIQUE (provider, external_id)`**, and `provider = "import"` is the `source=import`
marker, no new column), training load from `workout.TrainingLoad` with the current profile
(as sync does), and a `session_analyses` row (power curve, zone seconds, NP, TSS, max HR, best
20-minute HR and speeds) so the imported ride feeds threshold detection like any other.
Analyses are written for **every** imported ride, not only the last 42 days as sync does:
detection's 90-day history rule reads older curves, which is most of why we import.

After the batch: `RecomputeFitnessSnapshots(rider)` once, then `detectThresholdsFresh` through
the same path sync uses. Detection windows are relative to today, so old rides only affect the
90-day history rule and the fitness chart; a finding on a rider-typed value stays a suggestion,
an empty or estimated field may auto-apply, exactly as today. Imported rides are never matched
to planned workouts and never move a progression level (that would re-score last year's
sessions against this year's plan); FTP tests are not captured from imports.
`applyProgressionForAnalysis` is not called.

Training load and TSS use the rider's **current** FTP, not the one true on the day. Stated in
the completion message; same trade-off `CompletedSession.TrainingLoad`'s doc already accepts.

### Sport, dates, dedupe

- Sport from the FIT `sport` field: cycling (incl. virtual and e-bike) and running map to the
  two `model.Sport` values; anything else (swim, ski, ...) is counted `skippedSport`. Indoor
  and outdoor are both cycling.
- **Date** is the ride's *local* day: FIT `local_timestamp` minus `timestamp` gives the offset
  when present, else UTC. Provider sync stores the provider's start-time day, so the same ride
  must land on the same date to be recognised.
- **Dedupe against existing sessions**, per ride, before writing. `completed_sessions` keeps a
  day, not a start time, and a Garmin export file carries no activity id, so a provider id
  cannot be the key across sources. A ride **is already there** when a session of the same
  rider and sport exists on the same local date or the day either side (timezone slack) and:
  duration is within max(60 s, 2 %) **and**, when both have power, average power is within 3 %.
  Same-source re-imports are caught first, exactly, by the `external_id`. Skipped rides are
  counted `duplicate` and never overwrite the provider's row (its summary numbers are better).
- Two real rides of near-identical length and power on one day are the false-positive case.
  Accepted: the cost is one under-counted ride in a bulk import, not corrupted data.

### Safety limits

Uploads are large and hostile-by-default, and **raw files are never persisted** (AGENTS.md:
GPX and FIT are personal location data; only aggregates are kept):

- Request body cap **1 GiB**, `http.MaxBytesReader`; a rider's zip is spooled to one
  `os.CreateTemp` file (mode 0600, removed on every path via `defer`) because `archive/zip`
  needs random access. That temp file is a working copy, not storage, and needs ephemeral disk
  at least the cap; the chart's `emptyDir` is the deployment note. A nested zip is spooled the
  same way, depth **2**, one open at a time.
- Per entry, **decompressed size cap 64 MiB** enforced by counting bytes read
  (`io.LimitReader`), never by trusting the zip header's declared size; `.fit.gz` the same
  through `gzip.Reader` (gzip-in-zip counts against the entry's own cap). Whole upload
  **decompressed total cap 6 GiB**, **entry count cap 20 000**, a **ratio guard** (an entry
  that decompresses beyond 200x its compressed size is aborted). Any breach stops the job with
  `failed: too large` and writes nothing further; rides already committed stay (each is
  idempotent).
- Entry **names are never used** as paths, never written anywhere, never logged (names carry
  dates and places); type is chosen by extension and confirmed by the FIT header (`.FIT`
  magic at byte 8). Encrypted entries and non-regular ones are skipped.
- A FIT that fails to decode is `unreadable`, counted, and does not abort the job (one bad file
  never aborts a run, as elsewhere).
- Log lines: rider, job id, counts. No file names, no per-ride power/HR values next to a
  rider name.
- **Owner-only, session rider**, `PermManageTraining`; the rider is never taken from the form.
  One active import per rider (409); a per-rider cooldown of 10 minutes after a finished one
  via `internal/ratelimit`.

### Background job and status

`POST /api/training/import` (multipart, one or more `file` parts) returns **202** with a job id
once the upload is fully received and spooled; the work then runs in a goroutine (with a
`context` detached from the request, cancelled on server shutdown). Progress lives in one small
table so any replica can answer:

`ride_imports(id TEXT PK, rider, state ('running'|'done'|'failed'), phase ('reading'|
'analysing'|'recomputing'), added INT, duplicate INT, skipped_sport INT, unsupported INT,
unreadable INT, error TEXT, started_at, updated_at)`, `CREATE TABLE IF NOT EXISTS`, counts
and a short error class only; no names.

`GET /api/training/import/status` returns the rider's latest job. A `running` job whose
`updated_at` is over 2 minutes old is reported `interrupted` (a restart killed it); the rider
re-uploads and idempotency finishes the job. The UI polls every 2 s while running.

### UI

Fitness page, an "Import ride history" card (`RideImportCard.vue`): explains where to get each
export (Strava, Garmin) in two short lines, a file picker (`.zip`, `.fit`, `.gz`), an upload
progress bar, then the polled result: "Added 412 rides, 38 already here, 9 not supported
(GPX/TCX)", with a "Your fitness history now goes back to March 2021" line from the earliest
imported date. Nothing about names or power. Shown to any rider with training access.

## Data

Idempotent in `UseDB`, through `dbx`:

- New table `ride_imports` (above). No change to `completed_sessions` or `session_analyses`
  (`provider = "import"` is the marker; the existing UNIQUE key is the idempotency).
- Reader for "imported" in queries is `provider = 'import'`; a `Deleting my data` path
  (`riderdelete.go`) already deletes sessions by rider and must also drop `ride_imports`.

## Testing

- `workoutexport`: golden files per format for a threshold-over-unders workout (repeat maps to
  `IntervalsT`), a sweet-spot block with warmup and cooldown ramps, an FTP ramp test (ERG
  ok), a 20-minute test (`.zwo` FreeRide; `.mrc`/`.erg` refuse), unroll of a 3-child block,
  range midpoint, textevent escaping (`&`, `<`, quotes, non-ASCII), HR/pace refusal, no-FTP
  409 for zwo/mrc and success for erg, CRLF and two-decimal minutes.
- `indoor` + export: a distance/open step and an HR step come out time-and-power.
- Handler: status codes, headers, owner-only 404/403, unknown format, week zip contents and
  `SKIPPED.txt`.
- `rideimport`: a synthetic zip built in the test (never a real file) with: Strava-shaped
  `activities/*.fit.gz`, Garmin-shaped nested zip, GPX/TCX counted unsupported, a corrupt FIT,
  a zip bomb (a highly compressible entry over the ratio cap), an entry over the size cap, a
  total-cap breach, entry-count breach; dedupe boundaries (2 %/60 s and 3 % power both sides,
  date +-1 day, different sport), re-import idempotent, timezone: local-day derivation with
  offsets under UTC and Europe/Brussels, a ride at 23:30 local.
- Storage under `TestEachEngine`: `ride_imports` round-trip, upsert of counts, interrupted
  read; schema applies twice.
- Pipeline end to end: imported rides create sessions and analyses (including older than 42
  days), snapshots recomputed once, detection sees an older curve, no progression move, no
  plan match; raw bytes never reach the DB (a test scans the tables for the fixture's marker).
- Fixed clocks with an explicit zone; tests pass under UTC and Europe/Brussels. Synthetic
  fixtures only, generated in code with the FIT encoder already in the module.

## Out of scope

Zwift/TrainerRoad/Wahoo account sync (parked); `[COURSE TEXT]` cues; a `Ramp` element; `.fit`
export changes; GPX/TCX import; intervals.icu API import (v2); Garmin/Wahoo backfill; storing
uploads; re-scoring imported rides against plans; recalibrating levels from imports;
per-source dedupe by provider id; importing wellness or sleep from an export.
