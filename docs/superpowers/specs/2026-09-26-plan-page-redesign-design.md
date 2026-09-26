# Plan page redesign — design

Status: approved 2026-09-26.

## Why

Training → Plan (`apps/web/src/pages/TrainingPlanPage.vue`) is two stacked
lists — goals and workouts — with no sense of time. A rider cannot see what
to do today, what this week holds, or whether they did what was planned; the
periodized plan is hidden behind a per-goal toggle as a table of ISO dates;
"Schedule this week" and "Explain" only appear inside that expansion; and the
workout builder is a wall of number inputs in raw seconds and absolute watts.

## What riders get

The page answers **today and this week first**, the pattern JOIN and
TrainerRoad converge on, with the planned-vs-completed week TrainingPeaks and
intervals.icu are known for, and TrainerRoad's phase timeline for the season.

Top to bottom:

1. **Goal header** — focus goal name + priority, days to go, phase, "week N
   of M". Goal switcher when there is more than one goal. "Explain" (only
   when `me.narrationEnabled`) and a "New" menu (goal / workout). Replaces the
   static "Manual builder" alert.
2. **Today card** — today's planned session: name, duration, target range
   (as % FTP/threshold *and* absolute), an interval-profile chart, actions:
   push to Garmin (when `canSyncGarmin`), move, download FIT, edit. States:
   rest day, done (actual vs planned), yesterday missed, nothing planned.
3. **Week strip** — seven day tiles Mon–Sun, each showing planned session(s)
   and completed session(s), tinted by status. Header: prev/next week,
   phase badge, planned-vs-done hours progress bar against the week's target.
   The wand icon + tooltip shows the scheduler's adjustment note
   (`Adjusted automatically:` marker, already parsed today). Drag a session
   to another day to move it (`updateWorkout` with the new date); a "Move
   to…" menu on each session is the keyboard/touch equivalent. An empty
   week with a schedulable focus goal shows "Fill this week" (the existing
   `scheduleGoal` call).
4. **Season timeline** — one bar per week (height = target hours), grouped
   into phase bands, recovery weeks hatched, markers for today and the
   event. Clicking a week moves the week strip there. Replaces the
   periodization table.
5. **Goals + library** (secondary) — compact goal cards (edit/delete, set
   as focus) and the undated workouts (tests, templates) as a short list.
6. **Empty state** (no goals) — one invitation with the three existing
   shortcuts: describe it (narration only), start from a route, keep
   training (general fitness).

Goal and workout forms move from `UModal` to `USlideover`.

**Workout builder**: live interval-profile chart at the top; durations typed
as `mm:ss`; targets typed as % of FTP / threshold HR / threshold pace when the
profile has that value, converted to the absolute number on save (storage is
unchanged — `internal/fitworkout.Target` stays absolute), absolute input as
fallback; quick-add chips for warmup, N × work/rest intervals, cooldown;
running total of duration.

## API

One new endpoint, owner-only like every other training route
(`PermManageTraining`, `trainingAvailable`):

`GET /api/training/week?start=YYYY-MM-DD` — `start` defaults to the Monday of
the current week (server clock, `s.now()`); a non-Monday is snapped back to
its Monday; an unparsable value is 400.

```jsonc
{
  "start": "2026-09-28", "end": "2026-10-04", "today": "2026-09-29",
  "focus": {                      // omitted when the rider has no goal
    "goalId": "gran-fondo", "name": "Local Gran Fondo", "priority": "A",
    "sport": "cycling", "eventDate": "2026-11-10", "daysToEvent": 42,
    "weekNumber": 7, "totalWeeks": 12,
    "phase": "build", "recovery": false, "targetHours": 7.5
  },
  "days": [{
    "date": "2026-09-28",
    "status": "done",             // done|partial|missed|rest|upcoming|unplanned
    "planned": [ /* workoutDTO, now incl. plannedSeconds */ ],
    "completed": [ /* completedSessionDTO */ ]
  } /* ×7 */],
  "totals": { "plannedSeconds": 27000, "completedSeconds": 7440 }
}
```

`workoutDTO` gains `plannedSeconds` (from `workout.PlannedSeconds`), on every
endpoint that returns it.

### Matching — `internal/compliance` (new, pure)

