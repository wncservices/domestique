# Life events and natural-language plan edits — design

Status: draft 2026-09-29. Builds on whole-season planning and refresh (#352), manual moves (#336),
readiness (`2026-09-28-readiness-design.md`) and FTP tests (`2026-09-29-ftp-tests-design.md`).
Written to compose with two unmerged pieces, alternates (`2026-09-29-alternates-design.md`) and
indoor conversion (`2026-09-29-indoor-and-weather-design.md`), and with the ride survey / "Why?"
feature (`2026-09-29-ride-survey-and-why-design.md`, not on `main` when this was written); see
"Interactions".

## Why

The plan assumes a rider who is available and well. Real weeks are not: a work trip, a cold, a
week of deadlines. Today the rider deletes or drags each session by hand, then finds the next
Replan or refresh has put some of them back, and a plan-made session left on a sick day is
"missed" to adaptation. Humango's AI coach handles this in conversation ("I'm travelling Thursday
to Sunday" reshuffles the week); TrainerRoad and JOIN handle illness and life events with
plan-side rules. Domestique already has the ingredients: drag-to-move with `MovedFrom`, Replan,
a fill-once season, readiness, and an LLM package that turns free text into proposals.

Two pieces, in this order of importance:

1. **Life events**: a stored date range (travel, illness, busy, other) with deterministic rules
   that produce a **preview** of plan changes the rider confirms. No LLM.
2. **Natural-language edits**: with `ANTHROPIC_API_KEY`, a text box turns a sentence into the
   same structured requests, validated by the same rules, shown in the same preview. The model
   proposes; nothing is applied until the rider confirms.

## Life events

Table `life_events` (rider, owner-only): `id`, `rider`, `kind` (`travel` | `illness` | `busy` |
`other`), `start_date`, `end_date` (inclusive, `YYYY-MM-DD`), `option`, `note`, `created_at`,
`updated_at`. `option` by kind:

| Kind | Options | Default |
|---|---|---|
| `travel` | `no_bike` (nothing can be ridden) or `gym` ("hotel gym / can run") | `no_bike` |
| `illness` | `mild` (symptoms above the neck) or `proper` | `proper` |
| `busy`, `other` | none | none |

`note` is free text for the rider (max 200 characters), never sent to a model and never logged.
Limits: `start_date` no earlier than 7 days ago (a retroactive "I was ill Monday to Wednesday"
still triggers the return ramp), `end_date` at least `start_date` and at most 42 days after it.
Two events of the **same kind** may not overlap (409, edit the existing one); different kinds may
(illness while travelling is real) and their day sets are unioned.

**`blackout(events)`** is the set of dates inside any event. It is the single fact everything
else consults.

### Preview, then apply

`lifeevents.Preview(in) Diff` is a pure function: events, the rider's workouts, sessions, profile
and `now` in; an ordered list of changes out. It never writes. A change has a deterministic id
(`<op>:<workoutId>`) and an op:

| Op | Meaning |
|---|---|
| `remove` | delete the session (its Garmin copy comes off as replan does) |
| `move` | new date, same session |
| `ease` | the easier rung of the same zone (the ramp; reuses `scheduler.EasyVariant` or the alternates' *Easier*) |
| `shorten` | scale an endurance session down |
| `indoor` | convert to the indoor version (only when indoor conversion has landed) |
| `add` | a session regenerated from the plan on a day an edited or deleted event freed |

Each change carries a one-line reason. The preview also lists what it **leaves alone** and why
(a session already ridden, one the rider built, one already adjusted).

The apply call takes the same inputs plus `skip: [changeId]`. The server **recomputes** the diff
and applies it minus the skipped ids; it never trusts a diff sent by the client, so a plan that
changed between preview and apply (a background tick, another tab) cannot be half-applied from a
stale picture. Ids that no longer exist are ignored. It runs under the scheduling advisory lock
replan uses (`autoScheduleLockKey`) and returns 409 with the replan wording when the tick holds
it.

### Markers

- A moved session gets `Rescheduled by a life event: moved from YYYY-MM-DD.` in the form
  `scheduler.MovedFrom` already reads, so scheduling still treats the vacated day as taken, plus
  `AdjustedMarker`, so it is no longer `IsGenerated` and adaptation does not touch it again.
- An eased or shortened session gets `AdjustedMarker` and `Life event: eased after illness
  (return to training)` style notes. That is the one-automatic-change-per-workout guarantee.
- A removed session leaves nothing behind, so **the blackout is what keeps it removed** (below).
- Every note says which event caused it; with the survey/"Why?" feature landed these feed its
  "Why?" entries; until then they are the description note the rider already sees.

### Travel and busy (and other)

`no_bike`, `busy` and `other` behave the same: nothing is scheduled in the range, but the week's
work is kept where a sensible day exists.

Scope: plan-made, unridden sessions dated today or later inside the range. Rider-built sessions
and already-adjusted ones are listed under "left alone" with an opt-in `remove` change that is
**unticked** by default; a session the rider made is theirs.

Placement, per affected week (Monday to Sunday), deterministic:

1. **Free day** = in the rider's `AvailableDays`, today or later, outside the blackout, with no
   workout of any kind, and not the vacated `MovedFrom` day of an earlier move.
2. Order sessions: the week's **key session** first (the longest `IsKeySession`, else the longest
   session), then the other hard sessions, then endurance. Ties break by planned date.
3. Each goes to the nearest free day by distance, ties to the later day. One session per day.
4. **Never two hard days in a row**: a hard session (`IsHardSession`) may not take a day whose
   neighbour holds a hard session, existing or already placed this pass. A long ride is not hard
   and may follow one.
5. **Never more load**: moves preserve each session and never add one, so a week's planned
   seconds after are at most before. A week with fewer free days than sessions loses its
   lowest-priority sessions (endurance first, key last) with reason "no free day this week".
6. The move stays **inside the same week**. A key session that cannot be placed is removed with
   that reason rather than pushed into the next week's plan; the fill-once season owns next week.
7. An **FTP test** in the range moves with the rider (`TestProtocol != ""`): same placement
   but as an ordinary session, and if the week has no day it is removed and the FTP-test banner
   suggests another (that suggestion is computed on read, so it already does). The day-before
   easing rule runs afterwards in `adaptRider` and follows the test to its new day.

`gym` (travel only): each plan-made **cycling** session in the range stays on its day as an
indoor endurance session of at most 60 minutes (`indoor` plus `shorten`), because a hotel bike
is not a threshold session; running sessions are left as they are ("can run"). The week's key
session is additionally placed on a free home day by rules 1 to 6 if one exists. Without indoor
conversion (not merged) the option is hidden and the API returns 400 for it.

### Illness

Rules follow the "neck check" and conservative return-to-training guidance; sources at the end.
The day counts are this design's choice informed by them, **not a clinical protocol**.

- **`proper`** (fever, chest symptoms, body aches, or "not sure"): every plan-made session in
  the range is removed. No compensation moves: sickness is not made up.
- **`mild`** (symptoms above the neck only): hard and structured sessions in the range are
  removed; a remaining endurance session is shortened to at most 45 minutes. The copy says to
  stop and switch to `proper` if symptoms move below the neck.

**Return-to-training ramp**, from the day after `end_date`, for `d = end - start + 1` days:

| | Easy days | Then, hard sessions eased one rung until |
|---|---|---|
| `mild` | 1 | day 3 |
| `proper`, d < 5 | 2 | day 7 |
| `proper`, 5 <= d < 10 | 3 | day 7 |
| `proper`, d >= 10 | 3 | day 14 |

An **easy day** turns a generated, unadjusted session into `EasyVariant`, capped at 60 minutes
(an already-easy one is only capped). **One rung** eases a hard structured session to the rung
below in its zone. Long rides are kept in the easy window but capped at 90 minutes. The ramp only
touches sessions that are `IsGenerated`; rider-built and already-adjusted sessions are never
rewritten, and a session a life event already changed is not changed twice. An illness of 14 or
more days adds a "see a clinician before resuming hard training" line to the preview.

The preview covers sessions that exist now. **Weeks filled later** (the season is planned ahead
and refreshed as weeks come into reach) get the same ramp from `adaptRider`, which is where every
other automatic rule already runs after each schedule and replan; it reads the events, so an
event created once keeps applying. This is the only place a change is made without a fresh
confirmation, and it is confirmed in the sense that the rider confirmed the event and its
ramp is stated in the preview.

An FTP test that falls in the ramp window (`end + 1` to `end + 7`) moves to the first eligible
day on or after `end + 8` (same eligibility as the test scheduler: an available day, no key
session the day before), else it is removed and re-suggested.

### Ending and editing

`PUT /api/training/life-events/{id}` edits dates, option or note; `DELETE` removes the event.
Both preview first (`dryRun`), both go through the same recompute-and-apply. **Ending early** is
an edit with `end_date` set to yesterday (today is then the first day out of the event; for
illness, ramp day 1); an `end_date` before `start_date` deletes the event.

Shortening or deleting frees days. Removed sessions were deleted, so **freed days are refilled
from the plan**: the same `scheduler.WeekWorkouts` the refresh uses, filtered to freed dates that
are today or later, not in the blackout, not taken (any goal) and not a `MovedFrom` vacated day.
The refill appears in the preview as `add` changes, and the fill-once bookkeeping
(`scheduled_weeks`) is unchanged: the week stays recorded. Lengthening an event runs the normal
rules over the newly covered days only. Ramp changes already made are not reversed (the marker
says they were made); the event's new end date applies to sessions not yet adjusted.

### The blackout is enforced everywhere plans are made

This is the piece that makes a removal stick, and the one most easily missed:

- `fillWeek`, `refreshWeek` and `scheduleGoal` (so Replan and the tick) **skip blackout dates**.
  A season is planned months ahead: an event created today must also hold for a week that is
  filled next month, and an event created before its weeks are filled must keep those weeks
  clear.
- The day-taken set (`alreadyScheduled` plus `MovedFrom`) gains the blackout, so nothing is
  generated on an event day.
- `adapter.AdaptSessions` never chooses an event day as a target (missed-session make-up, the
  easy swap) and a session removed for an event is never "missed" (it does not exist).
- **Readiness** is unaffected except in display: the day card on an event day shows the event
  instead of a session and hides the readiness chip, and the tomorrow advisory is suppressed
  when tomorrow is an event day. Readiness only ever eases (`readiness.Verdict`), so no floor is
  added during the ramp; the ramp is its own rule.
- Adaptation guarantees are unchanged: at most one automatic change per workout, and a
  rider-touched session is never rewritten.

## Natural-language edits

Only when `Server.Narration != nil` (`ANTHROPIC_API_KEY` set; the same gate and
`NarrationEnabled` flag the goal and profile proposals use). `POST
/api/training/plan/propose-edit` with `{ "text": "..." }` (max 300 characters), under
`NarrationLimiter`, owner-only, returns a preview: it never applies. Applying is the ordinary
apply call for each accepted item, from the same modal.

### What the model sees

A new `narration.ProposePlanEdits(ctx, text, view)`. The model receives:

- today's date and its weekday (it has no clock), and the resolved date range it may pick from
  (today to today + 60 days);
- the rider's available days;
- the **next 14 days of the plan**: per day, date and weekday, and for each session an opaque
  handle (`w1`, `w2`, ...), its generated name ("Threshold 3x12"), sport, zone, planned minutes
  and whether it is an FTP test, indoor, ridden or rider-built;
- the note the rider typed.

It is **not** sent: the rider's name or account, FTP, watts, heart rate, readiness or any
wellness value, goal or event names, routes, coordinates, workout database ids (handles are
opaque and map back server-side), or ride history. The typed note is the only free text and may
itself mention health ("I'm sick"); the UI says, next to the box, that the sentence is sent to
Anthropic. The note is never logged or stored (only the rider, the outcome and the intent count).
The call is stateless, one turn, no conversation history.

### Strict structured output

One tool, `propose_plan_edits`, with `strict: true` and `additionalProperties: false`, called via
`tool_choice: auto` plus an instruction to call it exactly once. **Forced `tool_choice`
(`any`/`tool`) is not used**: it returns a 400 on the newer models (Opus 5.5, Sonnet 5.5, Fable
5.1), and the package's model may be bumped independently of this feature. (`output_config.format`
structured output is the alternative; tool use is chosen because the package already speaks the
tool block shape and the schema is small.) `narration` today is a hand-rolled `net/http` client
with `model = "claude-opus-5"`; this feature keeps both and does not touch the model constant.

