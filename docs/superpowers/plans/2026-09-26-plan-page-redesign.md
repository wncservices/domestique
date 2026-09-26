# Plan page redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn Training → Plan into a today / this week / season view with planned-vs-completed status, backed by a new `GET /api/training/week` endpoint, and replace the workout/goal modals with slide-overs and a friendlier step editor.

**Architecture:** A new pure Go package `internal/compliance` classifies each day; `internal/api` gains one handler that assembles a week from the existing workout store, completed sessions and `reconciledPeriodizationPlan`. The Vue page is split into focused components under `apps/web/src/components/plan/`, all Nuxt UI + hand-rolled SVG, no new dependencies.

**Tech Stack:** Go 1.x stdlib `net/http`, SQLite/PostgreSQL via existing `workout.DB`; Vue 3 `<script setup lang="ts">`, Nuxt UI v4 (auto-imported `U*` components), Tailwind v4 tokens from `apps/web/src/styles.css`.

**Spec:** `docs/superpowers/specs/2026-09-26-plan-page-redesign-design.md` — read it before starting any task.

## Global Constraints

- Read `AGENTS.md` (repo root) and `docs/design-system.md` before writing code.
- No new Go or npm dependencies.
- `gofmt` clean, `go vet` clean; `just check` must pass at the end of every task (runs `npm --workspace @domestique/web run typecheck`, `go vet`, `go test ./apps/api/...`).
- Comments explain *why*, not what — match the surrounding comment density.
- DTOs in `apps/api/internal/api/*.go` and `apps/web/src/api/types.ts` mirror each other by hand; change them together.
- Nuxt UI semantic classes only (`text-muted`, `text-highlighted`, `bg-elevated`, `border-default`, …) and the `--app-accent-*` vars; never raw Tailwind palette colours (`text-red-500`) and never `dark:` variants.
- Semantic colours mean compliance status only: `done`→`success`, `partial`→`warning`, `missed`→`error`, `unplanned`→`info`, `upcoming`→`neutral`, `rest`→`neutral` dimmed. Every status is also shown by an icon.
- Phase colours are categorical: base→`neutral`, build→`sky`, peak→`ember`, taper→`violet`. Never `primary` for a phase.
- Numbers (durations, hours, watts) use `font-mono tabular-nums`.
- UI copy: sentence case, no "successfully", no "please", no exclamation marks.
- Owner-only data: the rider always comes from `auth.FromContext(r.Context()).User`, never a query parameter.
- Never commit `.env*`, credentials, or real GPX/route data.
- Commit message trailer on every commit: `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`.

## Review Focus

1. **A rider with goals but no fitness profile** (no hours/available days) — `/api/training/week` must still return 200 with a focus goal (week fields present, `targetHours` 0/omitted) and the page must render without "NaN h". Test in Task 2 (`TestTrainingWeekWithoutProfile`).
2. **A goal whose event is in the past** — `periodization.Build` returns `ErrEventInThePast`; the week endpoint must skip that goal for focus, not 400/500. Test in Task 2 (`TestTrainingWeekSkipsPastGoals`).
3. **Another rider's workouts/sessions on the same dates** — must never appear. Test in Task 2 (`TestTrainingWeekIsOwnerOnly`).
4. **Workouts with no date** (tests, templates) — never in the week, still listed in the library section. Test in Task 2 (`TestTrainingWeekIgnoresUndatedWorkouts`); UI in Task 5.
5. **Workout builder with no FTP on file** — % input must fall back to absolute input, never write `NaN`/`0` targets. Covered by Task 3's `workoutMath` checks and Task 6's editor behaviour.

---

### Task 1: `internal/compliance` package

**Files:**
- Create: `apps/api/internal/compliance/compliance.go`
- Test: `apps/api/internal/compliance/compliance_test.go`

**Interfaces:**
- Consumes: `workout.Workout` (`Date`, `Sport model.Sport`, `Steps`), `workout.CompletedSession` (`Date`, `Sport string`, `DurationSeconds`), `workout.PlannedSeconds([]WorkoutStep) float64`.
- Produces:
  ```go
  type Status string
  const (StatusDone Status = "done"; StatusPartial = "partial"; StatusMissed = "missed"; StatusRest = "rest"; StatusUpcoming = "upcoming"; StatusUnplanned = "unplanned")
  func Day(date, today string, planned []workout.Workout, completed []workout.CompletedSession) Status
  ```

- [ ] **Step 1: Check the field types** — run `grep -n "Sport" apps/api/internal/workout/workout.go | head` and confirm `Workout.Sport` is `model.Sport` (a string type) and `CompletedSession.Sport` is `string`. Compare with `string(w.Sport) == s.Sport`.

- [ ] **Step 2: Write the failing test**

