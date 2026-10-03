package lifeevents

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/indoor"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// gymMaxSeconds is the longest a hotel-gym session is kept: a hotel bike is
// not a threshold session, and an hour is what the rider can reasonably
// expect to find time for.
const gymMaxSeconds = 3600

// class is how a day is treated, by the strictest event covering it.
type class int

const (
	classNone class = iota
	// classProper: ill with symptoms below the neck, or unsure: nothing is
	// ridden and nothing is made up.
	classProper
	// classMove: no bike, busy or other: nothing can be done that day, but
	// the week's work is kept where a sensible day exists.
	classMove
	// classMild: ill above the neck only: the hard work goes, easy work is cut.
	classMild
	// classGym: travelling with a hotel gym: kept as short indoor rides.
	classGym
)

// planner is the state one Preview works through.
type planner struct {
	in      Input
	today   string
	after   map[string]bool // blackout of the events as they will stand
	fresh   map[string]bool // days that became covered by this change
	freed   map[string]bool // days that stopped being covered
	noPlace map[string]bool // days a moved session may not take
	byDate  map[string][]workout.Workout
	// vacated are the days an earlier move left empty on purpose; the
	// scheduler treats them as taken, so a move must not land there either.
	vacated map[string]bool
	// changed is the workout ids that already have a change: one per session.
	changed map[string]bool
	// occupied and hardOn are filled in as sessions are placed.
	occupied map[string]bool
	hardOn   map[string]bool
	diff     Diff
}

func newPlanner(in Input) *planner {
	p := &planner{
		in:       in,
		today:    Today(in.Now),
		after:    Blackout(in.Events),
		fresh:    map[string]bool{},
		freed:    map[string]bool{},
		byDate:   map[string][]workout.Workout{},
		vacated:  map[string]bool{},
		changed:  map[string]bool{},
		occupied: map[string]bool{},
		hardOn:   map[string]bool{},
	}
	before := Blackout(in.Previous)
	for d := range p.after {
		if !before[d] {
			p.fresh[d] = true
		}
	}
	for d := range before {
		if !p.after[d] {
			p.freed[d] = true
		}
	}
	// An event whose kind or option changed is a new rule for its whole range,
	// not only for the days it newly covers: mild to proper, or no bike to the
	// hotel gym, must reach the sessions already on those days.
	for _, e := range in.Events {
		if rulesChanged(e, in.Previous) {
			for _, d := range daysOf(e) {
				p.fresh[d] = true
			}
		}
	}
	p.noPlace = map[string]bool{}
	for d := range p.after {
		p.noPlace[d] = true
	}
	for _, w := range in.Workouts {
		if w.Date == "" {
			continue
		}
		p.byDate[w.Date] = append(p.byDate[w.Date], w)
		p.occupied[w.Date] = true
		if isHard(w) {
			p.hardOn[w.Date] = true
		}
		if w.GoalID != "" {
			if from, ok := scheduler.MovedFrom(w.Description); ok {
				p.vacated[from] = true
			}
		}
	}
	return p
}

// rulesChanged is whether e exists in previous with a different kind or option.
func rulesChanged(e Event, previous []Event) bool {
	if e.ID == "" {
		return false
	}
	for _, o := range previous {
		if o.ID == e.ID {
			return o.Kind != e.Kind || Normalize(o).Option != Normalize(e).Option
		}
	}
	return false
}

// Preview is the diff a set of events implies for a rider's plan. It never
// writes and never reads the clock: the same inputs give the same diff.
func Preview(in Input) Diff {
	p := newPlanner(in)
	p.run()
	return p.finish()
}

func (p *planner) run() {
	scope := p.scopeByClass()
	// An FTP test is not a session to be turned into a short indoor ride: on a
	// trip it moves like it does with no bike (and is removed with nowhere to go).
	var gymSessions []scoped
	for _, s := range scope[classGym] {
		if s.w.TestProtocol != "" {
			scope[classMove] = append(scope[classMove], s)
		} else {
			gymSessions = append(gymSessions, s)
		}
	}
	scope[classGym] = gymSessions
	p.forgetLeavingHardDays(scope)
	p.removeProper(scope[classProper])
	p.moveSessions(scope[classMove])
	p.gym(scope[classGym])
	p.refill()
}

// forgetLeavingHardDays takes the sessions this pass is about to move, remove
// or soften out of the hard-day map: a threshold session leaving Thursday must
// not stop another taking Friday because it was on Thursday a moment ago.
func (p *planner) forgetLeavingHardDays(scope map[class][]scoped) {
	leaving := map[string]bool{}
	for _, list := range scope {
		for _, s := range list {
			leaving[s.w.ID] = true
		}
	}
	p.hardOn = map[string]bool{}
	for _, w := range p.in.Workouts {
		if w.Date != "" && !leaving[w.ID] && isHard(w) {
			p.hardOn[w.Date] = true
		}
	}
}

