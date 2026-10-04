package crewplan

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Both riders have a plan-made long ride this week (Wednesday is "now"), open
// days Saturday and Sunday, and a library of one route that fits.
func rider(name string, days []string, ws ...workout.Workout) Candidate {
	return Candidate{Rider: name, Days: days, Workouts: ws}
}

func long(id, date string, hours float64) workout.Workout {
	return gen(id, "Long ride", date, workout.ZoneEndurance, hours*3600)
}

var weekend = []string{"sat", "sun"}

func routes(opts ...RouteOption) []RouteOption { return opts }

func visible(slug string, seconds float64, riders ...string) RouteOption {
	v := map[string]bool{}
	for _, r := range riders {
		v[r] = true
	}
	return RouteOption{Slug: slug, Seconds: seconds, VisibleTo: v}
}

func group(cands []Candidate, rs []RouteOption) Group {
	return Group{CrewID: "crew-a", WeekStart: mon, Candidates: cands, Routes: rs}
}

func align(g Group) []Proposal { return Align(AlignInput{Groups: []Group{g}, Now: now}) }

func TestTwoRidersWithALongRideEachGetOneSharedDayAndRoute(t *testing.T) {
	out := align(group(
		[]Candidate{
			rider("wilant", weekend, long("w-long", sun, 3)),
			rider("sam", weekend, long("s-long", sat, 2.5)),
		},
		routes(visible("hill", 2.6*3600, "wilant", "sam")),
	))
	if len(out) != 1 {
		t.Fatalf("proposals = %+v, want one", out)
	}
	p := out[0]
	if p.Day != sat || p.RouteSlug != "hill" || p.CrewID != "crew-a" || p.WeekStart != mon {
		t.Errorf("proposal = %+v, want Saturday on hill", p)
	}
	if len(p.Riders) != 2 || p.Riders[0] != "sam" || p.Riders[1] != "wilant" {
		t.Errorf("riders = %v, want both, sorted", p.Riders)
	}
}

func TestOnlyOptedInRidersCountAndAnEmptyFlagIsOptedOut(t *testing.T) {
	g := group(
		[]Candidate{
			rider("wilant", weekend, long("w", sun, 3)),
			rider("sam", nil, long("s", sat, 3)), // no days: opted out
		},
		routes(visible("hill", 3*3600, "wilant", "sam")),
	)
	if out := align(g); len(out) != 0 {
		t.Errorf("proposals = %+v, want none: only one rider is open to it", out)
	}
}

func TestARiderWithAFixedCrewRideThatWeekIsLeftOut(t *testing.T) {
	row := workout.Workout{ID: "fixed", CrewRideID: "r", Date: fri, Steps: step(4 * 3600)}
	two := group(
		[]Candidate{rider("wilant", weekend, long("w", sun, 3)), rider("sam", weekend, long("s", sat, 3), row)},
		routes(visible("hill", 3*3600, "wilant", "sam")),
	)
	if out := align(two); len(out) != 0 {
		t.Errorf("proposals = %+v, want none: sam has a fixed ride and wilant is left alone", out)
	}
	three := group(
		[]Candidate{
			rider("wilant", weekend, long("w", sun, 3)),
			rider("sam", weekend, long("s", sat, 3), row),
			rider("alex", weekend, long("a", sun, 3)),
		},
		routes(visible("hill", 3*3600, "wilant", "sam", "alex")),
	)
	out := align(three)
	if len(out) != 1 || len(out[0].Riders) != 2 || out[0].Riders[0] != "alex" || out[0].Riders[1] != "wilant" {
		t.Errorf("proposals = %+v, want alex and wilant only", out)
	}
}

func TestSaturdayThenSundayThenTheRestInOrder(t *testing.T) {
	r := routes(visible("hill", 3*3600, "wilant", "sam"))
	days := []string{"sat", "sun", "thu", "fri"}
	blocker := func(date string) workout.Workout { return riderBuilt("busy-"+date, date) }

	cases := []struct {
		name  string
		block []string
		want  string
	}{
		{"saturday is free", nil, sat},
		{"saturday taken", []string{sat}, sun},
		{"weekend taken", []string{sat, sun}, thu}, // Thursday before Friday: chronological
		{"only friday left", []string{sat, sun, thu}, fri},
	}
	for _, tc := range cases {
		wilant := rider("wilant", days, long("w", thu, 3))
		for _, d := range tc.block {
			wilant.Workouts = append(wilant.Workouts, blocker(d))
		}
		out := align(group([]Candidate{wilant, rider("sam", days, long("s", fri, 3))}, r))
		if len(out) != 1 || out[0].Day != tc.want {
			t.Errorf("%s: proposals = %+v, want %s", tc.name, out, tc.want)
		}
	}
}

