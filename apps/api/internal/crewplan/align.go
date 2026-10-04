package crewplan

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Ride together. Two crew mates who are open to a shared ride, and who each have
// a plan-made long or endurance ride in the same week, may be offered one shared
// day and one shared route. Align is the pure half: it reads each rider's own
// plan on behalf of the group that opted in, and returns only a week, a day, a
// route and the riders in it. Nothing about why leaves this package.
//
// minShareSeconds is the shortest endurance session that counts as a ride worth
// sharing, and routeOverrun how much longer than the shortest rider's session
// the shared route may be.
const (
	minShareSeconds = 3600
	routeOverrun    = 0.15
)

// Candidate is one rider in a crew who opted in, with their own plan as the
// server read it. It never leaves the process: Align returns names and a day.
type Candidate struct {
	Rider string
	// Days are the weekdays ("sat", "sun", ...) the rider said they are open to.
	// None means opted out.
	Days     []string
	Workouts []workout.Workout
	// Ridden is the ids of workouts already ridden.
	Ridden map[string]bool
	// Blackout is the days the rider's life events cover.
	Blackout map[string]bool
}

// RouteOption is a route that could be shared, with how long it would take and
// who may see it. Only routes visible to every rider in a group are used.
type RouteOption struct {
	Slug      string
	Seconds   float64
	VisibleTo map[string]bool
}

// Group is one crew's week.
type Group struct {
	CrewID     string
	WeekStart  string // the Monday
	Candidates []Candidate
	Routes     []RouteOption
}

// AlignInput is every group to answer.
type AlignInput struct {
	Groups []Group
	Now    time.Time
}

// Proposal is the answer for one crew and week: when, on what, and who. It
// carries nothing else on purpose.
type Proposal struct {
	CrewID, WeekStart, Day, RouteSlug string
	// Riders are sorted by name.
	Riders []string
}

// Align proposes at most one shared ride per group, or none: it needs two or
// more qualifying riders, a day that is free for every one of them by the plan's
// own rules, and a route every one of them can see that is about as long as the
// shortest rider's session.
func Align(in AlignInput) []Proposal {
	var out []Proposal
	for _, g := range in.Groups {
		if p, ok := alignGroup(g, in.Now); ok {
			out = append(out, p)
		}
	}
	return out
}

func alignGroup(g Group, now time.Time) (Proposal, bool) {
	type member struct {
		cand Candidate
		sess workout.Workout
	}
	var members []member
	for _, c := range g.Candidates {
		if len(c.Days) == 0 || holdsCrewRide(c, g.WeekStart) {
			continue
		}
		if s, ok := qualifying(c, g.WeekStart, now); ok {
			members = append(members, member{c, s})
		}
	}
	if len(members) < 2 {
		return Proposal{}, false
	}
	sort.SliceStable(members, func(i, j int) bool { return members[i].cand.Rider < members[j].cand.Rider })

	// The days every rider offered.
	shared := map[string]int{}
	for _, m := range members {
		for _, d := range dedupe(m.cand.Days) {
			shared[d]++
		}
	}
	var flagged []string
	for d, n := range shared {
		if n == len(members) {
			flagged = append(flagged, d)
		}
	}
	tomorrow := dayString(now, 1)
	var day string
	for _, name := range orderDays(flagged) {
		date := dateOfWeekday(g.WeekStart, name)
		if date == "" || date < tomorrow {
			continue
		}
		free := true
		for _, m := range members {
			if !dayFree(m.cand, m.sess, date, tomorrow) {
				free = false
				break
			}
		}
		if free {
			day = date
			break
		}
	}
	if day == "" {
		return Proposal{}, false
	}

	shortest := math.MaxFloat64
	riders := make([]string, 0, len(members))
	for _, m := range members {
		shortest = math.Min(shortest, workout.PlannedSeconds(m.sess.Steps))
		riders = append(riders, m.cand.Rider)
	}
	slug, ok := chooseRoute(g.Routes, riders, shortest)
	if !ok {
		return Proposal{}, false
	}
	sort.Strings(riders)
	return Proposal{CrewID: g.CrewID, WeekStart: g.WeekStart, Day: day, RouteSlug: slug, Riders: riders}, true
}

// holdsCrewRide is whether the rider has a fixed crew ride in the week: they are
// left out, since the ride they have is the one to join.
func holdsCrewRide(c Candidate, weekStart string) bool {
	end := addDays(weekStart, 6)
	for _, w := range c.Workouts {
		if w.CrewRideID != "" && w.Date >= weekStart && w.Date <= end {
			return true
		}
	}
	return false
}

// qualifying is the one session a rider could move: the week's generated long
// ride, or failing that their longest generated endurance session of an hour or
// more, dated tomorrow or later, untouched and not ridden.
func qualifying(c Candidate, weekStart string, now time.Time) (workout.Workout, bool) {
	end := addDays(weekStart, 6)
	tomorrow := dayString(now, 1)
	var best workout.Workout
	found := false
	better := func(w workout.Workout) bool {
		if !found {
			return true
		}
		wl, bl := scheduler.IsLongRideName(w.Name), scheduler.IsLongRideName(best.Name)
		if wl != bl {
			return wl
		}
		ws, bs := workout.PlannedSeconds(w.Steps), workout.PlannedSeconds(best.Steps)
		if ws != bs {
			return ws > bs
		}
		return w.Date < best.Date
	}
	for _, w := range c.Workouts {
		if w.Date < weekStart || w.Date > end || w.Date < tomorrow || c.Ridden[w.ID] || !scheduler.IsGenerated(w) {
			continue
		}
		if !scheduler.IsLongRideName(w.Name) && w.Zone != workout.ZoneEndurance {
			continue
		}
		if workout.PlannedSeconds(w.Steps) < minShareSeconds {
			continue
		}
		if better(w) {
			best, found = w, true
		}
	}
	return best, found
}

