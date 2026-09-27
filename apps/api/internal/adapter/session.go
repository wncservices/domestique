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
// Three rules, all deliberately small:
//
//   - A missed key session is made up once, later this week: on a day the
//     rider can train that has nothing planned, or — since the scheduler books
//     every available day, which is the usual case — in place of the next easy
//     session, which is given up instead. Easy days are simply let go when
//     missed, nothing is lost by skipping them, and a key session with nowhere
//     to go is let go too, never stacked on top of another. "Missed" itself
//     comes from ride-analysis when an analysis exists — see done's own
//     comment — and falls back to the ≥ 50 % time rule when it does not.
//   - A hard session is swapped for an easy one when form (TSB) is very
//     negative. TrainingPeaks' Performance Management Chart treats roughly
//     −30 and below as the high-risk zone where more intensity buys injury
//     and illness, not fitness.
//   - The same swap happens on two ride-analysis signals that say the same
//     thing TSB does without needing a fitness snapshot: the last two
//     analysed key sessions both came back struggled, or the last week rode
//     noticeably harder than the plan called for. See detectFatigue.
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
// rode ten minutes of an hour did not. Only used as the fallback when no
// ride-analysis outcome exists for the session — see done.
const completedFraction = 0.5

// struggleLookbackDays is how far back "the last two analysed key sessions"
// looks for the struggle fatigue trigger. Two struggled sessions from a
// month ago say nothing about today; the spec's own number.
const struggleLookbackDays = 14

// overloadLookbackDays is the window the 7-day overload fatigue trigger
// compares analysed TSS against planned TSS over.
const overloadLookbackDays = 7

// overloadRatio is how far analysed TSS must run ahead of planned TSS over
// overloadLookbackDays before it counts as overload fatigue.
const overloadRatioTrigger = 1.3

// defaultPlannedIF is the intensity factor estimatePlannedTSS assumes for a
// workout with no power-target step (an open-ended endurance ride, say) — a
// deliberately easy default rather than a guess at a harder one.
const defaultPlannedIF = 0.65

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
// rider has no fitness history. analyses is ride-analysis's verdict on
// completed sessions, keyed by the planned workout id they matched — set
// only for a ride that actually matched a planned workout, never for an
// unplanned one, so a lookup by workout id is exactly "was this planned
// session analysed, and how did it go."
func AdaptSessions(workouts []workout.Workout, sessions []workout.CompletedSession, profile workout.RiderProfile, latest *workout.FitnessSnapshot, today time.Time, analyses map[string]workout.SessionAnalysis) []Change {
	day := func(t time.Time) string { return t.Format("2006-01-02") }
	todayStr := day(today)
	weekStart := day(mondayOf(today))
	weekEnd := day(mondayOf(today).AddDate(0, 0, 6))

	fatigue := detectFatigue(workouts, profile, latest, analyses, today)

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
		if w.Date == "" || !scheduler.IsGenerated(w) || done(w, sessions, analyses) {
			continue
		}

		switch {
		// Missed: this week, already over, and worth making up.
		case w.Date >= weekStart && w.Date < todayStr && scheduler.IsKeySession(w):
			// A tired rider does not make up a missed session; rest is the
			// right answer to whatever made them miss it.
			if fatigue.active {
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
			if slot, ok := nextEasySlot(ordered, sessions, analyses, todayStr, weekEnd, replaced); ok {
				replaced[slot.ID] = true
				changes = append(changes, Change{
					WorkoutID: w.ID, NewDate: slot.Date, ReplaceWorkoutID: slot.ID,
					Reason: fmt.Sprintf("moved from %s — that session was missed, so it is made up on %s in place of an easy day.", w.Date, slot.Date),
				})
			}

		// Upcoming: today or tomorrow, hard, and the rider is exhausted —
		// whether that shows up as very negative form or as what the ride
		// analyses themselves say (see detectFatigue).
		case fatigue.active && scheduler.IsHardSession(w) && (w.Date == todayStr || w.Date == day(today.AddDate(0, 0, 1))):
			changes = append(changes, Change{
				WorkoutID: w.ID, Downgrade: true,
				Reason: fatigue.reason,
			})
		}
	}
	return changes
}

