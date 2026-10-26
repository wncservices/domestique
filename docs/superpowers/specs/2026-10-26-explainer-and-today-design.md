# Session explainer and an interactive Today page — design

Status: approved 2026-10-26. There are two parts, shipped as two PRs: the explainer
first, then Today.

## Why

A planned session tells a rider what to do, as a name, steps and watts, but not
what the session is for or how it should feel. The Today page shows the day,
but nothing on it can be acted on: every action lives on the Plan page.

## 1. Session explainer

### What a rider sees

On a planned session there are two parts:

- **Why it's in your plan.** One line built from the plan without the model, for
  example "Build week 3 of 12 for Spring sportive · recovery week next".
  - It uses the week's phase, the week number and total weeks, the goal name, and
    whether next week is a recovery or taper week.
  - A session with no goal shows no line.
- **How to ride it.** It starts with 2–4 sentences on what the session trains. After
  that comes one short line per block on how to ride it: effort feel, cadence, what to
  do in the recoveries.

It appears in two places:

- A "How to ride it" section in the session panel (`WorkoutSlideover`).
- A collapsed "About this session" disclosure on the session card on Today and on
  Plan's today card.

### Key and cache

- **Key.** Sessions come from a fixed workout library, but they are edited, eased and
  repaid, so the key is the session's **shape**, not a template ID. The shape is
  `sha256` of a canonical text made of:
  - the sport and the name;
  - each step's kind and duration;
  - each step's target as %FTP or zone, never watts, plus its repeats.
- **Who shares it.** Two riders with the same session share one explanation. The
  shape holds nothing about the rider.
- **Table.** `workout_explanations(key TEXT PRIMARY KEY, text TEXT NOT NULL,
  created_at TEXT NOT NULL)`.
  - It has no rider column, so it is not in `riderTables`: it describes a workout,
    not a person.
  - A claim row (empty text) prevents two model calls for one key. The claim is
    released on a cancelled call or a failed save, the same rule as `llm_notes`
    after the review fix.

### The job (`explain_workout`, in `internal/llm`)

It follows 6a's pattern:

- **Output.** The schema is `{summary, blocks: [{label, cue}]}`.
- **Limits.**
  - `summary`: 1–4 sentences, at most 400 runes.
  - `blocks`: 1–8 entries, each `cue` at most 160 runes.
  - `max_tokens` 500.
- **Validation.** The no-invented-numbers check runs against the canonical shape
  text, so every number must come from the steps.
- **Input.** The canonical shape only, so no rider data reaches the model.
- **Switch.** It can be switched off with `llm.jobs.explain_workout`.

### When it is written

- **Morning notes pass.** After the changes and weekly notes, the pass collects the
  distinct shapes of the next 7 days' planned sessions across riders with a goal. It
  writes each one that is missing, at most 10 per pass.
- **On demand.** `GET /api/training/workouts/{id}/explanation` returns
  `{why, summary, blocks, source}`.
  - It reads the cache, writes a missing entry within the 90 s request cap
    (`llmRequestContext`), and stores it.
  - Access is owner only, with the same 404 rule as the other workout endpoints.
- **Fallback (`source: "template"`).** Used when the job is off, no model is
  configured, or the model fails. It is a fixed explanation per zone, written in
  code (endurance, tempo, sweet spot, threshold, VO2max, anaerobic, recovery, test).
  It is not stored, so a later request can still get the model's text.

### Evaluation

`testdata/eval/workout.json` holds 8 shapes, one per zone. A case passes when the job
returns without error, since the validator already enforces the limits and the
number rule.

## 2. Today page

Plan stays the place for the week and its editing. Today gets four pieces that act
on today, with no day-by-day list.

1. **Act on today's session.** The session card gets these actions:
   - **Send to Garmin**, or **Indoor version** for an indoor rider. They use the
     existing endpoints.
   - **Make it easier / shorter.** The existing alternates (`GET/POST
     .../alternates`) open in a small menu. **Undo** uses `.../alternates/revert`.
   - **Not today.** It moves the session to the next free available day with the
     existing move rules (the same check the Plan page's Move uses). A toast names
     the new day, with Undo.
     - It is hidden for crew rides, FTP tests and sessions already ridden, the same
       as on Plan.
2. **Readiness you can open.** The Ready / Caution / Rest badge becomes a button.
   - It opens a popover with the verdict, the reasons (the `reasons` that
     `/api/training/readiness` already returns), what it changed today (with the
     session's why chip), and a link to Fitness.
   - It shows no raw HRV or sleep numbers beyond what the reasons already say.
3. **Last ride feedback.** A card for the most recent completed ride from the last 3
   days that has no feel rating yet.
   - It shows the ride's name, date, duration and outcome (nailed / partial / missed).
   - It reuses `FeelRating` (feel 1–5, legs, stress) with `PUT
     /api/training/sessions/{id}/feel`.
   - It folds to a one-line thank-you once answered, and is gone the next day.
4. **Week load ring.** A ring with this week's TSS done against the target.
   - The figures are the ledger's effective target, ridden TSS and debt still to make
     up, from `GET /api/training/week`'s focus.
   - Under it is one line: "65 of 410 TSS · 30 to make up". A link goes to Plan.
   - It is hidden when the week has no CTL-based target.

### Rules

- **Components.** Everything uses Nuxt UI components and semantic tokens. That means
  the shared link style, button sizes (`sm` for card actions, at most one solid
  primary per card) and chip styles from the UI-review fixes.
- **Mobile layout.** At 375 px the order is: greeting, check-in, session card with
  its actions, readiness, last ride feedback, then week ring.
- **No new data.** Only new UI over existing endpoints, except the explainer
  endpoint.

## Testing

- **Explainer.**
  - Go: unit tests for the canonical shape (watts never appear; an eased session
    gets its own key), the job validator, and the claim release.
  - API: cache hit (no model call), cache miss (one call, then stored), the template
    fallback when the job is off, owner-only access.
  - Eval cases.
- **Today.**
  - Typecheck and a browser check at desktop and 375 px, in light and dark.
  - Each action against a stub or local server: an alternate is applied and undone;
    Not today moves the session and Undo restores it; Send to Garmin goes through
    the fake; the feel rating saves and the card folds; the ring matches the week
    figures.

## Out of scope

- A day-by-day week on Today.
- Explanations for running sessions beyond the zone template fallback (no
  running-specific eval set yet).
- Model-written "why this week" text: that line comes from the plan, not the model.
