# Training hours per weekday

## Why

A rider's week is not flat: an hour on a weekday evening, three on Saturday and Sunday. Today the
profile holds one `hoursPerAvailableDay` and a set of `availableDays`, so the planner cannot know
that Monday is short and Sunday is long. The rider wants to say how many hours each day has, and
have the plan respect it.

## Goals

- The profile stores hours per weekday (`mon`..`sun`), 0 meaning "not a training day", on SQLite
  and PostgreSQL.
- Each day's hours are a **hard cap** on the session the plan puts there, for every rider, old
  profiles included. The week's hours cap is the sum of the days.
- The long session goes on the day with the most hours.
- Changing any day's hours rebuilds untouched sessions through the existing
  `availabilityChanged` -> `rebuildDays` path.
- The profile form shows an hours input under each day that is switched on, and works at 375 px.

## Non-goals

- Hours that differ from week to week (a calendar of availability). Life events already cover a
  week that is different.
- Time-of-day windows. Hours only.
- Changing what the history inference (`autoprofile`) or the LLM profile proposal produce: they
  keep suggesting one figure, which is spread over the days they propose.

## Anti-goals

- A session longer than the hours the rider gave for that day.
- A rider who never opens the form losing their plan, their days or their hours.
- A rollback to the previous image that loses or misreads availability.

## Decided with the user

- **Seven figures, one per weekday.** The form keeps today's day buttons; each switched-on day
  gets its own hours input. Switching a day off is hours 0.
- **Hard cap on every day, for everyone (option 1 of three).** Today's single figure is a budget
  and the long ride runs past it by design (3 days at 1.5 h with a 1 h structured session: a long
  ride of about 2.25 h). With a hard cap that long ride becomes 1.5 h and the easy days take the
  rest. Accepted: an old profile's long ride may shorten in weeks built after the change. That
  includes `refreshWeek`, which rebuilds a pre-filled week's untouched sessions once when it
  becomes next week, so the cap reaches those weeks then. Touched sessions are never changed.
- **Old profiles:** the single figure applies to each of their available days.

## Approaches considered

1. **Hard cap on every day (chosen).** One rule: hours are hours. Cost: old profiles' long rides
   shrink to the figure in newly built weeks.
2. **Cap only when the days differ.** Old profiles identical, but a discontinuity: 1.5 h everywhere
   gives a 2.25 h long ride, changing Monday to 1.4 h drops it to 1.5 h. Rejected as confusing.
3. **Cap everything but the long ride.** Lets a 3 h Sunday hold a 4 h ride: exactly what the
   rider is trying to stop. Rejected.

Storage, my call: a **new column** beside the two existing ones, rather than seven columns or
re-encoding `available_days`. Re-encoding breaks a rollback (the old image would misparse the
text); seven columns is a heavier migration for no reader's benefit.

## Design

### Model and storage (`internal/workout`)

- `RiderProfile.HoursByDay map[string]float64`, keys `mon`..`sun`, only days with hours > 0.
- New column `rider_profiles.hours_by_day TEXT NOT NULL DEFAULT ''` (in the CREATE and as an
  `ALTER TABLE ... ADD COLUMN` beside the others at `db.go:853`). Encoding is the builder's call
  (JSON object, like `hr_zones_*`); '' means "not set".
- **One normalising step on read and on save** keeps the three fields consistent:
  - read: `hours_by_day` '' and `available_days` set -> `HoursByDay` = the single figure on each
    available day (an old profile, no backfill needed). A day with hours 0 under an old profile
    (figure 0) gives an empty map, as an empty profile does today.
  - save: when `HoursByDay` is set, `AvailableDays` is written as its days in Monday-first order
    and `HoursPerAvailableDay` as the **mean** of its values. The mean means an older image, which
    caps the week at `figure x days`, caps it at exactly the new sum: rollback keeps the weekly
    cap and only loses the per-day shape.
  - save: when `HoursByDay` is nil (an automatic writer that only knows the two old fields, an old
    browser tab), it is derived from them as on read.
