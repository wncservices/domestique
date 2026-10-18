# Readiness v2 — design (sub-project 3)

Status: shipped 2026-10-18. This is the detailed design for section 3 of
`2026-10-18-adaptive-training-builder-design.md`, plus the green-day push deferred
from the load ledger.

## Why

Readiness today lets Garmin's status strings decide. A single amber reading eases the
day's interval session. The HRV numbers and sleep duration are only displayed, there
is no way to say how you feel in the morning, and endurance rides are never eased.
Readiness v2 makes three changes:

- it judges you against your own HRV and sleep ranges;
- it needs two independent amber signals before it eases anything;
- it adds a fourth verdict, **Push**, which lets the ledger give back missed load on a
  good day.

## Decisions

| Question | Decision |
|---|---|
| Easing strictness | Caution needs amber signals from **two different sources**. Rest needs one **severe** signal. A single amber signal is shown, not acted on. |
| Rest on endurance and long rides | Replaced by a 45-minute recovery ride. The lost load becomes ledger debt. |
| Check-in | Optional, one tap, on the Today page and linked from the morning email. A missing answer is never a signal. |

## Signals and sources

Every rule produces a signal with a **source** and a **severity** (`amber` or
`severe`). Sources: `hrv`, `sleep`, `resting_hr`, `garmin`, `load`, `subjective`.

| Source | Amber | Severe |
|---|---|---|
| hrv, band available (≥ 14 nights of `HRVLastNight` in the last 60) | 7-night mean of ln(HRV) below baseline mean − 0.5 SD | the 7-night mean is below the band **and** today's and yesterday's nights are each below baseline mean − 1.0 SD |
| hrv, no band | Garmin status UNBALANCED, LOW or POOR | status LOW or POOR on today and yesterday (today's rule) |
| sleep | score 40–59, **or** 3-night shortfall against the own 28-night median of `SleepSeconds` (≥ 7 nights) of at least 2.5 h | score 1–39 |
| resting_hr | +4 to +6 bpm over the 28-day median (≥ 7 readings) | +7 or more |
| garmin | training readiness LOW or 25–49 | POOR or 1–24 |
| load | ACWR ≥ 1.5 | TSB < −30 (snapshot ≤ 2 days old) |
| subjective | check-in legs, mood or sleep quality = 2; post-ride survey heavy legs (today's rules) | check-in legs = 1 |

- The baseline is computed over the 60 nights before today, mean and standard
  deviation of ln(HRV).
- The 7-night mean includes today when today has a reading.

## Verdict

1. **Rest:** any severe signal.
2. **Caution:** amber signals from at least two different sources.
3. **Push:** all of the following:
   - the HRV band is available and the 7-night mean is at or above the baseline mean;
   - last night's `SleepSeconds` is at or above the 28-night median;
   - there are no amber signals;
   - the check-in, if answered today, has legs ≥ 3.
4. **Ready:** everything else.

Reasons and signals are listed as today: rest first, then amber. A Ready verdict
with a single amber signal still lists it, so the rider sees it, but nothing acts on
it.

## Morning check-in

- Table `daily_checkins(rider, date, legs, mood, sleep_quality, note, created_at,
  updated_at)`, primary key `(rider, date)`.
  - Scores are 1–5, with 0 meaning not answered.
  - `note` is at most 500 characters, stored and never logged.
  - Registered in `riderTables` (purged, renamed, merged on `(rider, date)`).
- Endpoints:
  - `GET /api/training/checkin` returns today's check-in or `{}`.
  - `PUT /api/training/checkin` takes `{legs, mood, sleepQuality, note}`.
  - The rider comes from the session, and the date is the training timezone's
    today. A body date is ignored.
  - Both need `PermManageTraining`.
- Web: a card on the Today page with three 1–5 rows and an optional note. Saving it
  re-reads readiness.
- Email: the morning summary adds the line `Answer today's check-in: <AppURL>/today`.

## What the verdict changes

**Adapter, today only** (all existing rules on hard sessions are kept):

| Session | Caution | Rest |
|---|---|---|
| Hard (structured) session | one rung down (unchanged) | easy variant (unchanged) |
| Endurance or long ride (new) | 20 % shorter, minimum 30 min | 45-minute recovery ride (`BuildEnduranceSession(0.75, false, …)`) |

- Same rules (`readiness_caution`, `readiness_rest`), the same eligibility
  (`IsGenerated`) and the same why inputs.

**Green-day push.** In the morning ledger pass, on a Push verdict with debt >
`MinDebt`, before the head-unit push, today's repayable session is raised:

- a structured session goes one rung up its ladder, capped at the rider's level + 1;
- an endurance session is lengthened, with the same caps as repayment.

Rule `green_push`, inputs `{debtBefore, tssAdded}`. Like `load_repaid`, `green_push`
does not lock a session (`EasedBy` ignores it). Once a day through the ledger claim.

**Other consumers:**

- Push behaves like Ready for Train Now, crew advice and the tomorrow forecast.
- The Harder alternate's warning shows only on Caution or Rest.

**UI and email:**

- Readiness chip: "Good to push".
- Morning summary word: "Good to push".
- `types.ts` `ReadinessVerdict` gains `'push'`.

## Privacy

- Nothing changes in what is logged: rule ids and counts only.
- The email carries the verdict word only.
- The check-in note never reaches a log or the email.

## Testing

- `readiness` table tests for every row of the signal table (band boundaries, the
  14-night threshold, sleep-debt boundaries, two-source Caution, single-amber Ready,
  each severe Rest) and for Push (each condition missing gives Ready).
- Existing readiness tests whose expectation was "single amber → Caution" change to
  "→ Ready with the reason listed". Each such change is listed in the PR.
- Adapter: endurance Caution shortening, endurance Rest recovery ride, hard sessions
  unchanged.
- `api`: the check-in round trip, owner-only access, a rejected out-of-range score,
  the note never logged, and the registry tests.
- Green push: a Push morning with debt raises today's session one rung, records
  `green_push` and pushes the raised session; with no debt, nothing happens.
- Web tests for the check-in card helpers and verdict labels.

## Out of scope

- Wahoo wellness, Garmin body battery and stress.
- Using the check-in note (sub-project 6 parses it locally).
