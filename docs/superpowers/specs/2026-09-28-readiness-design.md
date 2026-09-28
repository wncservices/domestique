# Readiness — design

Status: approved 2026-09-28. Sub-project 3 of
`2026-09-27-training-adaptation-design.md`; builds on ride analysis (#1)
and progression levels (#2).

## Why

Adaptation today reacts to training load (form below −30, struggled key
sessions, an overloaded week). It cannot see how the rider slept or
recovered. Garmin's Daily Suggested Workouts and Training Readiness show the
shape that works: read overnight HRV, sleep and resting heart rate, and
back off today's hard session when recovery is poor. Readiness here only
ever makes a day **easier**, never harder.

## Data (Garmin only)

Wahoo exposes no wellness data. New read-only calls on `garmin.Client`,
unofficial Connect endpoints as documented by python-garminconnect (the
reference the package already cites), each best-effort and failing closed
(a missing field or an error means "no reading", never a guessed value):

| Signal | Path | Fields used |
|---|---|---|
| HRV | `/hrv-service/hrv/{date}` | `hrvSummary.lastNightAvg`, `hrvSummary.weeklyAvg`, `hrvSummary.status` (BALANCED, UNBALANCED, LOW, POOR) |
| Sleep | `/wellness-service/wellness/dailySleepData/{displayName}?date={date}&nonSleepBufferMinutes=60` | `dailySleepDTO.sleepTimeSeconds`, `dailySleepDTO.sleepScores.overall.value` |
| Training Readiness | `/metrics-service/metrics/trainingreadiness/{date}` | first entry's `score`, `level` |
| Resting HR | existing `RestingHeartRate` | — |

One combined method `garmin.Client.Wellness(ctx, date) (Wellness, error)`
fetches all four; a single signal failing leaves only that field empty.

Stored in a new table `daily_wellness (rider, date, hrv_last_night,
hrv_weekly_avg, hrv_status, sleep_seconds, sleep_score, readiness_score,
readiness_level, resting_hr, updated_at, PRIMARY KEY (rider, date))` —
daily aggregates only, no sleep stages or raw data; owner-only.

The existing background sync (and "Sync now") fetches today and yesterday
for every rider with Garmin connected; the first time a rider has no rows,
it backfills the last 28 days (for the resting-HR baseline), capped to one
backfill per rider per sync.

## Readiness rules — `internal/readiness` (pure)

`Assess(today, history []DailyWellness, latest *FitnessSnapshot,
dailyLoads []DailyLoad) Assessment{Verdict, Reasons []string}` with
verdict `ready`, `caution` or `rest`.

- **rest** if any of:
  - Garmin readiness level `POOR`, or score < 25;
  - HRV status `LOW` or `POOR` on today *and* yesterday;
  - sleep score < 40;
  - resting HR ≥ baseline + 7 bpm (baseline = median of the previous 28
    days' readings, needs ≥ 7 readings);
  - form (TSB) < −30 on a snapshot ≤ 2 days old (the existing rule, moved
    here so all signals live in one place).
- **caution** if not rest and any of:
  - readiness level `LOW`, or score 25–49;
  - HRV status `UNBALANCED`, `LOW` or `POOR` on today only;
  - sleep score 40–59;
  - resting HR baseline + 4 … + 6 bpm;
  - acute:chronic load ratio ≥ 1.5 (7-day mean daily load ÷ 28-day mean,
    needs ≥ 21 days of history).
- **ready** otherwise. With no Garmin row for today (Wahoo-only riders,
  watch not worn), only the load/form rules apply.

Each triggered rule adds a plain reason, e.g. "HRV has been low for two
nights", "you slept 5h10 (sleep score 38)", "resting heart rate is 8 above
your usual 52". Reasons list in the order above.

## What it changes

Only **today's** generated, still-untouched **hard** session (the same
`scheduler.IsGenerated` / `IsHardSession` rules), through existing paths:

- **rest** → swapped for an easy session (the existing downgrade path);
  reason: "Swapped for an easy ride — " + joined reasons.
- **caution** → one level easier (the existing step-down path: the same
  ladder's rung at level − 1); reason: "Eased one level — " + reasons.
  The step-down source marker is `readiness:<date>`, so it happens once.
- **ready** → nothing. Readiness never raises a workout.

The TSB < −30 rule moves from `adapter.detectFatigue` into readiness; the
struggled-sessions and overloaded-week triggers stay in the adapter. The
one-change-per-workout-per-pass guarantee applies: a workout another rule
already changed is not touched again.

## API and UI

- `GET /api/training/readiness` → `{ today: { verdict, reasons, wellness? },
  days: [last 7 DailyWellness] }`, owner-only.
- Today card: a readiness chip — Ready (success, `i-lucide-battery-full`),
  Take care (warning, `i-lucide-battery-medium`), Rest (error,
  `i-lucide-battery-low`) — with the reasons in a popover; icon + label,
  never colour alone.
- Fitness page: a Recovery card after the Progression card — last 7 days:
  date, sleep (h:mm and score), HRV (last night + status), resting HR,
  readiness score/level; hidden when there are no rows.

## Testing

- `readiness`: table tests for every rule and boundary (score 24/25/49/50,
  sleep 39/40/59/60, RHR +3/+4/+6/+7, ACWR 1.49/1.5, HRV two nights vs one,
  stale snapshot), baseline needing ≥ 7 readings, reasons order.
- `garmin`: httptest for each endpoint, partial failure leaves other fields.
- Storage under `TestEachEngine`; sync fetch + backfill cap; adapter:
  rest → easy, caution → one rung easier once per day, ready → nothing,
  never twice; API owner-only.
- All tests use a fixed clock (`Server.Clock`) — no dependence on today's
  date.

## Out of scope

Body Battery; making days harder; sleep stages; Wahoo wellness; readiness
for tomorrow's plan.
