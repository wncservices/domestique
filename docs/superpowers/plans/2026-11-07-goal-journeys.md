# Goal and plan journeys: tests and fixes

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** every journey a rider takes with goals and the plan works end to end, and each one has a test that drives it over real HTTP.

**Background:** two bugs a rider hit followed one pattern: the first step worked, and a later change did not flow through. They were "in 9 months" giving no date, and a sport switch leaving the old plan (fixed in #535). An audit of the code found more of the same. The rider's decisions are recorded in each task below.

**Architecture:** the plan is filled once per goal and week (`scheduled_weeks`). `refreshWeek` rebuilds untouched sessions in place but never creates or deletes them. Every fix below follows the pattern #535 introduced for a sport change:
- delete the goal's untouched plan-made sessions from today (`untouchedPlanSession`; never one already ridden today), with their Garmin copies;
- forget the affected `scheduled_weeks` rows;
- top up this week from today with `scheduleGoalWeek`;
- let the season fill do the rest;
- run under the scheduling lock, with a background retry when it is busy.

Reuse `rebuildSeasonLocked`/`retryRebuildForSport`, and generalise them where a second caller needs the same steps. Do not write a second copy.

**Harnesses:**
- `newSeasonHarness`, with `h.profile`, `createGoal`, `patchGoal`, `goalSessions`, `week`, `backdate`, `now` and `WaitForBackground` (seasonplan_test.go, goalsport_test.go);
- `newAutoScheduleHarness`, `srv.AutoScheduleTick` and `settings.SetFlag(api.FlagAutoSchedule, true, "test")`;
- `newReplanHarness` and `pushHarness`, with `h.garmin.workoutCalls`;
- `newOnceHarness`;
- `pgtest.DSN(t)` for lock contention.

## Global constraints

- Touched sessions are always the rider's and stay: edited, moved, eased, indoor, routed, test, crew, ridden and past ones. Nothing deletes them.
- Logs carry counts and ids only, never a health or load figure beside a rider.
- Every behaviour change gets a test that fails without it. Update AGENTS.md "The whole season is planned" for each rule that changes.
- gofmt and vet are clean; `golangci-lint run --new-from-rev origin/main ./...` from apps/api gives 0 issues; `go test ./apps/api/...` passes (`TestParallelOversizedUploadsAreAllAnswered` is a known flake).
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` plus the implementer model's line. Never commit `.claude/` or `.superpowers/`.

---

### Task 1: A dated goal added later takes over an undated goal's untouched days

**Rider decision:** "Race takes over".

Today, `fillWeek` skips every day another goal holds and still records the week (training.go ~947-966, fillweek.go:39). A race goal added while a "stay fit" goal fills the plan stays empty in those weeks forever.

**Rule:**
- When a dated goal is created or edited, then from today up to its event, the untouched plan-made sessions of any **undated** goal of the same rider are removed (with their Garmin copies), and those weeks of the undated goal are forgotten.
- The dated goal then fills the weeks.
- After the event, the undated goal fills again through the normal season pass.
- Two dated goals: leave today's behaviour (first one keeps its days), but add a test that pins it.

**Tests:**
- `TestARaceAddedLaterTakesOverTheStayFitGoalsDays`: profile Tue/Thu/Sat/Sun, an undated goal filled, then a dated goal 20 weeks out. The race owns every available day to its event, and no day holds two sessions.
- `TestTheUndatedGoalComesBackAfterTheRace`.
- `TestARiderEditedStayFitSessionSurvivesTheTakeover`.
- `TestDeletingTheRaceLetsTheStayFitGoalRefill`.

### Task 2: Changing available days rebuilds the plan from today

**Rider decision:** "Rebuild from today".

When `PUT /api/training/profile` changes `availableDays` (or `hoursPerAvailableDay`), every goal of the rider gets the same rebuild as a sport change, from today. Untouched sessions move to the new days; touched ones stay.

**Tests:**
- `TestChangingAvailableDaysMovesUntouchedSessionsToTheNewDays`: Tue/Thu/Sat/Sun becomes Sat/Sun. No untouched future session is left on Tue or Thu, and Sat and Sun are filled this week (from today) and later.
- `TestChangingAvailableDaysKeepsTouchedSessions`.
- `TestChangingOnlyFTPDoesNotRebuild`. Pin today's behaviour, documented: FTP flows through refresh and recalibration.

### Task 3: Deleting a goal removes its untouched future sessions

**Rider decision:** "Remove untouched ones".

`DeleteGoal` today turns the goal's sessions into rider sessions. Instead, the handler first deletes the goal's untouched plan-made sessions dated today or later (except one ridden today), with their Garmin copies. Touched and past sessions keep today's behaviour (origin rider), and `scheduled_weeks` rows go.

**Tests:**
- `TestDeletingAGoalRemovesItsUntouchedFutureSessions`.
- `TestDeletingAGoalKeepsTheRidersOwnSessions`.
- `TestDeletingAGoalWithdrawsTodaysGarminCopy` (pushHarness).

### Task 4: Re-dating a goal re-anchors its plan

**Clear bug.** The plan is anchored at the goal's creation Monday (periodization.go:246). Giving an old, undated goal a date 10 weeks out compresses or skips the phases.

**Rule:** when `eventDate` changes, the plan anchor becomes this week's Monday, or a stored `plan_start` that the edit resets. Choose the smaller change: if the anchor is derived from `CreatedAt`, add a `plan_anchor` column set at create and reset on a date change, with the migration filling it from `created_at`. Then the same rebuild runs from today.

**Tests:**
- `TestRedatingAnOldGoalStartsItsPlanFromBase`: the phases match `periodization.Build` for a goal created today.
- `TestMovingTheEventLaterRebuildsTheTaperWeeks`: the old taper weeks become build content.
- `TestMovingTheEventEarlierKeepsATaperBeforeTheNewDate`.

### Task 5: Dated and undated switches, and a sport change over a week with a touched session

**Clear gaps.**

**Tests and fixes:**
- `TestMakingAGoalUndatedTrimsItToTwelveWeeksAndDropsTheTaper`.
- `TestDatingAnUndatedGoalFillsUpToTheEvent`: no duplicate days.
- `TestASportChangeFillsTheRestOfAWeekThatKeepsAnEditedSession`: the week with one edited cycling session gets running sessions on its other days. Today fill-once treats it as filled, so fix the fill to top up a week the rebuild forgot.

### Task 6: PATCH validation, and a sport change with today pushed

**Clear bugs.** `PATCH /api/training/goals/{id}` stores an unknown sport, a priority outside A/B/C or a malformed date as sent (workout/db.go ~1258-1301). A bad date logs an Error on every tick.

**Fix:** validate the same way create does, and answer 422 with a short message.

**Tests:**
- A table test for the 422s.
- `TestASportChangeWithTodayPushedWithdrawsTheCopyAndPushesTheNewSession`: newReplanHarness with auto-push on. The `workoutCalls` include a delete of the old copy and, with auto-push, a push of the running session in the same request rather than waiting for the tick.

### Task 7: Goal proposal phrasings

**Server-side fixes:**
- A year offset for "end of next year" / "volgend jaar": `eventYearOffset` 0 or 1. With 1, `placeMonth` uses next year even when the month is still ahead this year.
- Units: `targetDistanceUnit` "km"|"mi" and `targetElevationUnit` "m"|"ft", converted server-side.
- Unsupported sports (triathlon, swimming): sport `other`, which the server turns into **no proposal**. That is a 422 whose message says only cycling and running goals are supported, instead of a silent cycling goal.
- Weekday and "this weekend", "on the 14th": `eventDayOfMonth` (1-31), placed in the named month or the next month that has that day after today. "This weekend" goes to the coming Saturday via `eventWeekday` "sat"|"sun".

**Tests:** unit tests for every placement, conversion and refusal.

**Eval cases** (goal.json, compact style, text-appended):
- "this weekend";
- "on the 14th";
- "next spring" (month 4 mid);
- "volgend voorjaar";
- "over 9 maanden";
- "half juni";
- "14/06/2027";
- "a 62 mile ride";
- "2500 feet of climbing";
- "a sprint triathlon";
- "end of next year".

### Task 8: Priority shapes the taper

**Rider decision:** "Shape the taper".
- Priority A: the full taper and peak, as today.
- Priority B: a mini-taper of the last 3-4 days before the event. The week runs as a normal build or peak week, and the final days drop to the taper's daily load.
- Priority C: no taper. The event week is an ordinary training week, and the event day itself is the session.

The change lives in `periodization` (`Build`/`BuildWithLoad`), which reads `goal.Priority`. The race-day projection and the feasibility check read the plan, so they follow. Changing a goal's priority runs the same rebuild from today.

**Tests:**
- Periodization unit tests for each priority's last two weeks: phases, daily loads, planned CTL.
- `TestChangingPriorityToCRemovesTheTaper` over HTTP.
- A projection test: a C race shows its form without a taper.

---

## Review focus

1. Two goals overlapping in every direction: created before or after, dated or undated, deleted. A day must never hold two plan sessions.
2. Each rebuild running under a held lock retries, and does not skip.
3. Garmin copies: every removed session that had one is withdrawn, and no session is withdrawn twice.
4. Today's session that was already ridden is never deleted by any rebuild.
5. A rider with auto-schedule off still gets every rebuild, because the rider asked for it.
