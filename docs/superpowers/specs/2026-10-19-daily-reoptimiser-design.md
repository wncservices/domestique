# Daily re-optimiser — design (sub-project 4)

Status: approved 2026-10-19. Detailed design for section 4 of
`2026-10-18-adaptive-training-builder-design.md`, narrowed (see Deviations).

## Why

On a Caution or Rest morning the app eases today's hard session: it steps it down a
rung or swaps it for an easy ride, and that load is gone for the week. Often a later
day in the same week holds an easy ride that could change places with it. The
re-optimiser makes that swap first. The hard work moves to a day with room around
it, and today becomes the easy day that readiness then eases as usual.

## The swap

Once per rider per morning, in the first sync slot of the local day before 12:00
(the same `inMorningSlot` gate as the ledger), inside `AutoScheduleTick` under the
scheduling lock, **before** `AdaptWorkouts`:

1. **Trigger.** Today's verdict (`assessReadiness`) is Caution or Rest, and today
   holds a session that is:
   - **swappable** (see below);
   - `scheduler.IsHardSession`;
   - not done.
2. **Candidates.** Swappable sessions dated tomorrow…Sunday of this week that are not
   `IsHardSession`.
3. **The target day.** A candidate's day is allowed when all of these hold:
   - it is not in `lifeevents.NoTestDays` (the blackout plus the return-ramp
     windows);
   - placing the hard session there leaves no hard neighbour: the day before and
     the day after hold no hard session, FTP test or crew ride. Today counts with
     the easy session that will be there.
   - it is not the eve of an FTP test or a crew ride.
4. **Choice.**
   - Prefer the earliest allowed day ≥ the day after tomorrow, so a recovery day
     sits in between.
   - Tomorrow is allowed only when `outlookTomorrow` says Ready.
   - On a tie, the candidate with the longest planned duration wins: it best
     absorbs a hard session's slot.
5. **The move.** The two sessions swap dates. Each gets:
   - the adjusted marker and a plain note: `moved from <date> — swapped with an
     easier day.`;
   - flags `WithAdaptation(reshuffle_readiness)`, with `MovedFrom` kept if already
     set, else its old date, and `MovedBy = auto`;
   - an adjustment `reshuffle_readiness` with inputs `{from, to, verdict}`.
   - If the second write fails, the first is put back (date, description and flags),
     as the ride-together answer does.
6. Then `AdaptWorkouts` runs as today. On Caution, today's ride (now the easy one)
   is shortened 20%; on Rest it becomes a 45-minute recovery ride. The moved hard
   session is untouched.

**Swappable** means all of the following:
- `scheduler.IsGenerated`;
- no route, no test, no crew ride;
- not already carrying `reshuffle_readiness`;
- no **manual** Garmin copy (`workout_pushes.origin = manual`), because the push
  pass never withdraws a manual copy;
- cycling.

**Idempotent.** After a swap, today no longer holds a hard session and the moved
session carries the rule, so later ticks find nothing to do. No claim table is
needed.

**`reshuffle_readiness` does not lock a session.** `Flags.EasedBy` ignores it, as it
does `load_repaid` and `green_push`, so readiness easing still reaches today's
moved-in ride and the ledger may still repay it. A session carrying it is never
swapped again.

**Garmin.** Nothing extra. The push pass that follows in the same tick withdraws a
future **auto** copy and pushes today's session.

## One spacing rule

Package `internal/weekplan` (pure) holds `Hard(w)` (structured hard, FTP test, or
crew ride) and `HardNeighbour(date, workouts, ignore ...string)`.

- The swap uses them.
- The adapter's missed-key-session make-up (`nextFreeDay` / `nextEasySlot`) uses
  them too, when the session it moves is hard: it no longer puts two hard days back
  to back. This is the one behaviour change outside the swap.

## Display

- `why.Title(reshuffle_readiness)` is "Swapped with an easier day", with facts
  from / to / verdict.
- The morning email's `easingRules` gains `reshuffle_readiness`, so "already eased
  to match" stays true.

## Testing

- **`weekplan` table tests:**
  - every trigger and eligibility rule;
  - day preference (≥ day after tomorrow, tomorrow only on Ready);
  - the tie-break;
  - hard neighbours, including today after the swap;
  - test/crew eves;
  - no-test days;
  - nothing to swap.
- **`adapter`:** a missed hard session is not made up next to another hard day.
- **`api`, over HTTP with the fake Garmin:**
  - a Caution morning with today's threshold session and Thursday's endurance ride:
    the two swap, today's ride is shortened by readiness, the why reads "Swapped with
    an easier day", and the push sends today's (easy, shortened) session;
  - a second tick changes nothing;
  - outside the morning slot nothing happens;
  - a Ready morning: nothing;
  - a session with a manual Garmin copy is never moved;
  - a failed second write restores the first;
  - the parity check stays green.

## Deviations from the roadmap

| Roadmap said | This design | Why |
|---|---|---|
| Keep the key session on the best-readiness day | dropped | There is no multi-day readiness forecast: only tomorrow, load-based. Ranking days would be guesswork |
| `reshuffle_load` / place ledger repayments | dropped | The ledger already places repayments (sub-project 2) |
| Exhaustive scored search | a deterministic rule | With one move type and ≤ 6 candidate days, a scored search buys nothing |
| Runs after the repayment pass | runs **before** `AdaptWorkouts` | After easing, today's session is already eased and locked, so there would be nothing left to move |
| General "at most once per rule per day" | not needed | The swap is idempotent by state, and `reshuffle_readiness` is exempt from the lock like the ledger rules |
