package adapter

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/readiness"
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
//   - A hard session is swapped for an easy one on two ride-analysis signals:
//     the last two analysed key sessions both came back struggled, or the
//     last week rode noticeably harder than the plan called for. See
//     detectFatigue. Form (TSB) below −30 used to be a third signal here;
//     it now lives in internal/readiness alongside HRV, sleep and resting
//     heart rate — see the readiness rule below, and
//     docs/superpowers/specs/2026-09-28-readiness-design.md.
//   - Readiness's own verdict for *today* eases (never worsens) today's
//     generated, untouched hard session: rest swaps it for an easy one the
//     same way detectFatigue's swap does, caution steps it down one rung on
//     its own zone's ladder. See the readiness Verdict switch in
//     AdaptSessions.
//
// Only workouts the scheduler generated and nobody has touched are ever
// changed (scheduler.IsGenerated), and each is changed once: the note left in
// its description both tells the rider why and stops a second adjustment.
// Pure, like Reconcile: workouts, history and a clock in, changes out.

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

// stepDownWindowDays is the spec's own "within the next 7 days" — both how
// recent a struggled key session must be to trigger a step-down, and how far
// ahead of it the next same-zone workout may sit to be the one stepped down.
const stepDownWindowDays = 7

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
	// StepDown is set when the workout should be replaced by the next rung
	// down on its own zone's ladder — a struggled key session's own zone
	// stepping back a level, distinct from Downgrade's whole-plan fatigue
	// swap to an easy session. The caller (internal/api's adaptRider) is the
	// one that actually picks the lower rung, via workoutlib — this package
	// only decides which workout it should happen to.
	StepDown bool
	// StepDownSourceID is the id of the struggled key session that
	// triggered this StepDown. The caller records it in the replacement
	// workout's own description (via StepDownSourceNote) so a later
	// adaptation pass can tell this exact struggle already produced one
	// step-down and must not produce a second from it — see
	// stepDownTarget's own doc comment for why that matters with more than
	// one untouched same-zone workout in the window.
	StepDownSourceID string
	// Reason is shown to the rider.
	Reason string
}

// AdaptSessions decides what to change this week. analyses is ride-analysis's
// verdict on completed sessions, keyed by the planned workout id they
// matched — set only for a ride that actually matched a planned workout,
// never for an unplanned one, so a lookup by workout id is exactly "was this
// planned session analysed, and how did it go." assessment is today's
// readiness verdict (internal/readiness.Assess's result, built from Garmin
// wellness, form and load by the caller) — see the readiness.Verdict switch
// below for what it does to today's hard session; a zero-value Assessment
// (Verdict "") behaves exactly like Ready: no readiness-driven change.
func AdaptSessions(workouts []workout.Workout, sessions []workout.CompletedSession, profile workout.RiderProfile, today time.Time, analyses map[string]workout.SessionAnalysis, assessment readiness.Assessment) []Change {
	day := func(t time.Time) string { return t.Format("2006-01-02") }
	todayStr := day(today)
	weekStart := day(mondayOf(today))
	weekEnd := day(mondayOf(today).AddDate(0, 0, 6))

	fatigue := detectFatigue(workouts, profile, analyses, today)
	// tired gates the missed-session catch-up below: a rest verdict means the
	// rider is not in a state to make up a missed session either, the same
	// intent the old TSB < −30 branch of detectFatigue used to cover before
	// that rule moved into internal/readiness.
	tired := fatigue.active || assessment.Verdict == readiness.Rest

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
	// claimed holds every workout id this pass has already produced a Change
	// for (as the changed workout itself, or as the easy slot a missed
	// session is taking over) — stepDownTarget must not then also pick one of
	// these as the workout it steps down, or that workout would end up with
	// two Changes and adaptRider's own first-wins guard would silently drop
	// one of them instead of the two rules simply not colliding in the first
	// place.
	claimed := map[string]bool{}
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
			if tired {
				continue
			}
			if next, ok := nextFreeDay(today, weekEnd, available, taken); ok {
				taken[next] = true
				claimed[w.ID] = true
				changes = append(changes, Change{
					WorkoutID: w.ID, NewDate: next,
					Reason: fmt.Sprintf("moved from %s — that session was missed, so it is made up on %s.", w.Date, next),
				})
				continue
			}
			if slot, ok := nextEasySlot(ordered, sessions, analyses, todayStr, weekEnd, replaced); ok {
				replaced[slot.ID] = true
				claimed[w.ID] = true
				claimed[slot.ID] = true
				changes = append(changes, Change{
					WorkoutID: w.ID, NewDate: slot.Date, ReplaceWorkoutID: slot.ID,
					Reason: fmt.Sprintf("moved from %s — that session was missed, so it is made up on %s in place of an easy day.", w.Date, slot.Date),
				})
			}

		// Upcoming: today or tomorrow, hard, and the rider is exhausted —
		// whether that shows up as very negative form or as what the ride
		// analyses themselves say (see detectFatigue).
		case fatigue.active && scheduler.IsHardSession(w) && (w.Date == todayStr || w.Date == day(today.AddDate(0, 0, 1))):
			claimed[w.ID] = true
			changes = append(changes, Change{
				WorkoutID: w.ID, Downgrade: true,
				Reason: fatigue.reason,
			})
		}
	}

	// Readiness eases (never worsens) today's own generated, still-untouched
	// hard session — checked before stepDownTarget below so a rest verdict's
	// Downgrade claims that workout first when the two would otherwise pick
	// the same one (a struggled key session's next same-zone workout landing
	// on today), rather than the milder struggle-driven StepDown winning by
	// running last. Ready never changes anything: readiness only ever makes a
	// day easier.
	reasons := strings.Join(assessment.Reasons, "; ")
	switch assessment.Verdict {
	case readiness.Rest:
		// A downgrade replaces the workout wholesale with an easy variant
		// (scheduler.EasyVariant) — that works for a legacy zone-less hard
		// workout just as well as a structured one, so rest does not need
		// the structured-zone restriction caution's step-down does.
		if w, ok := readinessTarget(ordered, sessions, analyses, todayStr, claimed, false); ok {
			claimed[w.ID] = true
			changes = append(changes, Change{
				WorkoutID: w.ID, Downgrade: true,
				Reason: "Swapped for an easy ride — " + reasons,
			})
		}
	case readiness.Caution:
		// A step-down needs a rung on its own zone's ladder (workoutlib) —
		// a legacy hard workout with no zone has no ladder to step down on,
		// the same reason stepDownTarget itself requires
		// workout.IsStructuredZone.
		if w, ok := readinessTarget(ordered, sessions, analyses, todayStr, claimed, true); ok {
			claimed[w.ID] = true
			changes = append(changes, Change{
				WorkoutID: w.ID, StepDown: true, StepDownSourceID: readinessSourceID(today),
				Reason: "Eased one level — " + reasons,
			})
		}
	}

	if src, target, ok := stepDownTarget(ordered, analyses, today, claimed); ok {
		changes = append(changes, Change{
			WorkoutID: target.ID, StepDown: true, StepDownSourceID: src.ID,
			Reason: stepDownReason(src),
		})
	}
	return changes
}