func TestEveryRidersDayMustBeFreeByThePlansOwnRules(t *testing.T) {
	r := routes(visible("hill", 3*3600, "wilant", "sam"))
	sam := func(ws ...workout.Workout) Candidate {
		return rider("sam", weekend, append([]workout.Workout{long("s", fri, 3)}, ws...)...)
	}
	wilant := rider("wilant", weekend, long("w", thu, 3))

	t.Run("a hard session the day before", func(t *testing.T) {
		out := align(group([]Candidate{wilant, sam(hard("h", fri))}, r))
		if len(out) != 1 || out[0].Day != sun {
			t.Errorf("proposals = %+v, want Sunday: Saturday follows a hard session", out)
		}
	})
	t.Run("a key session the day before", func(t *testing.T) {
		out := align(group([]Candidate{wilant, sam(long("k", fri, 3))}, r))
		if len(out) != 1 || out[0].Day != sun {
			t.Errorf("proposals = %+v, want Sunday: Saturday follows a long ride", out)
		}
	})
	t.Run("a life event day", func(t *testing.T) {
		s := sam()
		s.Blackout = map[string]bool{sat: true}
		out := align(group([]Candidate{wilant, s}, r))
		if len(out) != 1 || out[0].Day != sun {
			t.Errorf("proposals = %+v, want Sunday: Saturday is a blackout day", out)
		}
	})
	t.Run("a day an earlier move left empty", func(t *testing.T) {
		moved := gen("moved", "Endurance ride", thu, workout.ZoneEndurance, 3600)
		moved.Description += " " + scheduler.AdjustedMarker + " moved from " + sat + " - that session was missed."
		out := align(group([]Candidate{wilant, sam(moved)}, r))
		if len(out) != 1 || out[0].Day != sun {
			t.Errorf("proposals = %+v, want Sunday: Saturday is a vacated day", out)
		}
	})
	t.Run("a session of any kind on the day", func(t *testing.T) {
		out := align(group([]Candidate{wilant, sam(riderBuilt("mine", sat))}, r))
		if len(out) != 1 || out[0].Day != sun {
			t.Errorf("proposals = %+v, want Sunday", out)
		}
	})
	t.Run("the day of their own qualifying session is free", func(t *testing.T) {
		own := rider("sam", weekend, long("s", sat, 3))
		out := align(group([]Candidate{wilant, own}, r))
		if len(out) != 1 || out[0].Day != sat {
			t.Errorf("proposals = %+v, want Saturday: the session on it is the one that moves", out)
		}
	})
	t.Run("no day free for everyone", func(t *testing.T) {
		s := sam(riderBuilt("a", sat), riderBuilt("b", sun))
		if out := align(group([]Candidate{wilant, s}, r)); len(out) != 0 {
			t.Errorf("proposals = %+v, want none", out)
		}
	})
	t.Run("no shared flagged day", func(t *testing.T) {
		s := rider("sam", []string{"sun"}, long("s", fri, 3))
		w := rider("wilant", []string{"sat"}, long("w", thu, 3))
		if out := align(group([]Candidate{w, s}, r)); len(out) != 0 {
			t.Errorf("proposals = %+v, want none: they share no day", out)
		}
	})
}

