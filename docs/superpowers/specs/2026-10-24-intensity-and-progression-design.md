# Intensity mix and progression — design (sub-project 5)

Status: approved 2026-10-24. This is the detailed design for section 5 of
`2026-10-18-adaptive-training-builder-design.md`. It ships as two PRs, 5a (intensity
mix) and 5b (progression).

## Why

Each phase's structured zones come from a fixed table, so a rider who has done
plenty of threshold work this month still gets threshold. Levels only rise:

- nothing measures how well a rider holds a long ride;
- a zone left alone for a month keeps its level;
- an FTP drop leaves levels where they were.

## 5a — Intensity mix

**Buckets.** The intensity mix is three buckets of time over the last 28 days,
built from `session_analyses.power_zone_seconds` (Coggan's 7 zones, as fractions of
FTP):

| Bucket | Coggan zones | % FTP |
|---|---|---|
| low aerobic | Z1–Z2 | < 75 |
| high aerobic | Z3–Z4 | 75–105 |
| anaerobic | Z5–Z7 | > 105 |

- **Which rides count:** only power-scored rides, meaning `load_source = "fit_power"`
  with `normalized_power > 0`. Rides with HR only, an estimated load or no FIT file
  would put every second in Z1, so they are left out.
- **Little history:** with fewer than 3 hours of counted time, there is no mix and
  today's fixed table applies unchanged.
- **Running** keeps today's table, because no pace-zone seconds are stored.

**Phase targets** (share of counted time):

| Phase | Low / high / anaerobic |
|---|---|
| base | 80 / 18 / 2 |
| build | 75 / 20 / 5 |
| peak | 75 / 15 / 10 |
| taper | today's table (opener) |

**Zone choice.**

- Each phase has an allowed set of structured zones, and each zone belongs to a
  bucket:

  | Phase | Allowed zones |
  |---|---|
  | base | sweet spot, tempo (high) |
  | build | threshold, sweet spot (high); vo2max (anaerobic) |
  | peak | vo2max, anaerobic (anaerobic); threshold (high) |

- For each structured slot in today's slot count, the scheduler picks the allowed
  zone whose bucket is furthest below its target.
- Inside a bucket it keeps today's order. A bucket at or above its target gives way
  to the next shortfall.
- No zone repeats in a week unless the allowed set is exhausted.
- When the gaps are tied (within 1 percentage point), it falls back to today's
  table, so a rider whose mix matches the plan sees no change.

**Third hard session.**

- A build or peak week gets a third structured slot when both hold:
  - the rider has at least 6 available days;
  - the week's `PlannedCTL` is at least 60.
- `placeStructuredDays` already refuses adjacent structured days. With no
  non-adjacent placement, the week stays at two.

**Recovery weeks** keep one short structured session:

- the phase's first allowed zone, at the rider's level − 1 (no productive offset);
- capped at 60 minutes, on the available day furthest from the week's last day.

The rest of the week stays easy, and there is still no long ride.

**Plumbing.**

- The mix is computed once per season pass (`seasonContext`) from `ListAnalyses` over
  28 days.
- It reaches the scheduler as `scheduler.WithZoneMix(intensity.Mix)` through
  `weekOptions`.
- Train Now's `weekZones` and the life-event refill build their week through the same
  options, so fill, refresh, the crew preview, life events and Train Now build the same
  week.
- `refreshWeek` re-derives the zones when a week becomes next week, from the mix as it
  is then.

**What stays.**

- `WithRouteDemand` still biases the rung within the chosen zone.
- `WithTargetTSS` still boosts levels.
- `StructuredZones` (used by the ledger's upgrade count) keeps returning today's table.
  That is a deliberate under-count, so the ledger never adds a third hard session on
  its own.

## 5b — Progression

- **Decoupling.**
  - `rideanalysis` computes `decoupling_pct` for steady cycling rides of at least 90
    minutes with power and HR.
  - It is the ratio of normalized power to average HR in the first half, against the
    second half, as a percentage.
  - It is stored as a new nullable column on `session_analyses`, for new analyses only,
    because raw samples are not kept.
- **Endurance (durability) level.**
  - A new zone, `endurance`, lives in `progression_levels` and is seeded from
    `progression.Initial` (and for existing riders on first read).
  - A matched long ride moves it:
    - decoupling under 5 % and completed: +0.3;
    - 5–8 %: +0.1;
    - over 10 %, or a duration ratio under 0.8: −0.2.
  - The move is recorded with rule `durability_move`.
  - The long slot's weight becomes `1.8 + 0.1 × (endurance level − starting level)`,
    clamped to the range 1.8–2.4. The long ride therefore grows with the level inside
    the week's hours.
- **Decay.**
  - Once per rider per ISO week, in the morning pass, every levelled zone with no
    matched ride in it for 21 days loses 0.1, never below its starting value.
  - The rule is `level_decay`. A claim row makes it once a week.
- **FTP drop.** `recalibrateLevelsForFTP` applies `RecalibrationDelta` for drops too,
  so a lower FTP raises cycling levels by the same log formula, capped at +2. This
  reverses the 2026-09-28 recalibration spec's one-way rule, and that spec's pinned
  "never moves on a decrease" test changes.

## Testing

- **5a:**
  - pure tests for buckets, the counted-ride filter and the 3-hour floor;
  - zone choice per phase, including tie fallback and no repeats;
  - the third-slot conditions;
  - the recovery session (zone, level − 1, 60-minute cap, placement);
  - the scheduler agreement tests (`StructuredZones`, crew preview) still green;
  - an API test that a rider with heavy threshold history gets vo2max in build.
- **5b:** decoupling on synthetic samples; the durability moves; the long-slot weight;
  decay once a week and its floor; symmetric recalibration.
