package api_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/routefixture"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type pacingOut struct {
	Available  bool   `json:"available"`
	Reason     string `json:"reason"`
	ReasonCode string `json:"reasonCode"`
	Route      *struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"route"`
	GoalID      string `json:"goalId"`
	Assumptions struct {
		MassKG      float64 `json:"massKg"`
		MassAssumed bool    `json:"massAssumed"`
		CdA         float64 `json:"cdA"`
		Crr         float64 `json:"crr"`
		IF          float64 `json:"if"`
		IFSource    string  `json:"ifSource"`
		Wind        string  `json:"wind"`
	} `json:"assumptions"`
	Hint   string `json:"hint"`
	HRNote string `json:"hrNote"`
	Totals struct {
		Seconds     float64 `json:"seconds"`
		NormalizedW float64 `json:"normalizedW"`
		AvgW        float64 `json:"avgW"`
		IF          float64 `json:"if"`
		AvgKph      float64 `json:"avgKph"`
		VI          float64 `json:"variabilityIndex"`
	} `json:"totals"`
	Segments []struct {
		StartM     float64 `json:"startM"`
		EndM       float64 `json:"endM"`
		Kind       string  `json:"kind"`
		ClimbIndex *int    `json:"climbIndex"`
		Gradient   float64 `json:"gradient"`
		WattsLow   int     `json:"wattsLow"`
		WattsHigh  int     `json:"wattsHigh"`
		HRLow      int     `json:"hrLow"`
		HRHigh     int     `json:"hrHigh"`
		SpeedKph   float64 `json:"speedKph"`
		Seconds    float64 `json:"seconds"`
	} `json:"segments"`
	Climbs []struct {
		Index     int     `json:"index"`
		StartM    float64 `json:"startM"`
		EndM      float64 `json:"endM"`
		LengthM   float64 `json:"lengthM"`
		Gradient  float64 `json:"avgGradient"`
		Watts     float64 `json:"watts"`
		WattsLow  int     `json:"wattsLow"`
		WattsHigh int     `json:"wattsHigh"`
		Seconds   float64 `json:"seconds"`
	} `json:"climbs"`
}

func (h *routeHarness) pacing(user, slug, query string) (*http.Response, []byte) {
	h.t.Helper()
	path := "/api/routes/" + slug + "/pacing"
	if query != "" {
		path += "?" + query
	}
	resp := h.as(user, "cyclists", http.MethodGet, path, "")
	return resp, readAll(h.t, resp)
}

func (h *routeHarness) mustPacing(user, slug, query string) pacingOut {
	h.t.Helper()
	resp, body := h.pacing(user, slug, query)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	var out pacingOut
	if err := json.Unmarshal(body, &out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func TestPacingPlanForARoute(t *testing.T) {
	h := newDemandsHarness(t)
	// FTP alone: no max HR and no threshold HR.
	if _, err := h.store.SaveProfile(t.Context(), workout.RiderProfile{Rider: "wilant", FTPWatts: 250}); err != nil {
		t.Fatal(err)
	}
	slug := h.addRoute("wilant", "Demo Hills", true)

	out := h.mustPacing("wilant", slug, "")
	if !out.Available || out.Route == nil || out.Route.Slug != slug {
		t.Fatalf("response = %+v", out)
	}
	a := out.Assumptions
	if a.MassKG != 75 || !a.MassAssumed || a.CdA != 0.32 || a.Crr != 0.005 || a.Wind != "none" || a.IFSource != "derived" {
		t.Errorf("assumptions = %+v", a)
	}
	if !strings.Contains(out.Hint, "75 kg") {
		t.Errorf("an assumed weight must say so: %q", out.Hint)
	}
	// The route is under two hours, so the short-event band applies.
	if a.IF != 0.95 {
		t.Errorf("derived IF = %v, want 0.95 for a short event", a.IF)
	}
	if out.Totals.Seconds < 600 || out.Totals.Seconds > 1800 {
		t.Errorf("an 8.8 km hilly route took %.0f s", out.Totals.Seconds)
	}
	if math.Abs(out.Totals.NormalizedW-250*0.95) > 0.01*250*0.95 || math.Abs(out.Totals.IF-0.95) > 0.01 {
		t.Errorf("totals = %+v, want NP near %.1f", out.Totals, 250*0.95)
	}
	if out.Totals.AvgKph <= 0 || out.Totals.VI < 1 {
		t.Errorf("totals = %+v", out.Totals)
	}
	if n := len(out.Segments); n < 4 || n > 14 {
		t.Errorf("%d segments, want a short readable table", n)
	}
	kinds := map[string]int{}
	for i, s := range out.Segments {
		kinds[s.Kind]++
		if s.EndM <= s.StartM || s.WattsLow > s.WattsHigh || s.WattsLow%5 != 0 || s.Seconds <= 0 {
			t.Errorf("segment %d malformed: %+v", i, s)
		}
		if (s.Kind == "climb") != (s.ClimbIndex != nil) {
			t.Errorf("segment %d: kind %s with climbIndex %v", i, s.Kind, s.ClimbIndex)
		}
	}
	if kinds["climb"] < 2 || kinds["descent"] < 1 || kinds["flat"] < 1 {
		t.Errorf("kinds = %v", kinds)
	}
	if len(out.Climbs) < 2 {
		t.Errorf("climbs = %+v", out.Climbs)
	}
	// No HR data on this rider's profile with FTP alone: none is invented.
	for _, s := range out.Segments {
		if s.HRLow != 0 || s.HRHigh != 0 {
			t.Errorf("HR %d-%d for a rider without HR data", s.HRLow, s.HRHigh)
		}
	}
	if out.HRNote != "" {
		t.Errorf("an HR note without HR ranges: %q", out.HRNote)
	}
}

func TestPacingUsesTheRidersWeightAndHRWhenKnown(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250) // sets max HR and LTHR as well
	if err := h.store.SetWeight(t.Context(), "wilant", 62); err != nil {
		t.Fatal(err)
	}
	slug := h.addRoute("wilant", "Demo Hills", true)
	light := h.mustPacing("wilant", slug, "")
	if light.Assumptions.MassKG != 62 || light.Assumptions.MassAssumed || light.Hint != "" {
		t.Errorf("assumptions = %+v hint %q", light.Assumptions, light.Hint)
	}
	if !strings.Contains(light.HRNote, "HR lags") {
		t.Errorf("HR ranges need the lag note, got %q", light.HRNote)
	}
	withHR := 0
	for _, s := range light.Segments {
		if s.HRLow > 0 && s.HRHigh >= s.HRLow {
			withHR++
		}
	}
	if withHR != len(light.Segments) {
		t.Errorf("%d of %d segments have HR ranges", withHR, len(light.Segments))
	}

	h.rider("other", 250)
	slug2 := h.addRoute("other", "Other Hills", true)
	heavy := h.mustPacing("other", slug2, "")
	if light.Totals.Seconds >= heavy.Totals.Seconds {
		t.Errorf("62 kg took %.0f s, 75 kg took %.0f s", light.Totals.Seconds, heavy.Totals.Seconds)
	}
}

func TestAGoalsPacingIFOverridesTheDerivedOne(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	slug := h.addRoute("wilant", "Demo Hills", true)
	g, err := h.store.CreateGoal(t.Context(), workout.CreateGoalRequest{
		Rider: "wilant", Name: "Fondo", Sport: model.SportCycling, EventDate: "2027-01-31", RouteSlug: slug, PacingIF: 0.7,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := h.mustPacing("wilant", slug, "goal="+g.ID)
	if out.Assumptions.IF != 0.7 || out.Assumptions.IFSource != "goal" || out.GoalID != g.ID {
		t.Errorf("assumptions = %+v goal %q", out.Assumptions, out.GoalID)
	}
	if math.Abs(out.Totals.NormalizedW-250*0.7) > 0.01*250*0.7 {
		t.Errorf("NP = %.1f, want about %.1f", out.Totals.NormalizedW, 250*0.7)
	}
	derived := h.mustPacing("wilant", slug, "")
	if out.Totals.Seconds <= derived.Totals.Seconds {
		t.Errorf("riding at 0.70 (%.0f s) should be slower than 0.95 (%.0f s)", out.Totals.Seconds, derived.Totals.Seconds)
	}
}

// Pass 1 rides the whole route at 0.85 FTP and takes the band of that duration;
// pass 2 is built at that band's IF and reported as it is, never re-banded.
func TestTheTwoPassIFPicksTheBandOfPassOneAndDoesNotFlip(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	cases := []struct {
		name   string
		lenM   float64
		wantIF float64
	}{
		{"60 km is a short event", 60_000, 0.95},
		{"100 km is a medium one", 100_000, 0.85},
		{"150 km is a long one", 150_000, 0.75},
	}
	for _, c := range cases {
		rt, err := h.lib.Create(t.Context(), source.CreateRequest{
			Name: c.name, UploadedBy: "wilant",
			GPX: routefixture.GPX(c.name, true, 100, 50, routefixture.Piece{LengthM: c.lenM, Grade: 0}),
		})
		if err != nil {
			t.Fatal(err)
		}
		out := h.mustPacing("wilant", rt.Slug, "")
		if out.Assumptions.IF != c.wantIF || math.Abs(out.Totals.IF-c.wantIF) > 0.005 {
			t.Errorf("%s: IF %v (plan %.3f), want %v", c.name, out.Assumptions.IF, out.Totals.IF, c.wantIF)
		}
	}
}

func TestPacingAccessRules(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	slug := h.addRoute("wilant", "Demo Hills", true)
	g := h.goalFor("wilant", slug, model.SportCycling)

	// Another rider cannot see a private route, and gets the 404 a missing one
	// gets.
	resp, body := h.pacing("friend", slug, "")
	resp2, body2 := h.pacing("friend", "no-such-route", "")
	if resp.StatusCode != http.StatusNotFound || resp2.StatusCode != http.StatusNotFound || string(body) != string(body2) {
		t.Errorf("invisible route: %d %s / missing: %d %s", resp.StatusCode, body, resp2.StatusCode, body2)
	}
	// A goal that is not the rider's is a 404 as well, even on a route they own.
	h.rider("friend", 250)
	own := h.addRoute("friend", "Friend Hills", true)
	if resp, _ := h.pacing("friend", own, "goal="+g.ID); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another rider's goal: status = %d, want 404", resp.StatusCode)
	}
	if resp, _ := h.pacing("friend", own, "goal=no-such-goal"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a goal that does not exist: status = %d, want 404", resp.StatusCode)
	}
	if got := h.as("wilant", "guests", http.MethodGet, "/api/routes/"+slug+"/pacing", "").StatusCode; got != http.StatusOK && got != http.StatusPreconditionFailed {
		// A viewer may read routes; with no FTP of their own they get an
		// unavailable plan, not an error.
		t.Errorf("a viewer: status = %d", got)
	}
}

func TestPacingIsUnavailableWithAReason(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *routeHarness) string
		ftp   float64
		code  string
		hint  string
	}{
		{"no elevation", func(h *routeHarness) string { return h.addRoute("wilant", "Flat Nowhere", false) }, 250, "no_elevation", "Recalculate"},
		{"no FTP", func(h *routeHarness) string { return h.addRoute("wilant", "Demo Hills", true) }, 0, "no_ftp", "FTP"},
		{"a running route", func(h *routeHarness) string {
			rt, err := h.lib.Create(t.Context(), source.CreateRequest{
				Name: "Trail Run", UploadedBy: "wilant", Sport: model.SportRunning,
				GPX: routefixture.GPX("Trail Run", true, 25, 100, routefixture.Hilly()...),
			})
			if err != nil {
				t.Fatal(err)
			}
			return rt.Slug
		}, 250, "not_cycling", "cycling"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newDemandsHarness(t)
			if c.ftp > 0 {
				h.rider("wilant", c.ftp)
			}
			slug := c.setup(h)
			resp, body := h.pacing("wilant", slug, "")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d: %s", resp.StatusCode, body)
			}
			var out pacingOut
			_ = json.Unmarshal(body, &out)
			if out.Available || out.ReasonCode != c.code || !strings.Contains(out.Reason, c.hint) || len(out.Segments) != 0 {
				t.Errorf("response = %s", body)
			}
		})
	}
}

func TestPacingCarriesNoCoordinatesAndWritesNothing(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	slug := h.addRoute("wilant", "Demo Hills", true)
	g := h.goalFor("wilant", slug, model.SportCycling)

	beforeW, _ := h.store.ListWorkouts(t.Context(), "wilant")
	beforeG, _ := h.store.GetGoal(t.Context(), g.ID)
	_, body := h.pacing("wilant", slug, "goal="+g.ID)
	assertNoCoordinates(t, body)
	h.srv.WaitForBackground()
	afterW, _ := h.store.ListWorkouts(t.Context(), "wilant")
	afterG, _ := h.store.GetGoal(t.Context(), g.ID)
	if len(beforeW) != len(afterW) || beforeG != afterG {
		t.Error("reading the pacing plan changed state")
	}
}

func TestPacingLogsSlugAndOutcomeOnly(t *testing.T) {
	h := newDemandsHarness(t)
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
	h.rider("wilant", 287)
	if err := h.store.SetWeight(t.Context(), "wilant", 83.4); err != nil {
		t.Fatal(err)
	}
	slug := h.addRoute("wilant", "Demo Hills", true)
	h.mustPacing("wilant", slug, "")

	logged := strings.ToLower(buf.String())
	if !strings.Contains(logged, "pacing") || !strings.Contains(logged, slug) || !strings.Contains(logged, "outcome") {
		t.Fatalf("expected a pacing line with slug and outcome, got %q", logged)
	}
	for _, secret := range []string{"287", "83.4", "watts", "4.567", "51.2", "lat=", "lon="} {
		if strings.Contains(logged, secret) {
			t.Errorf("the log carries %q: %s", secret, logged)
		}
	}
}

// The demands card and the pacing plan are one calculation: a climb's target
// on the goal's card is the plan's target for that climb, not a second guess.
func TestRouteDemandsReadTheSameClimbWattsAsThePacingPlan(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	slug := h.addRoute("wilant", "Demo Hills", true)
	g := h.goalFor("wilant", slug, model.SportCycling)

	demands := h.mustDemands("wilant", g.ID)
	plan := h.mustPacing("wilant", slug, "goal="+g.ID)
	if len(demands.Climbs) == 0 {
		t.Fatal("no demand climbs")
	}
	for _, d := range demands.Climbs {
		matched := false
		for _, p := range plan.Climbs {
			if math.Abs(p.StartM-d.StartM) <= 1 && math.Abs(p.EndM-d.EndM) <= 1 {
				matched = true
				if math.Abs(p.Watts-d.Watts) > 0.5 || math.Abs(p.Seconds-d.DurationSec) > 1 {
					t.Errorf("climb at %.0f m: demands say %.0f W / %.0f s, the plan %.0f W / %.0f s", d.StartM, d.Watts, d.DurationSec, p.Watts, p.Seconds)
				}
			}
		}
		if !matched {
			t.Errorf("demand climb at %.0f m has no counterpart in the plan: %+v", d.StartM, plan.Climbs)
		}
	}
	// The same intensity too.
	if joined := strings.Join(demands.Assumptions, " "); !strings.Contains(joined, "0.95") {
		t.Errorf("assumptions = %q", joined)
	}
}
