package api_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/routefixture"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A synthetic route whose long climb takes about 13 minutes at race pace and
// whose short one about 4.5: with threshold and vo2max at level 4 the plan
// would pick 4 x 8 and 6 x 3, and the route asks for 3 x 12 and 5 x 4.
func biasRoute() []routefixture.Piece {
	return []routefixture.Piece{{LengthM: 1500, Grade: 0}, {LengthM: 3300, Grade: 6}, {LengthM: 1000, Grade: -5},
		{LengthM: 500, Grade: 0}, {LengthM: 1000, Grade: 8}, {LengthM: 1000, Grade: -4}, {LengthM: 1500, Grade: 0}}
}

var biasDays = []string{"mon", "tue", "wed", "thu", "fri", "sat"}

const biasEvent = "2027-04-14"

func (h *seasonHarness) addBiasRoute(t *testing.T, owner string, withEle bool) string {
	t.Helper()
	rt, err := h.srv.Source.Create(context.Background(), source.CreateRequest{
		Name: "Bias Hills", UploadedBy: owner,
		GPX: routefixture.GPX("Bias Hills", withEle, 25, 100, biasRoute()...),
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt.Slug
}

func (h *seasonHarness) setLevels(t *testing.T) {
	t.Helper()
	for _, z := range []workout.Zone{workout.ZoneSweetSpot, workout.ZoneThreshold, workout.ZoneVO2Max, workout.ZoneAnaerobic} {
		if err := h.store.SaveLevel(context.Background(), workout.ProgressionLevel{
			Rider: "wilant", Sport: model.SportCycling, Zone: z, Level: 4, Reason: "test",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// biasHarness is a rider with a four-level profile and FTP; route "" makes the
// plain baseline.
func newBiasHarness(t *testing.T, ftp float64) *seasonHarness {
	t.Helper()
	h := newSeasonHarness(t)
	h.profile(t, biasDays, ftp)
	h.setLevels(t)
	return h
}

func (h *seasonHarness) seasonWith(t *testing.T, routeJSON string) workout.Goal {
	t.Helper()
	g := h.createGoal(t, `{"name":"Gran Fondo","eventDate":"`+biasEvent+`"`+routeJSON+`}`)
	h.srv.WaitForBackground()
	return g
}

func byDate(ws []workout.Workout) map[string]workout.Workout {
	m := map[string]workout.Workout{}
	for _, w := range ws {
		m[w.Date] = w
	}
	return m
}

func weekOf(weeks []periodization.Week, date string) (periodization.Week, bool) {
	for _, w := range weeks {
		if date >= w.StartDate && date <= addDays(w.StartDate, 6) {
			return w, true
		}
	}
	return periodization.Week{}, false
}

func addDays(date string, n int) string {
	d, _ := time.Parse("2006-01-02", date)
	return d.AddDate(0, 0, n).Format("2006-01-02")
}

func TestANewSeasonIsFilledWithRungsMatchedToTheRoute(t *testing.T) {
	plainH := newBiasHarness(t, 250)
	plainG := plainH.seasonWith(t, "")
	routedH := newBiasHarness(t, 250)
	slug := routedH.addBiasRoute(t, "wilant", true)
	routedG := routedH.seasonWith(t, `,"routeSlug":"`+slug+`"`)

	weeks := routedH.planWeeks(t, routedG)
	plain, routed := byDate(plainH.workouts(t)), byDate(routedH.workouts(t))
	if len(plain) == 0 || len(plain) != len(routed) {
		t.Fatalf("plain season has %d sessions, routed %d", len(plain), len(routed))
	}
	_ = plainG

	var biasedWeeks, renamedLong int
	for date, p := range plain {
		r, ok := routed[date]
		if !ok {
			t.Fatalf("%s: routed season has no session", date)
		}
		wk, ok := weekOf(weeks, date)
		if !ok {
			t.Fatalf("%s is in no plan week", date)
		}
		if p.Zone != r.Zone || p.Sport != r.Sport {
			t.Errorf("%s: zone/sport moved %s/%s -> %s/%s", date, p.Zone, p.Sport, r.Zone, r.Sport)
		}
		bias := !wk.Recovery && (wk.Phase == periodization.PhaseBuild || wk.Phase == periodization.PhasePeak)
		if !bias {
			if p.Name != r.Name || p.Level != r.Level || !reflect.DeepEqual(p.Steps, r.Steps) {
				t.Errorf("%s (%s, recovery=%v): a route changed a week it must not: %q -> %q", date, wk.Phase, wk.Recovery, p.Name, r.Name)
			}
			continue
		}
		if r.Level > p.Level+1 {
			t.Errorf("%s: level %v is more than one above the plain %v", date, r.Level, p.Level)
		}
		if p.Name != r.Name {
			biasedWeeks++
		}
		if r.Name == scheduler.ClimbingLongRideName {
			renamedLong++
		}
		if p.Name == scheduler.ClimbingLongRideName {
			t.Errorf("%s: the plain season has the climbing name", date)
		}
	}
	if biasedWeeks == 0 {
		t.Error("no Build or Peak session moved toward the route")
	}
	if renamedLong == 0 {
		t.Error("no long ride was named for a climbing route")
	}
	if n := countNamed(routedH.workouts(t), "Threshold 3×12"); n == 0 {
		t.Error("expected threshold sessions of 3 x 12 for a 13 minute climb")
	}
}

func countNamed(ws []workout.Workout, name string) int {
	n := 0
	for _, w := range ws {
		if w.Name == name {
			n++
		}
	}
	return n
}

// Whatever stops a demand from being computed must leave planning exactly as
// it is without a route: never an error, never a blocked season.
func TestNoUsableRouteOrFTPMeansTheSeasonIsPlainNotBroken(t *testing.T) {
	names := func(h *seasonHarness) []string {
		var out []string
		for _, w := range h.workouts(t) {
			out = append(out, w.Date+" "+w.Name)
		}
		return out
	}
	plain250 := newBiasHarness(t, 250)
	plain250.seasonWith(t, "")
	want250 := names(plain250)
	plain0 := newBiasHarness(t, 0)
	plain0.seasonWith(t, "")
	want0 := names(plain0)

	cases := []struct {
		name  string
		ftp   float64
		setup func(h *seasonHarness) string
		want  []string
	}{
		{"a route that does not exist", 250, func(h *seasonHarness) string { return "no-such-route" }, want250},
		{"another rider's route", 250, func(h *seasonHarness) string { return h.addBiasRoute(t, "someone-else", true) }, want250},
		{"a route with no elevation", 250, func(h *seasonHarness) string { return h.addBiasRoute(t, "wilant", false) }, want250},
		{"no FTP", 0, func(h *seasonHarness) string { return h.addBiasRoute(t, "wilant", true) }, want0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newBiasHarness(t, c.ftp)
			slug := c.setup(h)
			// Straight through the store: the API would refuse an invisible or
			// missing route, which is a different guarantee.
			g, err := h.store.CreateGoal(context.Background(), workout.CreateGoalRequest{
				Rider: "wilant", Name: "Gran Fondo", EventDate: biasEvent, RouteSlug: slug,
			})
			if err != nil {
				t.Fatal(err)
			}
			h.srv.AutoScheduleTick(context.Background()) // auto-schedule is off: nothing
			if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
				t.Fatal(err)
			}
			h.srv.AutoScheduleTick(context.Background())
			h.srv.WaitForBackground()
			got := names(h)
			if len(got) == 0 {
				t.Fatalf("goal %s: nothing was planned", g.ID)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("planning with %s differs from planning without a route", c.name)
			}
		})
	}
}

// Refresh: the route shapes an untouched generated session when its week comes
// within reach, and only that. Linking a route to a goal whose weeks are
// already filled rebuilds nothing; the week changes when it next refreshes.
func TestRefreshMatchesUntouchedSessionsAndLeavesTheRestAlone(t *testing.T) {
	h := newBiasHarness(t, 250)
	ctx := context.Background()
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	g, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: biasEvent})
	if err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(ctx)
	h.backdate(t)

	// A Build week well ahead that is not a recovery week.
	var target time.Time
	for _, w := range h.planWeeks(t, g) {
		start, _ := time.Parse("2006-01-02", w.StartDate)
		if w.Phase == periodization.PhaseBuild && !w.Recovery && start.After(h.now.AddDate(0, 0, 21)) {
			target = start
			break
		}
	}
	if target.IsZero() {
		t.Fatal("no Build week in the plan")
	}
	week := h.week(t, target)
	var untouched, edited, replaced *workout.Workout
	for i := range week {
		switch {
		case week[i].Zone == workout.ZoneThreshold && untouched == nil:
			untouched = &week[i]
		case week[i].Zone == workout.ZoneEndurance && edited == nil && week[i].Name != "Long ride":
			edited = &week[i]
		case week[i].Zone == workout.ZoneVO2Max && replaced == nil:
			replaced = &week[i]
		}
	}
	if untouched == nil || edited == nil || replaced == nil {
		t.Fatalf("the Build week lacks a threshold, an endurance and a vo2max session: %+v", week)
	}
	if untouched.Name != "Threshold 4×8" {
		t.Fatalf("fixture: plain threshold session is %q, want 4×8", untouched.Name)
	}

	// Link the route now. The filled week is not rebuilt.
	slug := h.addBiasRoute(t, "wilant", true)
	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+g.ID, `{"routeSlug":"`+slug+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("link route: status %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()
	if got, _ := h.store.GetWorkout(ctx, untouched.ID); got.Name != untouched.Name || got.UpdatedAt != untouched.UpdatedAt {
		t.Errorf("linking a route rebuilt an already filled week: %q", got.Name)
	}

	// Touch two sessions the ways a rider can.
	newName := "My own name"
	if _, err := h.store.UpdateWorkout(ctx, edited.ID, workout.UpdateWorkoutRequest{Name: &newName}); err != nil {
		t.Fatal(err)
	}
	test, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Name: "FTP test", GoalID: g.ID, Date: replaced.Date, Description: "A test.",
		TestProtocol: "twenty_minute", Steps: replaced.Steps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.DeleteWorkout(ctx, replaced.ID); err != nil {
		t.Fatal(err)
	}

	// The Wednesday before the target week: it is now next week.
	h.now = time.Date(target.Year(), target.Month(), target.Day(), 12, 0, 0, 0, time.UTC).AddDate(0, 0, -5)
	h.srv.AutoScheduleTick(ctx)

	got, _ := h.store.GetWorkout(ctx, untouched.ID)
	if got.Name != "Threshold 3×12" || got.Level != untouched.Level+1 || got.ID != untouched.ID || got.Date != untouched.Date || got.Zone != untouched.Zone {
		t.Errorf("the untouched session was not matched to the route: %+v", got)
	}
	if got, _ := h.store.GetWorkout(ctx, edited.ID); got.Name != newName {
		t.Errorf("an edited session was rewritten: %q", got.Name)
	}
	if got, _ := h.store.GetWorkout(ctx, test.ID); got.Name != "FTP test" || got.Description != "A test." {
		t.Errorf("the FTP test was touched: %+v", got)
	}

	// A second pass changes nothing.
	before := h.workouts(t)
	h.now = h.now.Add(time.Hour)
	h.srv.AutoScheduleTick(ctx)
	after := h.workouts(t)
	if len(before) != len(after) {
		t.Fatalf("a second pass changed the session count %d -> %d", len(before), len(after))
	}
	bm := byDate(before)
	for _, a := range after {
		if b := bm[a.Date]; b.ID == a.ID && (b.UpdatedAt != a.UpdatedAt || b.Name != a.Name) {
			t.Errorf("a second pass changed %s: %q -> %q", a.Date, b.Name, a.Name)
		}
	}
}

func TestRidingARiddenOrMovedSessionIsNeverRewrittenByTheBias(t *testing.T) {
	h := newBiasHarness(t, 250)
	ctx := context.Background()
	slug := h.addBiasRoute(t, "wilant", true)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	g, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: biasEvent})
	if err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(ctx)
	h.backdate(t)

	var target time.Time
	for _, w := range h.planWeeks(t, g) {
		start, _ := time.Parse("2006-01-02", w.StartDate)
		if w.Phase == periodization.PhaseBuild && !w.Recovery && start.After(h.now.AddDate(0, 0, 21)) {
			target = start
			break
		}
	}
	var moved workout.Workout
	for _, w := range h.week(t, target) {
		if w.Zone == workout.ZoneThreshold {
			moved = w
		}
	}
	sunday := target.AddDate(0, 0, 6).Format("2006-01-02")
	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+moved.ID, `{"date":"`+sunday+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("move: status %d", resp.StatusCode)
	}
	movedBefore, _ := h.store.GetWorkout(ctx, moved.ID)

	one := slug
	if _, err := h.store.UpdateGoal(ctx, g.ID, workout.UpdateGoalRequest{RouteSlug: &one}); err != nil {
		t.Fatal(err)
	}
	h.now = time.Date(target.Year(), target.Month(), target.Day(), 12, 0, 0, 0, time.UTC).AddDate(0, 0, -5)
	h.srv.AutoScheduleTick(ctx)

	got, _ := h.store.GetWorkout(ctx, moved.ID)
	if got.Name != movedBefore.Name || got.Date != sunday || got.UpdatedAt != movedBefore.UpdatedAt || !reflect.DeepEqual(got.Steps, movedBefore.Steps) {
		t.Errorf("a moved session was rewritten by the bias: %q -> %q", movedBefore.Name, got.Name)
	}
}

func TestSeasonLogsCarryGoalAndOutcomeOnly(t *testing.T) {
	h := newBiasHarness(t, 287)
	var buf bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	if err := h.store.SetWeight(context.Background(), "wilant", 83.4); err != nil {
		t.Fatal(err)
	}
	slug := h.addBiasRoute(t, "wilant", true)
	h.seasonWith(t, `,"routeSlug":"`+slug+`"`)

	logged := strings.ToLower(buf.String())
	if !strings.Contains(logged, "route demand") {
		t.Fatalf("expected a route-demand line in %q", logged)
	}
	for _, secret := range []string{"287", "83.4", "watts", "4.567", "51.2", "lat=", "lon="} {
		if strings.Contains(logged, secret) {
			t.Errorf("the log carries %q: %s", secret, logged)
		}
	}
}