```go
package compliance_test

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/compliance"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func planned(date string, seconds float64) workout.Workout {
	return workout.Workout{Date: date, Sport: "cycling", Steps: []workout.WorkoutStep{
		{Duration: workout.DurationTime, Seconds: seconds, Target: workout.TargetOpen},
	}}
}

func done(date, sport string, seconds float64) workout.CompletedSession {
	return workout.CompletedSession{Date: date, Sport: sport, DurationSeconds: seconds}
}

func TestDay(t *testing.T) {
	const today = "2026-09-29"
	openOnly := workout.Workout{Date: "2026-09-28", Sport: "cycling", Steps: []workout.WorkoutStep{{Duration: workout.DurationOpen, Target: workout.TargetOpen}}}

	cases := []struct {
		name      string
		date      string
		planned   []workout.Workout
		completed []workout.CompletedSession
		want      compliance.Status
	}{
		{"nothing at all is rest", "2026-09-28", nil, nil, compliance.StatusRest},
		{"ride with no plan is unplanned", "2026-09-28", nil, []workout.CompletedSession{done("2026-09-28", "cycling", 3600)}, compliance.StatusUnplanned},
		{"80% is done", "2026-09-28", []workout.Workout{planned("2026-09-28", 3600)}, []workout.CompletedSession{done("2026-09-28", "cycling", 2880)}, compliance.StatusDone},
		{"just under 80% is partial", "2026-09-28", []workout.Workout{planned("2026-09-28", 3600)}, []workout.CompletedSession{done("2026-09-28", "cycling", 2870)}, compliance.StatusPartial},
		{"30% is partial", "2026-09-28", []workout.Workout{planned("2026-09-28", 3600)}, []workout.CompletedSession{done("2026-09-28", "cycling", 1080)}, compliance.StatusPartial},
		{"past day under 30% is missed", "2026-09-28", []workout.Workout{planned("2026-09-28", 3600)}, []workout.CompletedSession{done("2026-09-28", "cycling", 600)}, compliance.StatusMissed},
		{"past day with nothing is missed", "2026-09-28", []workout.Workout{planned("2026-09-28", 3600)}, nil, compliance.StatusMissed},
		{"today with nothing yet is upcoming", today, []workout.Workout{planned(today, 3600)}, nil, compliance.StatusUpcoming},
		{"future day is upcoming", "2026-10-01", []workout.Workout{planned("2026-10-01", 3600)}, nil, compliance.StatusUpcoming},
		{"other sport does not count", "2026-09-28", []workout.Workout{planned("2026-09-28", 3600)}, []workout.CompletedSession{done("2026-09-28", "running", 3600)}, compliance.StatusMissed},
		{"sessions add up", "2026-09-28", []workout.Workout{planned("2026-09-28", 3600)}, []workout.CompletedSession{done("2026-09-28", "cycling", 1800), done("2026-09-28", "cycling", 1200)}, compliance.StatusDone},
		{"open-ended plan with any matching ride is done", "2026-09-28", []workout.Workout{openOnly}, []workout.CompletedSession{done("2026-09-28", "cycling", 60)}, compliance.StatusDone},
		{"open-ended plan with nothing is missed", "2026-09-28", []workout.Workout{openOnly}, nil, compliance.StatusMissed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := compliance.Day(c.date, today, c.planned, c.completed); got != c.want {
				t.Errorf("Day() = %q, want %q", got, c.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run it to see it fail** — `go test ./apps/api/internal/compliance/` → FAIL (package does not exist / undefined).

- [ ] **Step 4: Implement**

```go
// Package compliance answers "did the rider do what was planned?" for one
// day, the question the Plan page's week strip colours every tile by. Pure
// on purpose — no store, no clock — so the thresholds below are pinned by a
// table test rather than by whatever a handler happened to pass in.
package compliance

import "github.com/wncservices/domestique/apps/api/internal/workout"

type Status string

const (
	StatusDone      Status = "done"
	StatusPartial   Status = "partial"
	StatusMissed    Status = "missed"
	StatusRest      Status = "rest"
	StatusUpcoming  Status = "upcoming"
	StatusUnplanned Status = "unplanned"
)

// The same 80% line TrainingPeaks draws for "green": close enough that a
// ride cut short by a flat tyre still counts, not so loose that a coffee
// spin passes for a long ride.
const (
	doneRatio    = 0.8
	partialRatio = 0.3
)

// Day classifies date. Dates are "YYYY-MM-DD", so string comparison is
// calendar order. Only sessions of a planned sport count toward the ratio:
// a run on a bike day is training, but it is not the session that was
// asked for.
func Day(date, today string, planned []workout.Workout, completed []workout.CompletedSession) Status {
	if len(planned) == 0 {
		if len(completed) > 0 {
			return StatusUnplanned
		}
		return StatusRest
	}

	sports := make(map[string]bool, len(planned))
	var plannedSeconds float64
	for _, w := range planned {
		sports[string(w.Sport)] = true
		plannedSeconds += workout.PlannedSeconds(w.Steps)
	}
	var matched float64
	var anyMatch bool
	for _, s := range completed {
		if sports[s.Sport] {
			matched += s.DurationSeconds
			anyMatch = true
		}
	}

	// Open or distance-only steps have no honest duration (see
	// workout.PlannedSeconds); any matching ride is the best evidence there is.
	if plannedSeconds == 0 {
		if anyMatch {
			return StatusDone
		}
	} else {
		ratio := matched / plannedSeconds
		if ratio >= doneRatio {
			return StatusDone
		}
		if ratio >= partialRatio {
			return StatusPartial
		}
	}
	if date < today {
		return StatusMissed
	}
	return StatusUpcoming
}
```

- [ ] **Step 5: Run tests** — `go test ./apps/api/internal/compliance/` → PASS; `gofmt -l apps/api/internal/compliance` → empty.

- [ ] **Step 6: Commit**

```bash
git add apps/api/internal/compliance
git commit -m "Classify a training day as done, partial, missed or rest

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 2: `GET /api/training/week` and `plannedSeconds`

**Files:**
- Create: `apps/api/internal/api/trainingweek.go`
- Modify: `apps/api/internal/api/training.go` (`workoutDTO` + `workoutDTOFrom`, ~line 151-172)
- Modify: `apps/api/internal/api/server.go` (register route next to `GET /api/training/fitness`, ~line 452)
- Test: `apps/api/internal/api/trainingweek_test.go` (package `api_test`, uses `newTrainingHarness`, `h.as`, `h.seedSession`, `h.store` from `training_test.go`)
- Modify: `apps/web/src/api/types.ts`, `apps/web/src/api/client.ts`

**Interfaces:**
- Consumes: `compliance.Day` (Task 1); `s.reconciledPeriodizationPlan(ctx, g, rider) (periodization.Plan, workout.RiderProfile, error)`; `periodization.MondayOf(time.Time) time.Time`; `s.now()`; `s.Training.ListGoals/ListWorkouts/ListSessions(ctx, rider)`; `completedSessionDTO` (training.go ~line 1043); `Server.Clock func() time.Time`.
- Produces (JSON, consumed by Tasks 3–6):
  ```ts
  export type DayStatus = 'done' | 'partial' | 'missed' | 'rest' | 'upcoming' | 'unplanned'
  export interface WeekFocus { goalId: string; name: string; priority: GoalPriority; sport: Sport; eventDate?: string; daysToEvent?: number; weekNumber?: number; totalWeeks?: number; phase?: PeriodizationPhase; recovery?: boolean; targetHours?: number }
  export interface WeekDay { date: string; status: DayStatus; planned: Workout[]; completed: CompletedSession[] }
  export interface TrainingWeek { start: string; end: string; today: string; focus?: WeekFocus; days: WeekDay[]; totals: { plannedSeconds: number; completedSeconds: number } }
  // Workout gains: plannedSeconds: number
  // api.trainingWeek(start?: string): Promise<TrainingWeek>
  ```