func (p *planner) finish() Diff {
	sort.SliceStable(p.diff.Changes, func(i, j int) bool {
		a, b := p.diff.Changes[i], p.diff.Changes[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		return a.ID < b.ID
	})
	sort.SliceStable(p.diff.LeftAlone, func(i, j int) bool {
		a, b := p.diff.LeftAlone[i], p.diff.LeftAlone[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		return a.WorkoutID < b.WorkoutID
	})
	return p.diff
}

// isHard is a session that must not sit next to another: a tempo or interval
// session, or an FTP test, which is an all-out effort whatever zone it carries.
func isHard(w workout.Workout) bool {
	return w.TestProtocol != "" || scheduler.IsHardSession(w)
}

// covering is the events of the new set that cover day.
func (p *planner) covering(day string) []Event {
	var out []Event
	for _, e := range p.in.Events {
		if e.Start <= day && day <= e.End {
			out = append(out, e)
		}
	}
	return out
}

// dayClass is how day is treated and which event decides it. The strictest
// event wins: proper illness, then nothing-can-be-ridden, then mild illness,
// then the hotel gym.
func (p *planner) dayClass(day string) (class, Event) {
	best, bestEvent := classNone, Event{}
	for _, e := range p.covering(day) {
		var c class
		switch {
		case e.Kind == KindIllness && Normalize(e).Option == OptionProper:
			c = classProper
		case e.Kind == KindIllness:
			c = classMild
		case e.Kind == KindTravel && Normalize(e).Option == OptionGym:
			c = classGym
		default:
			c = classMove
		}
		if best == classNone || c < best {
			best, bestEvent = c, e
		}
	}
	return best, bestEvent
}

// scoped is a session a rule may change, with the event that caused it.
type scoped struct {
	w     workout.Workout
	event Event
}

// scopeByClass sorts the sessions on newly covered days into what each class
// of rule may change. Sessions it may not touch are listed as left alone, with
// an opt-in removal where the rider might want one.
func (p *planner) scopeByClass() map[class][]scoped {
	out := map[class][]scoped{}
	var days []string
	for d := range p.fresh {
		if d >= p.today {
			days = append(days, d)
		}
	}
	sort.Strings(days)
	for _, d := range days {
		c, ev := p.dayClass(d)
		for _, w := range p.byDate[d] {
			switch p.eligibility(w) {
			case eligible:
				out[c] = append(out[c], scoped{w, ev})
			case ineligibleRidden:
				p.leave(w, "you have already ridden it")
			case ineligibleBuilt:
				p.leaveWithRemove(w, ev, "you built this session yourself")
			case ineligibleAdjusted:
				p.leaveWithRemove(w, ev, "it has already been adjusted")
			case ineligibleSwapped:
				p.leaveWithRemove(w, ev, "you swapped it for another version")
			}
		}
	}
	return out
}

type eligibility int

const (
	eligible eligibility = iota
	ineligibleRidden
	ineligibleBuilt
	ineligibleAdjusted
	ineligibleSwapped
)

// eligibility is whether a session may be changed automatically. A session
// the plan made and nobody has touched may; an FTP test may (it moves with the
// rider); everything else is the rider's.
func (p *planner) eligibility(w workout.Workout) eligibility {
	switch {
	case p.in.Ridden[w.ID]:
		return ineligibleRidden
	case w.TestProtocol != "":
		return eligible
	case scheduler.IsGenerated(w):
		return eligible
	case w.GoalID == "" || !strings.HasPrefix(w.Description, scheduler.GeneratedDescription):
		return ineligibleBuilt
	case strings.Contains(w.Description, scheduler.SwappedMarker):
		return ineligibleSwapped
	default:
		return ineligibleAdjusted
	}
}

func (p *planner) leave(w workout.Workout, why string) {
	p.diff.LeftAlone = append(p.diff.LeftAlone, Note{
		WorkoutID: w.ID, Date: w.Date, Name: w.Name, Reason: capitalize(why) + ", so it was left alone.",
	})
}

// leaveWithRemove lists a session as left alone and offers its removal as a
// change that is not ticked: a session the rider made is theirs.
func (p *planner) leaveWithRemove(w workout.Workout, ev Event, why string) {
	p.leave(w, why)
	if p.changed[w.ID] {
		return
	}
	p.changed[w.ID] = true
	p.diff.Changes = append(p.diff.Changes, Change{
		ID: OpRemove + ":" + w.ID, Op: OpRemove, WorkoutID: w.ID, Date: w.Date, Name: w.Name, Kind: ev.Kind,
		Reason:  fmt.Sprintf("Remove it too: %s.", eventPhrase(ev)),
		Default: false,
	})
}

// remove adds a ticked removal of w.
func (p *planner) remove(w workout.Workout, ev Event, reason string) {
	if p.changed[w.ID] {
		return
	}
	p.changed[w.ID] = true
	p.diff.Changes = append(p.diff.Changes, Change{
		ID: OpRemove + ":" + w.ID, Op: OpRemove, WorkoutID: w.ID, Date: w.Date, Name: w.Name, Kind: ev.Kind,
		Reason: reason, Default: true,
	})
}

func (p *planner) removeProper(list []scoped) {
	for _, s := range list {
		p.remove(s.w, s.event, fmt.Sprintf("%s: removed, and not made up later.", capitalize(eventPhrase(s.event))))
	}
}

// moveSessions places the sessions of no-bike, busy and other days, week by
// week.
func (p *planner) moveSessions(list []scoped) {
	for _, group := range p.byWeek(list) {
		for _, s := range orderForPlacement(group) {
			p.placeOrRemove(s)
		}
	}
}

// byWeek groups sessions by the Monday of their week, earliest week first.
func (p *planner) byWeek(list []scoped) [][]scoped {
	weeks := map[string][]scoped{}
	for _, s := range list {
		weeks[mondayOf(s.w.Date)] = append(weeks[mondayOf(s.w.Date)], s)
	}
	keys := make([]string, 0, len(weeks))
	for k := range weeks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][]scoped, 0, len(keys))
	for _, k := range keys {
		out = append(out, weeks[k])
	}
	return out
}

