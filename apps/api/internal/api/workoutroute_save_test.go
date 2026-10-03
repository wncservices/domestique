package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/routeshare"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func withShares(h *wrHarness, srv *api.Server) {
	st, err := routeshare.UseDB(h.db.Conn(), h.db.DSN())
	if err != nil {
		h.t.Fatal(err)
	}
	srv.Shares = st
}

type workoutOut struct {
	ID    string `json:"id"`
	Route *struct {
		Slug             string  `json:"slug"`
		Name             string  `json:"name"`
		DistanceM        float64 `json:"distanceM"`
		AscentM          float64 `json:"ascentM"`
		EstimatedSeconds float64 `json:"estimatedSeconds"`
		Generated        bool    `json:"generated"`
		Inactive         bool    `json:"inactive"`
	} `json:"route"`
}

func (h *wrHarness) getWorkout(rider, id string) (workoutOut, string) {
	h.t.Helper()
	resp := h.as(rider, http.MethodGet, "/api/training/workouts/"+id, "")
	raw := h.body(resp)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("GET workout: %d %s", resp.StatusCode, raw)
	}
	var out workoutOut
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		h.t.Fatal(err)
	}
	return out, raw
}

// pick generates candidates for the workout and returns the first one's id.
func (h *wrHarness) pick(rider string, wk workout.Workout) candidateOut {
	h.t.Helper()
	out := h.decodeCandidates(h.candidatesFor(rider, wk.ID, "", ""))
	return out.Candidates[0]
}

func (h *wrHarness) saveRoute(rider, workoutID, candidateID string) *http.Response {
	h.t.Helper()
	return h.as(rider, http.MethodPost, "/api/training/workouts/"+workoutID+"/route", `{"candidateId":"`+candidateID+`"}`)
}

func (h *wrHarness) routeBySlug(slug string) (model.Route, bool) {
	h.t.Helper()
	routes, _, err := h.db.List(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, r := range routes {
		if r.Slug == slug {
			return r, true
		}
	}
	return model.Route{}, false
}

func TestSavingACandidateCreatesAnOwnerOnlyTaggedUntargetedRoute(t *testing.T) {
	h, wk := readyHarness(t)
	c := h.pick("wilant", wk)
	resp := h.saveRoute("wilant", wk.ID, c.ID)
	raw := h.body(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save: %d %s", resp.StatusCode, raw)
	}
	var out workoutOut
	_ = json.Unmarshal([]byte(raw), &out)
	if out.Route == nil || !out.Route.Generated {
		t.Fatalf("workout carries no generated route: %s", raw)
	}
	rt, ok := h.routeBySlug(out.Route.Slug)
	if !ok {
		t.Fatal("the route is not in the library")
	}
	if rt.Owner != "wilant" || rt.Targets != nil {
		t.Errorf("owner %q targets %v, want the rider and no targets: owner-only", rt.Owner, rt.Targets)
	}
	if len(rt.Tags) != 1 || rt.Tags[0] != "wroute:"+wk.ID {
		t.Errorf("tags = %v, want wroute:<workoutId>", rt.Tags)
	}
	if !regexp.MustCompile(`^Endurance ride loop, \d+ km$`).MatchString(rt.Name) {
		t.Errorf("name = %q", rt.Name)
	}
	if out.Route.EstimatedSeconds != c.EstimatedSeconds {
		t.Errorf("route seconds = %v, want the estimate when chosen %v", out.Route.EstimatedSeconds, c.EstimatedSeconds)
	}

	// The GPX is the server's own path, elevation and all.
	raw2, err := h.db.GPX(context.Background(), rt.Slug)
	if err != nil {
		t.Fatal(err)
	}
	pts, err := gpx.ParsePoints(raw2)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != len(c.Points) || !pts[len(pts)/2].HasEle {
		t.Errorf("saved %d points (elevation %v), want the held path's %d with elevation", len(pts), pts[len(pts)/2].HasEle, len(c.Points))
	}
}

func TestSavingNeverReadsPostedCoordinates(t *testing.T) {
	h, wk := readyHarness(t)
	c := h.pick("wilant", wk)
	resp := h.as("wilant", http.MethodPost, "/api/training/workouts/"+wk.ID+"/route",
		`{"candidateId":"`+c.ID+`","points":[[1,1],[2,2],[3,3]],"gpx":"<gpx/>"}`)
	var out workoutOut
	_ = json.Unmarshal([]byte(h.body(resp)), &out)
	rt, ok := h.routeBySlug(out.Route.Slug)
	if !ok {
		t.Fatal("no route")
	}
	if rt.Stats.StartLat < 47 || rt.Stats.StartLat > 48 {
		t.Errorf("route starts at %v: posted coordinates were used", rt.Stats.StartLat)
	}
}

func TestSavingAnUnknownOrForeignCandidateIs410(t *testing.T) {
	h, wk := readyHarness(t)
	other := h.enduranceWorkout("wilant", "2026-10-05")
	c := h.pick("wilant", wk)

	if resp := h.saveRoute("wilant", wk.ID, "deadbeef"); resp.StatusCode != http.StatusGone {
		t.Errorf("unknown id: status %d, want 410", resp.StatusCode)
	}
	if resp := h.saveRoute("wilant", other.ID, c.ID); resp.StatusCode != http.StatusGone {
		t.Errorf("a candidate made for another workout: status %d, want 410", resp.StatusCode)
	}
	h.setStart("marie")
	if resp := h.saveRoute("marie", wk.ID, c.ID); resp.StatusCode != http.StatusNotFound {
		t.Errorf("someone else's workout: status %d, want 404", resp.StatusCode)
	}
	h.setProfile("marie", 200, 70)
	mine := h.enduranceWorkout("marie", wrFuture)
	if resp := h.saveRoute("marie", mine.ID, c.ID); resp.StatusCode != http.StatusGone {
		t.Errorf("another rider's candidate id: status %d, want 410", resp.StatusCode)
	}
}

func TestSavingTwiceThenChoosingAgainReplacesOnlyItsOwnRoute(t *testing.T) {
	h, wk := readyHarness(t)
	first := h.saveRoute("wilant", wk.ID, h.pick("wilant", wk).ID)
	var a workoutOut
	_ = json.Unmarshal([]byte(h.body(first)), &a)

	second := h.saveRoute("wilant", wk.ID, h.pick("wilant", wk).ID)
	var b workoutOut
	_ = json.Unmarshal([]byte(h.body(second)), &b)
	if a.Route.Slug == b.Route.Slug {
		t.Fatal("choosing again made no new route")
	}
	if _, ok := h.routeBySlug(a.Route.Slug); ok {
		t.Error("the replaced loop is still in the library")
	}
	if _, ok := h.routeBySlug(b.Route.Slug); !ok {
		t.Error("the new loop is missing")
	}
}

func TestReplacingKeepsAGeneratedRouteAnotherWorkoutLinks(t *testing.T) {
	h, wk := readyHarness(t)
	var a workoutOut
	_ = json.Unmarshal([]byte(h.body(h.saveRoute("wilant", wk.ID, h.pick("wilant", wk).ID))), &a)

	other := h.enduranceWorkout("wilant", "2026-10-06")
	slug, secs := a.Route.Slug, 7000.0
	if _, err := h.training.UpdateWorkout(context.Background(), other.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs}); err != nil {
		t.Fatal(err)
	}
	h.saveRoute("wilant", wk.ID, h.pick("wilant", wk).ID)
	if _, ok := h.routeBySlug(slug); !ok {
		t.Error("a loop another workout links was deleted")
	}
}

