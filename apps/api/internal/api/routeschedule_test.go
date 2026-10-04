package api_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/routefixture"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type situationOut struct {
	Date           string   `json:"date"`
	RouteSeconds   float64  `json:"routeSeconds"`
	RouteAssumed   bool     `json:"routeAssumed"`
	Choices        []string `json:"choices"`
	Default        string   `json:"default"`
	NeedsWorkoutID bool     `json:"needsWorkoutId"`
	Sessions       []struct {
		ID             string  `json:"id"`
		PlannedSeconds float64 `json:"plannedSeconds"`
		KeySession     bool    `json:"keySession"`
		Close          bool    `json:"close"`
	} `json:"sessions"`
}

type scheduleOut struct {
	Workout            workoutOut `json:"workout"`
	Choice             string     `json:"choice"`
	ReplacedKeySession bool       `json:"replacedKeySession"`
}

// libraryRoute is a synthetic 55 km flat route owned by owner.
func (h *wrHarness) libraryRoute(name, owner string) model.Route {
	h.t.Helper()
	rt, err := h.db.Create(context.Background(), source.CreateRequest{
		Name: name, UploadedBy: owner,
		GPX: routefixture.GPX(name, true, 100, 100, routefixture.Piece{LengthM: 55000, Grade: 0}),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return rt
}

func (h *wrHarness) situation(rider, slug, date string) (situationOut, int) {
	h.t.Helper()
	resp := h.as(rider, http.MethodGet, "/api/routes/"+slug+"/schedule?date="+date, "")
	raw := h.body(resp)
	var out situationOut
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			h.t.Fatalf("decode %s: %v", raw, err)
		}
	}
	return out, resp.StatusCode
}

func (h *wrHarness) schedule(rider, slug, body string) (*http.Response, string) {
	h.t.Helper()
	resp := h.as(rider, http.MethodPost, "/api/routes/"+slug+"/schedule", body)
	return resp, h.body(resp)
}

// rideOf seeds an outdoor endurance ride of the given length on date.
func (h *wrHarness) rideOf(rider, date string, seconds float64) workout.Workout {
	return h.seedWorkout(rider, "Endurance ride", workout.ZoneEndurance, date,
		timedStep("Ride", workout.IntensityActive, seconds, 0))
}

func scheduleHarness(t *testing.T) (*wrHarness, model.Route, float64) {
	t.Helper()
	h := newWRHarness(t)
	h.setProfile("wilant", wrFTP, wrWeight)
	rt := h.libraryRoute("Sunday loop", "wilant")
	s, status := h.situation("wilant", rt.Slug, wrFuture)
	if status != http.StatusOK || s.RouteSeconds <= 0 {
		t.Fatalf("situation: %d %+v", status, s)
	}
	return h, rt, s.RouteSeconds
}

func TestScheduleAnEmptyDayOffersOnlyANewRide(t *testing.T) {
	h, rt, route := scheduleHarness(t)
	s, _ := h.situation("wilant", rt.Slug, wrFuture)
	if len(s.Choices) != 1 || s.Choices[0] != "new" || s.Default != "new" || len(s.Sessions) != 0 {
		t.Errorf("empty day = %+v, want only new", s)
	}
	// The route's time is the shared estimate at endurance power.
	if route < 3000 || route > 9000 {
		t.Errorf("a 55 km flat route estimated at %v s, want roughly two hours", route)
	}
	if s.RouteAssumed {
		t.Error("estimate flagged assumed with FTP and weight set")
	}
}

func TestScheduleEstimateIsFlaggedWithoutAnFTP(t *testing.T) {
	h := newWRHarness(t)
	rt := h.libraryRoute("Sunday loop", "wilant")
	s, _ := h.situation("wilant", rt.Slug, wrFuture)
	if !s.RouteAssumed {
		t.Error("no FTP but the estimate is not flagged")
	}
	if want := 55000/(25/3.6) + 0; math.Abs(s.RouteSeconds-want) > want*0.01 {
		t.Errorf("no-FTP estimate = %v, want 25 km/h fallback %v", s.RouteSeconds, want)
	}
}

