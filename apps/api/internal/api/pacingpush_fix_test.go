package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/pacingpush"
	"github.com/wncservices/domestique/apps/api/internal/routefixture"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestAPacingPushWithAGoalOnAnotherRouteIs422AndChangesNothing(t *testing.T) {
	h := newPacingPushHarness(t)
	h.connectGarmin("wilant")
	a := h.route("wilant", "Route A")
	b := h.route("wilant", "Route B")
	g := h.goal("wilant", a)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/routes/"+b+"/pacing/push", `{"provider":"garmin","goal":"`+g.ID+`"}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if len(h.courses.ids) != 0 || len(h.courses.deleted) != 0 {
		t.Errorf("a refused push touched the device: imported %v deleted %v", h.courses.ids, h.courses.deleted)
	}
	if _, ok, _ := h.pushes.Get(t.Context(), "wilant", "garmin", "pacing:"+g.ID); ok {
		t.Error("a refused push was recorded")
	}
	// The download is held to the same rule.
	if got := h.as("wilant", "cyclists", http.MethodGet, "/api/routes/"+b+"/pacing.fit?goal="+g.ID, "").StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("the FIT download: status = %d, want 422", got)
	}
}

// A 700 m ramp before a 2 km climb: the head unit's cue, the demands card and
// the pacing plan all call the big climb C2.
func TestTheCueOnTheHeadUnitNamesTheBigClimbC2(t *testing.T) {
	h := newPacingPushHarness(t)
	rt, err := h.db.Create(t.Context(), source.CreateRequest{
		Name: "Ramp Then Climb", UploadedBy: "wilant",
		GPX: routefixture.GPX("Ramp Then Climb", true, 25, 100,
			routefixture.Piece{LengthM: 1000, Grade: 0}, routefixture.Piece{LengthM: 700, Grade: 7},
			routefixture.Piece{LengthM: 800, Grade: -5}, routefixture.Piece{LengthM: 600, Grade: 0},
			routefixture.Piece{LengthM: 2000, Grade: 6}, routefixture.Piece{LengthM: 1000, Grade: 0}),
	})
	if err != nil {
		t.Fatal(err)
	}
	g := h.goal("wilant", rt.Slug)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/routes/"+rt.Slug+"/pacing.fit?goal="+g.ID, "")
	course := decodeCourse(t, readAll(t, resp))
	var starts, tops []string
	for _, cp := range course.CoursePoints {
		if strings.HasPrefix(cp.Name, "Top ") {
			tops = append(tops, cp.Name)
		} else {
			starts = append(starts, strings.SplitN(cp.Name, " ", 2)[0])
		}
	}
	if len(starts) != 2 || starts[0] != "C1" || starts[1] != "C2" || len(tops) != 2 || tops[1] != "Top C2" {
		t.Fatalf("cues = %v and %v, want C1, C2 and Top C1, Top C2", starts, tops)
	}

	// The demands card, on the same server, labels the same climb C2.
	var demands struct {
		Climbs []struct {
			Index       int `json:"index"`
			DeviceIndex int `json:"deviceIndex"`
		} `json:"climbs"`
	}
	if err := json.Unmarshal(readAll(t, h.as("wilant", "cyclists", http.MethodGet, "/api/training/goals/"+g.ID+"/route-demands", "")), &demands); err != nil {
		t.Fatal(err)
	}
	if len(demands.Climbs) != 1 || demands.Climbs[0].DeviceIndex != 1 {
		t.Errorf("demand climbs = %+v, want the one big climb with device index 1 (C2)", demands.Climbs)
	}
}