- **Reconcile on read (rollback round trip).** An older image edits only the two old columns. When
  `hours_by_day` is set but its day set differs from `available_days`, or its mean differs from
  `hours_per_available_day` by more than 0.01 h, the old columns win and the map is derived from
  them (my call).
- Helpers: `HoursOn(weekday string) float64` and `WeekHours() float64` (the sum). Every reader of
  the single figure goes through these.
- `AvailableDays` stays as a field, so its readers (scheduler day list, adapter, movenext,
  testschedule, lifeevents, weather, planedit, ledger open days, crew together days) are untouched.

### The weekly cap

Replace `HoursPerAvailableDay * len(AvailableDays)` with `WeekHours()` in `periodization/load.go:84`,
`periodization.go:252` and `:376`, `api/loadinput.go:19` and `api/goalcheck.go:84`. For an old
profile this is the same number.

### The scheduler (`internal/scheduler/buildWeek`)

- **Long slot placement.** `assignSlots` puts the long slot on the day with the most hours, ties
  going to the later day (an old profile is all ties, so the long day stays the last available day,
  as today). Structured placement (`placeStructured`) takes the long day's index instead of
  assuming `n-1`: no structured slot on the long day, and the "off the days beside the long day"
  first pass reads the actual long day. The mini-taper's `structuredLimit` and the recovery week's
  "first available day" rule are unchanged. In an event week the long day is chosen among the days
  `splitEventWeek` leaves.
- **Structured slots** get `capSeconds = min(share x target, HoursOn(day) x 3600)`; a rung that
  does not fit falls to a lower rung or demotes to endurance, as today.
- **Endurance and long slots** split the remaining hours by weight as today, but **water-filled**
  against each day's hours: a slot whose share exceeds its day is set to the day's hours and the
  excess is shared among the slots still under their cap, repeated until nothing exceeds. If every
  slot is full the remainder is dropped (the week target cannot exceed the sum, so this only
  happens after fixed sessions or rungs reshaped it, **and whenever a week sized at the weekly cap
  has structured days that do not fill their hours**). Decided with the user during
  implementation: the week is then smaller than its target, and that is accepted (hours mean hours).
  Example: an old profile of 3 h on four days had a 12 h build week with 5 h rides; capped, it
  places about 7.8 h. Spare hours on a structured day are not filled with easy riding.
- **Mini-taper days** get `min(week.MiniTaperDayHours, HoursOn(day))`.
- `slotWeight` and `longWeight` are unchanged.
- Fixed sessions (crew rides) are not capped: the crew chose the ride. `applyFixed` is unchanged;
  structured spacing is computed around the long day as chosen before `applyFixed` drops it, as
  today (my call).
- Load debt: the ledger opens a week at `min(plan target, planned TSS at open)`, so hours the caps
  could not place are not counted as debt. Nothing to change.

Known consequence, said to the user: at 1 h on weekdays many structured rungs do not fit, so hard
sessions fall to shorter rungs or become endurance.

### Other readers of the single figure

- `ledger/repay.go` (lengthen, green push): cap by `HoursOn(weekday of the session's date)`; when
  that day has no hours (the rider moved the session there), the largest day's hours. Builder's call.
- `autoprofile`: when it fills or replaces the days/hours (only while they are empty or estimated)
  it sets `HoursByDay` to the inferred figure on each inferred day. `Estimated` keeps its two field
  names; a per-day map filled this way counts as both estimated.
- LLM profile proposal: unchanged schema (one figure). Applying it in the web page sets
  `hoursByDay` to that figure on each proposed day.
- `metricssync.go:509` (history pattern) only reads; unchanged.
- `crewRideSeconds` and Train now do **not** read the hours (checked); nothing to change.

### Moving sessions between days (decided with the user)

- **Moves the app makes on its own respect the cap.** The missed make-up (`adapter/session.go`,
  through `weekplan.NextFreeDay`) only takes a day whose hours hold the session's planned length;
  with none, it skips as it does when no day is free. The readiness swap (`weekplan.PlanSwap`)
  only pairs two sessions when each fits the other's day; otherwise it looks at the next
  candidate, and with none there is no swap.