```json
{
  "intents": [
    {
      "type": "create_life_event | move_workout | swap_alternate | convert_indoor",
      "kind": "travel | illness | busy | other | null",
      "start_date": "YYYY-MM-DD | null",
      "end_date": "YYYY-MM-DD | null",
      "option": "no_bike | gym | mild | proper | null",
      "workout": "w1 | null",
      "to_date": "YYYY-MM-DD | null",
      "alternate": "easier | harder | shorter | longer | null"
    }
  ],
  "unsupported": "string | null"
}
```

`intents` has at most 5 items. The schema is **built per deployment**: `swap_alternate` is
included only when alternates have landed and `convert_indoor` only when indoor conversion has,
so the model cannot propose what the server cannot do.

### Server-side validation (the model is untrusted)

The tool input is parsed and every field re-checked; anything that fails is **dropped with a
visible reason**, never repaired:

- unknown `type`, extra properties, more than 5 intents, a wrong type: the whole reply is
  rejected (502-style "couldn't understand that, use the form");
- dates: valid, `create_life_event` per the limits above, `to_date` today or later and within 60
  days; `to_date` must be one the move rules allow (available day, not a blackout day, no
  hard-hard adjacency);
- `workout` must be a handle from the request and resolve to a session the rider owns that is
  plan-made, unridden and dated today or later (an FTP test can be moved, not swapped or
  converted);