func TestIndoorRunningAndRiddenSessionsDoNotBlockANewRide(t *testing.T) {
	h, rt, route := scheduleHarness(t)
	ctx := context.Background()
	indoor := h.rideOf("wilant", wrFuture, route)
	yes := true
	_, _ = h.training.UpdateWorkout(ctx, indoor.ID, workout.UpdateWorkoutRequest{Indoor: &yes})
	if _, err := h.training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: "running", Name: "Run", Date: wrFuture,
		Steps: []workout.WorkoutStep{timedStep("Run", workout.IntensityActive, 1800, 0)},
	}); err != nil {
		t.Fatal(err)
	}
	h.rideOf("wilant", wrToday, route)
	_, _ = h.training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "r1", Sport: "cycling", Date: wrToday, DurationSeconds: 3600,
	})

	for _, day := range []string{wrFuture, wrToday} {
		s, status := h.situation("wilant", rt.Slug, day)
		if status != http.StatusOK || len(s.Choices) != 1 || s.Choices[0] != "new" {
			t.Errorf("%s: %d %+v, want only new", day, status, s)
		}
	}
}

func TestScheduleOneCloseRideOffersLinkOnly(t *testing.T) {
	h, rt, route := scheduleHarness(t)
	h.rideOf("wilant", wrFuture, route*1.1)
	s, _ := h.situation("wilant", rt.Slug, wrFuture)
	if len(s.Choices) != 1 || s.Choices[0] != "link" || s.Default != "link" || !s.Sessions[0].Close {
		t.Errorf("close ride = %+v, want link only", s)
	}
}

func TestScheduleTheTwentyPercentBoundary(t *testing.T) {
	for name, c := range map[string]struct {
		planned float64 // as a multiple of the route time
		close   bool
	}{
		"19 percent apart": {1 / 0.81, true}, // route is 19 percent under the plan
		"21 percent apart": {1 / 0.79, false},
	} {
		t.Run(name, func(t *testing.T) {
			h, rt, route := scheduleHarness(t)
			h.rideOf("wilant", wrFuture, route*c.planned)
			s, _ := h.situation("wilant", rt.Slug, wrFuture)
			if s.Sessions[0].Close != c.close {
				t.Errorf("close = %v, want %v", s.Sessions[0].Close, c.close)
			}
			if !c.close && (len(s.Choices) != 2 || s.Choices[0] != "link" || s.Choices[1] != "adjust" || s.Default != "link") {
				t.Errorf("choices = %v default %q, want link and adjust, link first", s.Choices, s.Default)
			}
		})
	}
}

