# Crew-aware planning — design

Status: draft 2026-09-29. Builds on crews and crew rides (`internal/crew`, `internal/schedule`,
`CreateSeries`), whole-season planning and refresh (#352), readiness and the tomorrow forecast
(`2026-09-28-readiness-design.md`, `2026-09-28-readiness-tomorrow-design.md`) and FTP tests
(`2026-09-29-ftp-tests-design.md`, whose "linked to the goal so the day reads as taken" precedent
this reuses). Written to compose with the unmerged life events (`2026-09-29-life-events-design.md`,
on `claude/life-plan`): its blackout set is consulted when it exists and ignored until then.

## Why

A crew ride is the most important session many riders have in a week, and the plan does not know
it exists. Today crews share routes and schedule rides, and a rider can push a ride's route to every
member's devices, but the training plan fills Saturday with its own long ride, puts intervals the
day before, and cannot tell that two crew mates are both about to ride 3 hours alone on different
days. From the competitor brief this was written from (not re-verified), no competitor plans around
real group rides or lines up two riders' key sessions; Domestique already holds both halves, the
crews and the per-rider plans.

Two pieces, in this order of importance:

1. **Crew rides as fixed sessions**: a ride a rider has said "I'm going" to becomes a session in
   their own plan; the plan fills and adapts around it.
2. **Ride together**: an opt-in between crew members that proposes moving two riders' long or
   endurance rides in the same week onto one shared day, on a shared route. Each rider confirms
   for their own plan.

## What exists today

- `crew.Store`: crews, members (`pending`/`approved`, `CanSchedule`, `IsOwner`), `Snapshot`
  (crews plus approved members, which `config.VisibleTo`/`TargetsFor` read).
- `schedule.Store`: `crew_rides` (crew, route slug, date, optional time, creator, `series_id`),
  `ride_series`, `Create`, `CreateSeries`, `DeleteSeries`, `ListUpcoming`; `POST /api/crews/{id}/rides
  /{rideId}/sync` pushes the route to every approved member. There is **no RSVP or attendance** of any
  kind: a ride names a crew, not who is coming.
- A route is visible to a crew member when `config.VisibleTo` says so (its `targets` name the
  crew). `routeshare` is a link for people outside the deployment and is not involved.
- The plan: `scheduler.WeekWorkouts` builds a week from `Week.TargetHours` and the profile's
  `AvailableDays`; `fillWeek` skips dates already holding a workout of any goal (plus `MovedFrom`
  vacated days); `IsKeySession`/`IsHardSession`; `IsGenerated` (description prefix, no
  `AdjustedMarker`) is the only thing adaptation, easing and replan may touch; `adaptRider` runs
  the automatic rules after every schedule and replan. Readiness (`readiness.Verdict`) only ever
  eases and the tomorrow forecast may offer "Ease tomorrow".
- Rider data (`rider_profiles`, workouts, wellness, FTP) is owner-only.

## Crew rides as fixed sessions

### "I'm going"

New table `crew_ride_going` in `schedule` (`ride_id`, `rider`, `created_at`, primary key
`(ride_id, rider)`): a row means the rider is going; there is no "maybe" or "not going" state
(absence is not going). Only an approved member of the ride's crew may add their own row, for a
ride dated today or later, whose route still exists. **The rider comes from the session.** The
crew's other members see who is going (names, count); nobody can mark someone else going.

`PUT /api/training/crew-rides/{rideId}/going` `{going, dryRun?, skip?}` is the one write; it uses
the preview-then-apply shape below.

### The fixed session

Going creates a **workout row in the rider's own plan**, the FTP-test precedent: `workouts` gains
`crew_ride_id TEXT NOT NULL DEFAULT ''` (unique per `(rider, crew_ride_id)` where non-empty). The
row has the ride's date, sport cycling, zone endurance, name "Crew ride: <route name>", and one open
`active` step whose length is the estimate below, so `PlannedSeconds`, the week strip and the
volume maths need no special case. It is linked to the focus goal when the rider has one. Its
description is `Crew ride with <crew name>: <route>, N km, N m, about 3 h 10, estimated TSS 140.`
(no other rider's name). Because it does not start with `GeneratedDescription`, `IsGenerated` is
false and adaptation, easing, replan and the tick **never rewrite, move or delete it**; the
workout auto-push skips rows with `crew_ride_id` (the route reaches the head unit through the
crew's own sync, not as a structured workout).

**Estimate** (`crewplan.Estimate(route)`, pure): duration `km / 26 + ascentM / 1000 * 0.5` hours
(about 23 km/h for a 100 km, 1000 m ride; stops not included), capped at 8 hours; intensity factor
a fixed 0.65 (a group ride is mostly endurance with surges), so TSS is `hours * 0.65^2 * 100`. FTP
is not an input: TSS is FTP-relative by definition, so the factor cancels and a rider with no FTP
still gets an estimate. FTP is used only to word the advice ("stay under about 195 W", 75 % of
FTP), computed for the owner. Estimates are rough; the copy says "about". Planned-load readers that
price an open step at the default 50 TSS per hour (the tomorrow forecast) stay as they are; the
exact estimate lives in the DTO.

**Classification:** at least 120 minutes is **long**: it is the week's long ride and counts as a
key session (`IsKeySession` gains `crew_ride_id != "" and planned >= 7200 s`; `IsHardSession`
stays false, like any long ride). 60 to 119 minutes is **endurance**: fixed, not key. Under 60
minutes is **short**: fixed and it takes its day, nothing else changes around it.

### The plan fills around it

Rules, in `crewplan.Constrain` plus the fill change; they read the rider's own fixed rows dated in
the week:

1. **Its day is taken.** The day-taken set counts any row with `crew_ride_id`, whether or not it
   has a goal (a goal-less row would otherwise block nothing).
2. **Volume:** `Week.TargetHours` is reduced by the ride's estimated hours, floored at 50 % of the
   target. Structured slots keep their rungs; endurance and long slots absorb the reduction through
   the remainder `WeekWorkouts` already computes. A recovery week keeps its own lower target and
   the preview says the ride exceeds it.
3. **Long replaces long:** a long crew ride removes the week's own long slot. An endurance one
   replaces one endurance slot.
4. **No hard session the day before** (long and endurance): a generated, unadjusted hard session
   the day before becomes `scheduler.EasyVariant` with the `AdjustedMarker` and the reason "eased
   the day before your crew ride". Same mechanism and pass as the FTP-test day-before rule.
5. **An easy day after a long ride:** a generated, unadjusted session the day after becomes
   `EasyVariant`, capped at 60 minutes, reason "eased the day after your crew ride".
6. **Never auto-changed by readiness.** `IsGenerated` is false, so `AdaptSessions` never moves,
   eases or skips it and "Ease tomorrow" refuses it (409). Readiness gets advice instead, below.

Rules 1 to 3 run in `fillWeek`/`refreshWeek`, so a week filled later (the season is planned ahead)
already accounts for a ride joined earlier; rules 4 and 5 run in `adaptRider`, so they hold after
every schedule and replan, and the marker is the one-automatic-change-per-workout guarantee.

### Joining and leaving are confirmed, as a preview

A week is usually already filled when a rider says "I'm going", so the plan has to change under it.
`crewplan.Preview(in) Diff` is pure (rider's workouts, profile, the ride, `now`): an ordered list of
changes with a deterministic id (`<op>:<workoutId>`) and a one-line reason; it never writes.

| Op | Meaning |
|---|---|
| `remove` | delete a generated, unadjusted, unridden session (the one on the ride's day; the week's generated long ride when the crew ride is long); its Garmin copy comes off as replan does |
| `ease` | rules 4 and 5 |
| `shorten` | scale generated endurance sessions down so the week fits the reduced target, none below 45 minutes |
| `add` | on leaving: the session the plan would have put on the freed day (`WeekWorkouts`, today or later, day free) |

It also lists what it **leaves alone** (rider-built, already adjusted, already ridden) and warns
when the day is a life-event blackout day (warn, never block).

`PUT .../going` with `dryRun` returns the preview and writes nothing. Without it, the server
**recomputes** the diff and applies it minus `skip: [changeId]`, under the scheduling advisory lock
replan uses (409 with the replan wording when the tick holds it). It never trusts a diff from the
client. Going, creating the fixed row and the changes are applied together: any failure leaves the
rider as they were. Leaving deletes the fixed row and the going row, then applies the `add`. Eased
and shortened sessions are **not** restored (their marker says they were changed once); the
preview says so.

### When the crew changes something

A crew scheduler deleting a ride, or an owner removing a member, must **not** reach into another
rider's plan. The fixed row stays; the API marks it `orphaned` (`cancelled` when the ride is gone,
`left` when the rider is no longer approved in the crew) on read. The day card says "This crew
ride was cancelled" and offers the ordinary leave preview, so re-planning still happens **on the
rider's own confirmation**. The going rows of a deleted ride are removed with the ride (crew data);
the workout row is the rider's.

### Readiness advice

`readinessResponseDTO` gains `crewRide?: {date, routeName, severity, advice}` when today (or
tomorrow, from the forecast) is a fixed crew ride and the verdict is not ready: caution says "Group
ride today: sit in, skip the pulls, keep it easy, stay under about 195 W"; rest says "Your recovery
looks low. Ride at conversational pace, or skip it; your call". Nothing is changed and no "Swap"
is offered. The wattage is computed for the owner from their own FTP and omitted without one.

## Ride together

### The opt-in flag

Table `crew_ride_together` in `schedule` (`crew_id`, `rider`, `days`, `updated_at`), one row per
rider per crew: the rider says "I'm open to a shared ride on these days" (any weekdays; Saturday
and Sunday are tried first). The UI defaults the selector to the profile's available days that fall
on Saturday or Sunday, **copied at save, never read again**. No row means opted out.
`PUT /api/crews/{id}/together` `{days}` (empty deletes) writes the session rider's own row only.
Removing a member deletes their row (`RemoveRiderEverywhere`), as does rider deletion.

The profile's `AvailableDays` is **not** shared and is not read to decide what a peer sees: the flag
is separate so that opting in is an explicit, revocable act and a rider whose plan says "Sat" can
still say "not with these people".

### The proposal

Computed when either rider reads `GET /api/training/ride-together`, for the current and next
week; no background loop. `crewplan.Align(in) []Proposal` is pure. For each crew and week, take the
opted-in approved members who each have one **qualifying session**: the week's long ride or an
endurance session of at least 60 minutes, generated, unadjusted, unridden, dated tomorrow or
later. A rider who has a fixed crew ride in that week is left out, and if only one of two does,
there is no proposal (the other can join that ride). Two or more qualifying riders are a candidate
group.

**The shared day:** the intersection of the group's flagged days, ordered Saturday, Sunday, then
the rest chronologically, taking the first day where **every** rider's day is free by the plan's
own rules: tomorrow or later, no workout of any kind (their own qualifying session excepted), not a
`MovedFrom` vacated day, not a life-event blackout day, and the day before holds no hard or key
session. No such day, no proposal.

**The route:** from routes visible to every rider in the group (`config.VisibleTo` against the crew
snapshot), the one whose estimated duration is closest to the shortest of the riders' qualifying
sessions, not more than 15 % over it; ties by slug. No visible route fits, no proposal (a route
suggestion is part of the point).

A proposal is stored once so all riders see the same one: `ride_together_proposals` (`id`,
`crew_id`, `week_start`, `day`, `route_slug`, `created_at`) and `ride_together_members`
(`proposal_id`, `rider`, `status` pending | accepted | declined, `workout_id`), unique on
`(crew_id, week_start)`. Any decline ends it for that week; a premise that no longer holds (a
session moved, ridden or deleted, a flag removed, the day filled) marks it `stale` on the next read
and it is hidden.

### Confirming is each rider's own write

`POST /api/training/ride-together/{id}/accept` (and `/decline`) acts on the **session rider's own
member row and own plan only**: the qualifying session moves to the day (`MovedFrom` note so
scheduling keeps the vacated day taken, plus `AdjustedMarker`, plus "Ride together: <route> with
your crew"), under the scheduling lock, revalidated server-side (409 with the fresh proposal when it
no longer holds). It never touches another rider's row or plan; accepting does not require anyone
else to have accepted. When the last rider accepts the proposal is `agreed`. The moved session is an
ordinary plan session afterwards (not fixed, not crew-owned); a rider who wants a real crew ride
schedules one in the crew, and then rides fixed sessions above.

## What crosses between riders

Crew members already see each other's names, and crew rides and their routes. Added, exactly:

| Crosses | Who sees it | Why |
|---|---|---|
| Who is **going** to a crew ride (names) | approved members of that crew | that is what a ride is |
| The **together days** flag (`days`) | approved members of that crew | the offer itself |
| A proposal: week, day, route, the riders in it and each one's status | the riders in it | to confirm |

**Never crosses:** any plan detail (session dates, durations, zones, TSS estimates), FTP, HR,
wellness or readiness, available days, life events, goals, FTP tests, or **why** a proposal exists
beyond "you both have a long ride that week". The existence of a proposal reveals that each rider
has a qualifying session that week; accepted, since they opted in. The server reads each rider's
plan to compute a proposal, on behalf of the consenting group, and the response carries only the
table above. **Cross-rider writes:** none. The only rows written about a rider are written by that
rider (going, together, accept/decline), and the crew's own actions (delete ride, remove member)
change crew data only, never a rider's workouts.

## Data

Idempotent in `UseDB`: `workouts.crew_ride_id TEXT NOT NULL DEFAULT ''` plus a unique partial index
on `(rider, crew_ride_id) WHERE crew_ride_id <> ''`; tables `crew_ride_going`, `crew_ride_together`,
`ride_together_proposals`, `ride_together_members`, all TEXT columns and `CREATE ... IF NOT EXISTS`,
through `dbx`.

## API and UI

- `GET /api/training/crew-rides?from=` -> upcoming rides in the caller's crews with `going`, `mine`
  and `{km, ascentM, minutes, tss, kind}`; `PUT .../{rideId}/going` as above.
- `GET /api/training/ride-together`, `POST .../{id}/accept`, `POST .../{id}/decline`;
  `PUT /api/crews/{id}/together`. `crewDTO` members gain `togetherDays` (approved members only).
  `workoutDTO` gains `crewRide?: {rideId, crewId, crewName, routeName, going, estimatedTss,
  orphaned?}`; `readinessResponseDTO` gains `crewRide`. Plan endpoints are owner-only.
- **Week strip and day card:** a crew icon on a fixed session, "Long crew ride", the estimate,
  who is going, and the readiness advice line; an orphaned one shows the cancelled note and
  "Update my plan". No other Plan page change.
- **Plan page:** a `RideTogetherCard` above the week strip when a proposal exists: "You and <names>
  both have a long ride this week. Saturday on <route> works for everyone.", with "Move mine to
  Saturday" and "No thanks", and who has accepted.
- **Crew page (minimal):** an "I'm going" toggle and the going names on each ride (opens the
  preview modal before applying), and an "Open to riding together" days selector on your own member
  row.

## Testing

- `crewplan`: the estimate (duration, TSS, cap), classification at 59/60/119/120 minutes;
  `Preview` per op for a filled and an empty week, leaves-alone cases, `add` on leaving; `Align`:
  group formation, exclusions, Sat/Sun order, hard-day-before, blackout day, route choice and its
  15 % bound, no route no proposal.
- `scheduler`/`api`: fill honours fixed rows (day taken without a goal, volume floor, long slot
  removed) for a week filled before and after joining; rules 4 and 5 survive replan and the tick;
  auto-push skips the row; `IsKeySession` for a long row; readiness never moves it and advice
  appears for caution and rest; "Ease tomorrow" 409s.
- Storage under `TestEachEngine`; idempotent schema; going rules (membership, past date, missing
  route, own row only); preview writes nothing, apply recomputes and honours `skip`, a stale diff
  applies partly harmlessly; orphaned rows on ride delete and member removal with the workout
  untouched; the FTP test scheduler 409s on a crew ride day.
- Privacy: a peer's `GET` responses contain none of the "never crosses" fields; a rider can not
  accept for another; no endpoint writes another rider's workout (test every write endpoint as a
  second rider).
- Fixed clocks with an explicit zone; tests pass under UTC and Europe/Brussels.

## Out of scope

RSVP states other than going, editing a ride's date, choosing among proposed routes, proposals
across more than one week, turning an agreed proposal into a crew ride, TSS from the route's real
gradient, ride-day nutrition, carpooling, notifications, group rides outside a crew, restoring
eased sessions on leaving.
