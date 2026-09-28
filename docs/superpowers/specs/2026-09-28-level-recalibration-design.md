# Level recalibration after an FTP change — design

Status: draft 2026-09-28. Follow-up named in
`2026-09-28-threshold-detection-design.md`'s own "Out of scope": "Recalibrating
progression levels after an FTP change."

## Why

A progression level (`internal/progression`, `docs/superpowers/specs's
2026-09-27-progression-levels-design.md`) is not itself a wattage — it is a
rung on a ten-rung ladder (`internal/workoutlib`), and every rung in a zone's
ladder shares the same percentage-of-FTP target (`Ladder.LowPct`/`HighPct` are
per zone, not per rung; see `ladders.go`'s own comment: "the Target column of
the spec's table is per zone, not per rung"). What actually changes rung to
rung is reps and duration — `Pick` walks a ladder by nearest `Level`, not by
intensity. So a level's real meaning is "how much time-at-this-percentage a
rider can handle," at whatever FTP happens to be on file when the percentage
is converted to watts (`workoutlib.target`).

That is exactly the trap: raise FTP and every existing level keeps its
reps/duration but the watts under it jump, because the same percentage now
multiplies a bigger number. A rider sitting at threshold level 5.3 was
handling 3×12′ at 95–105% of 255 W (242–268 W); the moment FTP is corrected to
268 W the *same* level asks for the *same* 3×12′ at 255–281 W — more work at
a higher absolute intensity than anything that level was ever earned against.
The ordinary per-workout correction (`progression.Delta`'s struggled/
incomplete rules) would eventually walk it back down, but only after the
rider has already been over-reached for a week or two of sessions.

TrainerRoad ships the fix this spec follows: **Progression Levels drop after
an FTP increase** (support.trainerroad.com/hc/en-us/articles/4404977149211,
"Why did my Athlete Levels Adjust after an FTP change?"), so the wattage a
rider is actually asked to hold stays close to what they had just adapted to.
The same page states the asymmetric half that's easy to miss: **an FTP
*decrease* does not raise levels** — the lower FTP already makes every
existing level's target watts easier on its own, so there is nothing to
compensate for; TrainerRoad only lowers a level afterward if the rider then
struggles at the new, easier watts, through its ordinary per-workout rule.
No formula for the size of the decrease is published (forum reports vary
FTP-jump-to-level-drop by a large factor between riders, suggesting it isn't
a fixed ratio TrainerRoad discloses). intervals.icu and Xert were checked as
well: neither publishes a progression-level recalibration rule at all —
intervals.icu has no progression-level concept of its own, and Xert riders
report its own Training Peaks-equivalent metrics needing to be re-entered by
hand elsewhere rather than auto-recalibrating.

This spec derives its own formula from how this codebase's levels actually
work (above), rather than reverse-engineering TrainerRoad's undisclosed one,
and keeps the same asymmetry: **only an FTP increase recalibrates**.

## What recalibrates

**Cycling's five structured zones only** — tempo, sweet_spot, threshold,
vo2max, anaerobic (`workout.StructuredZones` minus running's). Running's
zones (tempo, threshold, intervals) are driven by threshold pace, a
different field with its own up/down detection; recalibrating those after a
threshold-pace change is a natural follow-up (see Out of scope) but is not
this spec.

A zone recalibrates only if the rider already has a saved level for it
(`workout.ProgressionLevel` row exists) — nothing seeds a level early just
to immediately lower it; a zone with no row yet is left for the ordinary
`levelsFor` seeding path to create at whatever FTP is current when it first
matters.

## When it fires

- **Trigger:** the rider's FTP rises to at least 1.03× and at most 1.25× the
  FTP levels were last calibrated against (`RiderProfile.FTPLevelsCalibratedAt`
  — new field, 0 meaning "never calibrated"; see Data). 1.03 is the same up-factor
  `internal/thresholds` already uses for "a real improvement, not noise" —
  mirrored by hand into `internal/progression`, the same
  package-independence convention `progression.Outcome`'s own doc comment
  already follows for `rideanalysis`'s outcome strings.
- **Never on a decrease**, matching TrainerRoad's own behaviour above, and
  matching this codebase's existing asymmetry that a downward threshold
  finding is already suggestion-only, never auto-applied
  (`docs/superpowers/specs/2026-09-28-threshold-detection-design.md`, "When
  a value changes").
- **Every source of an FTP change qualifies** — Garmin biometrics, the
  threshold-detection auto-apply (an empty/estimated field, sync-time), the
  fallback `fitnesstest.EstimateFTP`, an accepted threshold suggestion, and
  a rider's own manual profile save. The over-reach risk is identical
  regardless of which of these produced the new number, so there is no
  case among them worth excluding.
- **Typo guard: a rise of more than 25% never recalibrates.** A jump that
  large (255 → 2550, or 255 → 350) is far likelier a correction or a typo
  than a month of fitness, and lowering every level for it would punish the
  rider for a mistake. Nothing moves, and the helper logs at Info with the
  field name only (no watt values, per the health-values-out-of-logs rule).
  This holds for every source, not just manual saves — a bad auto-detection
  is no more real a rise than a mistyped one.
- **The marker follows FTP in both directions.** `FTPLevelsCalibratedAt` is
  set to the new FTP on a drop, a typo or a qualifying rise — but **not on a
  rise under 3%: there the marker stays** (build ruling). Those levels are
  still calibrated against the old FTP, and moving the marker would let a
  run of sub-3% steps (255 → 260 → 266 → 272) add up to any total without
  ever recalibrating. The consequence: with the marker at 255, FTP 262
  (+2.7%, nothing) → 250 (a drop, marker follows to 250) → 262 recalibrates
  250 → 262, although 262 was seen once already; the levels really were
  calibrated at 250 by then. Levels move only for a qualifying rise
  (≥ 3% and ≤ 25%, compared with a 1e-9 relative epsilon so 100 → 103 is
  inside the band). A
  ratchet-up-only marker would, after 255 → 2550 → 255, hold 2550 and block
  every future recalibration until FTP passed 2550. Following the FTP down
  means a later genuine rise (say 255 → 268) is measured against the right
  base. A drop never moves levels (see above), only the marker.
- **First save ever** (`FTPLevelsCalibratedAt == 0` and no FTP on file
  before this save, i.e. a brand-new profile): the marker is seeded to the
  FTP being saved and nothing recalibrates — there are no levels earned
  against a stale FTP to protect yet, the same reasoning
  `progression.Initial` already uses to seed a fresh rider without
  inventing a "previous" state that never existed.
- **A profile that predates the marker** (marker 0 but `before.FTPWatts > 0`,
  every rider who existed when the column was added) uses `before.FTPWatts`
  as the base: its levels were earned against that FTP, and skipping its
  first real rise for want of a marker would leave exactly the over-reach
  this feature exists to prevent. (Build ruling; the literal reading of the
  bullet above would have skipped it.)

## The formula

Every rung in a zone shares one percentage-of-FTP band, so the only lever a
level moves is time-at-that-band. Treat one full level as one step on that
time axis, and ask: after FTP rises by ratio `r = newFTP / oldFTP`, how many
levels' worth of time-at-band would keep the *absolute* watts×time (roughly,
training stress) a rider is asked for close to what it was before the
change? A doubling of FTP (`r = 2`) is treated as moving a rider from
scratch back to the bottom of the ladder they're on — an enormous, sanity-
bounding case — which fixes the scale: a full doubling costs all 10 levels.
Anything smaller scales logarithmically between those two points:

```
delta = max(-RecalibrationMaxDrop, -RecalibrationLevelsPerDoubling * log2(newFTP / oldFTP))
```

with named constants in `internal/progression`: `RecalibrationLevelsPerDoubling
= 10` (the one tunable — see Package) and `RecalibrationMaxDrop = 2.0`, a cap
on how far any single recalibration may move a level. The cap is a safety
net inside the 3–25% band: the raw formula exceeds 2.0 levels once
`log2(r) > 0.2`, i.e. a rise past about 14.9% (`r > 1.1487`), so the cap
matters for rises between ~14.9% and 25%.

applied identically to every structured cycling zone the rider already has a
level for — the ratio is the same for all of them, since none of the
ladders' percentage bands vary by zone in a way that changes how a given FTP
jump translates into "how many levels of time this is worth." The result
feeds the same `progression.Apply(cur, delta)` every other level change
already goes through — no new rounding or clamping: still one decimal,
still clamped to [1.0, 10.0].

**Worked example.** FTP 255 W → 268 W (a 5.1% rise, the exact example this
codebase already uses for a threshold-detection toast). `r = 268/255 =
1.0510`, `log2(r) = 0.0718`, `delta = -0.718`. A rider at threshold level 5.3
becomes `Apply(5.3, -0.718) = 4.6`. Every other structured cycling zone the
rider has a level for drops by the same ~0.7 (clamped/rounded per zone,
so a zone already near the floor stops at 1.0 rather than going negative).

Cap and guard, same base of 255 W:

| New FTP | Ratio | Raw delta | Result |
|---|---|---|---|
| 268 W | 1.051 | -0.72 | -0.7 |
| 280.5 W (+10%) | 1.10 | -1.38 | -1.4 |
| 306 W (+20%) | 1.20 | -2.63 | **capped at -2.0** |
| 318.75 W (+25%) | 1.25 | -3.22 | **capped at -2.0** |
| 2550 W | 10.0 | n/a | **no recalibration** (over 25%; marker still moves) |

This is a deliberately simple, monotonic approximation, not an exact inverse
of the ladder tables (which are hand-authored and not smooth — an exact
per-zone inversion would need a numeric search over `workoutlib.Rung`s at
recalibration time for a result no more defensible than this closed form).
The ordinary per-workout correction (`progression.Delta`) keeps correcting
from here exactly as it always has, the same self-healing property the
progression-levels design already relies on for every other misestimate.

## Idempotency

`FTPLevelsCalibratedAt` (new `rider_profiles` column, watts, 0 = never) is
the single guard, and it tracks the current FTP: writing the same FTP twice
gives ratio 1, which is under the 1.03 trigger, so nothing recalibrates a
second time. The helper compares the new FTP against the *marker* read from
the stored row, not against `before.FTPWatts` (which is only the base for a
pre-marker profile), so re-detected or dismissed-then-accepted findings
cannot double-count a rise.

**The marker is a compare-and-set, and `SaveProfile` never writes it.**
`workout.DB.SetFTPCalibrated(ctx, rider, from, to)` is
`UPDATE ... SET ftp_levels_calibrated_watts = to WHERE rider = ? AND
ftp_levels_calibrated_watts = from`, true only if a row changed; the profile
upsert leaves the column out of both INSERT and UPDATE. Levels move only for
the caller whose CAS wins, so two saves racing on one rise, or a sync that
saves late holding a stale profile, lower levels exactly once — a whole-row
save carrying a marker loaded earlier could otherwise revert one a
concurrent save had just moved. (This replaces the earlier "marker written
with the profile in the same `SaveProfile` call".)

**Failure recovery.** The CAS is won first, then the level writes follow. If
any of them fails, the helper restores the marker with
`SetFTPCalibrated(to → from)` so the next sync or save retries the rise, and
logs at Error: a write that should have landed did not, and the rise would
not self-heal — the next call would see marker == FTP and do nothing. A
retry skips zones whose `Reason` already names this same rise, so a partial
first attempt is not lowered twice. Only a crash between the CAS and the
level writes (no chance to restore) leaves the marker moved with zones
unadjusted for that rise — the same narrow race `handleResolveThreshold`'s
own accept path accepts for suggestion status vs. profile write; the next
qualifying rise, or an ordinary struggled/incomplete session, converges it.

## How the rider is told

No new UI surface for the ordinary case — the existing Progression card
(`ProgressionCard.vue`, `GET /api/training/progression`) already renders
each zone's `Reason` string, and recalibration writes one:

```
FTP 255 → 268 W — threshold 5.3 → 4.6
```

(mirroring `progression.Reason`'s own em-dash shape, built by a new
`progression.RecalibrationReason(oldFTP, newFTP, from, to float64) string`).
A rider who saved their own profile or just accepted a threshold suggestion
already lands back on a page with the Progression card one glance away.

The one case that needs an explicit nudge is the **background sync** path —
nobody is looking at a screen when auto-apply fires. `syncMetricsResultDTO`
(`internal/api/metricssync.go`) gains an optional
`levelsRecalibrated: {fromFtpWatts, toFtpWatts}`, the same shape
`EstimatedFTPWatts` already uses for "something changed, tell the toast."
The sync toast adds one line: "Levels adjusted for your new FTP (255 → 268
W)." The same small struct is reused on the threshold-accept response
(`handleResolveThreshold`) and the manual profile-save response
(`handleSaveRiderProfile`) as an optional sibling field, so all three
triggers can show the identical toast line without three different shapes.

## Replan

Recalibration does not touch any already-generated `workout.Workout` row —
a rung stays whatever it was built as; only the *level* used to build the
*next* one moves. Whichever of the three triggers just recalibrated offers
the same "Replan the rest of this week" action the threshold-suggestion
banner already offers after Update (`api.replan()`), never fires it
automatically — rewriting a rider's already-scheduled week is a bigger,
less-reversible action than adjusting a number, and the existing UI already
established "offer, don't force" for the adjacent case of an FTP change.

## Data

- `rider_profiles` gains `ftp_levels_calibrated_watts DOUBLE PRECISION NOT
  NULL DEFAULT 0` (idempotent add-column in `UseDB`, the same pattern every
  other column added to this table already follows).
- No new table. `progression_levels` rows are updated in place through the
  existing `SaveLevel` upsert — a recalibration is a level move like any
  other, not a new kind of row.

## Package

`internal/progression` (pure, no new dependency):

```go
const (
	RecalibrationUpFactor          = 1.03 // mirrors thresholds.upFactor by hand
	RecalibrationMaxFactor         = 1.25 // above this a rise is a typo/correction, not fitness
	RecalibrationLevelsPerDoubling = 10   // the tunable: levels a full FTP doubling would cost
	RecalibrationMaxDrop           = 2.0  // cap on any single recalibration
)

func RecalibrationDelta(oldFTP, newFTP float64) float64
func RecalibrationReason(oldFTP, newFTP, from, to float64) string
```

`RecalibrationDelta` returns the capped formula's result unconditionally
(the cap is part of the delta, so no caller can forget it); the caller (API
layer) is the one that checks `oldFTP * RecalibrationUpFactor <= newFTP <=
oldFTP * RecalibrationMaxFactor` before calling it at all — the same split
`thresholds.Detect` vs. its caller already uses (the pure package computes,
the API layer decides whether the trigger condition holds and persists).

## API

`internal/api/recalibration.go` (new file): one shared helper,

```go
func (s *Server) recalibrateLevelsForFTP(ctx context.Context, rider string, before workout.RiderProfile) (levelsRecalibratedDTO, bool, error)
```

called after each of the three places `workout.RiderProfile.FTPWatts` can
change and be persisted — `syncRiderMetrics` (after its own `SaveProfile`,
`internal/api/metricssync.go`), `handleResolveThreshold`'s accept branch
(`internal/api/thresholds.go`), and `handleSaveRiderProfile`
(`internal/api/training.go`). `before` is the profile as it stood before
this save (already loaded at every one of those three call sites for other
reasons); the helper re-reads the just-saved profile for the new FTP rather
than trusting a caller-passed "after" value, so it can never recalibrate
against a number that didn't actually make it to storage. It always
moves the marker to the new FTP; only when the rise is inside the 3–25% band
does it also touch levels, and a rise over 25% logs Info (field only) and
stops. It loads the
rider's existing cycling structured-zone levels via `ListLevels`, computes
`RecalibrationDelta` once, and writes each affected zone via `SaveLevel`
with `RecalibrationReason`. Owner-only by construction —
`rider` always comes from the session at every one of the three call sites,
never a request body, same as every other training write.

## Testing

- `progression`: the 255→268 worked example to the stated 0.1 precision;
  the cap: `RecalibrationDelta(255, 306)` is exactly `-2.0` (raw -2.63) and
  `RecalibrationDelta(255, 280.5)` is about `-1.375` (uncapped); a decrease or unchanged FTP produces a delta the caller must
  not apply (tested by the caller-side gate, not by this pure function
  refusing — it is intentionally unconditional); clamping at both ends
  (level already at 1.0 through a huge FTP jump stays at 1.0, never
  negative).
- `recalibrateLevelsForFTP`: fires exactly once per qualifying rise (same FTP
  written twice: no second move); never moves levels on a decrease or a rise
  under 3%; 255→2550 changes no level (typo guard, Info logged) but does move
  the marker; 255→2550→255 followed by 255→268 still recalibrates (marker
  followed both ways); a 20% rise (255→306) moves levels by exactly -2.0
  (capped from -2.63); exactly 1.25× recalibrates and just over does not; a
  drop moves the marker down and no level; only touches zones with an existing row; leaves running
  zones untouched; first-ever save seeds the marker without moving any
  level; storage under `TestEachEngine`.
- Acceptance: all three triggers (sync auto-apply, threshold accept, manual
  save) produce the same Progression-card Reason text and, where
  applicable, the same `levelsRecalibrated` toast field; a second sync pass
  at the same FTP does nothing further.
- Fixed clocks are not needed here — this feature has no date window of its
  own — but every new test still runs under both `TZ=UTC` and
  `TZ=Europe/Brussels` per the repo-wide constraint, since it shares process
  state with tests that do.

## Out of scope

Recalibrating running zones after a threshold-pace change (the same
mechanism, a different trigger field — a natural follow-up, not folded in
here); recalibrating on a decrease or on a rise over 25%; an exact per-rung inversion of the
ladder tables; surfacing `FTPLevelsCalibratedAt` itself anywhere in the UI;
any change to already-generated (past or already-scheduled) workouts.