func newWahooPacingHarness(t *testing.T) (*wahooHarness, *pacingpush.Store, workout.Goal, string) {
	t.Helper()
	h := newWahooHarness(t, true)
	h.connect("wilant", "cyclists")
	tr, err := workout.UseDB(h.db.Conn(), h.db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	pushes, err := pacingpush.UseDB(h.db.Conn(), h.db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	h.srv.Training, h.srv.PacingPushes = tr, pushes
	if _, err := tr.SaveProfile(t.Context(), workout.RiderProfile{Rider: "wilant", FTPWatts: 250}); err != nil {
		t.Fatal(err)
	}
	rt, err := h.db.Create(t.Context(), source.CreateRequest{
		Name: "Demo Hills", UploadedBy: "wilant", GPX: routefixture.GPX("Demo Hills", true, 25, 100, routefixture.Hilly()...),
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err := tr.CreateGoal(t.Context(), workout.CreateGoalRequest{Rider: "wilant", Name: "Fondo", EventDate: "2099-01-31", RouteSlug: rt.Slug})
	if err != nil {
		t.Fatal(err)
	}
	return h, pushes, g, rt.Slug
}

// Only a route that is gone from the account is made again. Any other failure
// is reported and leaves the record, so the next push tries the same route
// instead of piling up a copy beside it.
func TestWahooUpdateFallsBackToCreateOnlyWhenTheRouteIsGone(t *testing.T) {
	h, pushes, g, slug := newWahooPacingHarness(t)
	path := "/api/routes/" + slug + "/pacing/push"
	body := `{"provider":"wahoo","goal":"` + g.ID + `"}`
	if got := h.as("wilant", "cyclists", http.MethodPost, path, body).StatusCode; got != http.StatusOK {
		t.Fatalf("first push: %d", got)
	}
	first, _, _ := pushes.Get(t.Context(), "wilant", "wahoo", "pacing:"+g.ID)

	// A server error: reported, nothing created, record kept.
	h.upstream.updateStatus = http.StatusInternalServerError
	if got := h.as("wilant", "cyclists", http.MethodPost, path, body).StatusCode; got != http.StatusBadGateway {
		t.Errorf("a 500 on update: status = %d, want 502", got)
	}
	if len(h.upstream.createdRoutes) != 1 {
		t.Errorf("a 500 on update created another route (%d)", len(h.upstream.createdRoutes))
	}
	if got, _, _ := pushes.Get(t.Context(), "wilant", "wahoo", "pacing:"+g.ID); got != first {
		t.Errorf("a failed update changed the record: %q -> %q", first, got)
	}

	// Gone from the account: made again, and the record moves to the new id.
	h.upstream.updateStatus = http.StatusNotFound
	if got := h.as("wilant", "cyclists", http.MethodPost, path, body).StatusCode; got != http.StatusOK {
		t.Fatalf("a 404 on update: status = %d, want 200", got)
	}
	if len(h.upstream.createdRoutes) != 2 {
		t.Errorf("a 404 on update created %d routes in all, want 2", len(h.upstream.createdRoutes))
	}
	if got, _, _ := pushes.Get(t.Context(), "wilant", "wahoo", "pacing:"+g.ID); got == first || got == "" {
		t.Errorf("record = %q after re-creating, want a new id", got)
	}
}

// A pacing course we pushed is not something to bring back into the library, or
// to call a duplicate: it is ours, derived from a route already there.
func TestPacingCoursesAreNotOfferedForImportOrCalledDuplicates(t *testing.T) {
	h := newPacingPushHarness(t)
	h.connectGarmin("wilant")
	slug := h.route("wilant", "Demo Hills")
	g := h.goal("wilant", slug)
	if got := h.as("wilant", "cyclists", http.MethodPost, "/api/routes/"+slug+"/pacing/push", `{"provider":"garmin","goal":"`+g.ID+`"}`).StatusCode; got != http.StatusOK {
		t.Fatalf("push: %d", got)
	}
	pushed := h.courses.ids[0]
	h.garmin.listCourses = []garmin.Course{
		{ID: pushed, Name: "Demo Hills pacing", DistanceM: 8800, ActivityType: "cycling"},
		{ID: "old", Name: "Demo Hills pacing", DistanceM: 8800, ActivityType: "cycling"}, // a stray from before we kept records
		{ID: "x1", Name: "Foo", DistanceM: 10_000, ActivityType: "cycling"},
		{ID: "x2", Name: "Foo", DistanceM: 10_000, ActivityType: "cycling"},
	}

	var list []struct {
		ID string `json:"id"`
	}
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/garmin/courses", "")
	_ = json.NewDecoder(resp.Body).Decode(&list)
	for _, c := range list {
		if c.ID == pushed {
			t.Errorf("the pacing course we pushed (%s) is offered for import: %+v", pushed, list)
		}
	}
	if len(list) != 3 {
		t.Errorf("listed %d courses, want the 3 others", len(list))
	}

	var groups []struct {
		Courses []struct {
			ID string `json:"id"`
		} `json:"courses"`
	}
	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/garmin/courses/duplicates", "")
	_ = json.NewDecoder(resp.Body).Decode(&groups)
	for _, grp := range groups {
		for _, c := range grp.Courses {
			if c.ID == pushed {
				t.Errorf("the pacing course is in a duplicate group: %+v", groups)
			}
		}
	}
	if len(groups) != 1 {
		t.Errorf("%d duplicate groups, want only the two Foo courses (the pacing pair loses the one we pushed)", len(groups))
	}

	// Asking to import it anyway brings nothing in.
	before, _, _ := h.db.List(t.Context())
	h.garmin.gpxByID = map[string][]byte{pushed: []byte(aTestGPX)}
	resp = h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/courses/import", `{"courseIds":["`+pushed+`"]}`)
	var res struct {
		Imported []string `json:"imported"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&res)
	after, _, _ := h.db.List(t.Context())
	if len(res.Imported) != 0 || len(after) != len(before) {
		t.Errorf("the pacing course was imported: %+v, library %d -> %d", res, len(before), len(after))
	}
}

func TestWahooPacingRoutesAreNotOfferedForImport(t *testing.T) {
	h, pushes, g, slug := newWahooPacingHarness(t)
	if got := h.as("wilant", "cyclists", http.MethodPost, "/api/routes/"+slug+"/pacing/push", `{"provider":"wahoo","goal":"`+g.ID+`"}`).StatusCode; got != http.StatusOK {
		t.Fatalf("push: %d", got)
	}
	id, _, _ := pushes.Get(t.Context(), "wilant", "wahoo", "pacing:"+g.ID)
	h.upstream.addRoute(map[string]any{"id": json.Number(id), "name": "Demo Hills pacing", "distance": 8800.0, "ascent": 200.0}, aTestFIT(t, "Demo Hills pacing"))
	h.upstream.addRoute(map[string]any{"id": 999, "name": "Somewhere Else", "distance": 5500.0, "ascent": 100.0}, aTestFIT(t, "Somewhere Else"))

	var list []struct {
		ID string `json:"id"`
	}
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/wahoo/routes", "")
	_ = json.NewDecoder(resp.Body).Decode(&list)
	if len(list) != 1 || list[0].ID != "999" {
		t.Errorf("wahoo routes offered = %+v, want only the one that is not ours", list)
	}
}