// orderForPlacement is the order a week's sessions pick their day: the key
// session first (the longest key session, else the longest session), then the
// other hard sessions, then the rest. Within each, the earlier planned date.
func orderForPlacement(group []scoped) []scoped {
	key := keyIndex(group)
	rank := func(i int) int {
		switch {
		case i == key:
			return 0
		case isHard(group[i].w):
			return 1
		}
		return 2
	}
	idx := make([]int, len(group))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ra, rb := rank(idx[a]), rank(idx[b])
		if ra != rb {
			return ra < rb
		}
		wa, wb := group[idx[a]].w, group[idx[b]].w
		if wa.Date != wb.Date {
			return wa.Date < wb.Date
		}
		return wa.ID < wb.ID
	})
	out := make([]scoped, len(group))
	for i, j := range idx {
		out[i] = group[j]
	}
	return out
}

// keyIndex is the index of the week's key session: the longest key session,
// else the longest session. Ties go to the earlier date.
func keyIndex(group []scoped) int {
	best, bestKey := -1, false
	var bestSeconds float64
	for i, s := range group {
		key := scheduler.IsKeySession(s.w) || s.w.TestProtocol != ""
		secs := workout.PlannedSeconds(s.w.Steps)
		better := best < 0 ||
			(key && !bestKey) ||
			(key == bestKey && secs > bestSeconds) ||
			(key == bestKey && secs == bestSeconds && s.w.Date < group[best].w.Date)
		if better {
			best, bestKey, bestSeconds = i, key, secs
		}
	}
	return best
}

const (
	reasonNoFreeDay  = "no free day this week"
	reasonBackToBack = "no free day this week that avoids two hard days in a row"
)

// placeOrRemove moves s to the nearest free day of its own week, or removes it
// with the reason there was none. A session never leaves its week: the season
// is filled once, a week at a time, and next week's plan is next week's.
func (p *planner) placeOrRemove(s scoped) {
	to, why := p.place(s.w)
	if to == "" {
		p.remove(s.w, s.event, fmt.Sprintf("%s: removed, %s.", capitalize(eventPhrase(s.event)), why))
		return
	}
	p.move(s, to)
}

// place finds the day w moves to, "" with the reason when there is none.
func (p *planner) place(w workout.Workout) (string, string) {
	week := weekDays(mondayOf(w.Date))
	origin, _ := parseDate(w.Date)
	hard := isHard(w)

	best, bestDist := "", math.MaxInt
	sawFree := false
	for _, d := range week {
		if !p.free(d) {
			continue
		}
		sawFree = true
		if hard && p.neighbourHard(d) {
			continue
		}
		t, _ := parseDate(d)
		dist := int(math.Abs(t.Sub(origin).Hours() / 24))
		// Nearest wins; on a tie the later day (the loop runs earliest first,
		// and <= lets a later equal one replace it).
		if dist <= bestDist {
			best, bestDist = d, dist
		}
	}
	switch {
	case best != "":
		return best, ""
	case sawFree:
		return "", reasonBackToBack
	}
	return "", reasonNoFreeDay
}

