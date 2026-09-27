# Progression levels and a workout library — design

Status: approved 2026-09-27. Sub-project 2 of
`2026-09-27-training-adaptation-design.md`; builds on sub-project 1 (ride
analysis: every synced ride has an outcome and per-step results).

## Why

The scheduler builds four session types, at most one hard session a week,
a fixed 3′/2′ interval shape and the same workout every week of a phase.
Nothing about the plan changes when a rider nails or struggles with a
session. TrainerRoad's Adaptive Training shows the shape that works: a
1–10 **progression level** per training zone, a library of workouts each
rated on the same scale, the next workout picked a little above the
rider's level, and levels moved by how each workout actually went (plus a
short "how did it feel?" survey that only weights the change).

## What riders get

- **Levels** per sport and zone, 1.0–10.0, one decimal:
  cycling `tempo`, `sweet_spot`, `threshold`, `vo2max`, `anaerobic`;
  running `tempo`, `threshold`, `intervals`. Endurance/easy/long sessions
  stay volume-sized (hours) and carry zone `endurance` with no level.
- **Starting levels** from the profile's experience level: beginner 2.0,
  intermediate 4.0, advanced 6.0, unset 3.0 — created the first time a
  rider's plan is scheduled.
- **A workout library** (`internal/workoutlib`): hand-written ladders per
  zone, each rung a workout with a level (table below). Every structured
  workout gets a real warmup (10′ ramping 50→65 % FTP, or the HR/pace
  equivalent) and cooldown (10′ at 50 %), not open steps.
- **Weekly plans with a zone mix per phase** (hard sessions never on
  consecutive days; the long session stays on the last available day):

  | Phase | Cycling structured sessions (n = available days) | Running |
  |---|---|---|
  | Base | 1 × `sweet_spot` (n ≥ 3); 0 otherwise | 1 × `tempo` (n ≥ 3) |
  | Build | `threshold` + `vo2max` (n ≥ 4); `threshold` (n = 3) | `threshold` + `intervals` (n ≥ 4); `threshold` (n = 3) |
  | Peak | `vo2max` + `anaerobic` (n ≥ 4); `vo2max` (n = 3) | `intervals` + `threshold` (n ≥ 4); `intervals` (n = 3) |
  | Taper | 1 × `vo2max` at level − 2 (opener) | 1 × `intervals` at level − 2 |
  | Recovery week | none | none |

  The rest of the available days are endurance sessions sized from the
  week's hours as today. With n ≤ 2 only the long session and endurance.
- **Picking a workout**: target level = current level + 0.5 ("productive");
  choose the ladder rung whose level is closest to the target, preferring
  the lower rung on a tie, capped at the slot's time budget (a rung longer
  than the slot's hours is skipped for the next lower one). Taper openers
  use level − 2. Clamp to the ladder's ends.
- **Levels move after every analysed ride** (`internal/progression`,
  pure). With `cur` the rider's level in the workout's zone and `wl` the
  workout's level, `diff = wl − cur`:

  | Outcome | Change |
  |---|---|
  | nailed, diff ≥ −0.5 | new = max(cur, wl) + bump, bump = 0.3 adjusted by feel |
  | nailed, diff < −0.5 | +0.1 |
  | completed | +0.1 if diff ≥ 0, else 0 |
  | struggled | 0 (the adapter steps the next workout down, below) |
  | incomplete | −0.3 if diff ≤ 0, else 0 |
  | unplanned / no zone | no change |

  Feel (1 easy … 5 all-out) adjusts only a nailed bump: 1–2 → +0.2,
  3 → 0, 4 → −0.1, 5 → −0.2; the bump never drops below 0.1. Levels clamp
  to [1.0, 10.0] and round to one decimal. Each change records a reason
  ("Nailed Threshold 3×12 (5.0) — threshold 4.6 → 5.3").
- **Feel rating**: an optional 1–5 tap on the today card's done state and
  in the step-results modal. Rating (or re-rating) a ride re-applies that
  ride's level change with the new feel: the change is stored per analysis,
  so a re-rate replaces it rather than stacking.
- **Struggled → step down**: when an analysed key session is `struggled`,
  the adapter replaces the next still-untouched generated workout **in the
  same zone** within the next 7 days with the rung one level lower, reason
  "Stepped down after Tuesday's threshold session was under target".
- **UI**: a Progression card on the Fitness page (one bar per zone with its
  level and last change); planned workouts show "Threshold · 5.2" on the
  today card, week tiles and in the workout slide-over.

## Workout ladders (library content)

Targets are % of FTP for cycling (converted to watts / HR / open exactly as
`scheduler.zoneTarget` does today) and % of threshold speed for running.
Rest between reps at 50 % FTP (cycling) or 65 % threshold speed (running).

