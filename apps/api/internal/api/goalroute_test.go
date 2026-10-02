package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/routefixture"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// routeHarness is a training harness with a route library behind it, so a
// goal can name a route. Every route is synthetic (routefixture): none is a
// real place.
type routeHarness struct {
	*trainingHarness
	lib *source.DB
}

func newRouteHarness(t *testing.T) *routeHarness {
	t.Helper()
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	trainingStore, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.New(auth.Config{
		Mode:  auth.ModeProxy,
		Roles: auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}, Viewer: []string{"guests"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := &api.Server{Auth: authenticator, Training: trainingStore, Source: db}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	return &routeHarness{
		trainingHarness: &trainingHarness{t: t, client: server.Client(), base: server.URL, store: trainingStore, srv: srv, conn: db.Conn()},
		lib:             db,
	}
}

// addRoute puts a synthetic hilly route in the library, owned by owner.
func (h *routeHarness) addRoute(owner, name string, withEle bool) string {
	h.t.Helper()
	rt, err := h.lib.Create(h.t.Context(), source.CreateRequest{
		Name: name, UploadedBy: owner,
		GPX: routefixture.GPX(name, withEle, 25, 100, routefixture.Hilly()...),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return rt.Slug
}

func TestAGoalCanNameARouteTheRiderCanSee(t *testing.T) {
	h := newRouteHarness(t)
	slug := h.addRoute("wilant", "Demo Hills", true)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals",
		`{"name":"Hilly Fondo","eventDate":"2099-06-01","routeSlug":"`+slug+`","pacingIf":0.8}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var created struct {
		ID, RouteSlug string
		PacingIF      float64
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	if created.RouteSlug != slug || created.PacingIF != 0.8 {
		t.Fatalf("goal = %+v, want route %q and IF 0.8", created, slug)
	}

	// An update that omits routeSlug keeps it; an empty one clears it.
	resp = h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+created.ID, `{"notes":"hello"}`)
	_ = json.NewDecoder(resp.Body).Decode(&created)
	if resp.StatusCode != http.StatusOK || created.RouteSlug != slug {
		t.Fatalf("an unrelated update: status %d, route %q; want the link kept", resp.StatusCode, created.RouteSlug)
	}
	resp = h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+created.ID, `{"routeSlug":"","pacingIf":0}`)
	var cleared struct {
		RouteSlug string
		PacingIF  float64
	}
	_ = json.NewDecoder(resp.Body).Decode(&cleared)
	if resp.StatusCode != http.StatusOK || cleared.RouteSlug != "" || cleared.PacingIF != 0 {
		t.Fatalf("clearing: status %d, %+v", resp.StatusCode, cleared)
	}
}

func TestAGoalCannotNameARouteTheRiderCannotSee(t *testing.T) {
	h := newRouteHarness(t)
	private := h.addRoute("someone-else", "Their Hills", true)

	for name, slug := range map[string]string{"another rider's private route": private, "an unknown slug": "no-such-route"} {
		resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals",
			`{"name":"Fondo","routeSlug":"`+slug+`"}`)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", name, resp.StatusCode)
		}
		body := string(readAll(t, resp))
		// The two answers must be indistinguishable, and say nothing about
		// the route: not its name, not that it exists.
		if strings.Contains(body, "Their Hills") || strings.Contains(body, "someone-else") || strings.Contains(body, private) {
			t.Errorf("%s: the refusal describes the route: %s", name, body)
		}
	}
	a := string(readAll(t, h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"Fondo","routeSlug":"`+private+`"}`)))
	b := string(readAll(t, h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"Fondo","routeSlug":"no-such-route"}`)))
	if a != b {
		t.Errorf("a private route and a missing one answer differently:\n%s\n%s", a, b)
	}
	goals, _ := h.store.ListGoals(t.Context(), "wilant")
	if len(goals) != 0 {
		t.Errorf("a refused request still created %d goals", len(goals))
	}

	// An update is held to the same rule.
	ok := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"Plain"}`)
	var g goalDTOOut
	_ = json.NewDecoder(ok.Body).Decode(&g)
	resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+g.ID, `{"routeSlug":"`+private+`"}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("update to a private route: status = %d, want 422", resp.StatusCode)
	}
}

func TestAGoalCanNameARouteSharedWithTheirCrewOnlyViaVisibility(t *testing.T) {
	// A route with no explicit targets is visible to its owner alone, so
	// another rider is refused (covered above); the owner is not.
	h := newRouteHarness(t)
	own := h.addRoute("wilant", "Own Hills", true)
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"Own","routeSlug":"`+own+`"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("owner's own route: status = %d, want 201", resp.StatusCode)
	}
	resp = h.as("friend", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"Theirs","routeSlug":"`+own+`"}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("another rider naming it: status = %d, want 422", resp.StatusCode)
	}
}

func TestPacingIFMustBeZeroOrInRange(t *testing.T) {
	h := newRouteHarness(t)
	for body, want := range map[string]int{
		`{"name":"a","pacingIf":0}`:     http.StatusCreated,
		`{"name":"b","pacingIf":0.6}`:   http.StatusCreated,
		`{"name":"c","pacingIf":1.05}`:  http.StatusCreated,
		`{"name":"d","pacingIf":0.59}`:  http.StatusUnprocessableEntity,
		`{"name":"e","pacingIf":1.06}`:  http.StatusUnprocessableEntity,
		`{"name":"f","pacingIf":-0.8}`:  http.StatusUnprocessableEntity,
		`{"name":"g","pacingIf":0.001}`: http.StatusUnprocessableEntity,
	} {
		if got := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", body).StatusCode; got != want {
			t.Errorf("%s: status = %d, want %d", body, got, want)
		}
	}
	ok := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"h"}`)
	var g goalDTOOut
	_ = json.NewDecoder(ok.Body).Decode(&g)
	if got := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+g.ID, `{"pacingIf":2}`).StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("update pacingIf 2: status = %d, want 422", got)
	}
}