- [ ] **Step 1: Add `plannedSeconds` to the workout DTO.** In `training.go`, add field `PlannedSeconds float64 \`json:"plannedSeconds"\`` to `workoutDTO` after `Steps`, and set `PlannedSeconds: workout.PlannedSeconds(w.Steps)` in `workoutDTOFrom`. Comment: `// So the UI can show "1h 15m" without re-implementing repeat-block arithmetic.`

- [ ] **Step 2: Write the failing handler tests** in `trainingweek_test.go`. Use a fixed clock so dates are deterministic: after `h := newTrainingHarness(t)`, set `h.srv.Clock = func() time.Time { return time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC) }` (a Wednesday; its Monday is 2026-09-28). Seed data through the API (`POST /api/training/goals`, `POST /api/training/workouts` with `{"name":"…","sport":"cycling","date":"2026-09-28","steps":[{"name":"ride","duration":"time","seconds":3600,"target":"open"}]}`) and sessions through `h.seedSession(rider, date, hours)`. Decode into a local struct mirroring the JSON above. Tests:

  ```go
  func TestTrainingWeekDefaultsToThisMonday(t *testing.T)       // GET /api/training/week → start "2026-09-28", end "2026-10-04", today "2026-09-30", 7 days in order
  func TestTrainingWeekSnapsToMonday(t *testing.T)              // ?start=2026-10-08 → start "2026-10-05"
  func TestTrainingWeekRejectsBadStart(t *testing.T)            // ?start=nope → 400
  func TestTrainingWeekStatusesAndTotals(t *testing.T)          // workout Mon 1h + session Mon 1h → Mon "done"; workout Tue 1h, no session → Tue "missed"; workout Thu 1h → Thu "upcoming"; session Sat 0.5h, no workout → Sat "unplanned"; Sun "rest"; totals.plannedSeconds 10800, totals.completedSeconds 5400; Mon planned[0].plannedSeconds 3600
  func TestTrainingWeekIsOwnerOnly(t *testing.T)                // rider "other" has a workout + session on Mon; "wilant" sees Mon "rest", empty planned/completed
  func TestTrainingWeekIgnoresUndatedWorkouts(t *testing.T)     // undated workout → appears in no day
  func TestTrainingWeekFocusPrefersPriorityThenDate(t *testing.T) // goals: B event 2026-11-01, A event 2026-12-20, C undated → focus.goalId is the A goal; weekNumber>=1, totalWeeks>=weekNumber, phase non-empty, daysToEvent == 81
  func TestTrainingWeekSkipsPastGoals(t *testing.T)             // only goal has eventDate 2026-09-01 → 200, focus absent
  func TestTrainingWeekWithoutProfile(t *testing.T)             // one A goal, no profile saved → 200, focus present, targetHours absent or 0
  func TestTrainingWeekNoGoals(t *testing.T)                    // → 200, focus absent, 7 days
  ```
  Use groups `"cyclists"` for the rider role. Write each test body fully (create, request, decode, assert with `t.Errorf` naming the field).

- [ ] **Step 3: Run** `go test ./apps/api/internal/api/ -run TrainingWeek` → FAIL (404 / fields missing).

- [ ] **Step 4: Implement `trainingweek.go`**