- `kind` and `option` must be a legal pair from the table above;
- `unsupported` is shown as plain text (max 140 characters), never interpreted, never HTML.

Each surviving intent goes through **the same code as the form**: `create_life_event` calls
`Preview`; `move_workout` is a single `move` change validated by the placement rules;
`swap_alternate` and `convert_indoor` are single changes validated by the alternates and indoor
rules. The modal shows them as one combined diff, each item individually skippable. A
prompt-injection in the rider's own text can at worst propose something the rules already allow,
which the rider then declines; there is no path from model output to a write.

The prompt states "reply only through the tool", gives the schema meaning, and tells the model to
prefer `unsupported` over guessing a date.

### Failure

Model error, timeout (the package's 30 s), refusal, or a reply with no tool block: 502 with the
usual narration message and a Warn log (rider, outcome). The deterministic form is unaffected; no
retry loop.

## UI

Plan page (`TrainingPlanPage.vue`), YAGNI:

- **"Life event" button** in the page actions opening `LifeEventModal.vue`: kind, start and end
  date, the kind's option, a note; a "Preview" step shows the diff.
- **`LifeEventPreview.vue`**: changes grouped by day with the reason, a checkbox per change
  (rider-built removals unticked), a "Left alone" section, the clinician line when it applies,
  "Apply" and "Cancel". Nothing writes before Apply.
- **Event bands** on `WeekStrip.vue` (a tinted band with the kind, on every covered day) and
  `SeasonTimeline.vue` (a bar over the range); the day card on an event day shows the event with
  "Edit" and "I'm back" (ending early; for illness it asks "Feeling it below the neck?" first, per
  the neck check, with a "Not yet" that keeps the event).
- **NL box** above the week strip when `narrationEnabled`: one line, "Tell me what changed",
  helper text that the sentence is sent to Anthropic, submit opens the same preview modal seeded
  with the proposal and any dropped intents' reasons.
- Nuxt UI semantic tokens only; typed template handlers.

## Data

Idempotent `CREATE TABLE IF NOT EXISTS life_events` in `workout.UseDB`, via `dbx` (`?`
placeholders rebound per engine, dates as `TEXT`, index on `(rider, start_date)`), no other schema
change. `internal/lifeevents` holds the pure rules; the store methods sit on `workout.DB` beside
goals and workouts.

## API

Owner-only, rider from the session (never the body), 404 on someone else's id:

- `GET /api/training/life-events` -> events overlapping today - 7 days onward.
- `POST /api/training/life-events` `{kind, startDate, endDate, option?, note?, dryRun?, skip?}` ->
  `{event?, diff, applied?}`.
- `PUT /api/training/life-events/{id}` same body; `DELETE /api/training/life-events/{id}` with
  `?dryRun=1` -> `{diff}`.
- `POST /api/training/plan/propose-edit` `{text}` -> `{ items: [{intent, diff}], dropped:
  [{reason}], unsupported? }`. 412-style guard with a Warn log when no key.
- `workoutDTO` unchanged apart from the notes already in `description`; `trainingWeekDTO` gains
  `lifeEvents` for the days it covers.

## Testing

- `lifeevents`: placement (nearest free day, later on ties, key first, one per day, never
  two hard in a row, same week only, never more load), `gym`, each illness row of the ramp table
  including the day counts and the caps, an FTP test moved and re-suggested, blackout union, limits
  and same-kind overlap, refill on shortening.
- Storage under `TestEachEngine`; an existing DB gains the table; store owner-only.
- API: preview writes nothing; apply recomputes and honours `skip`; a plan changed between
  preview and apply is not half-applied; replan, refresh, the tick and a season pass all leave
  the blackout empty; a life-event move does not get re-created on its vacated day; adaptation
  never targets an event day; one automatic change per workout still holds after a ramp.
- Narration with a fake client (no network): well-formed reply, unknown field, extra intent, bad
  date, foreign handle, `unsupported`, no tool block, model error; the **request body is asserted**
  to contain no name, FTP, watts, heart rate or database id.
- Fixed clocks with an explicit zone; tests pass under UTC and Europe/Brussels.

## Out of scope

Reopening the LLM as a coach (multi-turn chat), applying anything without the preview,
restoring removed sessions byte for byte, easing after long travel, holidays synced from a
calendar, multiple riders' events, illness from wellness data, `cancel a session` as its own
intent (a one-day busy event does it), a free-text `note` sent to a model.

## Interactions

- **Ride survey / "Why?"** (unmerged): change notes are written in the shape it reads; if it lands
  first, each change also becomes a "Why?" entry, otherwise the description note is the whole
  surface. Nothing here depends on it.
- **Alternates and indoor conversion** (unmerged): `ease` and `swap_alternate` reuse alternates'
  *Easier*, `indoor` and `gym` reuse indoor conversion; each is capability-gated so this feature
  ships without them and gains them when they merge.
- **FTP tests**: a test moves with the rider; the day-before easing follows it; the banner
  re-suggests when it could not be placed.
- **Push-today-only (#351)** and Garmin copies: a removed or moved session's Garmin copy is
  removed as replan does; nothing is pushed by a life event.

## Sources

- Neck check ("above the neck" symptoms allow light exercise, "below the neck" symptoms such as
  fever, chest congestion or body aches mean rest): USA Triathlon, "The Neck Rule"
  (https://www.usatriathlon.org/articles/training-tips/the-neck-rule); "Acute Illness in the
  Athlete", PMC7126929 (https://pmc.ncbi.nlm.nih.gov/articles/PMC7126929/), which describes
  starting with 10 to 15 minutes of light exercise for above-the-neck symptoms and withholding
  activity until below-the-neck symptoms resolve. The rule is generally attributed to Eichner
  (1993, from memory, not re-checked).
- Graduated return after a viral illness: Elliott et al., "Infographic. Graduated return to play
  guidance following COVID-19 infection", Br J Sports Med 2021
  (https://pmc.ncbi.nlm.nih.gov/articles/PMC7371566/): stages of incremental load that begin only
  after a rest period and a symptom-free interval. Used here as the model for an easy-first ramp,
  **not** applied as a COVID protocol; this design's ramp is shorter and less conservative.
- Short-term detraining, why a missed week is cheap and a moved week is not needed: Mujika and
  Padilla, "Detraining: loss of training-induced physiological and performance adaptations", Sports
  Med 2000 (from memory, not re-fetched; verify before quoting in UI copy).
- Competitors' behaviour (Humango, TrainerRoad, JOIN): from the brief this was written from, not
  re-verified.