func TestOnlyASessionThePlanMayMoveQualifies(t *testing.T) {
	r := routes(visible("hill", 3*3600, "wilant", "sam"))
	partner := rider("sam", weekend, long("s", fri, 3))
	adjusted := long("adj", thu, 3)
	adjusted.Description += " " + scheduler.AdjustedMarker + " eased."
	cases := map[string]workout.Workout{
		"a hard session":              hard("h", thu),
		"an endurance ride under 60":  gen("short", "Endurance ride", thu, workout.ZoneEndurance, 3000),
		"an adjusted long ride":       adjusted,
		"a rider-built long ride":     riderBuilt("b", thu),
		"a long ride dated today":     long("today", wed, 3),
		"a long ride already passed":  long("past", tue, 3),
		"a long ride dated next week": long("later", "2026-10-13", 3),
	}
	for name, w := range cases {
		if out := align(group([]Candidate{rider("wilant", weekend, w), partner}, r)); len(out) != 0 {
			t.Errorf("%s qualified: %+v", name, out)
		}
	}
	ridden := rider("wilant", weekend, long("rid", thu, 3))
	ridden.Ridden = map[string]bool{"rid": true}
	if out := align(group([]Candidate{ridden, partner}, r)); len(out) != 0 {
		t.Errorf("a ridden session qualified: %+v", out)
	}
	// An endurance session of an hour or more is enough.
	e := gen("e", "Endurance ride", thu, workout.ZoneEndurance, 3600)
	hour := routes(visible("hour", 3600, "wilant", "sam"))
	if out := align(group([]Candidate{rider("wilant", weekend, e), partner}, hour)); len(out) != 1 {
		t.Errorf("an hour of endurance did not qualify: %+v", out)
	}
	// Tomorrow is the first day that counts.
	tomorrow := long("tomorrow", thu, 3)
	if out := align(group([]Candidate{rider("wilant", weekend, tomorrow), partner}, r)); len(out) != 1 {
		t.Errorf("a long ride tomorrow did not qualify: %+v", out)
	}
}

func TestTheRouteIsTheClosestVisibleToEveryoneAndNotMoreThanFifteenPercentLong(t *testing.T) {
	cands := func() []Candidate {
		return []Candidate{
			rider("wilant", weekend, long("w", thu, 3)), // 10800 s
			rider("sam", weekend, long("s", fri, 2)),    // 7200 s: the shortest
		}
	}
	both := []string{"wilant", "sam"}
	pick := func(rs ...RouteOption) string {
		out := align(group(cands(), rs))
		if len(out) == 0 {
			return ""
		}
		return out[0].RouteSlug
	}

	if got := pick(visible("far", 7200*1.4, both...), visible("near", 7200*1.05, both...), visible("short", 7200*0.8, both...)); got != "near" {
		t.Errorf("route = %q, want the one closest to the shortest session", got)
	}
	if got := pick(visible("long", 7200*1.16, both...)); got != "" {
		t.Errorf("route = %q, want none: more than 15%% over the shortest session", got)
	}
	if got := pick(visible("edge", 7200*1.15, both...)); got != "edge" {
		t.Errorf("route = %q, want the 15%% bound inclusive", got)
	}
	if got := pick(visible("only-wilants", 7200, "wilant"), visible("only-sams", 7200, "sam")); got != "" {
		t.Errorf("route = %q, want none: nothing is visible to everyone", got)
	}
	if got := pick(); got != "" {
		t.Errorf("route = %q with no routes at all, want no proposal", got)
	}
	if got := pick(visible("b", 7200, both...), visible("a", 7200, both...)); got != "a" {
		t.Errorf("route = %q, want ties broken by slug", got)
	}
	if got := pick(visible("shorter", 3600, both...)); got != "shorter" {
		t.Errorf("route = %q: a route shorter than the shortest session is allowed", got)
	}
}

func TestEachCrewAndWeekIsAnswered(t *testing.T) {
	r := routes(visible("hill", 3*3600, "wilant", "sam"))
	a := group([]Candidate{rider("wilant", weekend, long("w", thu, 3)), rider("sam", weekend, long("s", fri, 3))}, r)
	b := a
	b.CrewID = "crew-b"
	c := a
	c.Candidates = []Candidate{rider("wilant", weekend, long("w", thu, 3))} // alone
	c.CrewID = "crew-c"
	out := Align(AlignInput{Groups: []Group{a, b, c}, Now: now})
	if len(out) != 2 || out[0].CrewID != "crew-a" || out[1].CrewID != "crew-b" {
		t.Errorf("proposals = %+v, want crew-a and crew-b", out)
	}
}

func TestAlignIsDeterministic(t *testing.T) {
	g := group([]Candidate{rider("wilant", weekend, long("w", thu, 3)), rider("sam", weekend, long("s", fri, 3))},
		routes(visible("b", 3*3600, "wilant", "sam"), visible("a", 3*3600, "wilant", "sam")))
	first, second := align(g), align(g)
	if len(first) != 1 || len(second) != 1 || first[0].RouteSlug != second[0].RouteSlug || first[0].Day != second[0].Day {
		t.Errorf("two runs differ: %+v vs %+v", first, second)
	}
}