// readinessSourceID is the StepDownSourceID a readiness-driven caution
// step-down carries — "readiness:<date>" rather than a struggled workout's
// own id, so hasStepDownSource can tell the two kinds of step-down apart and
// this one is still recognisable as coming from today's readiness verdict,
// not a struggled key session.
func readinessSourceID(today time.Time) string {
	return "readiness:" + today.Format("2006-01-02")
}

// readinessTarget is today's generated, untouched, hard, not-yet-done
// workout — the one readiness's own rest/caution verdict may ease. Unlike
// detectFatigue's swap, this never reaches into tomorrow: readiness only
// ever speaks to how the rider is today. requireStructuredZone is set for
// caution's step-down, which needs a zone with a ladder to step down on
// (see stepDownTarget); rest's downgrade replaces the workout wholesale and
// has no such requirement.
func readinessTarget(ordered []workout.Workout, sessions []workout.CompletedSession, analyses map[string]workout.SessionAnalysis, todayStr string, claimed map[string]bool, requireStructuredZone bool) (workout.Workout, bool) {
	for _, w := range ordered {
		if w.Date != todayStr || claimed[w.ID] || !scheduler.IsGenerated(w) || !scheduler.IsHardSession(w) || done(w, sessions, analyses) {
			continue
		}
		if requireStructuredZone && !workout.IsStructuredZone(w.Zone) {
			continue
		}
		return w, true
	}
	return workout.Workout{}, false
}

// stepDownSourceMarker prefixes the note recording which struggled session
// caused a step-down — see StepDownSourceNote.
const stepDownSourceMarker = "step-down source:"

// StepDownSourceNote is the text internal/api's applyStepDown appends to a
// stepped-down workout's own description, alongside the ordinary adjustment
// marker/reason — naming the struggled session (by id) that caused it.
// AutoScheduleTick runs every 30 minutes, and a struggled session's own
// analysis does not change between runs, so without this a second pass
// would otherwise see the exact same struggle and step down a *second*
// same-zone workout too, once the first one is no longer IsGenerated —
// hasStepDownSource is what stepDownTarget checks to refuse that.
func StepDownSourceNote(sourceID string) string {
	return fmt.Sprintf("%s %s", stepDownSourceMarker, sourceID)
}

// hasStepDownSource reports whether any workout already carries sourceID's
// own StepDownSourceNote — meaning that struggled session has already
// produced one step-down and must not produce a second one.
func hasStepDownSource(ordered []workout.Workout, sourceID string) bool {
	note := StepDownSourceNote(sourceID)
	for _, w := range ordered {
		if strings.Contains(w.Description, note) {
			return true
		}
	}
	return false
}

