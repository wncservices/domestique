# Own heart-rate zones and time in zone

Riders choose how their heart-rate zones are set, separately for cycling and running, and see
each ride's time in zone. Changing the zones applies at once to every ride that carries a
heart-rate histogram.

## Zone settings

One setting per sport, stored on `rider_profiles` (`hr_zones_cycling`, `hr_zones_running`, JSON
text, '' = auto). `workout.HRZoneSetting{Method string; Bounds []int}`. Five zones, four boundaries
`b[0..3]` in bpm: a heart rate below `b[i]` is in zone i+1, at or above `b[3]` is Z5.

| Method | Boundaries | Needs |
|---|---|---|
| `""` auto | `threshold` when threshold HR is known, else `max` (today's behaviour) | either |
| `threshold` | LTHR x cycling `0.81, 0.90, 0.94, 1.00` / running `0.85, 0.90, 0.95, 1.00` (Friel) | threshold HR |
| `max` | max HR x `0.60, 0.70, 0.80, 0.90` | max HR |
| `reserve` | resting + (max - resting) x `0.60, 0.70, 0.80, 0.90` (Karvonen) | max and resting HR, max > resting |
| `custom` | `Bounds`, 4 ascending integers, each 40-230 | nothing |

A method whose inputs are missing yields no zones (the UI says which number is missing).
`SaveProfile` never writes the columns (an automatic save would wipe them); `SetHRZones` is their
own writer, like `SetWeight`. The profile PUT takes an optional `hrZones: {cycling?, running?}`;
an invalid method or custom bounds is a 422.

## Heart-rate targets on sessions

`workoutlib.HRRange` keeps today's tables when the sport's method is auto. With an explicit
method it reads the rider's boundaries: recovery `[b0 - (b1 - b0), b0]`, endurance `[b0, b1]`,
tempo `[b1, b2]`, sweet spot `[(b1+b2)/2, (b2+b3)/2]`, threshold `[b2, b3]`, vo2max / intervals /
anaerobic `[b3, ceiling]` with ceiling = max HR when above b3, else `b3 + (b3 - b2)`.

## Time in zone

- Each new analysis stores `hr_histogram` on `session_analyses`: seconds per whole bpm (rounded)
  for samples with heart rate, `{"min": <lowest bpm>, "s": [...], "n": <seconds without HR>}`.
  '' = never computed; `{"min":0,"s":[],"n":0}` = tried, no readable file (not retried).
- The analysis DTO gains `hrZones: [{zone: 1..5, low, high, seconds}]`, computed on read from the
  histogram and the rider's current setting for the ride's sport. Absent with no histogram or no
  zones. Seconds without heart rate are not counted.
- The old `hr_zone_seconds` column stays as written today and nothing reads it.
- **Backfill**: the metrics sync, after `analyseNewSessions`, fetches the FIT of up to 10 synced
  rides per provider per pass that have an analysis and no histogram, newest first, and stores
  only the histogram. A failed download leaves it '' (retried next pass); a file that decodes to
  no samples stores the empty histogram. Imports are never fetched: an import re-uploaded is
  re-analysed with a histogram anyway.
- Privacy: a histogram is health data in an already registered table (`session_analyses`), purged
  and renamed with the rider. Logs carry rider and counts only, never a bpm.

## UI

- Fitness profile form: "Heart-rate zones" with a Cycling / Running switch, a method select (Auto,
  % of threshold HR, % of max HR, Heart-rate reserve, My own) and, for My own, four bpm inputs
  ("Z1 ends at" ... "Z4 ends at"); the resulting five zones are previewed.
- Training zones card: the heart-rate zones per sport.
- A ride's analysis (where the step results show): a "Time in heart-rate zone" bar per zone.
