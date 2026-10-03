package api_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// flakyLibrary is the route library with a track read that can fail the way a
// transient storage error does (not "no such route"), and that counts how often
// the track is read.
type flakyLibrary struct {
	source.Library
	failTrack atomic.Bool
	tracks    atomic.Int32
}

func (f *flakyLibrary) Track(ctx context.Context, slug string) ([]gpx.Point, error) {
	f.tracks.Add(1)
	if f.failTrack.Load() {
		return nil, errors.New("library unavailable")
	}
	return f.Library.Track(ctx, slug)
}

// biasTarget is a Build week, not a recovery week, well ahead of the harness
// clock.
func biasTarget(t *testing.T, h *seasonHarness, g workout.Goal) time.Time {
	t.Helper()
	for _, w := range h.planWeeks(t, g) {
		start, _ := time.Parse("2006-01-02", w.StartDate)
		if w.Phase == periodization.PhaseBuild && !w.Recovery && start.After(h.now.AddDate(0, 0, 21)) {
			return start
		}
	}
	t.Fatal("no Build week in the plan")
	return time.Time{}
}

func beforeWeek(target time.Time) time.Time {
	return time.Date(target.Year(), target.Month(), target.Day(), 12, 0, 0, 0, time.UTC).AddDate(0, 0, -5)
}

// A route that cannot be read right now is not the same as a route that cannot
// be used: the bias must not be lost for good because of a blip. A week that
// would be filled and recorded as fresh waits for the next pass instead.
func TestAFailedRouteReadDelaysAFillInsteadOfLosingTheBias(t *testing.T) {
	h := newBiasHarness(t, 250)
	ctx := context.Background()
	slug := h.addBiasRoute(t, "wilant", true)
	fl := &flakyLibrary{Library: h.srv.Source}
	h.srv.Source = fl
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	g, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: biasEvent, RouteSlug: slug})
	if err != nil {
		t.Fatal(err)
	}
	target := biasTarget(t, h, g)
	h.now = beforeWeek(target) // the target week is next week: within the refresh horizon

	fl.failTrack.Store(true)
	h.srv.AutoScheduleTick(ctx)
	if n := len(h.week(t, target)); n != 0 {
		t.Fatalf("the target week was filled (%d sessions) while the route could not be read: the bias would be lost for good", n)
	}
	if done, _ := h.store.WeekScheduled(ctx, g.ID, target.Format("2006-01-02")); done {
		t.Fatal("the target week was recorded as filled")
	}

	fl.failTrack.Store(false)
	h.srv.AutoScheduleTick(ctx)
	if countNamed(h.week(t, target), "Threshold 3×12") == 0 {
		t.Errorf("after the library came back the week was not filled with matched rungs: %+v", h.week(t, target))
	}
}

func TestAFailedRouteReadDelaysARefreshInsteadOfConsumingIt(t *testing.T) {
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
	target := biasTarget(t, h, g)
	var untouched workout.Workout
	for _, w := range h.week(t, target) {
		if w.Zone == workout.ZoneThreshold {
			untouched = w
		}
	}
	if untouched.Name != "Threshold 4×8" {
		t.Fatalf("fixture: plain threshold session is %q", untouched.Name)
	}

	slug := h.addBiasRoute(t, "wilant", true)
	fl := &flakyLibrary{Library: h.srv.Source}
	h.srv.Source = fl
	one := slug
	if _, err := h.store.UpdateGoal(ctx, g.ID, workout.UpdateGoalRequest{RouteSlug: &one}); err != nil {
		t.Fatal(err)
	}
	h.now = beforeWeek(target)

	fl.failTrack.Store(true)
	h.srv.AutoScheduleTick(ctx)
	if got, _ := h.store.GetWorkout(ctx, untouched.ID); got.Name != "Threshold 4×8" {
		t.Fatalf("a refresh ran without the route: %q", got.Name)
	}
	if weeks, _ := h.store.ScheduledWeeks(ctx, g.ID); weeks[target.Format("2006-01-02")] {
		t.Fatal("the week was recorded as refreshed while the route could not be read")
	}

	fl.failTrack.Store(false)
	h.srv.AutoScheduleTick(ctx)
	if got, _ := h.store.GetWorkout(ctx, untouched.ID); got.Name != "Threshold 3×12" {
		t.Errorf("after the library came back the refresh did not match the route: %q", got.Name)
	}
}

// The route is read only when a week is about to be filled or refreshed, not on
// every pass over a season that has nothing left to do (the pass holds a lock).
func TestTheRouteIsNotReadWhenNothingIsToBeFilledOrRefreshed(t *testing.T) {
	h := newBiasHarness(t, 250)
	ctx := context.Background()
	slug := h.addBiasRoute(t, "wilant", true)
	fl := &flakyLibrary{Library: h.srv.Source}
	h.srv.Source = fl
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: biasEvent, RouteSlug: slug}); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(ctx)
	if fl.tracks.Load() == 0 {
		t.Fatal("the first pass filled the season without reading the route")
	}
	before := fl.tracks.Load()
	h.srv.AutoScheduleTick(ctx)
	h.srv.AutoScheduleTick(ctx)
	if after := fl.tracks.Load(); after != before {
		t.Errorf("a settled season read the route %d more times", after-before)
	}
}
