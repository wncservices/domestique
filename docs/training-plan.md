# Workout builder and adaptive training plans — design and plan

Domestique carries routes to head units. This is the design for a second thing it could
carry: **structured workouts**, generated from a goal (a race, a target date) and a rider's
own fitness data, and adapted as the rider actually trains. Nothing here is built yet — this
is the plan, in the same shape as `docs/plan.md`, so it can be picked apart before code exists.

## What this is, in one paragraph

A rider sets a goal — "half marathon, 14 weeks out" or "gran fondo, 180km, 2400m climbing,
10 weeks out" — states which days of the week they can train and roughly how much time they
have, and Domestique proposes a periodized plan: a sequence of structured workouts (warmup,
intervals at named targets, cooldown) scheduled across the weeks to that date. Each workout
pushes to the rider's Garmin or Wahoo as a **structured workout** the device can guide them
through — not a route to follow, a session to execute. As the rider actually trains,
completed-workout data pulled back from the same accounts feeds back into the plan: a missed
week or a string of workouts finished well under target reshapes what comes next, the way
Humango and TrainingPeaks' adaptive tools do, rather than handing out a static PDF on day one.

## Why this is a different shape of feature, not an extension of routes

Everything Domestique does today is **one-directional and stateless with respect to the
rider's body**: a route goes out, and whether it was ridden is not this app's business. A
training plan inverts both of those:

- **Data has to come back in.** `internal/garmin` and `internal/wahoo` today only ever push.
  Reading completed-workout summaries, heart rate, power and pace back from a rider's own
  account is new surface on both clients, not a new package bolted alongside them.
- **The desired state depends on history.** A route's desired state is "whatever the library
  currently holds" — `internal/sync`'s diff engine does not need to know what happened
  yesterday. A training plan's desired state for *next Tuesday* depends on what actually
  happened last Tuesday. The engine cannot be as purely stateless as `sync.BuildPlan`, though
  it should still be as pure a function as the inputs allow — see **The planning engine**
  below.
- **The payload is a different FIT message type.** `internal/fitcourse` encodes `course` /
  `record` / `course_point` messages — navigation. A structured workout is `workout` /
  `workout_step` messages — targets and durations, no track at all. Confirmed against the
  FIT SDK already in `go.sum`: `github.com/muktihari/fit/profile/mesgdef` already defines
  `Workout` and `WorkoutStep`, with `DurationType`/`TargetType`/`Intensity` fields matching
  the FIT Workout profile exactly. **No new dependency** — this is squarely the same "genuinely
  hard, budget-worthy" binary encoding `internal/fitcourse` already took on, extended to a
  sibling message type in a library already vendored for exactly this kind of problem.
- **The data is more sensitive than a route.** A GPX starts at somebody's front door; heart
  rate, power output and a training calendar describe somebody's body and daily habits. See
  **Security and privacy** below — the existing "never commit a credential / GPX is personal
  data" guardrails need a training-data equivalent, not a copy-paste.

None of that says a new architecture is needed — the same shape (a pure engine fed by a
store, adapters per provider, `Implemented` gating what the UI offers) carries over. It says
the desired-state input is a plan generated from history rather than a library re-read, and
that a new direction of data flow (pull, not just push) has to exist before the interesting
part can be built at all.

## Data model

New tables, alongside `routes`, `accounts` and `sync_state`, owned by new packages mirroring
`internal/source` and `internal/state`'s split (library vs. sync bookkeeping):