`Day(date, today string, planned []workout.Workout, completed []workout.CompletedSession) Status`,
plus a `Week` helper that buckets by date. Rules:

- Only completed sessions whose sport matches a planned workout's sport
  count toward it; others still appear in `completed` but not in the ratio.
- ratio = matched completed seconds / planned seconds (sum over the day).
- planned seconds 0 (open/distance-only steps) with any matching completed
  session → `done`.
- `done` ≥ 0.8; `partial` ≥ 0.3; otherwise `missed` if `date < today`,
  `upcoming` if `date ≥ today`.
- no planned workout: `unplanned` if anything was completed, else `rest`.

### Focus goal

Among the rider's goals, those whose reconciled plan has a week containing
`start`; pick highest priority (A > B > C), then nearest event date, dated
before undated. No such goal → first goal by the same ordering with no week
fields (phase/week/target omitted). Uses `reconciledPeriodizationPlan`, so
hours match what the scheduler asks for.

## Frontend structure

`TrainingPlanPage.vue` becomes a thin page that loads data and composes:

| Component | Purpose |
|---|---|
| `components/plan/PlanGoalHeader.vue` | goal header + switcher + New menu |
| `components/plan/TodayCard.vue` | today's session and its states |
| `components/plan/WeekStrip.vue` | seven tiles, nav, progress, drag/move |
| `components/plan/SeasonTimeline.vue` | SVG phase timeline |
| `components/plan/WorkoutProfile.vue` | SVG interval profile (shared by today card, tiles, builder) |
| `components/plan/GoalsSection.vue` | goal cards + undated workouts |
| `components/plan/PlanEmptyState.vue` | the three shortcuts |
| `components/plan/GoalSlideover.vue` | goal form (moved from the page) |
| `components/plan/WorkoutSlideover.vue` | workout builder shell |
| `utils/workoutMath.ts` | flatten steps, seconds ↔ `mm:ss`, %↔absolute, intensity factor per step |

`WorkoutStepEditor.vue` is reworked in place (mm:ss, % targets, quick-add).

Types in `api/types.ts` (`TrainingWeek`, `WeekDay`, `DayStatus`,
`WeekFocus`, `Workout.plannedSeconds`) mirror the Go DTOs by hand, per
AGENTS.md.

## Design-system rules applied

- Nuxt UI components and `styles.css` tokens only; no raw Tailwind colours,
  no `dark:` variants.
- Semantic colours mean compliance status only: `done` success, `partial`
  warning, `missed` error, `upcoming` neutral, `rest` dimmed, `unplanned`
  info. Every status also has an icon — never colour alone.
- Phase bands use categorical accents: base neutral, build sky, peak ember,
  taper violet (not `primary`, reserved for the brand/CTA). Interval
  profile bars by intensity: recovery/warmup/cooldown neutral, active sky,
  interval ember.
- Durations/hours/watts in `font-mono tabular-nums`.
- One primary CTA per section.
- Mobile (< `sm`): week strip becomes a horizontally scrollable row of
  day tiles, today card full width, slide-overs full screen.
- No new dependencies: native HTML5 drag-and-drop, hand-rolled SVG (same
  as `TrackPreview.vue`/`FitnessChart.vue`).

## Testing

- `internal/compliance`: table tests for every status boundary.
- `internal/api`: handler tests for `/api/training/week` (default start,
  snapping, bad start 400, other rider's data never included, focus
  selection, totals) under `TestEachEngine`; acceptance test entry.
- `just check` green (typecheck, vet, go test).
- `utils/workoutMath.ts` round-trips exercised by a small script if no JS
  test runner exists (there is none today; do not add one for this).
- Manual/Playwright walkthrough of `just demo` in light + dark, desktop +
  375px, screenshots attached to the PRs.

## Delivery

Four PRs, stacked:

1. API — `internal/compliance`, `GET /api/training/week`, `plannedSeconds`.
2. Today card + week strip + page shell (goal header, move/drag).
3. Season timeline, goals section, empty state.
4. Goal and workout slide-overs, reworked step editor.

## Out of scope

Month calendar view; multi-rider/coach views; Wahoo structured-workout push;
changing how the scheduler generates or adapts sessions.