// free is whether a moved session may take day: one of the rider's available
// days, today or later, outside every event, holding nothing, and not the day
// an earlier move left empty.
func (p *planner) free(day string) bool {
	if day < p.today || p.noPlace[day] || p.occupied[day] || p.vacated[day] {
		return false
	}
	return p.available(day)
}

// available reports whether the rider trains on the weekday of day. A rider
// with no stated days has nowhere to move a session to.
func (p *planner) available(day string) bool {
	t, ok := parseDate(day)
	if !ok {
		return false
	}
	name := strings.ToLower(t.Weekday().String()[:3])
	for _, d := range p.in.Profile.AvailableDays {
		if d == name {
			return true
		}
	}
	return false
}

// neighbourHard is whether a hard session already sits on the day before or
// after day, in the plan as it stands or placed in this pass.
func (p *planner) neighbourHard(day string) bool {
	t, ok := parseDate(day)
	if !ok {
		return false
	}
	return p.hardOn[t.AddDate(0, 0, -1).Format(DateLayout)] || p.hardOn[t.AddDate(0, 0, 1).Format(DateLayout)]
}

func (p *planner) move(s scoped, to string) {
	w := s.w
	if p.changed[w.ID] {
		return
	}
	p.changed[w.ID] = true
	p.occupied[to] = true
	if isHard(w) {
		p.hardOn[to] = true
	}
	reason := fmt.Sprintf("%s: moved to %s.", capitalize(eventPhrase(s.event)), dayName(to))
	description := movedDescription(w, w.Date, reason)
	p.diff.Changes = append(p.diff.Changes, Change{
		ID: OpMove + ":" + w.ID, Op: OpMove, WorkoutID: w.ID, Date: w.Date, ToDate: to, Name: w.Name, Kind: s.event.Kind,
		Reason: reason, Default: true,
		Update: &workout.UpdateWorkoutRequest{Date: &to, Description: &description},
	})
}

// movedDescription is w's description with the move noted in the form
// scheduler.MovedFrom reads, so the scheduler still treats the vacated day as
// taken, and the adjusted marker, so no automatic rule touches it again. An FTP
// test is not a generated session and carries no marker.
func movedDescription(w workout.Workout, from, reason string) string {
	d := strings.TrimRight(w.Description, " \n")
	note := "Rescheduled by a life event: moved from " + from + "."
	if _, already := scheduler.MovedFrom(d); already {
		// The first date is the one the scheduler must keep reading.
		note = "Rescheduled again by a life event (it was on " + from + ")."
	}
	if d != "" {
		d += "\n\n"
	}
	d += note
	if scheduler.IsGenerated(w) {
		d += " " + scheduler.AdjustedMarker + " " + reason
	}
	return d
}

// gym keeps cycling sessions on their day as short indoor endurance rides. The
// week's key session is moved to a free home day when one exists, because a
// hotel bike is no place for it.
func (p *planner) gym(list []scoped) {
	for _, group := range p.byWeek(list) {
		var cycling []scoped
		for _, s := range group {
			if s.w.Sport == model.SportCycling {
				cycling = append(cycling, s)
			}
		}
		// The key session among the cycling ones, by the same rule as placement.
		key := -1
		if len(cycling) > 0 {
			key = keyIndex(cycling)
		}
		for i, s := range cycling {
			if i == key {
				if to, _ := p.place(s.w); to != "" {
					p.move(s, to)
					continue
				}
			}
			p.indoorSession(s)
		}
		for _, s := range group {
			if s.w.Sport != model.SportCycling {
				p.leave(s.w, "you can run on this trip")
			}
		}
	}
}

