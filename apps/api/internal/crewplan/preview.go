package crewplan

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Ops of a Change.
const (
	// OpRemove deletes a generated, unadjusted, unridden session.
	OpRemove = "remove"
	// OpEase swaps the day before, or after, the ride for an easy session.
	OpEase = "ease"
	// OpShorten scales generated endurance sessions down so the week fits.
	OpShorten = "shorten"
	// OpAdd puts back, on leaving, what the plan would have made on the freed day.
	OpAdd = "add"
)

// KeptMarker is written into a session whose ease the rider unticked in a
// preview. The easing rules read it and leave the session alone, so a skip
// sticks instead of being redone by the next adaptation pass.
const KeptMarker = "Kept as planned by you around your crew ride."

const (
	dateLayout = "2006-01-02"
	// minShortenSeconds is the shortest a session is shortened to.
	minShortenSeconds = 45 * 60
	// afterRideCapSeconds caps the easy day after a long crew ride.
	afterRideCapSeconds = 3600
	// volumeFloor is the share of the week's target that stays, however long
	// the ride is.
	volumeFloor = 0.5
)

// Ride is the crew ride a preview is about. Only what the plan needs: no crew,
// no other rider.
type Ride struct {
	ID        string
	Date      string // "YYYY-MM-DD"
	RouteName string
	// Seconds is the estimated duration; it decides the ride's Kind.
	Seconds float64
}

// Kind is what the ride is to its week.
func (r Ride) Kind() Kind { return KindForSeconds(r.Seconds) }

// Input is everything Preview reads. Nothing here is written back.
type Input struct {
	Ride Ride
	// Leaving previews taking the ride out of the plan instead of putting it in.
	Leaving bool
	// Workouts are the rider's own, as they stand. On leaving they hold the
	// fixed session; on joining they do not.
	Workouts []workout.Workout
	// Ridden is the ids of workouts already ridden.
	Ridden  map[string]bool
	Profile workout.RiderProfile
	// Blackout is the days the rider's life events cover.
	Blackout map[string]bool
	// WeekTarget is the plan's target hours for the ride's week, 0 when the
	// rider has no plan for it. Recovery is whether that week is a recovery week.
	WeekTarget float64
	Recovery   bool
	// Refill is what the plan would make for the ride's week without the ride
	// (scheduler.WeekWorkouts), for the session put back on leaving.
	Refill []workout.CreateWorkoutRequest
	Now    time.Time
}

// Change is one write the server would make. Update, Create and Rule say how.
type Change struct {
	// ID is "<op>:<workoutId>" ("add:<date>" for an add): deterministic, so a
	// preview and the apply that follows name the same change.
	ID        string
	Op        string
	WorkoutID string
	Date      string
	Name      string
	Reason    string
	// Update is the content change of an ease or a shortening.
	Update *workout.UpdateWorkoutRequest
	// Create is the session an add makes.
	Create *workout.CreateWorkoutRequest
	// Eve and After say which easing rule an ease is, for the record of why,
	// and RideDate and RideKind name the ride that made it one.
	Eve, After         bool
	RideDate, RideKind string
}

// Note is a session the preview leaves alone, and why.
type Note struct {
	WorkoutID string
	Date      string
	Name      string
	Reason    string
}

// Diff is a preview.
type Diff struct {
	Changes   []Change
	LeftAlone []Note
	Warnings  []string
}

// Preview is what joining or leaving a crew ride does to the rest of the plan.
// Pure: it never writes and never reads the clock, so the same inputs give the
// same diff, which is what lets the server recompute it at apply time.
func Preview(in Input) Diff {
	p := &previewer{in: in, today: in.Now.Format(dateLayout), changed: map[string]bool{}, diff: Diff{Warnings: []string{}}}
	if in.Leaving {
		p.leaving()
	} else {
		p.joining()
	}
	return p.diff
}

type previewer struct {
	in      Input
	today   string
	changed map[string]bool
	diff    Diff
}