func TestRemovingUnlinksAndDeletesAGeneratedRouteButOnlyUnlinksALibraryRoute(t *testing.T) {
	h, wk := readyHarness(t)
	var a workoutOut
	_ = json.Unmarshal([]byte(h.body(h.saveRoute("wilant", wk.ID, h.pick("wilant", wk).ID))), &a)

	resp := h.as("wilant", http.MethodDelete, "/api/training/workouts/"+wk.ID+"/route", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: %d", resp.StatusCode)
	}
	if got, _ := h.getWorkout("wilant", wk.ID); got.Route != nil {
		t.Error("the route is still linked")
	}
	if _, ok := h.routeBySlug(a.Route.Slug); ok {
		t.Error("the generated loop survived removal")
	}
	if resp := h.as("wilant", http.MethodDelete, "/api/training/workouts/"+wk.ID+"/route", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("a second DELETE: %d, want idempotent", resp.StatusCode)
	}

	// A library route is only unlinked.
	lib, err := h.db.Create(context.Background(), sourceCreate("Sunday loop", "wilant"))
	if err != nil {
		t.Fatal(err)
	}
	slug, secs := lib.Slug, 5000.0
	_, _ = h.training.UpdateWorkout(context.Background(), wk.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs})
	h.as("wilant", http.MethodDelete, "/api/training/workouts/"+wk.ID+"/route", "")
	if _, ok := h.routeBySlug(lib.Slug); !ok {
		t.Error("a library route was deleted by unlinking it")
	}
}

