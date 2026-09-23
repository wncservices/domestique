package adapter

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Reconcile (adapter.go) adapts the plan a week at a time, from how much the
// rider trained. This file adapts it a session at a time, from what happened
// to the sessions themselves — the part of "re-plan after every workout" that
// a weekly hours multiplier cannot do: Tuesday's intervals were missed, and
// Thursday's rider is exhausted.
//
// Two rules, both deliberately small:
//
//   - A missed key session is made up once, later this week: on a day the
//     rider can train that has nothing planned, or — since the scheduler books
//     every available day, which is the usual case — in place of the next easy
//     session, which is given up instead. Easy days are simply let go when
//     missed, nothing is lost by skipping them, and a key session with nowhere
//     to go is let go too, never stacked on top of another.
//   - A hard session is swapped for an easy one when form (TSB) is very
//     negative. TrainingPeaks' Performance Management Chart treats roughly
//     −30 and below as the high-risk zone where more intensity buys injury
//     and illness, not fitness.
//
// Only workouts the scheduler generated and nobody has touched are ever
// changed (scheduler.IsGenerated), and each is changed once: the note left in
// its description both tells the rider why and stops a second adjustment.
// Pure, like Reconcile: workouts, history and a clock in, changes out.

// fatigueTSB is the form below which hard sessions are swapped for easy ones.
const fatigueTSB = -30.0

// freshSnapshotDays is how old the latest fitness snapshot may be before it
// is not trusted to describe how the rider feels today.
const freshSnapshotDays = 2

// completedFraction is how much of a planned session's time counts as having
// done it. A rider who cut a ride short at 60% did the session; one who
// rode ten minutes of an hour did not.
const completedFraction = 0.5

// Change is one adjustment to a planned workout.
type Change struct {
	WorkoutID string
	// NewDate is set for a reschedule.
	NewDate string
	// ReplaceWorkoutID is set when the missed session takes over an easy
	// workout's day: that easy workout is removed.
	ReplaceWorkoutID string
	// Downgrade is set when the workout should be replaced by an easy one.
	Downgrade bool
	// Reason is shown to the rider.
	Reason string
}

// AdaptSessions decides what to change this week. latest may be nil when the
// rider has no fitness history.
func AdaptSessions(workouts []workout.Workout, sessions []workout.CompletedSession, profile workout.RiderProfile, latest *workout.FitnessSnapshot, today time.Time) []Change {
	day := func(t time.Time) string { return t.Format("2006-01-02") }
	todayStr := day(today)
	weekStart := day(mondayOf(today))
	weekEnd := day(mondayOf(today).AddDate(0, 0, 6))

	fatigued := latest != nil && latest.TSB < fatigueTSB && withinDays(latest.Date, today, freshSnapshotDays)

	taken := map[string]bool{}
	for _, w := range workouts {
		if w.Date != "" {
			taken[w.Date] = true
		}
	}
	available := map[string]bool{}
	for _, d := range profile.AvailableDays {
		available[d] = true
	}

	ordered := append([]workout.Workout(nil), workouts...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Date < ordered[j].Date })

	replaced := map[string]bool{}
	var changes []Change
	for _, w := range ordered {
		if w.Date == "" || !scheduler.IsGenerated(w) || done(w, sessions) {
			continue
		}

		switch {
		// Missed: this week, already over, and worth making up.
		case w.Date >= weekStart && w.Date < todayStr && scheduler.IsKeySession(w):
			// A tired rider does not make up a missed session; rest is the
			// right answer to whatever made them miss it.
			if fatigued {
				continue
			}
			if next, ok := nextFreeDay(today, weekEnd, available, taken); ok {
				taken[next] = true
				changes = append(changes, Change{
					WorkoutID: w.ID, NewDate: next,
					Reason: fmt.Sprintf("moved from %s — that session was missed, so it is made up on %s.", w.Date, next),
				})
				continue
			}
			if slot, ok := nextEasySlot(ordered, sessions, todayStr, weekEnd, replaced); ok {
				replaced[slot.ID] = true
				changes = append(changes, Change{
					WorkoutID: w.ID, NewDate: slot.Date, ReplaceWorkoutID: slot.ID,
					Reason: fmt.Sprintf("moved from %s — that session was missed, so it is made up on %s in place of an easy day.", w.Date, slot.Date),
				})
			}

		// Upcoming: today or tomorrow, hard, and the rider is exhausted.
		case fatigued && scheduler.IsHardSession(w) && (w.Date == todayStr || w.Date == day(today.AddDate(0, 0, 1))):
			changes = append(changes, Change{
				WorkoutID: w.ID, Downgrade: true,
				Reason: fmt.Sprintf("swapped for an easy session — your form is %.0f, well into the fatigued zone, and a hard day now would cost more than it earns.", latest.TSB),
			})
		}
	}
	return changes
}

// done reports whether the rider did this session: something on the same
// day, in the same sport, of at least completedFraction of its planned time.
func done(w workout.Workout, sessions []workout.CompletedSession) bool {
	planned := workout.PlannedSeconds(w.Steps)
	for _, s := range sessions {
		if s.Date != w.Date || s.Sport != string(w.Sport) {
			continue
		}
		if planned == 0 || s.DurationSeconds >= planned*completedFraction {
			return true
		}
	}
	return false
}

// nextFreeDay is the first day from today to the end of the week that the
// rider can train and has nothing planned on.
func nextFreeDay(today time.Time, weekEnd string, available, taken map[string]bool) (string, bool) {
	names := [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	for d := today; d.Format("2006-01-02") <= weekEnd; d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		if available[names[d.Weekday()]] && !taken[date] {
			return date, true
		}
	}
	return "", false
}

// nextEasySlot is the first generated, untouched, easy workout from today to
// the end of the week that the rider has not already done — the one a missed
// key session may take the place of. replaced holds slots already promised to
// an earlier miss this pass.
func nextEasySlot(ordered []workout.Workout, sessions []workout.CompletedSession, today, weekEnd string, replaced map[string]bool) (workout.Workout, bool) {
	for _, w := range ordered {
		if w.Date < today || w.Date > weekEnd || replaced[w.ID] {
			continue
		}
		if !scheduler.IsGenerated(w) || scheduler.IsKeySession(w) || done(w, sessions) {
			continue
		}
		return w, true
	}
	return workout.Workout{}, false
}

func withinDays(date string, today time.Time, days int) bool {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return false
	}
	return today.Sub(d) <= time.Duration(days+1)*24*time.Hour
}

func mondayOf(t time.Time) time.Time {
	t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
}

// Note is the text appended to a generated workout's description for a
// change: the marker (which stops a second adjustment) followed by the reason.
func Note(c Change) string {
	return strings.TrimSpace(scheduler.AdjustedMarker + " " + c.Reason)
}