func (p *previewer) joining() {
	kind := p.in.Ride.Kind()
	if p.in.Blackout[p.in.Ride.Date] {
		p.warn("You are away on that day (a life event). The ride stays in your plan, and it is up to you whether to go.")
	}

	// A long ride the day before an FTP test reads FTP low.
	if kind == Long {
		next := addDays(p.in.Ride.Date, 1)
		for _, w := range p.in.Workouts {
			if w.TestProtocol != "" && w.Date == next {
				p.warn("Your FTP test is the day after this ride. A long ride the day before makes a test read low, so you may want to move the test.")
				break
			}
		}
	}

	// Its day is taken.
	for _, w := range p.byDate(p.in.Ride.Date) {
		p.removeOrLeave(w, "that day is now your crew ride")
	}

	// A long ride is the week's long ride; an endurance one replaces one
	// endurance slot.
	switch kind {
	case Long:
		for _, w := range p.weekSessions() {
			if w.Date != p.in.Ride.Date && scheduler.IsLongRideName(w.Name) {
				p.removeOrLeave(w, "your crew ride is the week's long ride")
			}
		}
	case Endurance:
		if p.replacedEnduranceOnItsDay() {
			break
		}
		if w, ok := p.enduranceToReplace(); ok {
			p.removeOrLeave(w, "your crew ride takes the place of one endurance session")
		}
	}

	// The days around it, by the same rule the plan applies on its own.
	row := fixedRow(p.in.Ride)
	around := append(p.survivors(), row)
	for _, c := range Eases(around, p.in.Profile, p.in.Ridden, p.today, p.in.Ride.ID) {
		p.changed[c.WorkoutID] = true
		p.diff.Changes = append(p.diff.Changes, c)
	}
	p.leftAloneAround(kind)

	p.shorten(kind)
	if p.in.Recovery && p.in.WeekTarget > 0 && p.in.Ride.Seconds/3600 > p.in.WeekTarget {
		p.warn("This ride is longer than this recovery week's target. The week keeps its own lower target.")
	}
}

func (p *previewer) leaving() {
	p.warn("Sessions that were eased or shortened around this ride are not restored.")
	date := p.in.Ride.Date
	if date < p.today || p.in.Blackout[date] {
		return
	}
	for _, w := range p.byDate(date) {
		if w.CrewRideID != p.in.Ride.ID {
			return // something else holds the day
		}
	}
	for i := range p.in.Refill {
		req := p.in.Refill[i]
		if req.Date != date {
			continue
		}
		create := req
		p.diff.Changes = append(p.diff.Changes, Change{
			ID: OpAdd + ":" + date, Op: OpAdd, Date: date, Name: req.Name,
			Reason: "the day is free again, so the plan's own session goes back on it",
			Create: &create,
		})
		return
	}
}

// byDate is the rider's workouts on date other than this ride's own fixed row.
func (p *previewer) byDate(date string) []workout.Workout {
	var out []workout.Workout
	for _, w := range p.in.Workouts {
		if w.Date == date && w.CrewRideID != p.in.Ride.ID {
			out = append(out, w)
		}
	}
	return out
}