func TestARunningGoalCannotCarryARoute(t *testing.T) {
	h := newRouteHarness(t)
	slug := h.addRoute("wilant", "Demo Hills", true)
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals",
		`{"name":"10k","sport":"running","routeSlug":"`+slug+`"}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("running goal with a route: status = %d, want 422", resp.StatusCode)
	}
	// And a cycling goal that has one cannot become a running goal.
	ok := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"Ride","routeSlug":"`+slug+`"}`)
	var g goalDTOOut
	_ = json.NewDecoder(ok.Body).Decode(&g)
	resp = h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+g.ID, `{"sport":"running"}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("switching a routed goal to running: status = %d, want 422", resp.StatusCode)
	}
}

func TestProfileWeightIsValidatedAndKept(t *testing.T) {
	h := newRouteHarness(t)
	put := func(body string) *http.Response {
		return h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", body)
	}
	get := func() map[string]any {
		var m map[string]any
		_ = json.NewDecoder(h.as("wilant", "cyclists", http.MethodGet, "/api/training/profile", "").Body).Decode(&m)
		return m
	}

	for body, want := range map[string]int{
		`{"ftpWatts":250,"weightKg":82.5}`:  http.StatusOK,
		`{"ftpWatts":250,"weightKg":29.9}`:  http.StatusUnprocessableEntity,
		`{"ftpWatts":250,"weightKg":250.1}`: http.StatusUnprocessableEntity,
		`{"ftpWatts":250,"weightKg":-70}`:   http.StatusUnprocessableEntity,
	} {
		if got := put(body).StatusCode; got != want {
			t.Errorf("%s: status = %d, want %d", body, got, want)
		}
	}
	if got := get()["weightKg"]; got != 82.5 {
		t.Fatalf("weight after the refusals = %v, want the 82.5 saved first", got)
	}

	// A save from a form that does not mention weight keeps it.
	if resp := put(`{"ftpWatts":260}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("save without weight: %d", resp.StatusCode)
	}
	if got := get()["weightKg"]; got != 82.5 {
		t.Errorf("weight after a save that omitted it = %v, want 82.5", got)
	}
	// Zero clears.
	put(`{"ftpWatts":260,"weightKg":0}`)
	if _, present := get()["weightKg"]; present {
		t.Error("a cleared weight is still reported")
	}
	// The limits themselves are accepted.
	for _, kg := range []string{"30", "250"} {
		if got := put(`{"ftpWatts":260,"weightKg":` + kg + `}`).StatusCode; got != http.StatusOK {
			t.Errorf("weight %s: status = %d, want 200", kg, got)
		}
	}
}
