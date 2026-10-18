# Adaptive training builder — design

Status: approved in brainstorming 2026-10-18 (roadmap; each sub-project gets
its own detailed spec, plan and PR, in the order below).

## Why

The plan should use everything Domestique already knows about a rider, adapt
every day to fatigue, and still keep the overall training load high. Today it
does the opposite of the last part:

- **Load is hours only.** `periodization.Build` sizes a week as
  `HoursPerAvailableDay × len(AvailableDays) × weekFraction(...)` with global
  constants (base 0.70, build 1.00, peak 0.80, recovery 0.6, 40/35/15/10 phase
  split). CTL, ramp rate and TSS never size a week; the doc comment at
  `periodization.go:24` says so.
- **Adaptation only takes load away.** Readiness, fatigue, the FTP-test eve and
  crew eases replace today's or tomorrow's hard session with an easier one. A
  missed *key* session gets one make-up chance inside the same week; easy days
  are dropped. Nothing gives lost load back, and `adapter.Reconcile` cuts future
  base/build weeks by up to 30 % after low compliance — including compliance
  lost to illness.
- **The richest data is unused.** HRV values and sleep duration are display
  only (readiness reads Garmin's *status strings*), weather only suggests, CTL
  and ramp rate are read by the projection's warning and nothing else, and the
  ride's zone seconds feed thresholds but not planning.
- **The intensity mix is a fixed table.** At most two hard sessions plus one
  long ride a week; the zone per slot is fixed per phase; long and endurance
  rides never move a level; recovery weeks have no structured work at all.
- **Adaptation state lives in description text** (`GeneratedDescription`,
  `AdjustedMarker`, `SwappedMarker`, `CrewKeptMarker`, `LifeKeptMarker`) plus
  `UpdatedAt == CreatedAt`. Every new rule makes this more fragile, and "each
  session is changed at most once, ever" blocks any multi-step adaptation.

## What the leaders do

- **TrainerRoad**: per-zone Progression Levels moved by workout outcome and a
  feel survey; ML only for FTP detection. Domestique already has this.
- **Xert / Garmin load focus**: load split by energy system (low aerobic, high
  aerobic, anaerobic); the next session is chosen to close the largest
  shortfall against a target.
- **TrainingPeaks**: CTL/ATL/TSB and a CTL ramp target (≈ 3–8 per week).
- **HRV-guided training** (Javaloyes et al. 2019, cyclists): 7-day rolling
  lnRMSSD against an individual smallest-worthwhile-change band from a baseline;
  train hard inside the band, easy outside it. Larger gains than a fixed plan
  at equal training time.
- **LLMs** are used by the products for coaching chat and explanation, not to
  decide load. The deterministic engine decides.

## Decisions taken

| Question | Decision |
|---|---|
| Goal of the builder | Goal-dependent: a dated goal peaks for the event; an undated goal holds a high sustainable load. |
| Autonomy | Auto-adapt and explain why (extends today's model and the "Why?" popover). |
| Approach | Evolve the current deterministic planner (not an Xert-style daily recommender, not a fitted optimiser). |
| Data the riders record | Overnight HRV, sleep, resting HR, and a daily subjective check-in. |
| Weather | Suggest only. The re-optimiser never moves a session because of weather. |
| AI | **Local only.** A self-hosted model in the cluster; the Anthropic client is removed. |

## Roadmap

| # | Sub-project | Depends on |
|---|---|---|
| 0 | Adaptation state in columns, not description text | — |
| 1 | Load model: weekly TSS budget from CTL and a ramp target (shipped) | 0 |
| 2 | Load ledger: debt, repayment, carry-over (shipped; green-day push moves to 3) | 1 |
| 3 | Readiness v2: HRV band, sleep debt, check-in, four verdicts | 0 (uses 2 for debt) |
| 4 | Daily re-optimiser for today → Sunday | 2, 3 |
| 5 | Intensity buckets and progression fixes | 1 |
| 6 | Local AI layer | 3 (check-in), why log |

Each sub-project ships behind the existing `auto_schedule` behaviour (no new
flag unless its own spec needs one) and must leave every existing test green.

---

## 0. Adaptation state in columns

Add to `workouts`:

| Column | Meaning |
|---|---|
| `origin` | `plan` \| `rider` \| `crew` \| `test` (a route is `route_slug`, which `IsPlanMade` still checks) |
| `adapted_rules` | comma-joined `why.Rule` ids of every automatic change, in order |
| `swapped` | bool — replaces `SwappedMarker` |
| `kept` | `''` \| `crew` \| `life` — which feature the rider kept it through; replaces `CrewKeptMarker` / `LifeKeptMarker` |
| `moved_from`, `moved_by` | the date it was moved from, and by whom (`''` \| `life` \| `rider` \| `auto`) |
| `edited_at` | set by every `UpdateWorkout`, so "any edit" for now; replaces `UpdatedAt == CreatedAt`. Sub-project 2 marks system writes |

- The predicates in `scheduler/adapt.go` (`IsPlanSession`, `IsGenerated`,
  `IsPlanMade`, `AutoReplaceable`, `RiderKept`) keep their names and meaning and
  read the columns. Nothing else matches description text.
- A one-off migration derives the columns from today's markers; the markers
  stay in descriptions as display text for one release, then are no longer
  written.
- New why rules: `legacy`, `life_event`, `crew_ride_shorten`, `ride_together_move`.
- Pure refactor: behaviour is identical, proven by the existing adapter,
  season-plan and acceptance tests passing unchanged, plus a migration test
  over fixtures for every marker.
- `workouts` already belongs to `riderTables`; no new rider table.

## 1. Load model

A week is sized in **TSS**, from the rider's current CTL. Hours stay as a cap.

- **Ramp → weekly TSS.** To move CTL by `r` in a week, the week needs about
  `7 × (CTL + 6r)` TSS (EWMA with a 42-day constant). One pure function,
  `periodization.WeeklyTSS(ctl, ramp)`, is the only copy.
- **Ramp per phase** (defaults, per rider overridable later):

  | Phase | Ramp (CTL/week) |
  |---|---|
  | base | +3 … +5 (rising within the 3-week block, as today's 0.8 → 1.0) |
  | build | +5 … +7 |
  | peak | 0 … +2 |
  | recovery week (every 4th, base/build) | ≈ −5 (≈ 65 % load) |
  | taper | solved backwards to land in the event's form band (the existing `projection` bands) |

- **Maximum ramp** per rider: 4 (beginner), 6 (intermediate, default), 8
  (advanced); the existing `projection.SustainableRamp` (8) is the ceiling.
- **Dated goal**: target CTL = the existing projection target
  (`eventTSS/4`, clamped 30–120), later rider-settable. The ramp needed is
  solved backwards from the event and capped at the maximum ramp; if the
  target is unreachable the plan says so (Plan page notice) and caps instead of
  overreaching.
- **Undated goal**: ramp until CTL reaches what the rider's available hours can
  carry (`hours × typical IF² × 100 / 7`, IF from the last 6 weeks, default
  0.70), then hold with 3:1 cycles.
- **Hours are a hard cap**: `HoursPerAvailableDay × days` is never exceeded;
  when the TSS target cannot fit, structured sessions take a higher rung before
  any session grows past its day's hours.
- **One TSS estimator.** Sessions are sized and checked with
  `adapter.PlannedTSS` (step-by-step); `estimatePlannedTSS` is removed and its
  callers moved over.
- **Spike guard** while history is short (< 6 weeks of rides): weekly target ≤
  1.15 × the best of the last 4 weeks.
- **No CTL yet** (new rider, no FTP): fall back to today's hours model,
  unchanged, so a first-run deployment still gets a plan.
- `scheduled_weeks`, fill-once and `refreshWeek` keep their rules; a refreshed
  week is re-sized from the CTL *at refresh time*.

## 2. Load ledger

Per rider and plan week: `target_tss`, `completed_tss`, `planned_remaining_tss`
and `debt = target − (completed + planned_remaining)`. Derived on read from
workouts and `session_analyses`; only the carry-over is stored
(`load_ledger(rider, week_start, carried_in_tss, forgiven_tss)`, registered in
`riderTables`).

- **Debt comes from**: missed sessions, eased sessions (readiness, fatigue,
  crew, FTP-test eve), rides cut short, and fixed rides that came in under
  their budget. A ride over budget reduces debt.
- **Not debt**: illness and life events *lower the week's target* instead, so a
  week in bed never becomes "make it up later". This also fixes `Reconcile`
  punishing illness.
- **Repayment pass** (every morning, after the 06:30 sync, under the scheduling
  advisory lock):
  1. Within the week: lengthen endurance and long rides (≤ +30 % or +45 min per
     session, never past the day's hours); if hard-day spacing allows, upgrade
     one endurance day to a structured session.
  2. Carry the rest into next week: ≤ +10 % of that week's target, inside the
     maximum ramp.
  3. Forgive anything older than 7 days (`forgiven_tss`, shown on the Plan
     page).
  - Never two hard days back to back; the weekly hard-session cap holds.
  - Only sessions the predicates allow are touched.
- **Green-day push**: verdict Push (sub-project 3) and debt > 0 → today's
  session goes one rung up its ladder, or an endurance session is lengthened.
  Capped at the rider's level + 1 (the same cap as the "harder" alternate).
- **At most once per rule per day**, keyed on `adjustments` (rule, subject,
  date): "changed at most once, ever" blocks any multi-step adaptation, so the
  ledger and the daily re-optimiser (sub-project 4) need this. It changes
  behaviour, so it lands here and not with the column refactor.
- **`adapter.Reconcile` is retired**; the ledger replaces it.
- New why-rules: `load_repaid`, `load_rolled`, `load_forgiven`, `green_push`.

## 3. Readiness v2

`internal/readiness` stays pure; new inputs, same `Assess*` entry points.

- **HRV**: 7-day rolling mean of ln(nightly HRV) against the rider's normal
  range from the last 60 days (mean ± 0.5 SD). Needs ≥ 14 nights; until then,
  Garmin's HRV status string as today.
- **Sleep**: 3-night sleep debt against the rider's own 28-day median duration,
  plus the existing score rule.
- **Resting HR**: unchanged (+4 Caution, +7 Rest vs the 28-day median).
- **Morning check-in** (new table `daily_checkins(rider, date, legs, mood,
  sleep_quality, note)`, 1–5 scales, registered in `riderTables`): a one-tap card
  on the Plan page and a link in the morning email. Optional: no answer means
  no signal, never "bad".
- **Load**: TSB and ACWR stay, demoted to supporting signals.
- **Four verdicts**: **Push / Ready / Caution / Rest**.
  - An automatic downgrade needs **two independent signals**, except a severe
    single one (HRV below range two days running, or check-in legs = 1).
  - Push needs HRV at or above range, sleep at or above median, and no amber
    signal.
- **Now reaches long and endurance rides**: Caution shortens them 20 %; Rest
  makes them a 45-minute recovery ride or a day off. The removed load is ledger
  debt.
- **Wahoo-only riders**: check-in plus load signals.
- Logging and privacy rules unchanged: no health value next to a rider name;
  the morning email still carries the verdict word only.

## 4. Daily re-optimiser

Runs after the repayment pass and before `pushWorkoutsForRider`, so the session
sent to the head unit is always the re-optimised one.

- **Scope**: today → Sunday, only sessions the predicates allow (never
  rider-kept, crew, test, routed, or rider-edited sessions).
- **Inputs**: the ledger's remaining budget, today's verdict, tomorrow's
  forecast (`ForecastTomorrow`), crew rides, life events, available days, and
  the hard-day spacing rules. **Weather is not an input**; the existing
  `weather` suggestions continue unchanged.
- **Moves**: swap today's hard session with a later easy day when today is
  Caution/Rest; place ledger repayments; keep the key session of the week on
  the best-readiness day available.
- **Search**: exhaustive over ≤ 7 days × ≤ 4 movable sessions. Score =
  load-target fit + readiness fit − rule violations (hard constraints are
  filters, not penalties). Pure package (`internal/weekplan`), fully table-
  tested.
- Never moves a session out of its week; never creates hard days back to back.
- New why-rules: `reshuffle_readiness`, `reshuffle_load`.

## 5. Intensity buckets and progression

**Buckets.** 28-day load in three buckets from `session_analyses` zone seconds:
low aerobic (Z1–2), high aerobic (tempo, sweet spot, threshold), anaerobic
(VO2max and above). Phase targets (shares of time):

| Phase | Low / high / anaerobic |
|---|---|
| base | 80 / 18 / 2 |
| build | 75 / 20 / 5 |
| peak | 75 / 15 / 10 |

- The scheduler fills a structured slot with the zone of the largest shortfall,
  within the phase's allowed zones; this replaces the fixed table in
  `structuredSlotsFor`. Ties fall back to today's table, so a rider with no
  history gets today's plan.
- A third hard session is allowed in build and peak for riders with ≥ 6
  available days and CTL ≥ 60; never back to back.
- `scheduler.WithRouteDemand` keeps its rules on top of the chosen zone.

**Progression.**
- **Endurance/durability level** (new zone `endurance` in `progression_levels`):
  moved by long rides — completed duration ratio and aerobic decoupling
  (power:HR drift, first half vs second). The long ride grows with this level
  instead of only with hours.
- **Decay**: −0.1 per week for a zone untrained for 21 days, floored at the
  rider's starting value.
- **FTP drop**: `RecalibrationDelta` applied symmetrically (levels rise).
- **Recovery weeks** keep one short structured session at level − 1.
- New why-rules: `level_decay`, `durability_move`.

## 6. Local AI layer

**Principle**: the deterministic engine decides; the model explains or
proposes. It never invents a number, and nothing it returns is stored without
server-side validation. This is the rule `narration` already follows.

- **No external LLM.** The Anthropic client in `internal/narration` is removed;
  `ANTHROPIC_API_KEY` is no longer read.
- **Serving**: `llama-server` (llama.cpp, official image) in the cluster with
  **Qwen3.5-4B Q4_K_M** (≈ 3 GB file on a PVC, ≈ 4 GB RAM, 4 threads, CPU limit
  4). The chart lives in `wncservices/Domestique-chart`; that repo's change is a
  separate PR. Single node (8 cores, 32 GB, no GPU): jobs run after the 06:30
  sync and re-optimiser so they don't compete with them for CPU.
- **Client**: `internal/llm`, hand-rolled (one POST to the OpenAI-compatible
  `/v1/chat/completions`, JSON parse), within the dependency budget. It uses
  JSON-schema-constrained output, `telemetry.OutboundTransport`, and a per-job
  timeout (90 s). Config: `llm.base_url` and `llm.model`; no URL means the
  features are off. A span carries only job and provider.
- **Jobs**:

  | Job | Input | Fallback |
  |---|---|---|
  | Explain the morning's changes (1–3 sentences, Plan page and morning email) | why log records | today's template sentence |
  | Parse the check-in note → `{legs, sleep, illness_hint, travel, equipment, dates}`, shown back as "we understood: …"; illness or travel becomes a life-event *proposal* the rider confirms | note + today's date | ignore the note |
  | Weekly coaching summary | precomputed aggregates (ledger, buckets, compliance, CTL trend) | no summary |
  | Existing: plan summary, profile proposal, goal proposal, plan edits | as today | feature off |

- Health data may reach the local model (it never leaves the node); the morning
  *email* still carries no numbers or reasons.
- Validate against a schema, retry once, then use the fallback. Prompts and
  outputs are never logged.
- **Evaluation gate**: a synthetic set in `apps/api/internal/llm/testdata` (≈ 30
  notes in English and Dutch, plus plan-edit sentences) with expected outputs.
  A job is switched on only when it passes; plan edits may use Qwen3.5-9B
  (≈ 8 GB RAM) if 4B falls short.
- Metric: `domestique_llm_requests_total{job,result}` (no rider label).
- Cost: ≈ €1 per year in power; benchmark with `llama-bench -t 4` on the node
  before committing to the model.

---

## Testing (every sub-project)

- Pure packages (`periodization`, `readiness`, `weekplan`, ledger,
  buckets) get table tests, including "rider with no history gets today's plan".
- `TestEachEngine` covers every new table on SQLite and PostgreSQL.
- New rider tables pass `TestEveryRiderKeyedTableIsRegistered`,
  `TestPurgeRemovesEveryRidersData` and the rename/merge registry tests.
- An acceptance test per sub-project drives the morning pass over real HTTP: a
  missed session → debt → repayment → the Garmin push carries the re-optimised
  session.
- A season simulation test: 16 weeks of synthetic rides and wellness with
  misses and one illness; it asserts CTL at the event is within 5 of target and
  that no week exceeds the maximum ramp.

## Out of scope

- Moving sessions because of weather (suggest only, by decision).
- Fitting per-rider CTL/ATL time constants (not enough data for two riders).
- Wahoo structured-workout push, body battery and stress sync.
- Any external LLM provider.