// weekSessions is the rider's workouts in the ride's Monday-to-Sunday week
// other than its own row, ordered by date.
func (p *previewer) weekSessions() []workout.Workout {
	start, end := weekOf(p.in.Ride.Date)
	var out []workout.Workout
	for _, w := range p.in.Workouts {
		if w.Date >= start && w.Date <= end && w.CrewRideID != p.in.Ride.ID {
			out = append(out, w)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// survivors is the week's workouts that no change has removed.
func (p *previewer) survivors() []workout.Workout {
	var out []workout.Workout
	for _, w := range p.in.Workouts {
		if w.CrewRideID == p.in.Ride.ID || p.removed(w.ID) {
			continue
		}
		out = append(out, w)
	}
	return out
}

func (p *previewer) removed(id string) bool {
	for _, c := range p.diff.Changes {
		if c.Op == OpRemove && c.WorkoutID == id {
			return true
		}
	}
	return false
}

// enduranceToReplace is the generated endurance session the ride stands in for
// when its own day did not already hold one: the week's last, which is the slot
// scheduler.WeekWorkouts drops (applyFixed), so the preview and a week filled
// after joining name the same session.
func (p *previewer) enduranceToReplace() (workout.Workout, bool) {
	var best workout.Workout
	found := false
	for _, w := range p.weekSessions() {
		if w.Date == p.in.Ride.Date || p.changed[w.ID] || !p.eligible(w) || scheduler.IsLongRideName(w.Name) || w.Zone != workout.ZoneEndurance {
			continue
		}
		if !found || w.Date > best.Date {
			best, found = w, true
		}
	}
	return best, found
}

// replacedEnduranceOnItsDay is whether joining removed an endurance session on
// the ride's own day: that is the slot an endurance ride replaces, and no second
// one goes.
func (p *previewer) replacedEnduranceOnItsDay() bool {
	for _, w := range p.byDate(p.in.Ride.Date) {
		if p.removed(w.ID) && w.Zone == workout.ZoneEndurance && !scheduler.IsLongRideName(w.Name) {
			return true
		}
	}
	return false
}

// eligible is whether the plan may change w on its own: it made it, nobody has
// touched it, it has not been ridden and it is not in the past.
func (p *previewer) eligible(w workout.Workout) bool {
	return scheduler.IsGenerated(w) && !p.in.Ridden[w.ID] && w.Date >= p.today && !strings.Contains(w.Description, KeptMarker)
}

// removeOrLeave removes w when the plan may, and otherwise says why not.
func (p *previewer) removeOrLeave(w workout.Workout, reason string) {
	if p.changed[w.ID] {
		return
	}
	if !p.eligible(w) {
		p.leave(w)
		return
	}
	p.changed[w.ID] = true
	p.diff.Changes = append(p.diff.Changes, Change{
		ID: OpRemove + ":" + w.ID, Op: OpRemove, WorkoutID: w.ID, Date: w.Date, Name: w.Name,
		Reason: capitalise(reason) + ".",
	})
}

// leave lists w as left alone, with the reason the plan may not touch it.
func (p *previewer) leave(w workout.Workout) {
	reason := "you built this session yourself"
	switch {
	case p.in.Ridden[w.ID]:
		reason = "you have already ridden it"
	case w.Date < p.today:
		reason = "it is in the past"
	case strings.Contains(w.Description, KeptMarker):
		reason = "you chose to keep it as planned"
	case strings.Contains(w.Description, scheduler.SwappedMarker):
		reason = "you swapped it for another version"
	case strings.Contains(w.Description, scheduler.AdjustedMarker):
		reason = "it has already been adjusted"
	case w.CrewRideID != "":
		reason = "it is another crew ride"
	case w.TestProtocol != "":
		reason = "it is an FTP test"
	}
	p.diff.LeftAlone = append(p.diff.LeftAlone, Note{
		WorkoutID: w.ID, Date: w.Date, Name: w.Name, Reason: capitalise(reason) + ", so it was left alone.",
	})
}

// leftAloneAround lists the hard session the day before, and the day after a
// long ride, that the easing rules would have changed but may not.
func (p *previewer) leftAloneAround(kind Kind) {
	if kind == Short {
		return
	}
	before := addDays(p.in.Ride.Date, -1)
	after := addDays(p.in.Ride.Date, 1)
	for _, w := range p.survivors() {
		if p.changed[w.ID] {
			continue
		}
		listed := (w.Date == before && scheduler.IsHardSession(w)) || (kind == Long && w.Date == after)
		if listed && !p.eligible(w) {
			p.leave(w)
		}
	}
}

// shorten scales generated endurance sessions down so the week's generated
// volume fits the reduced target. The target is reduced by the ride's hours but
// never below half of it, and a recovery week keeps its own lower target.
func (p *previewer) shorten(kind Kind) {
	if kind == Short || p.in.WeekTarget <= 0 || p.in.Recovery {
		return
	}
	target := math.Max(p.in.WeekTarget-p.in.Ride.Seconds/3600, volumeFloor*p.in.WeekTarget) * 3600

	// What the generated part of the week holds after the changes so far.
	var adjustable []workout.Workout
	var adjustableSecs, otherSecs float64
	eased := map[string]float64{}
	for _, c := range p.diff.Changes {
		if c.Op == OpEase && c.Update != nil && c.Update.Steps != nil {
			eased[c.WorkoutID] = workout.PlannedSeconds(*c.Update.Steps)
		}
	}
	for _, w := range p.weekSessions() {
		if p.removed(w.ID) || !scheduler.IsGenerated(w) {
			continue
		}
		secs := workout.PlannedSeconds(w.Steps)
		if e, ok := eased[w.ID]; ok {
			secs = e
		}
		if p.shortenable(w) {
			adjustable = append(adjustable, w)
			adjustableSecs += secs
		} else {
			otherSecs += secs
		}
	}
	if adjustableSecs+otherSecs <= target || adjustableSecs <= 0 {
		return
	}
	scale := math.Max(target-otherSecs, 0) / adjustableSecs
	for _, w := range adjustable {
		old := workout.PlannedSeconds(w.Steps)
		next := math.Round(old*scale/60) * 60
		if next < minShortenSeconds {
			next = minShortenSeconds
		}
		if next >= old {
			continue // never lengthen, and never go below the floor by shortening
		}
		req := scheduler.BuildEnduranceSession(next/3600, scheduler.IsLongRideName(w.Name), w.Sport, p.in.Profile)
		reason := "shortened so the week still fits around your crew ride"
		desc := w.Description + " " + scheduler.AdjustedMarker + " " + reason + ". Replaces: " + w.Name + "."
		p.changed[w.ID] = true
		p.diff.Changes = append(p.diff.Changes, Change{
			ID: OpShorten + ":" + w.ID, Op: OpShorten, WorkoutID: w.ID, Date: w.Date, Name: w.Name,
			Reason: capitalise(reason) + ".",
			Update: &workout.UpdateWorkoutRequest{Steps: &req.Steps, Description: &desc},
		})
	}
}

// shortenable is whether w is a generated endurance session the preview may
// scale: not one it has already changed, not the ride's own day, not past.
func (p *previewer) shortenable(w workout.Workout) bool {
	return p.eligible(w) && !p.changed[w.ID] && w.Zone == workout.ZoneEndurance && w.Date != p.in.Ride.Date
}

func (p *previewer) warn(s string) { p.diff.Warnings = append(p.diff.Warnings, s) }

// ---------- the easing rules, shared with the automatic pass ----------

// Eases is the easing the plan applies around every crew ride row in workouts
// (or only rideID's, when given): a generated, unadjusted hard session the day
// before a long or endurance ride becomes easy, and so does the session the day
// after a long ride, capped at an hour. Preview calls it with a row standing in
// for the ride, and the adaptation pass calls it with the real rows, so a
// preview and the rule that keeps holding afterwards cannot disagree.
//
// Only sessions dated today or later are eased; ridden sessions never are, and
// a session the rider chose to keep (KeptMarker) is left as it is.
func Eases(workouts []workout.Workout, profile workout.RiderProfile, ridden map[string]bool, today, rideID string) []Change {
	byDate := map[string][]workout.Workout{}
	for _, w := range workouts {
		if w.Date != "" {
			byDate[w.Date] = append(byDate[w.Date], w)
		}
	}
	var out []Change
	done := map[string]bool{}
	ordered := append([]workout.Workout(nil), workouts...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Date < ordered[j].Date })
	for _, row := range ordered {
		if row.CrewRideID == "" || row.Date == "" || (rideID != "" && row.CrewRideID != rideID) {
			continue
		}
		kind := KindForSeconds(workout.PlannedSeconds(row.Steps))
		if kind == Short {
			continue
		}
		free := func(w workout.Workout) bool {
			return !done[w.ID] && w.CrewRideID == "" && w.Date >= today && !ridden[w.ID] &&
				scheduler.IsGenerated(w) && !strings.Contains(w.Description, KeptMarker)
		}
		for _, w := range byDate[addDays(row.Date, -1)] {
			if free(w) && scheduler.IsHardSession(w) {
				done[w.ID] = true
				out = append(out, easeChange(w, profile, row, false))
			}
		}
		if kind != Long {
			continue
		}
		for _, w := range byDate[addDays(row.Date, 1)] {
			if !free(w) {
				continue
			}
			if w.Zone == workout.ZoneEndurance && workout.PlannedSeconds(w.Steps) <= afterRideCapSeconds {
				continue // already easy and short: nothing to ease
			}
			done[w.ID] = true
			out = append(out, easeChange(w, profile, row, true))
		}
	}
	return out
}

// easeChange builds the ease of w around the crew ride row.
func easeChange(w workout.Workout, profile workout.RiderProfile, row workout.Workout, after bool) Change {
	easy := scheduler.EasyVariant(w, profile)
	if after && workout.PlannedSeconds(easy.Steps) > afterRideCapSeconds {
		easy = scheduler.BuildEnduranceSession(float64(afterRideCapSeconds)/3600, false, w.Sport, profile)
	}
	reason := "eased the day before your crew ride"
	if after {
		reason = "eased the day after your crew ride"
	}
	desc := w.Description + " " + scheduler.AdjustedMarker + " " + reason + ". Replaces: " + w.Name + "."
	return Change{
		ID: OpEase + ":" + w.ID, Op: OpEase, WorkoutID: w.ID, Date: w.Date, Name: w.Name,
		Reason: capitalise(reason) + ".",
		Update: &workout.UpdateWorkoutRequest{Name: &easy.Name, Steps: &easy.Steps, Zone: &easy.Zone, Level: &easy.Level, Description: &desc},
		Eve:    !after, After: after,
		RideDate: row.Date, RideKind: string(KindForSeconds(workout.PlannedSeconds(row.Steps))),
	}
}

// ---------- small helpers ----------

// fixedRow is the session a preview stands in for the ride's fixed row.
func fixedRow(r Ride) workout.Workout {
	return workout.Workout{
		ID: "crew-ride:" + r.ID, Name: "Crew ride: " + r.RouteName, Date: r.Date, CrewRideID: r.ID, Zone: workout.ZoneEndurance,
		Steps: []workout.WorkoutStep{{Name: "Crew ride", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: r.Seconds, Target: workout.TargetOpen}},
	}
}

func addDays(date string, n int) string {
	d, err := time.Parse(dateLayout, date)
	if err != nil {
		return ""
	}
	return d.AddDate(0, 0, n).Format(dateLayout)
}

// weekOf is the Monday and Sunday of the week date falls in.
func weekOf(date string) (string, string) {
	d, err := time.Parse(dateLayout, date)
	if err != nil {
		return date, date
	}
	off := (int(d.Weekday()) + 6) % 7
	mon := d.AddDate(0, 0, -off)
	return mon.Format(dateLayout), mon.AddDate(0, 0, 6).Format(dateLayout)
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