func TestScheduleSeveralRidesNeedAWorkoutID(t *testing.T) {
	h, rt, route := scheduleHarness(t)
	a := h.rideOf("wilant", wrFuture, route)
	h.rideOf("wilant", wrFuture, route*2)
	s, _ := h.situation("wilant", rt.Slug, wrFuture)
	if !s.NeedsWorkoutID || len(s.Sessions) != 2 {
		t.Fatalf("two rides = %+v, want the list and needsWorkoutId", s)
	}
	resp, _ := h.schedule("wilant", rt.Slug, `{"date":"`+wrFuture+`","choice":"link"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("link without a workoutId: %d, want 400", resp.StatusCode)
	}
	resp, raw := h.schedule("wilant", rt.Slug, `{"date":"`+wrFuture+`","choice":"link","workoutId":"`+a.ID+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("link with a workoutId: %d %s", resp.StatusCode, raw)
	}
}

func TestLinkChangesOnlyTheRoute(t *testing.T) {
	h, rt, route := scheduleHarness(t)
	wk := h.rideOf("wilant", wrFuture, route)
	before, _ := h.training.GetWorkout(context.Background(), wk.ID)

	resp, raw := h.schedule("wilant", rt.Slug, `{"date":"`+wrFuture+`","choice":"link"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	after, _ := h.training.GetWorkout(context.Background(), wk.ID)
	if after.RouteSlug != rt.Slug || math.Abs(after.RouteSeconds-route) > 1 {
		t.Errorf("link = %q / %v, want %q / %v", after.RouteSlug, after.RouteSeconds, rt.Slug, route)
	}
	if after.Name != before.Name || after.Description != before.Description || len(after.Steps) != len(before.Steps) ||
		workout.PlannedSeconds(after.Steps) != workout.PlannedSeconds(before.Steps) || after.Zone != before.Zone {
		t.Errorf("link changed more than the route: %+v -> %+v", before, after)
	}
}

func TestAdjustRewritesTheRideInPlaceAndDropsTheGeneratedPrefix(t *testing.T) {
	h, rt, route := scheduleHarness(t)
	ctx := context.Background()
	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Fondo", Sport: model.SportCycling, EventDate: "2027-05-01"})
	if err != nil {
		t.Fatal(err)
	}
	wk, err := h.training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "Threshold 3x12", GoalID: goal.ID, Date: wrFuture,
		Description: scheduler.GeneratedDescription, Zone: workout.ZoneThreshold, Level: 5,
		Steps: []workout.WorkoutStep{timedStep("On", workout.IntensityActive, route*3, 250)},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, raw := h.schedule("wilant", rt.Slug, `{"date":"`+wrFuture+`","choice":"adjust"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	var out scheduleOut
	_ = json.Unmarshal([]byte(raw), &out)
	if out.Workout.ID != wk.ID {
		t.Errorf("adjust changed the id %q -> %q: it must edit in place", wk.ID, out.Workout.ID)
	}
	if !out.ReplacedKeySession {
		t.Error("a threshold session was replaced without saying so")
	}
	got, _ := h.training.GetWorkout(ctx, wk.ID)
	if got.GoalID != goal.ID {
		t.Errorf("goal = %q, want it kept so the day counts as taken", got.GoalID)
	}
	if len(got.Steps) != 1 || math.Abs(got.Steps[0].Seconds-math.Round(route)) > 1 || got.Steps[0].Duration != workout.DurationTime {
		t.Errorf("steps = %+v, want one step of the route time %v", got.Steps, route)
	}
	if got.Zone != workout.ZoneEndurance || got.Level != 0 {
		t.Errorf("zone/level = %q/%v, want endurance and 0", got.Zone, got.Level)
	}
	if got.Name != "Endurance ride: Sunday loop" || got.Description != "Sized to Sunday loop." {
		t.Errorf("name/description = %q / %q", got.Name, got.Description)
	}
	if scheduler.IsGenerated(got) || strings.HasPrefix(got.Description, scheduler.GeneratedDescription) {
		t.Error("the session is still plan-generated: replan and refresh would rebuild it")
	}
	if got.RouteSlug != rt.Slug {
		t.Errorf("route = %q", got.RouteSlug)
	}
}

func TestAdjustOfANonKeySessionDoesNotFlagAReplacement(t *testing.T) {
	h, rt, route := scheduleHarness(t)
	h.rideOf("wilant", wrFuture, route*2)
	_, raw := h.schedule("wilant", rt.Slug, `{"date":"`+wrFuture+`","choice":"adjust"}`)
	var out scheduleOut
	_ = json.Unmarshal([]byte(raw), &out)
	if out.ReplacedKeySession {
		t.Error("a plain endurance ride was reported as a replaced key session")
	}
}

func TestNewMakesAnEnduranceRideWithTheFocusGoalAndNeverASecondOutdoorRide(t *testing.T) {
	h, rt, route := scheduleHarness(t)
	ctx := context.Background()
	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Fondo", Sport: model.SportCycling, EventDate: "2027-05-01", Priority: workout.Priority("A")})
	if err != nil {
		t.Fatal(err)
	}
	resp, raw := h.schedule("wilant", rt.Slug, `{"date":"`+wrFuture+`","choice":"new"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	var out scheduleOut
	_ = json.Unmarshal([]byte(raw), &out)
	got, _ := h.training.GetWorkout(ctx, out.Workout.ID)
	if got.GoalID != goal.ID || got.Zone != workout.ZoneEndurance || got.Date != wrFuture || got.RouteSlug != rt.Slug {
		t.Errorf("new ride = %+v", got)
	}
	if math.Abs(workout.PlannedSeconds(got.Steps)-math.Round(route)) > 1 {
		t.Errorf("planned %v, want the route time %v", workout.PlannedSeconds(got.Steps), route)
	}
	if got.Description != "Sized to Sunday loop." {
		t.Errorf("description = %q", got.Description)
	}

	// The day now holds an unridden outdoor ride: new is no longer offered.
	resp, _ = h.schedule("wilant", rt.Slug, `{"date":"`+wrFuture+`","choice":"new"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("a second new ride on the day: %d, want 409", resp.StatusCode)
	}
}

func TestScheduleRefusalsPastRiddenRunningAndInvisible(t *testing.T) {
	h, rt, route := scheduleHarness(t)
	ctx := context.Background()

	if _, status := h.situation("wilant", rt.Slug, wrPast); status != http.StatusConflict {
		t.Errorf("a past day: %d, want 409", status)
	}
	resp, _ := h.schedule("wilant", rt.Slug, `{"date":"`+wrPast+`","choice":"new"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("POST on a past day: %d, want 409", resp.StatusCode)
	}

	// Targeting a ridden ride is refused even when named.
	ridden := h.rideOf("wilant", wrToday, route)
	_, _ = h.training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "r1", Sport: "cycling", Date: wrToday, DurationSeconds: 3600,
	})
	resp, _ = h.schedule("wilant", rt.Slug, `{"date":"`+wrToday+`","choice":"link","workoutId":"`+ridden.ID+`"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("link onto a ridden ride: %d, want 409", resp.StatusCode)
	}

	run, err := h.db.Create(ctx, source.CreateRequest{
		Name: "Park run", UploadedBy: "wilant", Sport: model.SportRunning,
		GPX: routefixture.GPX("Park run", true, 50, 100, routefixture.Piece{LengthM: 5000}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, status := h.situation("wilant", run.Slug, wrFuture); status != http.StatusUnprocessableEntity {
		t.Errorf("a running route: %d, want 422", status)
	}

	other := h.libraryRoute("Marie's loop", "marie")
	if _, status := h.situation("wilant", other.Slug, wrFuture); status != http.StatusNotFound {
		t.Errorf("a route the rider cannot see: %d, want 404", status)
	}
	if resp, _ := h.schedule("wilant", other.Slug, `{"date":"`+wrFuture+`","choice":"new"}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST on a route the rider cannot see: %d, want 404", resp.StatusCode)
	}
	if _, status := h.situation("wilant", rt.Slug, "not-a-date"); status != http.StatusBadRequest {
		t.Errorf("a bad date: %d, want 400", status)
	}
}

func TestSchedulingIsARiderActionSoItWritesNoAdjustmentRows(t *testing.T) {
	h, rt, route := scheduleHarness(t)
	wk := h.rideOf("wilant", wrFuture, route*2)
	h.schedule("wilant", rt.Slug, `{"date":"`+wrFuture+`","choice":"adjust"}`)
	found, err := h.training.LatestAdjustments(context.Background(), "wilant", workout.SubjectWorkout, []string{wk.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("%d adjustment rows after a rider action, want none", len(found))
	}
}

// A day inside a life event (travel, illness, a busy spell) is a day the rider is
// away: nothing may be put on it, a ride made from a route included.
func TestSchedulingOntoALifeEventDayIsRefused(t *testing.T) {
	h, rt, route := scheduleHarness(t)
	if _, err := h.training.CreateLifeEvent(context.Background(), workout.LifeEvent{
		Rider: "wilant", Kind: "travel", Start: wrFuture, End: "2026-10-05", Option: "no_bike",
	}); err != nil {
		t.Fatal(err)
	}
	ride := h.rideOf("wilant", "2026-10-05", route) // a session on a covered day, to link or adjust

	if _, status := h.situation("wilant", rt.Slug, wrFuture); status != http.StatusConflict {
		t.Errorf("situation for a blackout day: %d, want 409", status)
	}
	for _, body := range []string{
		`{"date":"` + wrFuture + `","choice":"new"}`,
		`{"date":"2026-10-05","choice":"link","workoutId":"` + ride.ID + `"}`,
		`{"date":"2026-10-05","choice":"adjust","workoutId":"` + ride.ID + `"}`,
	} {
		resp, raw := h.schedule("wilant", rt.Slug, body)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(raw, "away") {
			t.Errorf("POST %s = %d %s, want 409 saying the rider is away", body, resp.StatusCode, raw)
		}
	}
	if got, _ := h.training.GetWorkout(context.Background(), ride.ID); got.RouteSlug != "" || got.Name != "Endurance ride" {
		t.Errorf("a refused schedule still changed the ride: %+v", got)
	}

	// The next day is free.
	if _, status := h.situation("wilant", rt.Slug, "2026-10-06"); status != http.StatusOK {
		t.Errorf("a day after the event: %d, want 200", status)
	}
}

// A day holding a crew ride is the crew's: the ride already has the crew's
// route, so neither linking a library route to it nor adding a second ride
// beside it is offered.
func TestSchedulingOntoACrewRideDayIsRefused(t *testing.T) {
	h, rt, route := scheduleHarness(t)
	crewRide, err := h.training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "Sunday Club", Date: wrFuture, CrewRideID: "ride-1",
		Steps: []workout.WorkoutStep{timedStep("Crew ride", workout.IntensityActive, route, 0)},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, status := h.situation("wilant", rt.Slug, wrFuture); status != http.StatusConflict {
		t.Errorf("situation for a crew-ride day: %d, want 409", status)
	}
	for _, body := range []string{
		`{"date":"` + wrFuture + `","choice":"new"}`,
		`{"date":"` + wrFuture + `","choice":"link","workoutId":"` + crewRide.ID + `"}`,
		`{"date":"` + wrFuture + `","choice":"adjust","workoutId":"` + crewRide.ID + `"}`,
	} {
		resp, raw := h.schedule("wilant", rt.Slug, body)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(raw, "crew_ride") {
			t.Errorf("POST %s = %d %s, want 409 with code crew_ride", body, resp.StatusCode, raw)
		}
	}
	if got, _ := h.training.GetWorkout(context.Background(), crewRide.ID); got.RouteSlug != "" {
		t.Errorf("a refused schedule linked a route to the crew ride: %+v", got)
	}
}

func TestSchedulePermissionIsCheckedBeforeTheBodyIsRead(t *testing.T) {
	h, rt, _ := scheduleHarness(t)
	resp := h.asGroup("guest", "guests", http.MethodPost, "/api/routes/"+rt.Slug+"/schedule", "not json")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer posting garbage got %d, want 403 before any parsing", resp.StatusCode)
	}
}

func TestSchedulingTodayUsesTheRidersOwnDay(t *testing.T) {
	h, rt, _ := scheduleHarness(t)
	// The rider's browser says it is already the 4th: the 3rd is the past.
	resp := h.as("wilant", http.MethodGet, "/api/routes/"+rt.Slug+"/schedule?date="+wrToday+"&today="+wrFuture, "")
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("a day before the rider's today: %d, want 409", resp.StatusCode)
	}
}
