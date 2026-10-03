package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/routefixture"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// demandsClock is a fixed Monday, in an explicit zone, so every date below is
// the same wherever the suite runs.
var demandsClock = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

type demandsOut struct {
	Available   bool     `json:"available"`
	Reason      string   `json:"reason"`
	ReasonCode  string   `json:"reasonCode"`
	Assumptions []string `json:"assumptions"`
	Route       *struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"route"`
	Climbs []struct {
		Index       int     `json:"index"`
		StartM      float64 `json:"startM"`
		EndM        float64 `json:"endM"`
		LengthM     float64 `json:"lengthM"`
		GainM       float64 `json:"gainM"`
		AvgGradient float64 `json:"avgGradient"`
		Category    string  `json:"category"`
		DurationSec float64 `json:"durationSec"`
		Watts       float64 `json:"watts"`
		PctFtp      float64 `json:"pctFtp"`
		Kind        string  `json:"kind"`
		Covered     bool    `json:"covered"`
	} `json:"climbs"`
	Coverage struct {
		LongestSustainedSec float64 `json:"longestSustainedSec"`
		Uncovered           int     `json:"uncovered"`
		Message             string  `json:"message"`
	} `json:"coverage"`
	Bias struct {
		Active bool   `json:"active"`
		Phase  string `json:"phase"`
	} `json:"bias"`
}

func newDemandsHarness(t *testing.T) *routeHarness {
	t.Helper()
	h := newRouteHarness(t)
	h.srv.Clock = func() time.Time { return demandsClock }
	return h
}

func (h *routeHarness) rider(name string, ftp float64) {
	h.t.Helper()
	if _, err := h.store.SaveProfile(h.t.Context(), workout.RiderProfile{Rider: name, FTPWatts: ftp, MaxHR: 190, ThresholdHR: 170}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *routeHarness) goalFor(rider, slug string, sport model.Sport) workout.Goal {
	h.t.Helper()
	g, err := h.store.CreateGoal(h.t.Context(), workout.CreateGoalRequest{
		Rider: rider, Name: "Hilly Fondo", Sport: sport, EventDate: "2027-01-31", RouteSlug: slug,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return g
}

func (h *routeHarness) demands(user, goalID string) (*http.Response, []byte) {
	h.t.Helper()
	resp := h.as(user, "cyclists", http.MethodGet, "/api/training/goals/"+goalID+"/route-demands", "")
	return resp, readAll(h.t, resp)
}

func (h *routeHarness) mustDemands(user, goalID string) demandsOut {
	h.t.Helper()
	resp, body := h.demands(user, goalID)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	var out demandsOut
	if err := json.Unmarshal(body, &out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func TestRouteDemandsReportClimbsWithTimeAndWatts(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	slug := h.addRoute("wilant", "Demo Hills", true)
	g := h.goalFor("wilant", slug, model.SportCycling)

	out := h.mustDemands("wilant", g.ID)
	if !out.Available || out.Route == nil || out.Route.Slug != slug || out.Route.Name != "Demo Hills" {
		t.Fatalf("response = %+v, want available with the route", out)
	}
	if len(out.Climbs) != 2 {
		t.Fatalf("got %d climbs, want the fixture's 2: %+v", len(out.Climbs), out.Climbs)
	}

	for i, c := range out.Climbs {
		if c.Index != i || c.EndM <= c.StartM || c.LengthM <= 0 || c.GainM <= 0 {
			t.Errorf("climb %d malformed: %+v", i, c)
		}
		if math.Abs(c.EndM-c.StartM-c.LengthM) > 1 {
			t.Errorf("climb %d: endM-startM %.0f vs lengthM %.0f", i, c.EndM-c.StartM, c.LengthM)
		}
		// The target is FTP x IF x factor(duration): recover the factor from
		// the response and check it matches the duration it came with.
		factor := 1.00
		switch {
		case c.DurationSec < 300:
			factor = 1.10
		case c.DurationSec <= 1200:
			factor = 1.05
		}
		found := false
		for _, ifv := range []float64{0.95, 0.85, 0.75} {
			if math.Abs(c.Watts-250*ifv*factor) <= 0.6 {
				found = true
			}
		}
		if !found {
			t.Errorf("climb %d: %.0f W at %.0f s is not 250 x {0.95,0.85,0.75} x %.2f", i, c.Watts, c.DurationSec, factor)
		}
		if math.Abs(c.PctFtp-c.Watts/250*100) > 1 {
			t.Errorf("climb %d: pctFtp %.1f does not match %.0f W of 250", i, c.PctFtp, c.Watts)
		}
		wantKind := "long"
		switch {
		case c.DurationSec < 240:
			wantKind = "short"
		case c.DurationSec < 480:
			wantKind = "medium"
		case c.DurationSec <= 1200:
			wantKind = "sustained"
		}
		if c.Kind != wantKind {
			t.Errorf("climb %d: kind %q at %.0f s, want %q", i, c.Kind, c.DurationSec, wantKind)
		}
	}
	// 2.3 km at 6 % and 1 km at 8 %: the first takes longer and is rated.
	if out.Climbs[0].DurationSec <= out.Climbs[1].DurationSec {
		t.Errorf("the long climb (%.0f s) should take longer than the steep short one (%.0f s)", out.Climbs[0].DurationSec, out.Climbs[1].DurationSec)
	}
	if out.Climbs[0].Kind != "sustained" || out.Climbs[1].Kind != "medium" {
		t.Errorf("kinds = %q, %q; want sustained, medium for the fixture", out.Climbs[0].Kind, out.Climbs[1].Kind)
	}
	if out.Climbs[0].Category == "" {
		t.Errorf("a 2.3 km 6%% climb (score ~13,800) should be categorised 4, got none")
	}
	// Nothing is planned yet, so nothing is covered, and the message says so.
	if out.Coverage.Uncovered != 2 || out.Coverage.LongestSustainedSec != 0 || !strings.Contains(out.Coverage.Message, "2 climbs") {
		t.Errorf("coverage = %+v", out.Coverage)
	}
	if out.Bias.Active {
		t.Errorf("bias reported active before generation uses it: %+v", out.Bias)
	}
	joined := strings.Join(out.Assumptions, " | ")
	if !strings.Contains(joined, "75 kg") {
		t.Errorf("a rider with no weight must see the 75 kg assumption, got %q", joined)
	}
}

func TestRouteDemandsAssumptionsFollowTheWeight(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	if err := h.store.SetWeight(t.Context(), "wilant", 68); err != nil {
		t.Fatal(err)
	}
	slug := h.addRoute("wilant", "Demo Hills", true)
	g := h.goalFor("wilant", slug, model.SportCycling)

	out := h.mustDemands("wilant", g.ID)
	joined := strings.Join(out.Assumptions, " | ")
	if strings.Contains(joined, "Assumed 75 kg") {
		t.Errorf("a rider with a weight is told it was assumed: %q", joined)
	}
	// A lighter rider climbs faster than the 75 kg default.
	h.rider("other", 250)
	slug2 := h.addRoute("other", "Other Hills", true)
	g2 := h.goalFor("other", slug2, model.SportCycling)
	heavy := h.mustDemands("other", g2.ID)
	if out.Climbs[0].DurationSec >= heavy.Climbs[0].DurationSec {
		t.Errorf("68 kg took %.0f s, 75 kg took %.0f s on the same climb", out.Climbs[0].DurationSec, heavy.Climbs[0].DurationSec)
	}
}

func TestRouteDemandsAreUnavailableWithAReason(t *testing.T) {
	cases := []struct {
		name  string
		build func(h *routeHarness) workout.Goal
		code  string
		hint  string
	}{
		{"no route on the goal", func(h *routeHarness) workout.Goal {
			h.rider("wilant", 250)
			return h.goalFor("wilant", "", model.SportCycling)
		}, "no_route", "route"},
		{"the route was deleted", func(h *routeHarness) workout.Goal {
			h.rider("wilant", 250)
			slug := h.addRoute("wilant", "Gone", true)
			g := h.goalFor("wilant", slug, model.SportCycling)
			if err := h.lib.Delete(t.Context(), slug); err != nil {
				t.Fatal(err)
			}
			return g
		}, "route_unavailable", "no longer"},
		{"the route is not visible to the rider", func(h *routeHarness) workout.Goal {
			h.rider("wilant", 250)
			slug := h.addRoute("someone-else", "Their Hills", true)
			return h.goalFor("wilant", slug, model.SportCycling) // straight through the store: the API would refuse this
		}, "route_unavailable", "no longer"},
		{"the route has no elevation", func(h *routeHarness) workout.Goal {
			h.rider("wilant", 250)
			slug := h.addRoute("wilant", "Flat Nowhere", false)
			return h.goalFor("wilant", slug, model.SportCycling)
		}, "no_elevation", "Recalculate"},
		{"a running goal", func(h *routeHarness) workout.Goal {
			h.rider("wilant", 250)
			slug := h.addRoute("wilant", "Demo Hills", true)
			return h.goalFor("wilant", slug, model.SportRunning)
		}, "not_cycling", "cycling"},
		{"no FTP", func(h *routeHarness) workout.Goal {
			slug := h.addRoute("wilant", "Demo Hills", true)
			return h.goalFor("wilant", slug, model.SportCycling)
		}, "no_ftp", "FTP"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newDemandsHarness(t)
			g := c.build(h)
			resp, body := h.demands("wilant", g.ID)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d: %s", resp.StatusCode, body)
			}
			var out demandsOut
			_ = json.Unmarshal(body, &out)
			if out.Available || out.ReasonCode != c.code {
				t.Fatalf("available=%v code=%q, want unavailable with %q: %s", out.Available, out.ReasonCode, c.code, body)
			}
			if !strings.Contains(out.Reason, c.hint) {
				t.Errorf("reason %q should mention %q so the rider knows the fix", out.Reason, c.hint)
			}
			// An unavailable answer carries nothing about the route.
			if out.Route != nil || len(out.Climbs) != 0 || strings.Contains(string(body), "Their Hills") || strings.Contains(string(body), "Gone") {
				t.Errorf("an unavailable answer leaked route details: %s", body)
			}
		})
	}
}

func TestAnotherRidersGoalIs404AndAViewerIsRefused(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	slug := h.addRoute("wilant", "Demo Hills", true)
	g := h.goalFor("wilant", slug, model.SportCycling)

	resp, body := h.demands("friend", g.ID)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("another rider's goal: status = %d, want 404", resp.StatusCode)
	}
	resp2, body2 := h.demands("friend", "no-such-goal")
	if resp2.StatusCode != http.StatusNotFound || string(body) != string(body2) {
		t.Errorf("a goal that exists and one that does not must answer alike: %d %s / %d %s", resp.StatusCode, body, resp2.StatusCode, body2)
	}
	// Even an admin: a goal is a rider's own training.
	adm := h.as("boss", "admins", http.MethodGet, "/api/training/goals/"+g.ID+"/route-demands", "")
	if adm.StatusCode != http.StatusNotFound {
		t.Errorf("admin: status = %d, want 404", adm.StatusCode)
	}
	viewer := h.as("wilant", "guests", http.MethodGet, "/api/training/goals/"+g.ID+"/route-demands", "")
	if viewer.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer: status = %d, want 403", viewer.StatusCode)
	}
}

func work(secs float64) workout.WorkoutStep {
	return workout.WorkoutStep{Name: "Work", Intensity: workout.IntensityActive, Duration: workout.DurationTime,
		Seconds: secs, Target: workout.TargetPower, TargetLow: 230, TargetHigh: 245}
}

func plannedSession(h *routeHarness, goalID, date string, zone workout.Zone, steps ...workout.WorkoutStep) {
	h.t.Helper()
	if _, err := h.store.CreateWorkout(h.t.Context(), workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "S " + date, GoalID: goalID, Date: date, Zone: zone, Steps: steps,
	}); err != nil {
		h.t.Fatal(err)
	}
}

func TestCoverageUsesTheLongestWorkStepFromTodayToTheEvent(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	slug := h.addRoute("wilant", "Demo Hills", true)
	g := h.goalFor("wilant", slug, model.SportCycling)
	warm := workout.WorkoutStep{Name: "Warmup", Intensity: workout.IntensityWarmup, Duration: workout.DurationTime, Seconds: 1500, Target: workout.TargetOpen}
	rest := workout.WorkoutStep{Name: "Recovery", Intensity: workout.IntensityRecovery, Duration: workout.DurationTime, Seconds: 900, Target: workout.TargetOpen}

	// Counts: a threshold session today (the 360 s step), and a sweet-spot one
	// with 3 x 480 s inside a repeat block, the longest single step.
	plannedSession(h, g.ID, "2026-10-05", workout.ZoneThreshold, warm, work(360))
	plannedSession(h, g.ID, "2026-10-08", workout.ZoneSweetSpot, warm,
		workout.WorkoutStep{Name: "Sets", Repeat: 3, Steps: []workout.WorkoutStep{work(480), rest}})
	// Does not count: endurance is not sweet-spot or above, however long;
	// before today; after the event; another goal's; a recovery step.
	plannedSession(h, g.ID, "2026-10-09", workout.ZoneEndurance, work(7200))
	plannedSession(h, g.ID, "2026-10-04", workout.ZoneThreshold, work(1500))
	plannedSession(h, g.ID, "2027-02-02", workout.ZoneThreshold, work(1500))
	plannedSession(h, "", "2026-10-10", workout.ZoneThreshold, work(1500))
	plannedSession(h, g.ID, "2026-10-11", workout.ZoneVO2Max, rest)

	out := h.mustDemands("wilant", g.ID)
	if out.Coverage.LongestSustainedSec != 480 {
		t.Fatalf("longest sustained effort = %.0f s, want the 480 s step in the sweet-spot session", out.Coverage.LongestSustainedSec)
	}
	uncovered := 0
	for i, c := range out.Climbs {
		want := 480 >= 0.8*c.DurationSec
		if c.Covered != want {
			t.Errorf("climb %d: covered=%v at %.0f s with a 480 s effort, want %v", i, c.Covered, c.DurationSec, want)
		}
		if !c.Covered {
			uncovered++
		}
	}
	if out.Coverage.Uncovered != uncovered {
		t.Errorf("uncovered = %d, want %d", out.Coverage.Uncovered, uncovered)
	}
	if uncovered > 0 && !strings.Contains(out.Coverage.Message, "sweet-spot") {
		t.Errorf("the message should name the plan's longest effort: %q", out.Coverage.Message)
	}
}

func TestACompletelyCoveredRouteSaysSo(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	slug := h.addRoute("wilant", "Demo Hills", true)
	g := h.goalFor("wilant", slug, model.SportCycling)
	plannedSession(h, g.ID, "2026-10-06", workout.ZoneThreshold, work(1500))

	out := h.mustDemands("wilant", g.ID)
	if out.Coverage.Uncovered != 0 || out.Coverage.Message != "Your plan trains for your route's climbs." {
		t.Errorf("coverage = %+v", out.Coverage)
	}
}

func TestARouteWithNoQualifyingClimbsHasNoMessage(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	rt, err := h.lib.Create(t.Context(), source.CreateRequest{
		Name: "Pan Flat", UploadedBy: "wilant",
		GPX: routefixture.GPX("Pan Flat", true, 25, 10, routefixture.Piece{LengthM: 6000, Grade: 0}),
	})
	if err != nil {
		t.Fatal(err)
	}
	g := h.goalFor("wilant", rt.Slug, model.SportCycling)

	out := h.mustDemands("wilant", g.ID)
	if !out.Available || len(out.Climbs) != 0 || out.Coverage.Message != "" || out.Coverage.Uncovered != 0 {
		t.Errorf("a flat route: %+v", out)
	}
}

// The response carries distances, elevations, watts and times: never a
// latitude or a longitude.
func TestRouteDemandsCarryNoCoordinates(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	slug := h.addRoute("wilant", "Demo Hills", true)
	g := h.goalFor("wilant", slug, model.SportCycling)
	_, body := h.demands("wilant", g.ID)
	assertNoCoordinates(t, body)
}

func assertNoCoordinates(t *testing.T, body []byte) {
	t.Helper()
	s := string(body)
	for _, key := range []string{`"lat"`, `"lon"`, `"lng"`, `"latitude"`, `"longitude"`, `"coordinates"`, `"points"`} {
		if strings.Contains(strings.ToLower(s), key) {
			t.Errorf("response has a %s key: %s", key, s)
		}
	}
	if strings.Contains(s, "4.5678") || strings.Contains(s, fmt.Sprintf("%.3f", routefixture.StartLon)) {
		t.Errorf("response contains the fixture's longitude: %s", s)
	}
	if regexp.MustCompile(`51\.[0-9]{3,}`).MatchString(s) {
		t.Errorf("response contains a latitude from the fixture's track: %s", s)
	}
}

func TestRouteDemandsWriteNothing(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	slug := h.addRoute("wilant", "Demo Hills", true)
	g := h.goalFor("wilant", slug, model.SportCycling)
	plannedSession(h, g.ID, "2026-10-06", workout.ZoneThreshold, work(600))

	beforeW, _ := h.store.ListWorkouts(t.Context(), "wilant")
	beforeG, _ := h.store.GetGoal(t.Context(), g.ID)
	beforeP, _, _ := h.store.GetProfile(t.Context(), "wilant")
	routes, _, _ := h.lib.List(t.Context())

	h.mustDemands("wilant", g.ID)
	h.mustDemands("wilant", g.ID)
	h.srv.WaitForBackground()

	afterW, _ := h.store.ListWorkouts(t.Context(), "wilant")
	afterG, _ := h.store.GetGoal(t.Context(), g.ID)
	afterP, _, _ := h.store.GetProfile(t.Context(), "wilant")
	routesAfter, _, _ := h.lib.List(t.Context())
	if len(afterW) != len(beforeW) || afterG != beforeG || afterP.UpdatedAt != beforeP.UpdatedAt || len(routesAfter) != len(routes) {
		t.Errorf("a read changed state: workouts %d->%d, goal %+v -> %+v", len(beforeW), len(afterW), beforeG, afterG)
	}
	if levels, _ := h.store.ListLevels(t.Context(), "wilant"); len(levels) != 0 {
		t.Errorf("a read initialised progression levels: %+v", levels)
	}
}

func TestRouteDemandsLogSlugAndOutcomeOnly(t *testing.T) {
	h := newDemandsHarness(t)
	var buf bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		// The wall-clock time would put arbitrary digits in the line, and this
		// test looks for specific numbers.
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	h.rider("wilant", 287)
	if err := h.store.SetWeight(t.Context(), "wilant", 83.4); err != nil {
		t.Fatal(err)
	}
	slug := h.addRoute("wilant", "Demo Hills", true)
	g := h.goalFor("wilant", slug, model.SportCycling)
	h.mustDemands("wilant", g.ID)

	logged := buf.String()
	if !strings.Contains(logged, "route-demands") || !strings.Contains(logged, slug) || !strings.Contains(logged, "outcome") {
		t.Errorf("expected a route-demands line with the slug and an outcome, got %q", logged)
	}
	for _, secret := range []string{"287", "83.4", "watts", "ftp", "hr=", "lat", "lon", "4.567", "51.2"} {
		if strings.Contains(strings.ToLower(logged), secret) {
			t.Errorf("the log line carries %q: %s", secret, logged)
		}
	}
}