// indoorSession replaces s with an indoor endurance ride of at most an hour.
func (p *planner) indoorSession(s scoped) {
	w := s.w
	if p.changed[w.ID] || w.TestProtocol != "" {
		return
	}
	outdoorSeconds := workout.PlannedSeconds(w.Steps)
	if w.OutdoorSteps != nil {
		outdoorSeconds = workout.PlannedSeconds(*w.OutdoorSteps)
	}
	if w.Indoor && w.Zone == workout.ZoneEndurance && workout.PlannedSeconds(w.Steps) <= gymMaxSeconds {
		return
	}
	seconds := math.Min(outdoorSeconds, gymMaxSeconds)
	if seconds <= 0 {
		seconds = gymMaxSeconds
	}
	built := scheduler.BuildEnduranceSession(seconds/3600, false, w.Sport, p.in.Profile)
	outdoor := append([]workout.WorkoutStep{}, built.Steps...)
	res, err := indoor.Convert(workout.Workout{Sport: w.Sport, Name: built.Name, Zone: built.Zone, Steps: outdoor}, p.in.Profile)
	if err != nil {
		return
	}
	p.changed[w.ID] = true
	reason := fmt.Sprintf("%s: kept on its day as an indoor endurance ride of at most %d minutes.",
		capitalize(eventPhrase(s.event)), gymMaxSeconds/60)
	steps, name, zone, level, yes := res.Steps, built.Name, built.Zone, 0.0, true
	description := replaceMarked(w.Description, res.Note, reason)
	p.diff.Changes = append(p.diff.Changes, Change{
		ID: OpIndoor + ":" + w.ID, Op: OpIndoor, WorkoutID: w.ID, Date: w.Date, Name: w.Name, Kind: s.event.Kind,
		Reason: reason, Default: true,
		Update: &workout.UpdateWorkoutRequest{
			Name: &name, Steps: &steps, Zone: &zone, Level: &level, Indoor: &yes, OutdoorSteps: &outdoor,
			Description: &description,
		},
	})
}

// replaceMarked is description with note and the adjusted marker appended. A
// test is not generated and takes no marker.
func replaceMarked(description, note, reason string) string {
	d := strings.TrimRight(description, " \n")
	if note != "" {
		if d != "" {
			d += " "
		}
		d += note
	}
	return d + " " + scheduler.AdjustedMarker + " " + reason
}

// refill puts back the plan's own sessions on days a shortened or deleted event
// freed, the same way the fill-once season would have made them: today or
// later, outside every event, not on a day any goal already has a session, not
// a day an earlier move vacated.
func (p *planner) refill() {
	if len(p.freed) == 0 {
		return
	}
	goalTaken := map[string]bool{}
	for _, w := range p.in.Workouts {
		if w.GoalID != "" && w.Date != "" && !p.removedByThisPass(w) {
			goalTaken[w.Date] = true
		}
	}
	for d := range p.vacated {
		goalTaken[d] = true
	}
	for _, req := range p.in.Refill {
		d := req.Date
		if !p.freed[d] || d < p.today || p.after[d] || goalTaken[d] || p.placedOn(d) {
			continue
		}
		goalTaken[d] = true
		req := req
		p.diff.Changes = append(p.diff.Changes, Change{
			ID: OpAdd + ":" + d, Op: OpAdd, Date: d, Name: req.Name,
			Reason:  "Your plan's session for this day is back, now the day is free.",
			Default: true, Create: &req,
		})
	}
}

// removedByThisPass is whether a change in this diff takes w off its day.
func (p *planner) removedByThisPass(w workout.Workout) bool {
	for _, c := range p.diff.Changes {
		if c.WorkoutID == w.ID && (c.Op == OpRemove || c.Op == OpMove) {
			return true
		}
	}
	return false
}

// placedOn is whether this pass has moved a session onto d.
func (p *planner) placedOn(d string) bool {
	for _, c := range p.diff.Changes {
		if c.Op == OpMove && c.ToDate == d {
			return true
		}
	}
	return false
}

// ---------- small helpers ----------

// mondayOf is the Monday of the week containing date.
func mondayOf(date string) string {
	t, ok := parseDate(date)
	if !ok {
		return date
	}
	return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7)).Format(DateLayout)
}

// weekDays lists the seven dates from the Monday.
func weekDays(monday string) []string {
	t, ok := parseDate(monday)
	if !ok {
		return nil
	}
	out := make([]string, 7)
	for i := range out {
		out[i] = t.AddDate(0, 0, i).Format(DateLayout)
	}
	return out
}

// dayName is "Fri 9 Oct".
func dayName(date string) string {
	t, ok := parseDate(date)
	if !ok {
		return date
	}
	return t.Format("Mon 2 Jan")
}

// eventPhrase says what the rider is doing, for a reason line.
func eventPhrase(e Event) string {
	switch e.Kind {
	case KindTravel:
		if Normalize(e).Option == OptionGym {
			return "travelling with a hotel gym"
		}
		return "travelling, no bike"
	case KindIllness:
		if Normalize(e).Option == OptionMild {
			return "ill, symptoms above the neck"
		}
		return "ill"
	case KindBusy:
		return "busy"
	}
	return "away from training"
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