// ---------- the premise still holding ----------

func holds(day string, members []MemberState, g Group) bool {
	return Premise(PremiseInput{Group: g, Day: day, RouteSlug: "hill", Members: members, Now: now})
}

func TestAProposalHoldsWhileEveryRidersPremiseDoes(t *testing.T) {
	g := group(
		[]Candidate{rider("wilant", weekend, long("w", thu, 3)), rider("sam", weekend, long("s", fri, 3))},
		routes(visible("hill", 3*3600, "wilant", "sam")),
	)
	pending := []MemberState{{Rider: "sam"}, {Rider: "wilant"}}
	if !holds(sat, pending, g) {
		t.Fatal("a fresh proposal does not hold")
	}

	t.Run("a flag removed", func(t *testing.T) {
		x := g
		x.Candidates = []Candidate{g.Candidates[0], rider("sam", nil, long("s", fri, 3))}
		if holds(sat, pending, x) {
			t.Error("still holds with sam opted out")
		}
	})
	t.Run("the day no longer flagged", func(t *testing.T) {
		x := g
		x.Candidates = []Candidate{g.Candidates[0], rider("sam", []string{"sun"}, long("s", fri, 3))}
		if holds(sat, pending, x) {
			t.Error("still holds on a day sam no longer offers")
		}
	})
	t.Run("a session ridden, deleted or moved", func(t *testing.T) {
		for name, ws := range map[string][]workout.Workout{
			"deleted": nil,
			"adjusted": func() []workout.Workout {
				a := long("s", fri, 3)
				a.Description += " " + scheduler.AdjustedMarker + " eased."
				return []workout.Workout{a}
			}(),
		} {
			x := g
			x.Candidates = []Candidate{g.Candidates[0], rider("sam", weekend, ws...)}
			if holds(sat, pending, x) {
				t.Errorf("%s: still holds without a qualifying session", name)
			}
		}
		x := g
		ridden := rider("sam", weekend, long("s", fri, 3))
		ridden.Ridden = map[string]bool{"s": true}
		x.Candidates = []Candidate{g.Candidates[0], ridden}
		if holds(sat, pending, x) {
			t.Error("still holds after the session was ridden")
		}
	})
	t.Run("the day filled", func(t *testing.T) {
		x := g
		x.Candidates = []Candidate{g.Candidates[0], rider("sam", weekend, long("s", fri, 3), riderBuilt("new", sat))}
		if holds(sat, pending, x) {
			t.Error("still holds after something was put on the day")
		}
	})
	t.Run("the route is no longer visible to everyone", func(t *testing.T) {
		x := g
		x.Routes = routes(visible("hill", 3*3600, "wilant"))
		if holds(sat, pending, x) {
			t.Error("still holds with the route hidden from sam")
		}
		x.Routes = nil
		if holds(sat, pending, x) {
			t.Error("still holds with the route gone")
		}
	})
	t.Run("the day has passed", func(t *testing.T) {
		if holds(tue, pending, g) {
			t.Error("still holds on a day that has gone")
		}
	})
	t.Run("an accepted rider is on the shared day", func(t *testing.T) {
		moved := long("w", sat, 3)
		moved.Description += " " + scheduler.AdjustedMarker + " moved from " + sun + "."
		x := g
		x.Candidates = []Candidate{rider("wilant", weekend, moved), g.Candidates[1]}
		accepted := []MemberState{{Rider: "sam"}, {Rider: "wilant", Accepted: true, WorkoutID: "w"}}
		if !holds(sat, accepted, x) {
			t.Error("a proposal one rider has already accepted no longer holds")
		}
		gone := g
		gone.Candidates = []Candidate{rider("wilant", weekend), g.Candidates[1]}
		if holds(sat, accepted, gone) {
			t.Error("still holds after the accepted session was deleted")
		}
		elsewhere := g
		elsewhere.Candidates = []Candidate{rider("wilant", weekend, long("w", sun, 3)), g.Candidates[1]}
		if holds(sat, accepted, elsewhere) {
			t.Error("still holds after the accepted session moved off the day")
		}
	})
	t.Run("a member who is not a candidate any more", func(t *testing.T) {
		x := g
		x.Candidates = g.Candidates[:1]
		if holds(sat, pending, x) {
			t.Error("still holds with sam gone from the group")
		}
	})
}