- **Moves the rider asks for are exempt.** "Not today" (`movenext.go`) prefers the first free day
  the session fits and falls back to the first free day, so it never refuses for hours alone. A
  harder alternate and a date the rider picks are left as asked.
- `NextFreeDay` gains a fit predicate (or the available map becomes hours) so both callers share
  one rule; the shape is the builder's call. "Fits" compares the session's planned seconds with the
  day's hours, with a 5-minute tolerance for rounding (my call).

### API (`internal/api/training.go`) and `apps/web/src/api/types.ts`

- `riderProfileDTO.HoursByDay map[string]float64 json:"hoursByDay,omitempty"`. Responses also keep
  `availableDays` and `hoursPerAvailableDay` (derived), for the web readers that use them
  (`PlanPage` canFillWeek, `FitnessPage` needsSetup, `CrewTogetherDays`).
- Save: `hoursByDay` present wins. Absent (an old tab), the old two fields are used, **except** when
  their days and figure equal what the stored map derives to (same day set, figure equal to its
  mean within 0.01 h): then the stored map is kept, so an unrelated save from a stale tab neither
  flattens the shape nor triggers a rebuild (my call). Validation
  (422, nothing written): an unknown day key, a value that is negative, not finite or over 12 h
  (`maxSaneHoursPerDay`'s value). A day at 0 is "off" and dropped from the map.
- `availabilityChanged` compares the two `HoursByDay` maps (a day added, removed or with different
  hours). The "first days" case (`before` had none) still only plans.
- `profileProposalDTO` keeps its two fields.

### Form (`ProfileForm.vue`, `FitnessPage.vue`)

```
Availability
[Mon] [Tue] [Wed] [Thu] [Fri] [Sat] [Sun]     <- today's buttons, toggle a day
Mon  [ 1.0 ] h
Tue  [ 1.0 ] h
Thu  [ 1.0 ] h
Sat  [ 3.0 ] h
Sun  [ 3.0 ] h
8.5 h a week
```

- The single "Hours per available day" input goes. Under the day buttons, one row per switched-on
  day, Monday first: day label and a number input (step 0.25 or 0.5, builder's call). Rows stack in
  one column, so 375 px holds them.
- Switching a day on starts it at the nearest switched-on day's hours (or 1 h when none is on).
  Switching it off removes its row.
- A line under the rows gives the weekly total.
- The "auto-filled" badge sits on the Availability label when `available_days` or
  `hours_per_available_day` is estimated.
- `FitnessPage` `normalise` sorts the map's keys so the Save bar only shows on a real change;
  `needsSetup` is "no day with hours".

## Tests

- **Migration**: an old row (`hours_by_day` '') reads as the figure on each day, on both engines
  (`TestEachEngine`); a save writes the mean and the Monday-first days; an older reader's view
  (the two old columns) of a per-day save has the same weekly sum.
- **Scheduler**: the long slot lands on the most-hours day, ties to the later day; an old uniform
  profile places every slot as today; no session exceeds its day's hours; water-fill sends the
  excess to days with room; structured rungs fall to a lower rung on a short day; no structured
  slot beside a long day that is not the last day; a mini-taper day is capped.
- **Moves**: the make-up skips a day too short for the session; the swap refuses a pair that does
  not fit; Not today takes a later fitting day over an earlier short one, and falls back when none
  fits.
- **Rebuild**: changing one day's hours (days unchanged) triggers `rebuildRiderPlan`; an identical
  map does not; an old tab's save with only the old fields still works.
- **API**: validation 422s; the DTO round-trips `hoursByDay`.
- Web: typecheck; the form checked at 375 px in a browser.

## Docs

AGENTS.md: the "Available days or hours changed" paragraph (any day's hours), the long slot
paragraph ("there is no per-day-hours cap on it, as there never was" becomes the hard cap and the
most-hours day), and a short note on the three profile fields and the mean kept for rollback.

## Left to the builder

The column encoding, the input step, the repay fallback for a day without hours, and how the
water-fill is factored, all within the rules above.