// QualifyingSession is the one session of the candidate's that a shared ride
// could move: the same rule Align applies, for the rider's own accept.
func QualifyingSession(c Candidate, weekStart string, now time.Time) (workout.Workout, bool) {
	return qualifying(c, weekStart, now)
}

// dayFree is whether date is free for the rider by the plan's own rules: from
// tomorrow, nothing of any kind on it (their own qualifying session, which is the
// one that moves, excepted), not a day an earlier move left empty on purpose, not
// a life event day, and no hard or key session the day before.
func dayFree(c Candidate, own workout.Workout, date, tomorrow string) bool {
	if date < tomorrow || c.Blackout[date] {
		return false
	}
	before := addDays(date, -1)
	for _, w := range c.Workouts {
		if w.ID == own.ID {
			continue
		}
		if w.Date == date {
			return false
		}
		if w.GoalID != "" {
			if from, ok := scheduler.MovedFrom(w.Description); ok && from == date {
				return false
			}
		}
		if w.Date == before && (scheduler.IsHardSession(w) || scheduler.IsKeySession(w)) {
			return false
		}
	}
	return true
}

// chooseRoute is the route visible to every rider whose duration is closest to
// the shortest rider's session and not more than 15 % over it; ties by slug.
func chooseRoute(routes []RouteOption, riders []string, shortest float64) (string, bool) {
	best, bestDist, found := "", math.MaxFloat64, false
	for _, r := range routes {
		visible := true
		for _, who := range riders {
			if !r.VisibleTo[who] {
				visible = false
				break
			}
		}
		if !visible || r.Seconds > shortest*(1+routeOverrun)+1e-6 {
			continue
		}
		dist := math.Abs(r.Seconds - shortest)
		if !found || dist < bestDist-1e-9 || (math.Abs(dist-bestDist) <= 1e-9 && r.Slug < best) {
			best, bestDist, found = r.Slug, dist, true
		}
	}
	return best, found
}

// ---------- the premise still holding ----------

// MemberState is a rider's answer so far: pending, or accepted with the session
// they moved.
type MemberState struct {
	Rider     string
	Accepted  bool
	WorkoutID string
}

// PremiseInput is a stored proposal against the plans as they stand now.
type PremiseInput struct {
	// Group holds every member as a Candidate and the routes visible to them.
	Group     Group
	Day       string
	RouteSlug string
	Members   []MemberState
	Now       time.Time
}

// Premise is whether a proposal still holds: every rider who has not answered is
// still open to that weekday, still has a session the plan may move, and the day
// is still free for them; every rider who accepted still has their moved session
// on the day; and the route still exists and is visible to all of them. A session
// moved, ridden or deleted, a flag removed or a day filled makes it false, and
// the proposal is then stale.
func Premise(in PremiseInput) bool {
	byRider := map[string]Candidate{}
	for _, c := range in.Group.Candidates {
		byRider[c.Rider] = c
	}
	tomorrow := dayString(in.Now, 1)
	weekday := weekdayName(in.Day)
	var riders []string
	for _, m := range in.Members {
		c, ok := byRider[m.Rider]
		if !ok {
			return false
		}
		riders = append(riders, m.Rider)
		if m.Accepted {
			found := false
			for _, w := range c.Workouts {
				if w.ID == m.WorkoutID && w.Date == in.Day {
					found = true
				}
			}
			if !found {
				return false
			}
			continue
		}
		if !contains(c.Days, weekday) || holdsCrewRide(c, in.Group.WeekStart) {
			return false
		}
		sess, ok := qualifying(c, in.Group.WeekStart, in.Now)
		if !ok || !dayFree(c, sess, in.Day, tomorrow) {
			return false
		}
	}
	for _, r := range in.Group.Routes {
		if r.Slug != in.RouteSlug {
			continue
		}
		for _, who := range riders {
			if !r.VisibleTo[who] {
				return false
			}
		}
		return true
	}
	return false
}

// ---------- small helpers ----------

var weekdayNames = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// orderDays is Saturday, Sunday, then the rest in calendar order.
func orderDays(days []string) []string {
	rank := func(d string) int {
		switch d {
		case "sat":
			return -2
		case "sun":
			return -1
		}
		for i, n := range weekdayNames {
			if n == d {
				return i
			}
		}
		return len(weekdayNames)
	}
	out := append([]string(nil), days...)
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// dateOfWeekday is the date of name in the week starting at weekStart.
func dateOfWeekday(weekStart, name string) string {
	for i, n := range weekdayNames {
		if n == name {
			return addDays(weekStart, i)
		}
	}
	return ""
}

// weekdayName is the three-letter name of date's weekday.
func weekdayName(date string) string {
	d, err := time.Parse(dateLayout, date)
	if err != nil {
		return ""
	}
	return strings.ToLower(d.Weekday().String()[:3])
}

// dayString is the calendar date of now plus n days, in now's own zone.
func dayString(now time.Time, n int) string { return now.AddDate(0, 0, n).Format(dateLayout) }