```go
package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/compliance"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const dateLayout = "2006-01-02"

type weekFocusDTO struct {
	GoalID      string  `json:"goalId"`
	Name        string  `json:"name"`
	Priority    string  `json:"priority"`
	Sport       string  `json:"sport"`
	EventDate   string  `json:"eventDate,omitempty"`
	DaysToEvent *int    `json:"daysToEvent,omitempty"`
	WeekNumber  int     `json:"weekNumber,omitempty"`
	TotalWeeks  int     `json:"totalWeeks,omitempty"`
	Phase       string  `json:"phase,omitempty"`
	Recovery    bool    `json:"recovery,omitempty"`
	TargetHours float64 `json:"targetHours,omitempty"`
}

type weekDayDTO struct {
	Date      string                `json:"date"`
	Status    compliance.Status     `json:"status"`
	Planned   []workoutDTO          `json:"planned"`
	Completed []completedSessionDTO `json:"completed"`
}

type weekTotalsDTO struct {
	PlannedSeconds   float64 `json:"plannedSeconds"`
	CompletedSeconds float64 `json:"completedSeconds"`
}

type trainingWeekDTO struct {
	Start  string        `json:"start"`
	End    string        `json:"end"`
	Today  string        `json:"today"`
	Focus  *weekFocusDTO `json:"focus,omitempty"`
	Days   []weekDayDTO  `json:"days"`
	Totals weekTotalsDTO `json:"totals"`
}

// handleTrainingWeek is the Plan page's one read: seven days with what was
// planned, what was ridden and how the two compare, plus the focus goal's
// place in its plan. Assembled server-side so the matching rules live in
// Go next to the scheduler that produced the plan, not re-derived in the
// browser.
func (s *Server) handleTrainingWeek(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	ctx := r.Context()
	rider := auth.FromContext(ctx).User
	now := s.now()

	start := periodization.MondayOf(now)
	if q := r.URL.Query().Get("start"); q != "" {
		parsed, err := time.ParseInLocation(dateLayout, q, now.Location())
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "start must be YYYY-MM-DD"})
			return
		}
		start = periodization.MondayOf(parsed)
	}
	today := now.Format(dateLayout)

	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	plannedBy := map[string][]workout.Workout{}
	for _, wk := range workouts {
		if wk.Date != "" {
			plannedBy[wk.Date] = append(plannedBy[wk.Date], wk)
		}
	}
	doneBy := map[string][]workout.CompletedSession{}
	for _, sess := range sessions {
		doneBy[sess.Date] = append(doneBy[sess.Date], sess)
	}

	dto := trainingWeekDTO{
		Start: start.Format(dateLayout),
		End:   start.AddDate(0, 0, 6).Format(dateLayout),
		Today: today,
		Days:  make([]weekDayDTO, 0, 7),
	}
	for i := 0; i < 7; i++ {
		date := start.AddDate(0, 0, i).Format(dateLayout)
		day := weekDayDTO{
			Date:      date,
			Status:    compliance.Day(date, today, plannedBy[date], doneBy[date]),
			Planned:   make([]workoutDTO, 0, len(plannedBy[date])),
			Completed: make([]completedSessionDTO, 0, len(doneBy[date])),
		}
		for _, wk := range plannedBy[date] {
			d := workoutDTOFrom(wk)
			day.Planned = append(day.Planned, d)
			dto.Totals.PlannedSeconds += d.PlannedSeconds
		}
		for _, sess := range doneBy[date] {
			day.Completed = append(day.Completed, completedSessionDTOFrom(sess))
			dto.Totals.CompletedSeconds += sess.DurationSeconds
		}
		dto.Days = append(dto.Days, day)
	}

	focus, err := s.weekFocus(r, rider, start, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	dto.Focus = focus
	writeJSON(w, http.StatusOK, dto)
}

// weekFocus picks the goal the header talks about: the most important one
// whose plan covers this week, nearest event first, dated before undated —
// a race on the calendar outranks "keep training". A goal that cannot be
// planned (event already past) is skipped rather than failing the page.
func (s *Server) weekFocus(r *http.Request, rider string, start, now time.Time) (*weekFocusDTO, error) {
	goals, err := s.Training.ListGoals(r.Context(), rider)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(goals, func(i, j int) bool {
		a, b := goals[i], goals[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority // "A" < "B" < "C"
		}
		if (a.EventDate == "") != (b.EventDate == "") {
			return a.EventDate != ""
		}
		return a.EventDate < b.EventDate
	})

	startDate := start.Format(dateLayout)
	for _, g := range goals {
		plan, _, err := s.reconciledPeriodizationPlan(r.Context(), g, rider)
		if err == periodization.ErrNoEventDate || err == periodization.ErrEventInThePast {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, wk := range plan.Weeks {
			if wk.StartDate != startDate {
				continue
			}
			f := &weekFocusDTO{
				GoalID: g.ID, Name: g.Name, Priority: string(g.Priority), Sport: string(g.Sport),
				EventDate: g.EventDate, WeekNumber: wk.Number, TotalWeeks: len(plan.Weeks),
				Phase: string(wk.Phase), Recovery: wk.Recovery, TargetHours: wk.TargetHours,
			}
			if g.EventDate != "" {
				if ev, err := time.ParseInLocation(dateLayout, g.EventDate, now.Location()); err == nil {
					today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
					days := int(ev.Sub(today).Hours()/24 + 0.5)
					f.DaysToEvent = &days
				}
			}
			return f, nil
		}
	}
	return nil, nil
}
```

  Before writing: confirm `periodization.Week.StartDate` is formatted `"2006-01-02"` (`grep -n StartDate apps/api/internal/periodization/periodization.go`), and that `g.Priority`/`g.Sport` are string types. If `completedSessionDTO` has no constructor, add `func completedSessionDTOFrom(sess workout.CompletedSession) completedSessionDTO` in `training.go` and make `handleGetFitness` use it too (the literal at ~line 1100 moves into it). **Week not covering start**: a goal whose plan has no week starting on `start` (e.g. browsing a past week) yields no focus from that goal — try the next goal; if none, `nil`. That is the spec's behaviour.

- [ ] **Step 5: Register the route** in `server.go` after `GET /api/training/fitness`:
  `mux.HandleFunc("GET /api/training/week", s.handleTrainingWeek)`

- [ ] **Step 6: Run** `go test ./apps/api/internal/api/ -run 'TrainingWeek|Workout|Fitness'` → PASS, then `go test ./apps/api/...` → PASS, `go vet ./apps/api/...`, `gofmt -l apps/api` empty.

- [ ] **Step 7: Frontend types and client.** In `types.ts` add `plannedSeconds: number` to `Workout` (doc comment: time steps only; 0 means unknown), and add `DayStatus`, `WeekFocus`, `WeekDay`, `TrainingWeek` exactly as in **Interfaces** above, next to `FitnessResponse`. In `client.ts` training section add:
  ```ts
  /** One Monday–Sunday week of planned vs completed training — see
   *  internal/api/trainingweek.go. start snaps to its Monday server-side. */
  trainingWeek: (start?: string) =>
    request<TrainingWeek>(`/api/training/week${start ? `?start=${encodeURIComponent(start)}` : ''}`),
  ```
  Import `TrainingWeek` in client.ts. Run `npm --workspace @domestique/web run typecheck` → PASS.

- [ ] **Step 8: Commit**