func TestAGeneratedRouteRefusesShareLinksUntilTheTagIsRemoved(t *testing.T) {
	h, wk := readyHarness(t, withShares)
	var a workoutOut
	_ = json.Unmarshal([]byte(h.body(h.saveRoute("wilant", wk.ID, h.pick("wilant", wk).ID))), &a)

	path := "/api/routes/" + a.Route.Slug + "/shares"
	if resp := h.as("wilant", http.MethodPost, path, `{"ttlDays":7}`); resp.StatusCode != http.StatusConflict {
		t.Fatalf("share of a tagged route: %d, want 409", resp.StatusCode)
	}
	if resp := h.as("wilant", http.MethodPatch, "/api/routes/"+a.Route.Slug, `{"tags":[]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("removing the tag: %d", resp.StatusCode)
	}
	if resp := h.as("wilant", http.MethodPost, path, `{"ttlDays":7}`); resp.StatusCode != http.StatusCreated {
		t.Errorf("share after the tag is gone: %d, want 201", resp.StatusCode)
	}
}

func TestAGeneratedRouteRefusesCrewTargetsUntilTheTagIsRemoved(t *testing.T) {
	h, wk := readyHarness(t)
	var a workoutOut
	_ = json.Unmarshal([]byte(h.body(h.saveRoute("wilant", wk.ID, h.pick("wilant", wk).ID))), &a)
	url := "/api/routes/" + a.Route.Slug

	if resp := h.as("wilant", http.MethodPatch, url, `{"targets":["some-crew"]}`); resp.StatusCode != http.StatusConflict {
		t.Fatalf("crew target on a tagged route: %d, want 409", resp.StatusCode)
	}
	// Removing the tag in the same request is deliberate: it gets past the
	// guard (and then fails only because the crew does not exist).
	resp := h.as("wilant", http.MethodPatch, url, `{"tags":[],"targets":["some-crew"]}`)
	if resp.StatusCode == http.StatusConflict {
		t.Errorf("removing the tag in the same request still hit the guard: %s", h.body(resp))
	}
	// Clearing targets is never refused.
	if resp := h.as("wilant", http.MethodPatch, url, `{"targets":[]}`); resp.StatusCode != http.StatusOK {
		t.Errorf("clearing targets: %d", resp.StatusCode)
	}
}

func TestWorkoutDTOCarriesNoCoordinatesAndOnlyAVisibleRoute(t *testing.T) {
	h, wk := readyHarness(t)
	h.saveRoute("wilant", wk.ID, h.pick("wilant", wk).ID)
	got, raw := h.getWorkout("wilant", wk.ID)
	if got.Route == nil {
		t.Fatal("no route")
	}
	for _, leak := range []string{"47.37", "8.54", "lat", "lon", "Ghent", "points"} {
		if strings.Contains(raw, leak) {
			t.Errorf("workout response leaks %q: %s", leak, raw)
		}
	}

	// A link to a route the rider cannot see is omitted, not confirmed.
	foreign, err := h.db.Create(context.Background(), sourceCreate("Marie's loop", "marie"))
	if err != nil {
		t.Fatal(err)
	}
	slug, secs := foreign.Slug, 3600.0
	_, _ = h.training.UpdateWorkout(context.Background(), wk.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs})
	if got, raw := h.getWorkout("wilant", wk.ID); got.Route != nil || strings.Contains(raw, "Marie") {
		t.Errorf("another rider's route shows on the workout: %s", raw)
	}
}

func TestIndoorKeepsTheLinkButMarksItInactive(t *testing.T) {
	h, wk := readyHarness(t)
	h.saveRoute("wilant", wk.ID, h.pick("wilant", wk).ID)
	yes := true
	_, _ = h.training.UpdateWorkout(context.Background(), wk.ID, workout.UpdateWorkoutRequest{Indoor: &yes})
	got, _ := h.getWorkout("wilant", wk.ID)
	if got.Route == nil || !got.Route.Inactive {
		t.Errorf("route = %+v, want kept and inactive while indoor", got.Route)
	}
	no := false
	_, _ = h.training.UpdateWorkout(context.Background(), wk.ID, workout.UpdateWorkoutRequest{Indoor: &no})
	if got, _ := h.getWorkout("wilant", wk.ID); got.Route == nil || got.Route.Inactive {
		t.Errorf("route = %+v, want active again once outdoor", got.Route)
	}
}

func TestDeletingALibraryRouteClearsEveryWorkoutLink(t *testing.T) {
	h, wk := readyHarness(t)
	lib, _ := h.db.Create(context.Background(), sourceCreate("Sunday loop", "wilant"))
	slug, secs := lib.Slug, 5000.0
	_, _ = h.training.UpdateWorkout(context.Background(), wk.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs})

	if resp := h.as("wilant", http.MethodDelete, "/api/routes/"+lib.Slug, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete route: %d", resp.StatusCode)
	}
	w, err := h.training.GetWorkout(context.Background(), wk.ID)
	if err != nil || w.RouteSlug != "" || w.RouteSeconds != 0 {
		t.Errorf("workout still links %q / %v after the route was deleted (err %v)", w.RouteSlug, w.RouteSeconds, err)
	}
}

func TestSavingIsRefusedForARideThatCannotTakeARoute(t *testing.T) {
	h, wk := readyHarness(t)
	c := h.pick("wilant", wk)
	yes := true
	_, _ = h.training.UpdateWorkout(context.Background(), wk.ID, workout.UpdateWorkoutRequest{Indoor: &yes})
	if resp := h.saveRoute("wilant", wk.ID, c.ID); resp.StatusCode != http.StatusConflict {
		t.Errorf("save on an indoor ride: %d, want 409", resp.StatusCode)
	}
}
