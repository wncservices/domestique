# LLM jobs — design (sub-project 6b)

Status: approved 2026-10-26. 6b implemented 2026-10-26. This is the second half of section 6 of
`2026-10-18-adaptive-training-builder-design.md`. It builds on 6a
(`2026-10-25-local-llm-design.md`), which supplies the `internal/llm` client.

## Why

The deterministic engine already eases, swaps and repays sessions every morning, and
riders write check-in notes. Three things are missing:

- an explanation of the morning's changes in plain words;
- a reading of the note that the rider can act on in one tap;
- a short look back at each week.

The model explains and proposes. It never decides a number, and nothing it returns is
applied without the rider's tap.

## Decisions

| Question | Decision |
|---|---|
| Where the morning-changes explanation appears | The Today page only. The morning email is unchanged: no reasons, no numbers. |
| What a check-in reading may do | Suggest only. The rider confirms with a tap, which fills empty scores or opens the life-event preview. |
| Weekly summary | Generated in the background on Monday morning and stored. It shows as a dismissible card on the Plan page all week, in the app only. |
| Base | Stacked on 6a (#503). The intensity mix (5a, #501) is not on `main` yet; the summary's mix input stays empty until it is. |

## Jobs (`internal/llm`)

Each job follows 6a's pattern:
- a JSON schema with `additionalProperties: false`;
- a validator and one retry through `Complete`;
- a system prompt that says the data is data, not instructions.

Each job is a `Job` constant, an entry in `llm.Jobs` and `config.LLMJobNames`, and can be
switched off with `llm.jobs`.

| Job | Output | Limits | `max_tokens` |
|---|---|---|---|
| `explain_changes` | `{text}` | 1–3 sentences, at most 300 runes | 300 |
| `understand_checkin` | `{legs, sleep, mood, life_event}` | scores 1–5 or null; at most one life event | 250 |
| `weekly_summary` | `{text}` | at most 5 sentences, at most 700 runes | 450 |

- **`explain_changes`.**
  - Input: today's `adjustments` rows for the rider, each with its rule title (`why.Title`), its facts (`why.Facts`), and its workout's name and date.
  - Validator:
    - The text is non-empty and within the limit.
    - **No invented numbers**: every number in the text appears in the input facts or dates. This uses a shared `numbersIn(text)` helper.
- **`understand_checkin`.**
  - Input: today's date with its weekday, the next 14 days with their weekdays, and the note.
  - Output fields:
    - `legs`, `sleep` and `mood` are each `1..5` or null.
    - `life_event` is null or `{kind: illness|travel, start_date, end_date, option}`.
    - `option` follows the life-event options: `no_bike`/`gym` for travel, `mild`/`proper` for illness, or null.
  - Validator:
    - Enums.
    - Dates as `YYYY-MM-DD`, with start ≤ end.
    - Start on or after today and end within today + 60.
- **`weekly_summary`.**
  - Input: the week's Monday, its phase, and the figures below. A figure that is unknown is left out, never zero.
    - From the ledger: target, ridden, debt and forgiven TSS.
    - From compliance: sessions done, partial and missed.
    - CTL at the start and at the end of the week.
    - The intensity mix, once 5a is merged.
  - Validator: within the limit, plus the no-invented-numbers rule.

## Storage

- **Table.** `llm_notes(rider, kind, day, text, created_at)`, with a unique key on `(rider, kind, day)`.
  - `kind` is `changes` (day = today) or `weekly` (day = the summarised week's Monday).
  - It is registered in `riderTables`: purged with the rider; under rename, rows move; under merge, the target's row wins on the key.
  - Rows older than 60 days are deleted by the background run.
- **What is stored.** Only validated text. No prompt and no model output that failed validation.
- **Store API** (`workout` package):
  - `SaveLLMNote(ctx, rider, kind, day, text) error`, an upsert;
  - `GetLLMNote(ctx, rider, kind, day) (string, bool, error)`;
  - `ClaimLLMNote(ctx, rider, kind, day) (bool, error)`, which inserts an empty row once so replicas never both call the model;
  - `PruneLLMNotes(ctx, before string) error`.
  - A claimed row with empty text means "claimed, not written". The read endpoints treat it as absent.
- **New read for adjustments.** `AdjustmentsOnDay(ctx, rider, day) ([]Adjustment, error)`, backed by an index on `(rider, day)`.

## Background run

- **When it runs.** `runLLMNotes(ctx)` runs once `AutoScheduleTick` has finished, from the same loop, **after** the scheduling lock is released. It only runs in the first sync slot of the day (`inMorningSlot`) and only with auto-schedule on, because the changes it explains only happen then.
  - It takes its own advisory lock (`llmNotesLockKey`). It skips if the lock is held.
  - It runs on the tick's goroutine. Nothing else waits for it: the push has already happened.
- **Each rider with a goal.**
  - `changes`: if `AdjustmentsOnDay(today)` has at least one row and `ClaimLLMNote(changes, today)` wins, write the model's text. If the model fails or the job is off, write the fallback.
  - `weekly`: if today is Monday and `ClaimLLMNote(weekly, lastMonday)` wins, write the summary. If it fails or the job is off, write nothing; there is no card.
- **Fallback for `changes`.** The rows' `why.Title`s, de-duplicated and joined as sentences, for example "Swapped with an easier day. Load made up.". It is deterministic and contains no numbers.
- **Logs.** Counts only (riders written, failed). Never the text, the note or a figure.
- **Per call.** Model calls use 6a's 120 s budget. Riders are processed one after another; there are two riders.

## API

- `GET /api/training/notes`, owner only, returns `{changes: {day, text} | null, weekly: {weekStart, text} | null}`:
  - `changes`: today's note;
  - `weekly`: the note for last week's Monday, while today is in the following week.
- `POST /api/training/checkin/understand`, owner only.
  - It reads today's saved note from the store and never takes it from the body.
  - It returns `{legs, sleep, mood, lifeEvent}`.
  - Status codes:
    - 412 when the job is off;
    - 404 when there is no note today;
    - 502 on a model failure;
    - 429 from the existing LLM limiter.
  - It stores nothing.
- `/api/me` `llmJobs` gains `explainChanges`, `understandCheckin` and `weeklySummary`. The DTOs change in `server.go` and `types.ts` together.

## UI

- **Today page.** A "This morning's changes" block in TodayCard, under the session, shown when `changes` is present: the text, plus a small "Why?" link to the existing popovers.
- **Check-in card.**
  - After a save with a non-empty note, it calls `understand` when the job is on.
    - The card shows "Reading your note…" while it waits.
    - It then shows "We understood: legs 2, sleep 2, travel Thu 29 Oct – Sun 1 Nov".
  - **Fill empty scores** sets only scores that are still 0, then saves.
  - **Plan the trip / Plan the illness** opens LifeEventModal with a proposal (the same `proposal` prop that plan edits use).
  - Nothing changes without a tap. Errors show a quiet line ("Couldn't read the note this time"). The check-in itself is already saved.
- **Plan page.** A weekly summary card at the top, titled "Last week" with the text. Its dismissal is stored in localStorage per week start, and the card survives a reload.

## Evaluation

The cases live in `internal/llm/testdata/eval/`, next to 6a's, and run with `just llm-eval`.

| File | Cases | Checked |
|---|---|---|
| `checkin.json` | 12, half English and half Dutch | the expected scores, and the life event or its absence |
| `changes.json` | 6 | validity and no invented numbers |
| `weekly.json` | 6 | validity and no invented numbers |

- A job ships on only at 80 % or more against `qwen3.5-9b`. A job that fails ships with `llm.jobs.<job>: false`. The result is recorded in the PR.
- The notes are synthetic: no real places, names or routes.

## Testing

- **`internal/llm`.** For each job, using the fake server from 6a's tests:
  - the request shape;
  - the validator, including no-invented-numbers;
  - the retry;
  - enum and date checks for `understand_checkin`.
- **Store.**
  - `llm_notes` on SQLite and PostgreSQL (`TestEachEngine`).
  - Claim once.
  - Prune.
  - The registry tests: purge, rename, merge, and the unique index.
- **API.**
  - `runLLMNotes`:
    - writes `changes` only on a morning with adjustments;
    - writes the fallback when the job is off;
    - writes `weekly` only on Monday;
    - writes nothing outside the morning slot;
    - writes once across two runs.
  - `/notes`:
    - owner only;
    - a claimed empty row reads as absent.
  - `understand`:
    - 404 with no note;
    - the note comes from the store, not the body;
    - 412 when the job is off.
  - The logs never contain the note or the text.
- **Web.** Typecheck, and a browser check against a stub model: the changes block, the check-in reading with Fill empty scores, and the weekly card's dismissal.

## Out of scope

- Any email change.
- Applying a reading without a tap.
- Plan edits, which stay off from 6a.
- The intensity mix in the summary until 5a is on `main`.