// stepDownTarget looks for the most recent analysed, generated key session
// in a structured zone that struggled within stepDownWindowDays of today,
// and the next still-untouched generated workout in that same zone dated
// after it — within stepDownWindowDays of the struggled session itself —
// the one the spec says to step down a level (docs/superpowers/specs's
// progression-levels design, "Struggled -> step down"). False when there is
// no recent struggle to react to, that struggle has already produced a
// step-down (hasStepDownSource), or nothing is left in that zone to step
// down. claimed holds every workout id AdaptSessions' own per-session loop
// already produced a Change for this pass — a workout already rescheduled,
// replaced, or downgraded this pass is skipped as a step-down target so it
// never ends up with two Changes (a missed-session reschedule onto a
// same-zone slot, say, followed by that same slot being stepped down).
func stepDownTarget(ordered []workout.Workout, analyses map[string]workout.SessionAnalysis, today time.Time, claimed map[string]bool) (workout.Workout, workout.Workout, bool) {
	var source workout.Workout
	found := false
	for _, w := range ordered {
		if !scheduler.IsGenerated(w) || !scheduler.IsKeySession(w) || w.Date == "" || !workout.IsStructuredZone(w.Zone) {
			continue
		}
		a, ok := analyses[w.ID]
		if !ok || a.Outcome != string(outcomeStruggled) || !withinDays(w.Date, today, stepDownWindowDays) {
			continue
		}
		if !found || w.Date > source.Date {
			source, found = w, true
		}
	}
	if !found || hasStepDownSource(ordered, source.ID) {
		return workout.Workout{}, workout.Workout{}, false
	}

	sourceDate, err := time.Parse("2006-01-02", source.Date)
	if err != nil {
		return workout.Workout{}, workout.Workout{}, false
	}
	for _, w := range ordered {
		if w.Date <= source.Date || w.Zone != source.Zone || !scheduler.IsGenerated(w) || claimed[w.ID] {
			continue
		}
		d, err := time.Parse("2006-01-02", w.Date)
		if err != nil || d.Sub(sourceDate) > stepDownWindowDays*24*time.Hour {
			continue
		}
		return source, w, true
	}
	return workout.Workout{}, workout.Workout{}, false
}

// stepDownReason names the struggled session that triggered the step-down,
// in the spec's own shape: weekday, then the zone label — "Stepped down
// after Tuesday's threshold session was under target."
func stepDownReason(w workout.Workout) string {
	weekday := w.Date
	if d, err := time.Parse("2006-01-02", w.Date); err == nil {
		weekday = d.Weekday().String()
	}
	zone := strings.ReplaceAll(string(w.Zone), "_", " ")
	return fmt.Sprintf("Stepped down after %s's %s session was under target", weekday, zone)
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

// detectFatigue looks for either of two independent ride-analysis signs that
// the rider is carrying too much fatigue for more intensity, checked in
// order and returning the first that fires: the last two analysed key
// sessions both coming back struggled, or the last week's analysed load
// running well ahead of what was planned. Any one is enough. Form (TSB)
// below −30 used to be a third signal checked first here; it now lives in
// internal/readiness, alongside HRV, sleep and resting heart rate — see
// AdaptSessions' own readiness.Verdict switch for what replaces it.
func detectFatigue(workouts []workout.Workout, profile workout.RiderProfile, analyses map[string]workout.SessionAnalysis, today time.Time) fatigueSignal {
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
// own name lower-cased, and how many of its *hard* steps ride-analysis
// scored were actually hit. Only Hard steps count toward that fraction — a
// generated interval session's recovery step carries a real power target
// too (see rideanalysis.StepResult.Hard's own doc comment), so counting
// every scored step would double a 4-rep session's denominator to 8 and
// read "2 of 8" for what the rider experienced as 4 hard reps.
func struggleReason(w workout.Workout, a workout.SessionAnalysis) string {
	weekday := w.Date
	if d, err := time.Parse("2006-01-02", w.Date); err == nil {
		weekday = d.Weekday().String()
	}
	hit, total := 0, 0
	for _, s := range a.Steps {
		if !s.Hard {
			continue
		}
		total++
		if s.Result == "hit" {
			hit++
		}
	}
	// A session can struggle on duration alone (an endurance ride cut short,
	// no hard interval steps to score) — total stays 0 and there is no
	// "N of M" to report, so the "(0 of 0)" clause is dropped rather than
	// printed nonsensically.
	if total == 0 {
		return fmt.Sprintf(
			"swapped for an easy session — %s's %s was cut short, and two struggled sessions in a row call for a break before more intensity.",
			weekday, strings.ToLower(w.Name),
		)
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
