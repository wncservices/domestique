# Load ledger — design (sub-project 2)

Status: approved 2026-10-18. Detailed design for section 2 of
`2026-10-18-adaptive-training-builder-design.md`, with the deviations listed at
the end.

## Why

Since sub-project 1 a week has a TSS target. When a session is missed, eased or cut
short, nothing gives that load back, so the week quietly comes in light. The ledger
keeps the week's target, works out the debt every morning, repays it on the days
left, carries a little into next week, and forgives the rest. Riding through a
fatigued week therefore does not silently lower the season's load.

## Scope

- **Only riders whose plan is on the CTL basis.** The hours fallback keeps
  `adapter.Reconcile`.
- **Not in recovery or taper weeks.** A recovery or taper week's debt is forgiven
  at its end and never repaid. Adding load there defeats the week.

## Storage

Table `load_ledger(rider, week_start, target_tss, carried_in_tss, forgiven_tss, last_pass_date, created_at, updated_at)`.

| Column | Meaning |
|---|---|
| `week_start` | the week's Monday |
| `target_tss` | the week's target, fixed by the first morning pass of that week |
| `carried_in_tss` | debt rolled in from the previous week |
| `forgiven_tss` | debt written off at the end of this week |
| `last_pass_date` | the local date of the last repayment pass; compare-and-set, so one pass per rider per day across replicas |

- Primary key `(rider, week_start)`.
- Registered in `riderTables` as `Purged: true`, `Rename{Columns: rider, Unique: true, With: week_start}`, deleted in `Training.DeleteRider`.

## Debt

```
debt = target_tss + carried_in_tss − completed − planned_remaining
```

- `completed` is the sum of `CompletedSession.TrainingLoad` dated Monday…today in
  the training timezone.
- `planned_remaining` is the sum of `workout.PlannedTSSOrHours` over the rider's
  goal-linked workouts dated today…Sunday that are not done (`adapter.WorkoutDone`).
- Missed, eased and cut-short sessions all grow the debt by themselves, because they
  lower planned or completed TSS. An over-budget ride shrinks it. Debt below 0 is
  treated as 0.
- **Life events.** The target is fixed as the plan week's `TargetTSS` × (available
  days not blacked out ÷ available days), using `lifeevents.Blackout`. Illness and
  travel lower the target and never become debt.

## Morning pass

The pass runs inside `AutoScheduleTick`, after `AdaptWorkouts` and before
`autoPushWorkouts`, under the scheduling lock. It runs only when the tick is in the
first sync slot of the local day, before 12:00 (the same gate as the morning
summary). For each rider with a CTL-basis focus goal:

1. **Claim the day.** Compare-and-set `last_pass_date` on this week's row, creating
   the row if absent. If another replica or tick already ran today, stop.
2. **Open the week** (only when the row is new):
   - Fix `target_tss`.
   - Settle last week's row, if one exists:
     - `own = max(0, last_debt − last.carried_in_tss)` is last week's own debt.
     - `rolled = min(own, 0.10 × target_tss)` is carried in. It is 0 when this week
       is recovery or taper.
     - Everything else is written to last week's `forgiven_tss`: the unpaid carried
       part plus `own − rolled`.
   - Set `carried_in_tss = rolled`.
3. **Repay** while debt > 5 TSS and the week is neither recovery nor taper. Only
   sessions dated tomorrow…Sunday are touched, and only if *repayable*: plan-made,
   generated, not eased, not rider-kept, not routed, not a test, not crew.
   1. Lengthen endurance and long rides, longest first. Each gets at most
      `+min(30 %, 45 min)`, never past `HoursPerAvailableDay`, and the main step is
      rebuilt with `BuildEnduranceSession`. Indoor rides stay indoor (`keepIndoor`),
      so the trainer cap may give less.
   2. Upgrade one endurance day to a structured session, at most once a week, when:
      - debt is still above 20 TSS;
      - the week has fewer structured sessions than the phase allows
        (`scheduler.StructuredSlotCount`);
      - neither neighbouring day is hard;
      - FTP is set.
      The zone is the phase's first structured zone, at the rider's level, picked by
      `workoutlib.Pick` within the session's current seconds.
   3. Each change records `load_repaid` with inputs `{debtBefore, tssAdded}`.

## Eligibility

- Repayment adds `load_repaid` to the session's `adapted_rules` and appends the
  adjusted marker (dual write, as every adaptation does).
- **A repayment does not lock the session.** `scheduler.IsPlanSession` now ignores
  `load_repaid` when deciding whether a session was adapted, so readiness and
  fatigue easing still reach a lengthened session.
- **An eased session is never repayable.** Any other adaptation rule excludes it.
- A session is repaid at most once per day (`adjustments` key: rule, subject, day).

## Plan page

- `weekFocusDTO` gains `debtTss`, `carriedTss` and `forgivenTss` (all `omitempty`).
  `types.ts` mirrors them.
- The week strip reads, after the load label, `· +N TSS carried` and/or
  `· M forgiven` (integers).
- `why.Title` has titles for `load_repaid` ("Load made up"), `load_rolled` and
  `load_forgiven`. The last two are week-level and are shown only as the strip text,
  since there is no week-level Why attach point.

## Testing

- Pure `internal/ledger` table tests:
  - debt arithmetic;
  - the blackout-scaled target;
  - settle and roll (the 10 % cap, recovery and taper give 0, the unpaid carried part
    is forgiven, no infinite roll);
  - the repayment plan (caps per session, the day-hours cap, longest first, the
    upgrade rules, a routed, eased or kept session left alone, debt ≤ 5 does
    nothing).
- `scheduler`: `IsPlanSession` ignores `load_repaid`, and any other rule still
  excludes.
- `api` over HTTP:
  - a missed session on Tuesday raises Wednesday-morning debt, and the pass lengthens
    Thursday's endurance ride;
  - a second pass the same day does nothing;
  - readiness can still ease a lengthened session;
  - a recovery week is not repaid;
  - the Monday pass rolls ≤ 10 % and forgives the rest;
  - a rider on the hours basis is untouched;
  - the registry tests pass.
- Both engines.

## Deviations from the roadmap spec

| Roadmap said | This design | Why |
|---|---|---|
| Only carry-over stored | `target_tss` and `last_pass_date` stored too | The current week's target is recomputed from today's CTL on every read; debt needs it fixed |
| Green-day push | deferred to sub-project 3 | It needs the Push verdict |
| `Reconcile` retired | kept for the hours fallback | The ledger needs TSS and runs only on the CTL basis |
| "At most once per rule per day" for every rule | only `load_repaid` is exempt from the lock; the general relaxation moves to sub-project 4 | It changes every easing rule's behaviour; the re-optimiser is where it is needed |
| Forgive anything older than 7 days | forgive what is still unpaid at the end of the week it was carried into | Same effect, and it falls on week boundaries the ledger already has |
