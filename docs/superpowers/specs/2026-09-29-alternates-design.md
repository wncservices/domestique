# Workout alternates and "I have N minutes today" — design

Status: draft 2026-09-29. Builds on progression levels (`2026-09-27-progression-levels-design.md`),
readiness (`2026-09-28-readiness-design.md`) and FTP tests (`2026-09-29-ftp-tests-design.md`).
Touches, and is written to compose with, three unmerged pieces: whole-season planning (#352), indoor
conversion (`2026-09-29-indoor-and-weather-design.md`) and push-today-only (#351); see "Interactions".

## Why

The plan decides a session and the rider can only take it, move it, or delete it. A rider with a
hard threshold session and a bad night has "Move" and the automatic easing; a rider whose
meeting was cancelled has no way to use the extra hour. TrainerRoad offers Workout Alternates
(an easier, harder, shorter or longer version of any session, each with a predicted difficulty)
and TrainNow (say how long you have, get a few fitting workouts). Garmin's Daily Suggested
Workouts and Xert's Adaptive Training Advisor answer the same "what should I do today" question
from load and readiness. Domestique already has every ingredient: ten-rung ladders per zone
(`workoutlib`), a per-zone level (`progression`), a readiness verdict (`readiness.Assess`) and a
planned-TSS estimate. This feature adds the two rider-facing choices on top and no new model.

Both are **suggestions the rider applies**. Nothing here changes a session on its own.

## Alternates

Available for a **plan-made session** (`GoalID` set, description starts with
`scheduler.GeneratedDescription`, including one an automatic adjustment already changed), dated
today or later, not ridden (`adapter.WorkoutDone`), not an FTP test (`TestProtocol != ""`, a test
is what it is), with a ladder for its sport and zone or zone `endurance`. Anything else returns no
options, and the day card hides the menu.

Let `cur` be the session's rung level (`workout.Level`), `L` the rider's current level for that
zone (`progression`), and `total(r)` = `workoutlib.TotalSeconds(r)`.

| Option | Structured session (has a ladder) | Endurance or long ride |
|---|---|---|
| **Easier** | rung `cur - 1`, floor 1 | not offered |
| **Harder** | rung `cur + 1`, capped at `floor(L) + 1` | not offered |
| **Shorter** | the longest rung with level <= `cur` and `total <= 0.75 x total(cur)` | main step scaled to 75 % |
| **Longer** | the shortest rung with level >= `cur`, <= the harder cap, and `total >= 1.25 x total(cur)` | main step scaled to 125 % |

An option with no rung behind it is **not offered** (rung 1 has no easier, the cap has no harder)
rather than shown disabled. Endurance time scaling rounds to 5 minutes, floors at 30 minutes
(the fixed 20-minute warmup and cooldown plus a 10-minute main step) and tops out at 6 hours; it
reuses the scheduler's endurance builder (exported), so a rescaled long ride is exactly the shape
a generated one is. The harder cap is **the rider's level + 1, not the session's + 1**: a session
the plan set below the rider's level (a recovery week, an eased one) can be swapped back up to
where the rider actually is, and never past one rung beyond it.

Each option carries name, zone, level, minutes, **planned TSS** (the adapter's planned-TSS
estimate, exported; 0 without an FTP, shown as "-"), and a **predicted difficulty** from
`d = rung - L`:

| `d` | Label |
|---|---|
| <= -2 | Recovery |
| (-2, -0.5] | Achievable |
| (-0.5, 0.5) | Productive |
| [0.5, 1) | Stretch |
| >= 1 | Breakthrough |

Endurance and long options have no level: the label is Recovery for the easy suggestion below
and Achievable otherwise. The labels are the vocabulary TrainerRoad uses, mapped onto the
distance between the rung and where the rider is, which is the one signal this app has; they
are not TrainerRoad's model and the copy does not claim it. `Pick` already rounds to the nearest
rung, so a plan-made session lands on `Productive` by construction.

A swap to a harder option on a day whose readiness verdict is `caution` or `rest` (today's
assessment, applied to today's or tomorrow's session) is still offered, with a warning line
("Readiness is low today"). The rider decides.

### Applying a swap

`POST /api/training/workouts/{id}/alternates {kind}` with kind `easier|harder|shorter|longer`
recomputes the options and applies that one (409 when it is not offered any more: the workout or
the rider's level changed since the menu opened). The swap **edits the session in place**: same
id, date and goal; new name, steps, zone and level. In place, because a copy already on a head
unit updates instead of duplicating, and it re-pushes when the session is today's (push-today-only).

It appends one note to the description, `Swapped by you: harder, was Threshold 5 (1h30).`, using a
new `scheduler.SwappedMarker = "Swapped by you:"`. The marker does three jobs, and is why the swap
is **rider-touched** everywhere the app decides that:

- `IsGenerated` returns false for it, like `AdjustedMarker`, so readiness easing, missed-session
  make-up and step-down never rewrite a session the rider chose on purpose.
- The whole-season refresh treats a session as untouched only when its description is exactly
  the generated one and it was never updated; a swap fails both, so a later refresh leaves it.
- `isPlanMade` (replan) returns false for it, so "Re-plan this week" does not delete the rider's
  choice. The day stays taken for scheduling either way (`GoalID` is kept).

A swap is repeatable: unlike automatic adjustment there is no "changed once" limit, because each
one is the rider acting. `MovedFrom` still finds a moved session's original date, since the
description prefix is untouched.

**Progression is not touched.** A swap changes what is planned, never a level. The level moves
only from the ride's outcome, as today: `progression.Delta` already weighs the outcome against
the workout's own level, so completing a harder swap counts for more and a struggled one is read
against that harder rung with no special case here.

### Reversible: "Back to planned version"

Column `workouts.planned_snapshot` (nullable JSON): `{name, zone, level, description, steps,
indoor, outdoorSteps}` of the row **before the first swap**. Later swaps leave it alone, so
"Back to planned version" always returns to what the plan made, not to the previous swap. Revert
restores every snapshot field except the description's swap marker, clears the column, and
appends `Back to the planned version.` (the marker stays so the session stays touched: the rider
has expressed a preference and the plan must not silently redo it). It is idempotent (no
snapshot: unchanged, 200), and refused (409) for a ridden or past session.

**Not reusing the indoor column.** The indoor design keeps `outdoor_steps` (steps only, because
converting to indoor changes nothing else). A swap changes name, zone and level as well, and
the two are independent axes that can both be set: an indoor session can be swapped, and a swapped
session can go indoor. One shared "original" column would have to say which of the two it is
undoing, and reverting one would clobber the other. So `planned_snapshot` is its own column and
it records the indoor state too, which is what lets revert be exact. It lands independent of the
indoor work; see "Interactions".

## "I have N minutes today"

Input: `minutes` 30 to 180, in the UI a chip picker (30, 45, 60, 75, 90, 120, 150, 180) plus
"other". `GET /api/training/trainnow?minutes=N` returns up to three suggestions and the context
that shaped them; nothing is stored. Owner-only.

Each suggestion is `{kind, name, zone, level, minutes, tss, difficulty, why, warning?}` and
**fits N**: `total <= N minutes`. Structured rungs are chosen with `workoutlib.Pick(ladder, level,
N x 60)`, which takes the rung closest to the target level among those that fit. When even rung 1
does not fit, it is demoted to an endurance ride of N minutes, never trimmed below the fixed
warmup. Endurance suggestions use N exactly, except **never more than 1.25 x the session they
stand in for** (so "180 minutes" does not turn a planned 60-minute easy ride into three hours;
the easy option below is the one that takes all of N).

The three, in order, each dropped when it does not apply and deduplicated when two land on the
same zone, level and minutes:

1. **`planned`: today's plan fitted to N.** Today's plan-made undone session, else the next
   undone plan-made session this week. Structured: its zone at level `L`. Endurance or long: its
   time fitted to N under the 1.25 cap. It is the plan, shortened or lengthened for the day.
2. **`wanted`: a zone the plan wants that isn't done yet.** `scheduler.WeekWorkouts` for the
   current week lists the zones the plan puts in it; those with no completed session yet this
   week (matched through the completed sessions' workouts), other than the one in `planned`, in
   plan order; the first, at level `L`, fitted to N. This is what gets a rider who missed
   Tuesday something Thursday without waiting for the make-up rule.
3. **`easy`: an endurance ride of N minutes** (zone endurance, no level, the same builder). Always
   available.

**Readiness and load shape all three**, in this order:

- **`rest`:** only `easy`, with N capped at 60 minutes, `why` = the readiness reasons. Nothing
  structured, because that is what the verdict says and the rest of the app already agrees.
- **`caution`:** structured suggestions use level `L - 1` (one rung down, floor 1) and `wanted` is
  dropped; `planned` and `easy` remain.
- **Load:** count the days in the last 7 (before today) with a completed structured session. With
  two or more, **`wanted` is dropped** and `planned` carries a warning ("A third hard day in 7"):
  the plan's own hard day stays visible, since the rider set it up, but the app does not suggest
  an extra one. Under `rest` or `caution` the load rule is moot.

Readiness comes from the existing `assessReadiness` for today; a rider with no wellness data gets
`ok` and the load rule only. A rider with no plan (no focus goal) gets `easy` only.

### Use this

`POST /api/training/trainnow/apply {minutes, kind, zone, level}` recomputes the suggestions and
applies the one that matches all of kind, zone and level (409 when nothing matches: the day moved
on since the sheet opened; the client refetches). Then:

- **Today already has a plan-made, unridden, unadjusted session** (`scheduler.IsGenerated`, not a
  test, not swapped): it is replaced **in place**, exactly like a swap: same id, so a copy on a
  head unit updates, with `planned_snapshot` recording the plan's version and the same
  `Swapped by you:` note ("Swapped by you: for a 45-minute day, was ..."). "Back to planned
  version" undoes it.
- **Otherwise** (a rider-built session, an adjusted one, a test, or nothing): a **new** workout is
  added on today, with the focus goal, and the description `Chosen by you for a day with N
  minutes.` It does not start with the generated description, so replan and the season refresh
  never delete or rebuild it, and it never replaces anything. A rider-built or ridden day is only
  ever added to, never overwritten.

A day with a ridden session can still take one (a second ride is legitimate). If today's plan
session was replaced or one was added, it is pushed to the head unit exactly as today's session
is (push-today-only), through the existing push path, when the rider has auto-push on.

Nothing is applied by GET, by opening the sheet, or by a timer.

## Interactions

- **Whole-season planning (#352).** `untouchedPlanSession` requires the exact generated
  description and `UpdatedAt == CreatedAt`; a swap fails both, so a refreshed week never
  rewrites it. A test in the alternates PRs pins this against the real function once #352 is
  merged (before that, against its stated contract).
- **Indoor conversion.** A swap builds the new session as the usual outdoor one; when the
  original is indoor the swap re-runs conversion on it (`keepIndoor`, the same hook adaptation
  uses), so an indoor session stays indoor. The rebuilt outdoor version becomes the new
  `outdoor_steps`; `planned_snapshot` holds the pre-swap indoor state. If the indoor PR has not
  merged when this lands, the hook is a documented no-op and the snapshot fields for it stay
  empty; nothing else changes.
- **FTP tests.** No alternates for a test, and a test is never a `planned` suggestion's base or
  an easing target. "I have N minutes" on a day with a scheduled test never replaces the test:
  it is not plan-made-untouched, so a suggestion is added, and the UI says so.
- **Push-today-only (#351).** Both apply endpoints go through the same push helper; a swap of a
  future session does not push (it will on its day).
- **Adaptation.** `applyChange` and the tomorrow forecast only act on `IsGenerated` sessions;
  the marker keeps them off a swapped session. The readiness card still shows the verdict for
  the day.

## Data

Idempotent add-column in `UseDB`, no new tables: `workouts.planned_snapshot TEXT NULL` (JSON,
NULL when none). Reads scan it as NULL-able. No new settings or config.

## API and UI

- `GET /api/training/workouts/{id}/alternates` -> `{options: [{kind, name, zone, level, minutes,
  tss, difficulty, warning?}], hasSnapshot}`; `POST .../alternates {kind}` -> `workoutDTO`;
  `POST .../alternates/revert` -> `workoutDTO`; `GET /api/training/trainnow?minutes=` ->
  `{minutes, verdict, suggestions}`; `POST /api/training/trainnow/apply` -> `workoutDTO`.
  `workoutDTO` gains `swapped` (bool, from the marker) and `hasPlannedSnapshot`. Owner-only; the
  rider comes from the session; a workout id that is not theirs is a 404.
- **Day card:** an "Alternates" menu (`UDropdownMenu`) next to Move, FIT and Edit, listing
  the offered options as "Easier: Threshold 4, 1h10, Achievable, 62 TSS", with the warning as a
  muted line and "Back to planned version" when a snapshot exists. Hidden for a ridden, past
  or test day. Works on whichever day the week strip has selected (#325).
- **Plan page:** an "I have ... minutes" button beside the week strip opens a slideover with the
  chip picker; picking a time fetches and shows the suggestions as cards (name, minutes, TSS,
  difficulty chip, the one-line why, any warning) each with "Use this". Result toast:
  "Put on today: Sweet spot 3, 45 min".
- Difficulty chip colours use Nuxt UI semantic colours (neutral, info, success, warning, error
  for the five labels), no raw palette.

## Testing

- `alternates`: each option on a table of rungs (rung 1 has no easier, the cap has no harder,
  cap = floor(L)+1, shorter/longer thresholds at exactly 75 % and 125 %, none-available omitted);
  endurance scaling (rounding, 30-minute floor, 6-hour ceiling); every difficulty boundary;
  ladders as shipped for both sports; a test, a ridden day and a past day yield no options.
- `trainnow`: fit under N for every N in the picker on every ladder, demotion to endurance
  when nothing fits, the 1.25 cap, `wanted` from the plan's week minus done zones, dedup, and
  readiness/load shaping (`rest` easy-only and capped, `caution` one rung down without
  `wanted`, two hard days drops `wanted` and warns on `planned`, no plan gives only `easy`).
- Storage under `TestEachEngine`: column round-trips on both engines, an existing DB gains it,
  the first swap writes a snapshot and the second keeps it, revert restores every field and is
  idempotent.
- API: a swap keeps id and date, appends the marker, leaves the level row untouched, 409 for
  ridden/past/test, `IsGenerated` and `isPlanMade` false after a swap, refresh and replan leave
  it alone, adaptation does not rewrite it; apply replaces an untouched session in place and
  adds beside a rider-built one; a stale apply 409s; the push helper runs for today only;
  owner-only 404s, another rider's workout 404.
- Fixed clocks with an explicit zone; tests pass under UTC and Europe/Brussels.

## Out of scope

A predicted-difficulty model beyond rung versus level, multi-day suggestions, alternates for
rider-built or test sessions, swapping across zones from the day card (the minutes flow does
that), learning from which suggestion a rider takes, and any provider work.