| Table | Rider-owned? | Holds |
|---|---|---|
| `goals` | yes | Event name, sport, target date, target distance/elevation/duration, priority (an A/B/C race, TrainingPeaks' own vocabulary for "peak for this one, the rest are practice") |
| `rider_profiles` | yes | Threshold power (FTP) and/or threshold pace, max HR, HR zone model, available training days (a weekly template, e.g. Tue/Thu/Sat/Sun), typical available hours per available day, experience level |
| `training_plans` | yes | One per goal: start date, phase boundaries (base/build/peak/taper), generated-at timestamp, superseded-by (a replan does not overwrite history, it supersedes) |
| `workouts` | yes | One planned session: plan id, date, sport, a structured step list (JSON, the same "blob beside the row" shape `routes.gpx_data` already uses for the track) |
| `completed_sessions` | yes | Pulled back from Garmin/Wahoo after the fact: date, duration, distance, average/normalized power or pace, average HR, a computed training load value, and the provider activity id it came from (so a re-pull is an upsert, the same idempotency `komoot:<id>` tags give route re-imports) |
| `fitness_snapshots` | yes | One row per day per rider: rolling short- and long-term training load and their difference — see **Reading the industry** for what these actually are |

`workouts.steps` deliberately does not get its own normalized table of rows — a step list is
read and written as a whole, never queried by individual step, the same reasoning
`model.Route` gives for not splitting `RouteStats` into its own table. `completed_sessions`
and `fitness_snapshots` are separate because they have different write patterns: a session is
written once per activity pulled, a snapshot is recomputed and overwritten as more history
arrives.

Ownership follows the existing rule exactly: **the rider comes from the session, never the
request body**, for every one of these tables. A route with no `targets` reaches its owner's
accounts only; a workout has no `targets` concept to get wrong in the first place — it is
never shared the way a route can be shared with a crew. If two riders training for the same
event want to see that overlap, that is a `goals`-level "same event, different rider" join for
display, not a shared plan — the plan itself must stay individual, because two riders' fitness
never matches closely enough for one plan to serve both.

## Reading the industry, and what to actually build

The vocabulary below is not this app inventing terms — it is the shared vocabulary
TrainingPeaks, Humango, TrainerRoad and Xert all converge on, because the underlying sports
science (Bannister's impulse-response model, later Coggan's power-based version of it) is the
same paper everyone implements:

- **Training load** (TrainingPeaks: TSS: Training Stress Score; a duration × intensity score
  per session, normalizable across power, pace and heart-rate-only data with progressively
  worse precision in that order).
- **CTL / ATL / TSB** (TrainingPeaks' Performance Management Chart) — CTL is a ~42-day
  exponentially-weighted average of daily load ("fitness"), ATL a ~7-day one ("fatigue"), TSB
  is CTL minus ATL ("form" — very negative before a big week, rising through a taper). This is
  exactly what `fitness_snapshots` above stores per rider per day, and it is a small, well-
  specified, entirely offline computation — no external service, no ML, just an EWMA over
  `completed_sessions`.
- **Periodization** — TrainingPeaks' Annual Training Plan works backward from the goal date:
  a taper week immediately before it, a peak phase before that, build, base, with a classic
  3-weeks-load/1-week-recover microcycle pattern inside each phase. This is the shape a v1
  planner should produce: deterministic, well-documented, and — crucially for this codebase's
  own testing culture — a pure function you can unit test against a fixed goal and profile and
  assert the phase boundaries land where the science says they should.
- **Continuous adaptation** — this is where Humango (and TrainerRoad's Adaptive Training,
  and Xert's Signature model) go further than a plan generated once: they re-evaluate after
  *every* completed session rather than on a fixed weekly cadence, comparing what was planned
  against `completed_sessions` and nudging the next few days — ease off after a missed or
  badly-fatigued session, hold or progress after one completed at or above target. This is the
  feature that actually earns the word "AI" in the loose, applied sense the industry uses it —
  it does not require a language model, it requires a feedback loop, which this app does not
  have anywhere today (the closest analogue is `sync`'s diff against `state.Store`, and that
  diff has no memory of *why* something changed).

**Recommendation: build the deterministic periodization engine and the adaptation feedback
loop first, in that order, and treat an LLM as an optional narrator on top, not the planner
itself.** Three reasons, all specific to this codebase rather than generic caution:

1. It matches `internal/sync`'s own precedent — "the diff engine. Pure: give it routes,
   config and a store, get a plan" — and this repo's tests-first culture explicitly rewards
   that shape: "check the test fails without it" only works cleanly against a deterministic
   function.
2. It matches the dependency-budget culture in `AGENTS.md`'s **Conventions**: a new direct
   dependency is for something "genuinely hard," the bar FIT encoding and OIDC verification
   cleared. Calling out to an LLM for the actual weekly schedule is not that — periodization
   is a solved, specified algorithm; wrapping it in a model adds nondeterminism and a network
   dependency for a problem that does not need either.
3. An LLM layer *is* a good fit for something this repo does not have today: explaining a plan
   in prose ("why did today move to an easy spin"), or taking a free-text constraint ("I'm
   traveling next week") and turning it into a structured profile change. That is squarely
   "spec a well-defined interface, hand it to a model" — closer to how `auth0mgmt` is "a
   narrow hand-rolled client" for one specific need than a core dependency. Worth a phase of
   its own, after the deterministic engine exists to constrain what the model is allowed to
   change.

## Structured workouts and the providers

### Pushing a workout out

`internal/fitcourse` already proves FIT encoding works and is tested two ways (round-trip
through the library, and an independent header/CRC check) — a `workout` (`internal/fitworkout`
or similar, alongside `internal/fitcourse` rather than inside it, since the message types and
validation rules genuinely differ) should follow the same two-track test shape.

| Provider | Push mechanism | Status |
|---|---|---|
| **Wahoo** | Cloud API — documented, the same clean surface `internal/wahoo` already talks to for routes. Believed to have a workouts/plans resource (this is the mechanism TrainerRoad and TrainingPeaks use to push structured sessions to an ELEMNT) but **not yet confirmed against current API docs in this pass** — verify scope names and payload shape before Phase B below, the same way `routes_write` was confirmed before Phase 4 of the routes work. |
| **Garmin** | The unofficial Connect web session `internal/garmin` already authenticates. Connect's own UI has a separate "Training → Workouts" surface from "Training → Courses" — almost certainly a sibling undocumented endpoint to the `course-service` one this app reverse-engineered already, at the same risk level `AGENTS.md` already documents for Garmin: "grey-area and breakable... acceptable for two personal accounts, not for anything shared more widely." Needs the same kind of exploratory work `internal/garmin`'s course push did, not assumed to exist until probed. |

Both providers appear to support **scheduling** a workout onto a specific calendar date, not
just uploading it — which is new for this app. `internal/schedule` today is explicit that
"neither provider integration this app has today has a scheduling concept to place [a ride]
on," written for routes/courses, which is true. A structured workout is different: Garmin
Connect's training calendar and Wahoo's app both do have a per-day workout slot. Confirming
exactly how to write to it is Phase B/C work, not assumed here.

### Pulling metrics back

This is new to both clients, but reuses sign-in work already done rather than needing a new
one:

- **Garmin** — `internal/garmin` already exchanges a password for an OAuth2 bearer over the
  unofficial Connect session (`garmin.go`'s four-step handshake). The same bearer token that
  authorizes a course upload today authorizes Connect's activity-list and activity-detail
  endpoints — this is additive surface on an existing client and an existing stored session,
  not a new credential or a new package.
- **Wahoo** — the Cloud API's own documented `workouts_read`-style scope (today's `scopes`
  constant in `internal/wahoo` is fixed at `"user_read routes_read routes_write"`, deliberately
  minimal per that file's own comment); pulling completed workouts needs that scope added and
  the app's existing Wahoo registration re-approved for it, the same approval gate that already
  governs `routes_write` per `docs/plan.md`'s Phase 4 account.

Neither provider reliably exposes FTP or threshold pace through account data — both TrainingPeaks
and Humango still ask the rider to enter or periodically re-test these. `rider_profiles` should
treat them the same way: rider-entered, with a manual "record a new FTP test result" action, not
inferred from provider data.

## The planning engine

Shaped like `internal/sync`, one level more removed from "pure" because it has to read history,
but everything the history-reading and the actual periodization logic can be kept separate:

```
goal + rider_profile ──┐
                        ├──> periodization.Plan(...) ──> TrainingPlan (phases, weekly load targets)
history (completed_sessions,
  fitness_snapshots)  ──┘

TrainingPlan + today's date ──> scheduler.NextWorkouts(...) ──> []Workout (this week's sessions)

new completed_sessions ──> adapter.Reconcile(plan, actual) ──> either "no change" or a
                                                                 superseding TrainingPlan
```

- `periodization.Plan` is the pure function described above: goal, profile and a fitness
  snapshot in, a phased plan out. Testable exactly the way `sync.BuildPlan` is tested — fixed
  inputs, assert the shape of the output, no network, no clock dependency beyond the goal date
  itself.
- `scheduler.NextWorkouts` turns a phase's weekly load target into actual sessions on actual
  days, respecting the rider's available-days template from `rider_profiles`. This is the layer
  that knows "a running goal wants a long run, a tempo run, one interval session and easy days;
  a cycling goal wants an endurance ride, a threshold session, and (once fitness supports it) a
  VO2max session" — a small, explicit table per sport and per phase, not a model.
- `adapter.Reconcile` is the Humango-shaped feedback loop: given what just came back through
  metrics ingestion, decide whether the rest of the plan still holds or needs to shift. Its
  output is a *new* `training_plans` row superseding the old one — plans are append-only
  history, the same reasoning `sync_state`'s own audit trail already follows, so "why did my
  plan change on the 14th" has an answer.

This wants its own scheduled job to run `adapter.Reconcile` after new metrics land, which is
the same open need `docs/plan.md`'s Phase 5 already flags and has not built yet ("a reconcile
schedule — a CronJob, or an in-process ticker"). Worth building one scheduling primitive that
serves both the existing push-reconcile need and this one, rather than two separately, when
either gets picked up.

## UI

New pages under `apps/web/src/pages`, in the same `<script setup lang="ts">` / Nuxt UI style
as the rest of the app — no new frontend dependency needed, Nuxt UI already ships form,
calendar and chart-adjacent primitives (or a small dedicated chart is a copy of the existing
`TrackPreview.vue` inline-SVG approach, not a new charting library, for the one chart this
needs):

- **Goal setup** — a short wizard: event, sport, date, target distance/elevation, available
  days.
- **Plan calendar** — a week/month view of `workouts`, mirroring the density of the existing
  library grid, each cell showing the session type and target load.
- **Workout detail / manual builder** — view or hand-edit a step list (warmup / interval ×N /
  recovery / cooldown, each with a duration and a target power/pace/HR zone) for the rider who
  wants to override what the engine produced, the same "AI proposes, rider can still hand-edit"
  relationship a route library already has between auto-import and manual upload.
- **Fitness chart** — the CTL/ATL/TSB line, TrainingPeaks' Performance Management Chart in
  miniature; the one chart in this feature actually worth a dedicated view, since it is the
  whole point of pulling metrics back in the first place.

## Security and privacy

`AGENTS.md`'s existing guardrails ("never commit a credential," "GPX files are personal
location data … belong in a private source, never in this repo") need a direct training-data
equivalent, not an assumption that the same rules already cover it:

- Heart rate, power and a training calendar are health-adjacent personal data, arguably more
  sensitive than a GPX track. `examples/routes/` stays a synthetic GPX; there must never be a
  committed example `completed_sessions` row with real numbers either — synthetic fixtures
  only, the same as the route example.
- **The same ownership rule applies without exception**: a goal, profile, plan or completed
  session comes from the authenticated rider's session, never a request body field, exactly
  like `handleUpload` already ignores a client-supplied `uploadedBy`.
- Provider OAuth scopes should ask for exactly what is used — Wahoo's `scopes` constant is
  already "fixed, not operator-configurable: exactly what this app functionally needs," and
  a workouts-read scope addition should follow the same discipline rather than requesting
  broader access "in case it's useful later."
- `admin` can already edit or delete anyone's *routes*; it should not by default extend to
  anyone's *training data* without a separate, explicit decision — a coach-view feature
  (an admin or a crew member seeing another rider's plan) is worth designing on purpose,
  not inherited silently from the existing role table.

## Proposed phases

| Phase | What | Depends on |
|---|---|---|
| A | Data model + manual workout builder. CRUD for `goals`/`rider_profiles`/`workouts`; hand-built step lists; FIT `workout` encoding (`internal/fitworkout`), tested the same two ways `fitcourse` is. **No AI, no metrics pull yet** — prove a hand-built structured workout actually lands on a real Garmin/Wahoo and executes correctly, the same "prove the conversion end to end" step `docs/plan.md` already calls out for courses. | Nothing new — confirms the FIT Workout push path before building intelligence on top of it |
| B | Metrics ingestion. Extend `internal/garmin` and `internal/wahoo` to pull activities/completed workouts; `completed_sessions` and `fitness_snapshots` (CTL/ATL/TSB) land in the database. Confirm Wahoo's actual workouts-read scope and endpoint shape; probe Garmin's unofficial activity endpoints. | A (accounts and sign-in already exist; this is new read surface on them) |
| C | Deterministic periodization engine. `periodization.Plan` + `scheduler.NextWorkouts`, unit-tested against fixed goals the way `sync.BuildPlan` is tested. A goal + profile produces a full plan; no adaptation yet. | A, B (needs a fitness snapshot to plan from) |
| D | Adaptive replanning. `adapter.Reconcile` runs after new metrics land — via whatever scheduling primitive Phase 5's still-open reconcile job ends up using — and supersedes the plan when reality diverges from it. | B, C |
| E (stretch) | LLM-assisted plan narration and free-text profile edits, gated behind the deterministic engine from C/D so the model explains and adjusts constraints rather than invents the schedule itself. | C, D |

Confirming Wahoo's structured-workout scope and probing Garmin's unofficial workout endpoint
are both credible enough to fail or come back smaller than hoped that Phase A is deliberately
scoped to not need either — the same lesson `docs/plan.md` already drew from Wahoo access being
"the long pole and nothing else unblocks it" for routes.

## Open questions

- **Wahoo workouts scope** — confirm against current Cloud API docs before Phase B; if it does
  not exist or is gated separately from `routes_write`, that is its own approval-lead-time risk
  the same way `routes_write` itself was.
- **Garmin's workout endpoint** — unknown until probed; budget for it being a genuine
  reverse-engineering effort, not a small extension, the same category of work
  `course-service` was.
- **How much history to keep** — `completed_sessions` going back further improves the fitness
  snapshot's accuracy but is also more sensitive data retained; worth a stated retention policy
  rather than "keep everything by default."
- **Coach/crew visibility into another rider's plan** — deliberately out of scope for Phases
  A–D above; needs its own design once it is actually wanted, not inherited from the existing
  crew-sharing model for routes.

## Sources

- [TrainingPeaks — Performance Management Chart (CTL/ATL/TSB)](https://www.trainingpeaks.com/blog/what-is-the-performance-management-chart/)
- [TrainingPeaks — Annual Training Plan / periodization](https://www.trainingpeaks.com/coach-blog/training-periodization/)
- [Humango — adaptive daily planning](https://www.humango.ai/)
- [TrainerRoad — Adaptive Training](https://www.trainerroad.com/adaptive-training/)
- [Xert — the Signature fitness model](https://www.xertonline.com/science/)
- [Wahoo Cloud API](https://cloud-api.wahooligan.com/) — same source already cited in `docs/plan.md`
- [muktihari/fit](https://github.com/muktihari/fit) — the FIT SDK already vendored; `profile/mesgdef.Workout`/`WorkoutStep` confirmed present in this pass