```bash
git add apps/api apps/web/src/api
git commit -m "Add GET /api/training/week: planned vs completed, with the focus goal's phase

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 3: `workoutMath.ts` and `WorkoutProfile.vue`

**Files:**
- Create: `apps/web/src/utils/workoutMath.ts`
- Create: `apps/web/src/components/plan/WorkoutProfile.vue`

**Interfaces:**
- Consumes: `WorkoutStep`, `StepIntensity`, `StepTarget`, `RiderProfile` from `@/api/types`.
- Produces:
  ```ts
  export interface FlatStep { seconds: number; intensity: StepIntensity | undefined; level: number } // level 0..1.5, relative effort for chart height
  export function flattenSteps(steps: WorkoutStep[], profile?: RiderProfile): FlatStep[]
  export function formatDuration(seconds: number): string   // 0→"—", 45→"45s", 900→"15m", 4500→"1h 15m"
  export function formatClock(seconds: number): string      // 90→"1:30", 3725→"1:02:05"
  export function parseClock(text: string): number | null   // "1:30"→90; a bare number is minutes: "90"→5400; "1:02:05"→3725; ""/"abc"/"-1"→null
  export function thresholdFor(target: StepTarget, profile: RiderProfile): number | null // power→ftpWatts; heart_rate→maxHr; pace→1000/thresholdPaceSecPerKm (m/s); cadence/open→null; missing/0→null
  export function toPercent(value: number, threshold: number): number   // Math.round(value/threshold*100)
  export function fromPercent(percent: number, threshold: number, target: StepTarget): number // pace → 2 decimals, everything else → integer
  export function describeTarget(step: WorkoutStep, profile: RiderProfile): string // "248–270 W · 92–100% FTP", "140–150 bpm", "" for open
  ```
  `WorkoutProfile.vue` props: `{ steps: WorkoutStep[]; profile?: RiderProfile; height?: number }` (default height 48). Renders nothing (a dashed "No timed steps" placeholder line of `text-dimmed text-xs`) when total seconds is 0.

- [ ] **Step 1: Write `workoutMath.ts`.** `flattenSteps` expands repeat blocks (`(repeat ?? 0) >= 2` → children repeated `repeat` times, recursively), keeps only `duration === 'time'` steps with `seconds > 0`. `level`: if the step has a target with a known threshold and `targetHigh`/`targetLow`, use `midpoint/threshold` clamped to [0.3, 1.5]; else by intensity: warmup/cooldown 0.5, recovery/rest 0.4, active 0.7, interval 1.0, other/undefined 0.6. Comment the fallback: scheduler-generated and hand-built workouts both need a shape even without a profile.

- [ ] **Step 2: Verify with a throwaway script (not committed).** Write `$SCRATCH/wm-check.ts` importing `../path/to/apps/web/src/utils/workoutMath.ts` by absolute path, asserting with `node:assert`: `formatDuration(4500)==='1h 15m'`, `formatDuration(900)==='15m'`, `formatClock(3725)==='1:02:05'`, `parseClock('1:30')===90`, `parseClock('90')===5400`, `parseClock('abc')===null`, `parseClock('')===null`, `thresholdFor('power',{})===null`, `thresholdFor('power',{ftpWatts:250})===250`, `fromPercent(100,250,'power')===250`, `toPercent(270,250)===108`, a repeat block `{repeat:4, steps:[{duration:'time',seconds:480,…},{duration:'time',seconds:120,…}]}` flattens to 8 steps. Run with `node --experimental-strip-types $SCRATCH/wm-check.ts` (Node 24; the flag is harmless if already default). `workoutMath.ts` must use only type imports from `@/api/types` (`import type`) so Node can strip them — if the `@/` alias blocks running it, temporarily copy the file to scratch and fix the import there. Expected: no output, exit 0.

- [ ] **Step 3: Write `WorkoutProfile.vue`.** SVG with `role="img"` and `<title>` ("Workout profile, {formatDuration(total)}"), width tracked via `useTemplateRef` + `clientWidth` exactly like `FitnessChart.vue` (read it first), `preserveAspectRatio="none"` not used. Each flat step is a `<rect>` whose x/width are proportional to seconds and height to `level / 1.5 * height`, bottom-aligned, 1px gap between rects. Fill by intensity via classes that read tokens: interval → `var(--app-accent-ember)`, active → `var(--app-accent-sky)`, everything else → `var(--ui-border-accented)`. No text inside the chart.

- [ ] **Step 4: Typecheck** — `npm --workspace @domestique/web run typecheck` → PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/web/src/utils/workoutMath.ts apps/web/src/components/plan/WorkoutProfile.vue
git commit -m "Add workout math helpers and an interval profile chart

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 4: Page shell — goal header, today card, week strip

**Files:**
- Create: `apps/web/src/components/plan/PlanGoalHeader.vue`
- Create: `apps/web/src/components/plan/TodayCard.vue`
- Create: `apps/web/src/components/plan/WeekStrip.vue`
- Modify: `apps/web/src/pages/TrainingPlanPage.vue`

**Interfaces:**
- Consumes: `api.trainingWeek`, `api.updateWorkout`, `api.scheduleGoal`, `api.pushWorkoutToGarmin`, `api.workoutFitUrl`, `api.explainPlan`, `TrainingWeek`, `WeekDay`, `WeekFocus`, `Workout`, `Goal`, `Me`, `RiderProfile`; `WorkoutProfile.vue`, `formatDuration`, `describeTarget` (Task 3); `useLibrary().canSyncGarmin`.
- Produces:
  - `PlanGoalHeader.vue` props `{ focus?: WeekFocus; goals: Goal[]; narrationEnabled: boolean; explaining: boolean; explanation: string }`, emits `explain: []`, `newGoal: []`, `newWorkout: []`, `selectGoal: [goalId: string]`.
  - `TodayCard.vue` props `{ day?: WeekDay; yesterday?: WeekDay; profile: RiderProfile; canSyncGarmin: boolean; pushing: string }`, emits `push: [w: Workout]`, `edit: [w: Workout]`, `move: [w: Workout, date: string]`.
  - `WeekStrip.vue` props `{ week: TrainingWeek; profile: RiderProfile; canFill: boolean; filling: boolean }`, emits `prev: []`, `next: []`, `thisWeek: []`, `move: [w: Workout, date: string]`, `open: [w: Workout]`, `fill: []`.
  - `TrainingPlanPage.vue` keeps its existing goal/workout modals and all existing handlers for now (Task 6 replaces the modals); it gains `week = ref<TrainingWeek|null>`, `weekStart = ref<string|undefined>`, `loadWeek()`, `moveWorkout(w, date)`, and a `focusGoalId` override (`selectGoal` sets it; when set and different from `week.focus.goalId`, header shows that goal's name/priority from `goals` without phase fields).

- [ ] **Step 1: Read first**: `TrainingPlanPage.vue` (whole), `docs/design-system.md` (icon-chip pattern, eyebrow labels, spacing), `App.vue` stats tiles for the icon-chip markup, and `apps/web/src/styles.css` for `--app-accent-*` names.

- [ ] **Step 2: `PlanGoalHeader.vue`.** Left: icon-chip (`i-lucide-flag`, accent `primary`) + goal name (`text-lg font-semibold text-highlighted`) + priority `UBadge` (neutral subtle) + a muted line: `"{daysToEvent} days to go · {Phase} · week {n} of {m}"` (drop missing parts; undated goal: `"Rolling plan · week {n}"`; recovery: append `" · recovery week"`). When `goals.length > 1`, the name is a `UDropdownMenu` trigger listing goals (`selectGoal`). Right: `UButton` "Explain" (`i-lucide-sparkles`, neutral soft, only when `narrationEnabled && focus`) and a `UDropdownMenu` "New" (`i-lucide-plus`, primary) with items "Goal" and "Workout". Below, when `explanation` is non-empty, a `UAlert` neutral subtle with the text and a close button that emits `explain` again (the page toggles). No focus and no goals → render nothing (the empty state in Task 5 takes over).

- [ ] **Step 3: `TodayCard.vue`.** `UCard variant="outline"`. Eyebrow `text-[0.7rem] uppercase tracking-wide text-dimmed`: "Today · Tue 29 Sep" (format with `toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short' })` on `day.date + 'T00:00:00'`). States, in order:
  1. `day.status === 'done'` → success `UIcon i-lucide-circle-check` + "Done" + completed vs planned duration (`font-mono tabular-nums`).
  2. `day.planned.length` → first planned workout: name (`text-xl font-semibold`), `formatDuration(plannedSeconds)` + `describeTarget` of the first targeted step, `<WorkoutProfile>` full width, adjustment note (parse `Adjusted automatically:` like the page's `adjustmentNote` — move that function to `workoutMath.ts` as `export function adjustmentNote(description?: string): string` and use it from both). Actions: primary `UButton` "Send to Garmin" (`i-lucide-watch`, only `canSyncGarmin`, `:loading="pushing === w.id"`), neutral outline "Move" — a `UDropdownMenu` with the remaining days of the week that are not today, each emitting `move` — "FIT" (`:to="api.workoutFitUrl(w.id)" target="_blank"`), ghost "Edit". A second planned workout shows as a muted "+1 more this day" line.
  3. no planned, no completed → `i-lucide-coffee` "Rest day" + muted "Nothing planned today."
  4. completed but unplanned → info icon + "Unplanned ride" + duration.
  Above the card content, when `yesterday?.status === 'missed'`: a small `UAlert` warning subtle, "Yesterday's {name} was missed. Move it to later this week?" with the same Move menu.

- [ ] **Step 4: `WeekStrip.vue`.** `UCard variant="outline"`. Header row: `UButton` ghost icon `i-lucide-chevron-left` (`prev`), title "This week" when `week.start <= week.today <= week.end` else `"{start} – {end}"` formatted short, `i-lucide-chevron-right` (`next`), a "Today" ghost button when not on the current week (`thisWeek`), phase `UBadge` from `week.focus` coloured per Global Constraints (map phase→`neutral|info`… **no**: Nuxt UI badge `color` only takes semantic names, so render the phase chip as a `<span>` with `style="background: var(--app-accent-{sky|ember|violet}-soft); color: var(--app-accent-{…})"`, base uses `bg-elevated text-muted`). Right: hours progress — `"{done} / {planned or target} h"` in mono and a `UProgress` of `completedSeconds / max(plannedSeconds, targetHours*3600)`.
  Grid: `grid grid-cols-7 gap-2` at `sm:` and up; below `sm`, `flex overflow-x-auto snap-x` with tiles `min-w-[7.5rem] snap-start`. Each tile (`rounded-lg border p-2 flex flex-col gap-1 min-h-28`):
  - header: weekday short + day number; status icon: done `i-lucide-circle-check text-success`, partial `i-lucide-circle-dot-dashed text-warning`, missed `i-lucide-circle-x text-error`, unplanned `i-lucide-circle-plus text-info`, upcoming none, rest none; today's tile gets `border-primary ring-1 ring-primary`, others `border-default`; rest tiles `border-dashed text-dimmed`.
  - each planned workout: a draggable chip (`draggable="true"`, `@dragstart` sets `dataTransfer.setData('text/plain', w.id)`), name (truncate), `formatDuration`, mini `<WorkoutProfile :height="16">`, wand icon with `UTooltip` showing `adjustmentNote` when non-empty; click → `open`; a `UDropdownMenu` (`i-lucide-ellipsis-vertical`, `aria-label="Move {name}"`) listing the other 6 days → `move`. Only future/today workouts are draggable (past ones are history).
  - completed sessions: muted line with `i-lucide-activity` + duration.
  - drop target: `@dragover.prevent` (only for dates ≥ today) + `@drop` reads the id, finds the workout in `week.days`, emits `move(w, tile.date)` when the date differs; highlight with `bg-elevated` while dragged over.
  Footer: when `canFill` and no day has planned workouts and `week.focus` exists → primary soft `UButton` "Fill this week" (`i-lucide-calendar-plus`, `:loading="filling"`) emitting `fill`, with muted helper "Builds this week's sessions from your plan."

- [ ] **Step 5: Wire the page.** In `TrainingPlanPage.vue`: remove the "Manual builder" `UAlert`. Add `loadWeek()` (calls `api.trainingWeek(weekStart.value)`, toast on error like the others). `prev/next` shift `weekStart` by ∓7 days from `week.value.start` (build the date with `new Date(start + 'T00:00:00')`, `setDate`, format `YYYY-MM-DD` via a local helper — do not use `toISOString()`, it shifts the day in negative-offset timezones). `thisWeek` sets `weekStart` undefined. `moveWorkout(w, date)` → `api.updateWorkout(w.id, { date })`, toast `"Moved {name} to {weekday}"` with `i-lucide-calendar-check`, then `loadWeek()` and `loadWorkouts()`. `fill` → existing `scheduleGoal` for `week.focus.goalId`, then `loadWeek()`. `canFill` = profile has `hoursPerAvailableDay` and `availableDays?.length`. After every existing save/delete/push handler that reloads workouts or goals, also call `loadWeek()`. Template order: `PlanGoalHeader`, `TodayCard` (today = `week.days.find(d => d.date === week.today)`, yesterday = previous index; only when viewing the current week), `WeekStrip`, then the existing Goals and Workouts cards unchanged (Task 5 replaces them). Header `newGoal`/`newWorkout` call the existing `openCreateGoal`/`openCreateWorkout`; `explain` calls `explainPlan` for the focus goal (it needs a `Goal`: look it up in `goals`). Delete the now-unused "Plan" periodization toggle UI? **No** — leave the goals card as-is in this task.

- [ ] **Step 6: Verify** — `npm --workspace @domestique/web run typecheck` → PASS; `just check` → PASS.

- [ ] **Step 7: Commit**

```bash
git add apps/web/src
git commit -m "Plan page: today card and a week strip of planned vs completed sessions

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 5: Season timeline, goals section, empty state

**Files:**
- Create: `apps/web/src/components/plan/SeasonTimeline.vue`
- Create: `apps/web/src/components/plan/GoalsSection.vue`
- Create: `apps/web/src/components/plan/PlanEmptyState.vue`
- Modify: `apps/web/src/pages/TrainingPlanPage.vue`

**Interfaces:**
- Consumes: `api.goalPeriodization(id)`, `PeriodizationPlan`, `PeriodizationWeek`, `Goal`, `Workout`, `TrainingWeek`; page handlers `openEditGoal`, `deleteGoal`, `openEditWorkout`, `deleteWorkout`, `pushWorkoutToGarmin`, `startGeneralPlan`, `openCreateGoal`; `formatDuration` (Task 3).
- Produces:
  - `SeasonTimeline.vue` props `{ plan: PeriodizationPlan; eventDate?: string; today: string; selectedStart: string }`, emits `select: [startDate: string]`.
  - `GoalsSection.vue` props `{ goals: Goal[]; workouts: Workout[]; focusGoalId?: string; canSyncGarmin: boolean; deletingGoal: string; deletingWorkout: string; pushingWorkout: string }`, emits `editGoal: [g: Goal]`, `deleteGoal: [g: Goal]`, `focusGoal: [id: string]`, `editWorkout: [w: Workout]`, `deleteWorkout: [w: Workout]`, `pushWorkout: [w: Workout]`, `newWorkout: []`.
  - `PlanEmptyState.vue` props `{ narrationEnabled: boolean; starting: boolean }`, emits `describe: []`, `fromRoute: []`, `general: []`.

- [ ] **Step 1: `SeasonTimeline.vue`** — `UCard variant="outline"`, header "Season" + muted `"{totalWeeks} weeks"` and, when `plan.adjustment` is outside 0.99–1.01, a muted note "Upcoming weeks adjusted to {pct}% based on recent training." (move the copy from the old table). SVG, width tracked like `FitnessChart.vue`, height 96: one bar per week (x by index, height ∝ `targetHours / maxTargetHours`, min 4px when `targetHours` is 0/undefined), bars coloured by phase using the same accents as the week strip's phase chip; recovery weeks at 50% opacity with a `stroke-dasharray` outline; the week whose `startDate === selectedStart` gets a `var(--ui-primary)` 2px outline. A band row under the bars (12px) groups consecutive same-phase weeks into one rounded rect with the phase name as SVG `<text>` when the band is ≥ 48px wide (`fill: currentColor` on a `text-muted` class). A vertical dashed line for `today` (the week containing it) and a flag marker (`▲` is not allowed — use a small circle) for `eventDate`'s week, each with an SVG `<title>`. Every bar is a `<g role="button" tabindex="0" :aria-label="`Week ${n}, ${phase}, ${hours} h`">` that emits `select(startDate)` on click and on Enter/Space. Below the SVG, a legend of the four phases (chip + label).

- [ ] **Step 2: `GoalsSection.vue`** — `UCard variant="outline"` titled "Goals and workouts". Goals as a list of rows: name, priority badge, `sport · eventDate or "rolling"` · km · m; a "Focus" `UBadge` (primary subtle) on `focusGoalId`, otherwise a ghost `UButton` "Set as focus" (`focusGoal`); pencil and trash ghost icon buttons with `aria-label`s. Then a sub-heading "Workout library" (eyebrow style) listing `workouts.filter(w => !w.date)` with name, `formatDuration(plannedSeconds)`, and the same push/download/edit/delete icon buttons the old list had (copy them, including `title`s). A ghost `UButton` "Build a workout" (`newWorkout`). Empty library → muted "Tests and templates you build without a date live here."

- [ ] **Step 3: `PlanEmptyState.vue`** — centred card, icon-chip `i-lucide-flag`, heading "Start your plan", body "Pick what you're training for, and Domestique builds the weeks toward it." Three option cards in a `grid sm:grid-cols-3 gap-3` (each a `button` with icon, title, one-line description): "Describe it" (`i-lucide-sparkles`, only when `narrationEnabled`) → `describe`; "Start from a route" (`i-lucide-route`, "Pick a route in your library and press Train for this route.") → `fromRoute`; "Keep training" (`i-lucide-wand-sparkles`, "Twelve rolling weeks of steady base, no event needed.", shows loading when `starting`) → `general`.

- [ ] **Step 4: Wire the page.** Remove the old Goals card (including the periodization table, `togglePeriodization`, `periodizationOpenFor`, `loadingPeriodization`, `phaseColors/phaseColor`) and the old Workouts card. Add `seasonPlan = ref<PeriodizationPlan|null>`, loaded via `api.goalPeriodization(focusId)` whenever the effective focus goal id changes (watch; clear on error without a toast when the error is the 400 "no event date"/"in the past" case — just hide the timeline). `SeasonTimeline @select` sets `weekStart` and reloads the week. `describe` → `openCreateGoal()` (the modal already has the Describe box); `fromRoute` → `router.push('/')` (the library, where "Train for this route" lives — confirm the library path in `apps/web/src/main.ts` router config first); `general` → `startGeneralPlan`. Show `PlanEmptyState` instead of header/today/week/timeline when `!loadingGoals && goals.length === 0`; `GoalsSection` only when `goals.length || workouts.length`. The "Schedule this week's workouts" button inside the removed expansion is replaced by the week strip's "Fill this week" (Task 4) — check nothing else referenced it.

- [ ] **Step 5: Verify** — `just check` → PASS.

- [ ] **Step 6: Commit**

```bash
git add apps/web/src
git commit -m "Plan page: season timeline, goals and library section, empty state

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 6: Goal and workout slide-overs, friendlier step editor

**Files:**
- Create: `apps/web/src/components/plan/GoalSlideover.vue`
- Create: `apps/web/src/components/plan/WorkoutSlideover.vue`
- Modify: `apps/web/src/components/WorkoutStepEditor.vue`
- Modify: `apps/web/src/pages/TrainingPlanPage.vue`

**Interfaces:**
- Consumes: everything in the page's current goal/workout modal code (`goalForm`, `saveGoal`, `proposeGoal`, `goalNote`, `goalExplanation`, `workoutForm`, `saveWorkout`, `NO_GOAL`, `goalOptions`, `sports`, `priorities`); `WorkoutProfile.vue`, `parseClock`, `formatClock`, `thresholdFor`, `toPercent`, `fromPercent`, `formatDuration` (Task 3); `RiderProfile`.
- Produces:
  - `GoalSlideover.vue`: `v-model:open`, props `{ editing: boolean; form: GoalForm; narrationEnabled: boolean; saving: boolean; proposing: boolean; explanation: string }`, `v-model:note` (string), emits `update:form`, `propose: []`, `save: []`. Export `type GoalForm` from the component's `<script lang="ts">` block or from `workoutMath.ts`'s sibling `apps/web/src/components/plan/forms.ts` (create it; also holds `WorkoutForm` and `NO_GOAL`).
  - `WorkoutSlideover.vue`: `v-model:open`, props `{ editing: boolean; form: WorkoutForm; goalOptions: {value:string;label:string}[]; profile: RiderProfile; saving: boolean }`, emits `update:form`, `save: []`.
  - `WorkoutStepEditor.vue`: adds optional prop `profile?: RiderProfile` (passed down recursively); `v-model` contract unchanged.

- [ ] **Step 1: `forms.ts`** — move `NO_GOAL`, the goal form type/`freshGoalForm`, and the workout form type/`freshWorkoutForm` out of the page into `components/plan/forms.ts`; keep the `NO_GOAL` comment about Reka UI's empty-string restriction verbatim.

- [ ] **Step 2: `GoalSlideover.vue`** — `USlideover` (`side="right"`, title "Add a goal"/"Edit goal", `:ui="{ content: 'max-w-lg' }"`) whose body is the existing goal form markup moved from the page unchanged in behaviour (Describe box first when narration and not editing), footer with Cancel / Save. Emit instead of mutating props: bind inputs with `:model-value` + `@update:model-value` producing `emit('update:form', { ...form, field: v })`.

- [ ] **Step 3: `WorkoutStepEditor.vue` rework** (keep the `update()` / `updateChildSteps()` patterns and their comments):
  - Duration `time`: replace the seconds input with a text `UInput` showing `formatClock(step.seconds ?? 0)`, `placeholder="mm:ss"`, committing on `blur`/`Enter` via `parseClock`; invalid input reverts and shows `color="error"` until corrected (local ref per row keyed by index).
  - Targets: when `thresholdFor(step.target, profile)` is non-null, show a `%`/absolute `UFieldGroup` toggle (default `%`); in `%` mode the low/high inputs show `toPercent(value, threshold)` and write `fromPercent(v, threshold, step.target)`; a muted hint after the inputs shows the other unit (`"248–270 W"` or `"92–100% FTP"`). When null, absolute only, with the old unit placeholders. Never write `NaN`: ignore empty/non-finite input.
  - Quick-add row replacing the two buttons: "Warmup" (10:00, warmup, 50–65% or open), "Intervals" (repeat block 4 × [8:00 interval 95–105%, 2:00 recovery 50–60%]; targets only when a power/HR threshold exists, else `target: 'open'`), "Steady" (20:00 active 70–80%), "Cooldown" (10:00 cooldown), "Step", "Repeat block" (the two originals). Percent defaults convert through `fromPercent` against the profile's power threshold for cycling (`ftpWatts`), heart-rate otherwise; with neither, `target: 'open'`.
  - Rows: collapse the wall of inputs into one line on `sm:` — name, intensity, duration, target, values — and wrap on mobile; add `aria-label`s to every icon-only button.

- [ ] **Step 4: `WorkoutSlideover.vue`** — `USlideover` (`side="right"`, `:ui="{ content: 'max-w-2xl' }"`), title "Build a workout"/"Edit workout". Sticky top section: `<WorkoutProfile :steps="form.steps" :profile="profile" :height="64">` and a mono summary `"{formatDuration(total)} · {n} steps"`. Then Name/Sport, Date/Goal fields (as in the old modal), then `<WorkoutStepEditor :profile="profile">`. Footer Cancel / Save (disabled rule unchanged: name and ≥1 step).

- [ ] **Step 5: Wire the page** — replace both `UModal`s with the slide-overs, passing `profile` (already loaded on the page). Remove the moved form helpers from the page and import from `forms.ts`. The `?goalFromRoute=` flow must still open the goal slide-over pre-filled.

- [ ] **Step 6: Verify** — `just check` → PASS. `wc -l apps/web/src/pages/TrainingPlanPage.vue` should now be well under the original 761 lines; if it is still > 450, move the goal-handling functions into `apps/web/src/composables/usePlanGoals.ts` (same function names, returned from the composable).

- [ ] **Step 7: Commit**

```bash
git add apps/web/src
git commit -m "Plan page: goal and workout slide-overs, mm:ss and %FTP step editing

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 7: Walkthrough and docs (controller, not a subagent)

- [ ] Run `just demo` / `just api` + `just web`, seed a goal, profile, workouts and sessions through the UI/API, and check in the browser pane at desktop and 375px, light and dark: today card states, drag and "Move to…" menu, prev/next week, timeline click, empty state, both slide-overs, % ↔ W toggle with and without FTP.
- [ ] Update `docs/training-plan.md` (one short section: the Plan page's layout and `GET /api/training/week`) and the `TrainingPage.vue`/`TrainingPlanPage.vue` doc comments where they describe the old layout.
- [ ] `just check`; whole-branch review; commit.