| Zone | Target | Rungs (level: reps × work / rest) |
|---|---|---|
| tempo (bike) | 76–87 % | 1: 2×10′/5′ · 2: 3×10′/5′ · 3: 2×15′/5′ · 4: 4×10′/5′ · 5: 2×20′/5′ · 6: 3×20′/5′ · 7: 2×30′/5′ · 8: 1×60′ · 9: 3×30′/5′ · 10: 1×90′ |
| sweet_spot | 88–94 % | 1: 3×6′/3′ · 2: 3×8′/4′ · 3: 3×10′/5′ · 4: 2×15′/5′ · 5: 3×12′/4′ · 6: 2×20′/5′ · 7: 3×15′/5′ · 8: 2×30′/5′ · 9: 3×20′/5′ · 10: 2×40′/5′ |
| threshold (bike) | 95–105 % | 1: 3×5′/5′ · 2: 3×6′/4′ · 3: 3×8′/4′ · 4: 4×8′/4′ · 5: 3×12′/5′ · 6: 2×20′/8′ · 7: 3×15′/5′ · 8: 2×25′/8′ · 9: 3×20′/6′ · 10: 2×30′/8′ |
| vo2max | 106–120 % | 1: 4×2′/2′ · 2: 5×2′/2′ · 3: 5×3′/3′ · 4: 6×3′/3′ · 5: 5×4′/4′ · 6: 6×4′/3′ · 7: 5×5′/4′ · 8: 6×5′/4′ · 9: 5×6′/5′ · 10: 6×6′/4′ |
| anaerobic | 121–150 % | 1: 6×30″/3′ · 2: 8×30″/3′ · 3: 6×45″/3′ · 4: 8×45″/3′ · 5: 6×1′/3′ · 6: 8×1′/3′ · 7: 6×90″/4′ · 8: 8×90″/4′ · 9: 6×2′/4′ · 10: 8×2′/4′ |
| tempo (run) | 88–95 % thr. speed | 1: 2×8′/3′ · 2: 3×8′/3′ · 3: 2×12′/3′ · 4: 3×12′/3′ · 5: 2×20′/4′ · 6: 1×40′ · 7: 3×15′/3′ · 8: 1×50′ · 9: 2×30′/4′ · 10: 1×60′ |
| threshold (run) | 96–103 % | 1: 4×4′/2′ · 2: 5×4′/2′ · 3: 4×6′/2′ · 4: 5×6′/2′ · 5: 3×10′/3′ · 6: 4×10′/3′ · 7: 3×14′/3′ · 8: 2×22′/4′ · 9: 4×12′/3′ · 10: 3×20′/4′ |
| intervals (run) | 106–115 % | 1: 6×1′/1′ · 2: 8×1′/1′ · 3: 6×2′/1′30″ · 4: 7×2′/1′30″ · 5: 5×3′/2′ · 6: 6×3′/2′ · 7: 5×4′/2′30″ · 8: 6×4′/2′30″ · 9: 5×5′/3′ · 10: 6×5′/3′ |

Names are generated: "Threshold 3×12", "VO2max 5×4", "Tempo run 2×20".

## Data

- `workouts` gains `zone TEXT NOT NULL DEFAULT ''` and
  `level DOUBLE PRECISION NOT NULL DEFAULT 0` (added in `UseDB` the way
  earlier columns were); `Workout`, create/update requests, the DTO and the
  TS type carry `zone` and `level`. `scheduler.IsKeySession` /
  `IsHardSession` read the zone when set (key = long or any structured
  zone; hard = structured zone) and fall back to the existing name tables
  for rows made before this change.
- New table `progression_levels (rider, sport, zone, level, reason,
  updated_at, PRIMARY KEY (rider, sport, zone))`.
- `session_analyses` gains `feel INTEGER NOT NULL DEFAULT 0` and
  `level_delta DOUBLE PRECISION NOT NULL DEFAULT 0` (the change this ride
  applied, so a re-rate can replace it).

## API

- `GET /api/training/progression` → `{ levels: [{ sport, zone, level,
  reason?, updatedAt }] }` (owner-only; initialises levels if none).
- `PUT /api/training/sessions/{id}/feel` `{ feel: 1..5 }` → 200 with the
  updated analysis; 404 for another rider's session; 400 outside 1–5.
- Workout DTOs carry `zone` and `level`; the analysis DTO carries `feel`.

## Testing

- `workoutlib`: every ladder has 10 rungs with levels 1…10 and
  non-decreasing total work time (reps × work) up the ladder; instantiation targets for
  power, HR, pace and open; warmup/cooldown present; FIT encoding of every
  rung succeeds (existing `fitworkout` encoder).
- `progression`: table tests for every row of the update table, feel
  adjustments, clamping, rounding, re-rate replacing the prior delta.
- `scheduler`: zone mix per phase and day count, no consecutive hard days,
  productive-level picking, time-budget cap, taper opener, recovery week,
  legacy name fallback for IsKeySession/IsHardSession.
- Storage under `TestEachEngine`; API handler tests for both endpoints
  (owner-only, validation); adapter step-down test.

## Out of scope

Level decay with inactivity; levels for endurance; a machine-learned
workout-level model; readiness (sub-project 3); threshold detection
(sub-project 4).