// done reports whether the rider did this session. A ride-analysis outcome
// wins when one exists — analyses is keyed by workout id, so a hit here
// means a completed session actually matched this planned one: "incomplete"
// is missed regardless of how much time was logged (a ride cut short for a
// reason the time rule alone cannot see), "struggled" still counts as done
// — it happened, just not well, which is a fatigue signal (see
// detectFatigue), not a reason to make it up again — and everything else
// ("nailed", "completed") is plainly done. Without an analysis, the ≥ 50 %
// time rule is the only signal available and remains the fallback.
func done(w workout.Workout, sessions []workout.CompletedSession, analyses map[string]workout.SessionAnalysis) bool {
	if a, ok := analyses[w.ID]; ok {
		return a.Outcome != string(outcomeIncomplete)
	}
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

// outcomeIncomplete/outcomeStruggled mirror rideanalysis.Outcome's own
// string values without importing that package — the same reasoning
// workout.SessionAnalysis's own doc comment gives for not pulling
// rideanalysis into workout: this package stays dependency-free of it too.
type outcome string

const (
	outcomeIncomplete outcome = "incomplete"
	outcomeStruggled  outcome = "struggled"
)

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
func nextEasySlot(ordered []workout.Workout, sessions []workout.CompletedSession, analyses map[string]workout.SessionAnalysis, today, weekEnd string, replaced map[string]bool) (workout.Workout, bool) {
	for _, w := range ordered {
		if w.Date < today || w.Date > weekEnd || replaced[w.ID] {
			continue
		}
		if !scheduler.IsGenerated(w) || scheduler.IsKeySession(w) || done(w, sessions, analyses) {
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

// fatigueSignal is whether a hard session should be swapped for an easy one
// right now, and why — detectFatigue's result.
type fatigueSignal struct {
	active bool
	reason string
}

// detectFatigue looks for any of three independent signs that the rider is
// carrying too much fatigue for more intensity, checked in order and
// returning the first that fires: very negative form (TSB), the last two
// analysed key sessions both coming back struggled, or the last week's
// analysed load running well ahead of what was planned. Any one is enough —
// they are different ways of noticing the same thing, and a rider who has
// no fitness snapshot but two rough rides in a row should not have to wait
// for TSB to catch up before getting a break.
func detectFatigue(workouts []workout.Workout, profile workout.RiderProfile, latest *workout.FitnessSnapshot, analyses map[string]workout.SessionAnalysis, today time.Time) fatigueSignal {
	if latest != nil && latest.TSB < fatigueTSB && withinDays(latest.Date, today, freshSnapshotDays) {
		return fatigueSignal{active: true, reason: fmt.Sprintf(
			"swapped for an easy session — your form is %.0f, well into the fatigued zone, and a hard day now would cost more than it earns.",
			latest.TSB,
		)}
	}
	if w, a, ok := lastTwoStruggledKeySessions(workouts, analyses, today); ok {
		return fatigueSignal{active: true, reason: struggleReason(w, a)}
	}
	if analysed, planned, ok := overloadedWeek(workouts, analyses, profile.FTPWatts, today); ok {
		return fatigueSignal{active: true, reason: fmt.Sprintf(
			"swapped for an easy session — the last %d days carried %.0f TSS against a planned %.0f, more load than the plan called for, and a hard day now would only add to it.",
			overloadLookbackDays, analysed, planned,
		)}
	}
	return fatigueSignal{}
}

// lastTwoStruggledKeySessions looks at the two most recent generated key
// sessions that have an analysis, within struggleLookbackDays of today, and
// reports whether both struggled. A single struggled ride is not a pattern;
// two in a row, closest one returned for the reason text, is the spec's own
// bar for treating it as fatigue rather than one off day.
func lastTwoStruggledKeySessions(workouts []workout.Workout, analyses map[string]workout.SessionAnalysis, today time.Time) (workout.Workout, workout.SessionAnalysis, bool) {
	type entry struct {
		w workout.Workout
		a workout.SessionAnalysis
	}
	var entries []entry
	for _, w := range workouts {
		if !scheduler.IsGenerated(w) || !scheduler.IsKeySession(w) || w.Date == "" {
			continue
		}
		a, ok := analyses[w.ID]
		if !ok || !withinDays(w.Date, today, struggleLookbackDays) {
			continue
		}
		entries = append(entries, entry{w, a})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].w.Date > entries[j].w.Date })
	if len(entries) < 2 || entries[0].a.Outcome != string(outcomeStruggled) || entries[1].a.Outcome != string(outcomeStruggled) {
		return workout.Workout{}, workout.SessionAnalysis{}, false
	}
	return entries[0].w, entries[0].a, true
}

// struggleReason names the most recent of the two struggled rides that
// triggered the swap, in the shape the spec gives: weekday, the workout's
// own name lower-cased, and how many of the steps ride-analysis could score
// were actually hit.
func struggleReason(w workout.Workout, a workout.SessionAnalysis) string {
	weekday := w.Date
	if d, err := time.Parse("2006-01-02", w.Date); err == nil {
		weekday = d.Weekday().String()
	}
	hit, total := 0, 0
	for _, s := range a.Steps {
		total++
		if s.Result == "hit" {
			hit++
		}
	}
	return fmt.Sprintf(
		"swapped for an easy session — %s's %s were under target (%d of %d), and two struggled sessions in a row call for a break before more intensity.",
		weekday, strings.ToLower(w.Name), hit, total,
	)
}

// overloadedWeek sums analysed and planned TSS over the last
// overloadLookbackDays, counting only days that had both a planned workout
// and an analysis of it — a planned day nobody rode says nothing about how
// hard the rider actually went, and an analysed ride with nothing planned
// was not part of what the plan asked for. Skipped entirely without an FTP:
// estimatePlannedTSS has no threshold to measure a power target against, and
// TSB and the struggle trigger above already cover fatigue without it.
func overloadedWeek(workouts []workout.Workout, analyses map[string]workout.SessionAnalysis, ftpWatts float64, today time.Time) (analysedTSS, plannedTSS float64, ok bool) {
	if ftpWatts <= 0 {
		return 0, 0, false
	}
	for _, w := range workouts {
		if w.Date == "" {
			continue
		}
		d, err := time.Parse("2006-01-02", w.Date)
		if err != nil {
			continue
		}
		if age := today.Sub(d); age < 0 || age > overloadLookbackDays*24*time.Hour {
			continue
		}
		a, has := analyses[w.ID]
		if !has {
			continue
		}
		analysedTSS += a.TSS
		plannedTSS += estimatePlannedTSS(w, ftpWatts)
	}
	return analysedTSS, plannedTSS, plannedTSS > 0 && analysedTSS >= overloadRatioTrigger*plannedTSS
}

// estimatePlannedTSS is the TSS a planned workout would score if ridden
// exactly as planned — hours x IF^2 x 100, the standard formula. IF is the
// mid-point of the first power-target step found in the workout (its
// intended hard effort) divided by FTP, or defaultPlannedIF when nothing in
// the workout targets power at all. This never touches a real ride's TSS —
// it exists only so overloadedWeek has something to compare analysed TSS
// against for a day that was never itself ridden and scored.
func estimatePlannedTSS(w workout.Workout, ftpWatts float64) float64 {
	if ftpWatts <= 0 {
		return 0
	}
	hours := workout.PlannedSeconds(w.Steps) / 3600
	if hours <= 0 {
		return 0
	}
	intensity := defaultPlannedIF
	if mid, ok := firstPowerTargetMid(w.Steps); ok {
		intensity = mid / ftpWatts
	}
	return hours * intensity * intensity * 100
}

// firstPowerTargetMid returns the midpoint of the first power-target step
// found in steps, descending into repeat blocks (their own Steps are what
// actually repeats — see WorkoutStep's own doc comment).
func firstPowerTargetMid(steps []workout.WorkoutStep) (float64, bool) {
	for _, s := range steps {
		if s.Repeat > 1 {
			if mid, ok := firstPowerTargetMid(s.Steps); ok {
				return mid, true
			}
			continue
		}
		if s.Target == workout.TargetPower {
			return (s.TargetLow + s.TargetHigh) / 2, true
		}
	}
	return 0, false
}

// Note is the text appended to a generated workout's description for a
// change: the marker (which stops a second adjustment) followed by the reason.
func Note(c Change) string {
	return strings.TrimSpace(scheduler.AdjustedMarker + " " + c.Reason)
}
