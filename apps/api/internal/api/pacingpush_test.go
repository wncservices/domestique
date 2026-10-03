package api_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/wncservices/domestique/apps/api/internal/pacingpush"
	"github.com/wncservices/domestique/apps/api/internal/routefixture"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// idCourses is a Garmin that hands out a new course id for every import, so a
// test can tell a replacement from a duplicate.
type idCourses struct {
	mu       sync.Mutex
	files    []string
	data     [][]byte
	ids      []string
	deleted  []string
	failNext bool
}

func (c *idCourses) ImportCourse(_ context.Context, filename string, data []byte) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failNext {
		c.failNext = false
		return "", fmt.Errorf("garmin said no")
	}
	id := fmt.Sprintf("garmin-course-%d", len(c.ids)+1)
	c.files, c.data, c.ids = append(c.files, filename), append(c.data, data), append(c.ids, id)
	return id, nil
}

func (c *idCourses) DeleteCourse(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleted = append(c.deleted, id)
	return nil
}

func decodeCourse(t *testing.T, raw []byte) *filedef.Course {
	t.Helper()
	fit, err := decoder.New(bytes.NewReader(raw)).Decode()
	if err != nil {
		t.Fatalf("the FIT does not decode: %v", err)
	}
	return filedef.NewCourse(fit.Messages...)
}

// pacingPushHarness is a Garmin deployment with training and a synthetic route.
type pacingPushHarness struct {
	*connectHarness
	courses *idCourses
	store   *workout.DB
	pushes  *pacingpush.Store
	log     *bytes.Buffer
}

func newPacingPushHarness(t *testing.T) *pacingPushHarness {
	t.Helper()
	h := newConnectHarness(t, true)
	courses := &idCourses{}
	h.garmin.courses = courses

	tr, err := workout.UseDB(h.db.Conn(), h.db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	pushes, err := pacingpush.UseDB(h.db.Conn(), h.db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	h.srv.Training, h.srv.PacingPushes = tr, pushes
	h.srv.Clock = func() time.Time { return demandsClock }
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
	if _, err := tr.SaveProfile(t.Context(), workout.RiderProfile{Rider: "wilant", FTPWatts: 250, MaxHR: 190, ThresholdHR: 170}); err != nil {
		t.Fatal(err)
	}
	return &pacingPushHarness{connectHarness: h, courses: courses, store: tr, pushes: pushes, log: &buf}
}

func (h *pacingPushHarness) connectGarmin(user string) {
	h.t.Helper()
	resp := h.as(user, "cyclists", http.MethodPost, "/api/garmin/connection", `{"email":"r@example.com","password":"pw"}`)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("connect garmin: status %d", resp.StatusCode)
	}
}

func (h *pacingPushHarness) route(owner, name string) string {
	h.t.Helper()
	rt, err := h.db.Create(h.t.Context(), source.CreateRequest{
		Name: name, UploadedBy: owner, GPX: routefixture.GPX(name, true, 25, 100, routefixture.Hilly()...),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return rt.Slug
}

func (h *pacingPushHarness) goal(rider, slug string) workout.Goal {
	h.t.Helper()
	g, err := h.store.CreateGoal(h.t.Context(), workout.CreateGoalRequest{Rider: rider, Name: "Hilly Fondo", EventDate: "2027-01-31", RouteSlug: slug})
	if err != nil {
		h.t.Fatal(err)
	}
	return g
}

func TestPacingFITDownload(t *testing.T) {
	h := newPacingPushHarness(t)
	slug := h.route("wilant", "Demo Hills")
	g := h.goal("wilant", slug)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/routes/"+slug+"/pacing.fit?goal="+g.ID, "")
	raw := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.ant.fit" {
		t.Errorf("content type = %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, slug+"-pacing.fit") || !strings.HasPrefix(cd, "attachment") {
		t.Errorf("content disposition = %q, want an attachment named from the slug", cd)
	}

	course := decodeCourse(t, raw)
	if course.Course == nil || course.Course.Name != "Demo Hills pacing" {
		t.Errorf("course name = %v, want \"Demo Hills pacing\"", course.Course)
	}
	if len(course.Records) < 100 {
		t.Errorf("only %d track records", len(course.Records))
	}
	starts, tops := 0, 0
	startRe, topRe := regexp.MustCompile(`^C\d+ \d+-\d+W$`), regexp.MustCompile(`^Top C\d+$`)
	for _, cp := range course.CoursePoints {
		if len(cp.Name) > 15 {
			t.Errorf("course point %q is longer than 15 characters", cp.Name)
		}
		switch {
		case startRe.MatchString(cp.Name):
			starts++
		case topRe.MatchString(cp.Name):
			tops++
		default:
			t.Errorf("unexpected course point %q", cp.Name)
		}
	}
	if starts < 2 || starts != tops {
		t.Errorf("%d climb points and %d summits", starts, tops)
	}

	// Heart rate as the target: bpm names.
	hr := decodeCourse(t, readAll(t, h.as("wilant", "cyclists", http.MethodGet, "/api/routes/"+slug+"/pacing.fit?target=hr", "")))
	for _, cp := range hr.CoursePoints {
		if !strings.HasPrefix(cp.Name, "Top") && !strings.HasSuffix(cp.Name, "bpm") {
			t.Errorf("an HR course has %q", cp.Name)
		}
	}
}

func TestPacingFITAccessAndAvailability(t *testing.T) {
	h := newPacingPushHarness(t)
	slug := h.route("wilant", "Demo Hills")
	other := h.route("someone-else", "Their Hills")

	if got := h.as("wilant", "cyclists", http.MethodGet, "/api/routes/"+other+"/pacing.fit", "").StatusCode; got != http.StatusNotFound {
		t.Errorf("an invisible route: status = %d, want 404", got)
	}
	g := h.goal("wilant", slug)
	if got := h.as("friend", "cyclists", http.MethodGet, "/api/routes/"+slug+"/pacing.fit?goal="+g.ID, "").StatusCode; got != http.StatusNotFound {
		t.Errorf("another rider: status = %d, want 404", got)
	}
	// No FTP: nothing to build.
	if got := h.as("friend", "cyclists", http.MethodGet, "/api/routes/"+h.route("friend", "Friend Hills")+"/pacing.fit", "").StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("a rider with no FTP: status = %d, want 422", got)
	}
}

func TestPushingThePacingCourseToGarminRecordsAndReplaces(t *testing.T) {
	h := newPacingPushHarness(t)
	h.connectGarmin("wilant")
	slug := h.route("wilant", "Demo Hills")
	g := h.goal("wilant", slug)
	body := `{"provider":"garmin","goal":"` + g.ID + `"}`

	libraryBefore, _ := h.store.ListWorkouts(t.Context(), "wilant")
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/routes/"+slug+"/pacing/push", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, readAll(t, resp))
	}
	if len(h.courses.ids) != 1 || h.courses.files[0] != slug+"-pacing.fit" {
		t.Fatalf("imports = %v %v, want one file named from the slug", h.courses.ids, h.courses.files)
	}
	if c := decodeCourse(t, h.courses.data[0]); c.Course.Name != "Demo Hills pacing" || len(c.CoursePoints) < 4 {
		t.Errorf("the pushed course is %q with %d points", c.Course.Name, len(c.CoursePoints))
	}
	got, ok, err := h.pushes.Get(t.Context(), "wilant", "garmin", "pacing:"+g.ID)
	if err != nil || !ok || got != "garmin-course-1" {
		t.Fatalf("recorded id = %q ok=%v err=%v", got, ok, err)
	}

	// A second push replaces: a new import, then the old course deleted, and
	// the record moves on. Never two.
	resp = h.as("wilant", "cyclists", http.MethodPost, "/api/routes/"+slug+"/pacing/push", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second push: status = %d", resp.StatusCode)
	}
	if len(h.courses.ids) != 2 || len(h.courses.deleted) != 1 || h.courses.deleted[0] != "garmin-course-1" {
		t.Errorf("imports %v deleted %v; want the first course deleted after the second import", h.courses.ids, h.courses.deleted)
	}
	if got, _, _ := h.pushes.Get(t.Context(), "wilant", "garmin", "pacing:"+g.ID); got != "garmin-course-2" {
		t.Errorf("recorded id = %q, want garmin-course-2", got)
	}

	// A failed import leaves the previous course and its record alone.
	h.courses.failNext = true
	if got := h.as("wilant", "cyclists", http.MethodPost, "/api/routes/"+slug+"/pacing/push", body).StatusCode; got != http.StatusBadGateway {
		t.Errorf("a failed import: status = %d, want 502", got)
	}
	if got, _, _ := h.pushes.Get(t.Context(), "wilant", "garmin", "pacing:"+g.ID); got != "garmin-course-2" || len(h.courses.deleted) != 1 {
		t.Errorf("a failed push disturbed the record (%q) or deleted a course (%v)", got, h.courses.deleted)
	}

	// The library: the route's own course and everything else are untouched.
	if after, _ := h.store.ListWorkouts(t.Context(), "wilant"); len(after) != len(libraryBefore) {
		t.Error("a pacing push created workouts")
	}
	if entries, _ := h.store.ListGoals(t.Context(), "wilant"); len(entries) != 1 {
		t.Error("goals changed")
	}
	// The logs: rider, route and outcome, no coordinates, no health values.
	logged := strings.ToLower(h.log.String())
	for _, secret := range []string{"lat=", "lon=", "4.567", "51.2", "watts", "250", "ftp"} {
		if strings.Contains(logged, secret) {
			t.Errorf("the log carries %q: %s", secret, logged)
		}
	}
}

func TestAPacingPushKeysOnTheRouteWhenThereIsNoGoal(t *testing.T) {
	h := newPacingPushHarness(t)
	h.connectGarmin("wilant")
	slug := h.route("wilant", "Demo Hills")
	if got := h.as("wilant", "cyclists", http.MethodPost, "/api/routes/"+slug+"/pacing/push", `{"provider":"garmin"}`).StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d", got)
	}
	if _, ok, _ := h.pushes.Get(t.Context(), "wilant", "garmin", "pacing:"+slug); !ok {
		t.Error("the push was not recorded under the route's slug")
	}
}

func TestPushingWithNoLinkedAccountIs412WithAWarnAndNoCoordinates(t *testing.T) {
	h := newPacingPushHarness(t)
	// wilant has a Garmin account (seeded) but never signed in; "friend" has
	// nothing linked at all.
	slug := h.route("friend", "Friend Hills")
	if _, err := h.store.SaveProfile(t.Context(), workout.RiderProfile{Rider: "friend", FTPWatts: 240}); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"garmin", "wahoo"} {
		resp := h.as("friend", "cyclists", http.MethodPost, "/api/routes/"+slug+"/pacing/push", `{"provider":"`+provider+`"}`)
		if resp.StatusCode != http.StatusPreconditionFailed {
			t.Errorf("%s: status = %d, want 412", provider, resp.StatusCode)
		}
	}
	if len(h.courses.ids) != 0 {
		t.Error("a course reached Garmin from a rider with no account")
	}
	logged := h.log.String()
	if !strings.Contains(logged, "level=WARN") || !strings.Contains(logged, "pacing") || !strings.Contains(logged, slug) {
		t.Errorf("expected a warn line with the route slug, got %q", logged)
	}
	for _, secret := range []string{"lat=", "lon=", "4.567", "51.2", "240"} {
		if strings.Contains(strings.ToLower(logged), secret) {
			t.Errorf("the warn line carries %q", secret)
		}
	}
	// A rider cannot ride someone else's account: wilant's is untouched.
	if _, ok, _ := h.pushes.Get(t.Context(), "wilant", "garmin", "pacing:"+slug); ok {
		t.Error("a push by another rider was recorded against wilant")
	}
}

func TestPacingPushRefusals(t *testing.T) {
	h := newPacingPushHarness(t)
	h.connectGarmin("wilant")
	slug := h.route("wilant", "Demo Hills")
	private := h.route("someone-else", "Their Hills")
	g := h.goal("wilant", slug)

	post := func(user, slug, body string) int {
		return h.as(user, "cyclists", http.MethodPost, "/api/routes/"+slug+"/pacing/push", body).StatusCode
	}
	if got := post("wilant", private, `{"provider":"garmin"}`); got != http.StatusNotFound {
		t.Errorf("an invisible route: %d, want 404", got)
	}
	if got := post("friend", slug, `{"provider":"garmin","goal":"`+g.ID+`"}`); got != http.StatusNotFound {
		t.Errorf("another rider's goal and route: %d, want 404", got)
	}
	if got := post("wilant", slug, `{"provider":"garmin","goal":"nope"}`); got != http.StatusNotFound {
		t.Errorf("an unknown goal: %d, want 404", got)
	}
	if got := post("wilant", slug, `{"provider":"komoot"}`); got != http.StatusBadRequest {
		t.Errorf("an unknown provider: %d, want 400", got)
	}
	if got := post("wilant", slug, `not json`); got != http.StatusBadRequest {
		t.Errorf("bad JSON: %d, want 400", got)
	}
	if got := h.as("wilant", "guests", http.MethodPost, "/api/routes/"+slug+"/pacing/push", `{"provider":"garmin"}`).StatusCode; got != http.StatusForbidden {
		t.Errorf("a viewer: %d, want 403", got)
	}
	if len(h.courses.ids) != 0 {
		t.Errorf("a refused request still imported %d courses", len(h.courses.ids))
	}
}

func TestPushingThePacingCourseToWahooCreatesThenUpdates(t *testing.T) {
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
	body := `{"provider":"wahoo","goal":"` + g.ID + `"}`
	path := "/api/routes/" + rt.Slug + "/pacing/push"

	if got := h.as("wilant", "cyclists", http.MethodPost, path, body).StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d", got)
	}
	if len(h.upstream.createdRoutes) != 1 {
		t.Fatalf("created %d routes on wahoo, want 1", len(h.upstream.createdRoutes))
	}
	created := h.upstream.createdRoutes[0]
	if created.Get("route[external_id]") != "pacing:"+g.ID {
		t.Errorf("external_id = %q, want pacing:<goal> so it never collides with the library route's slug", created.Get("route[external_id]"))
	}
	if created.Get("route[name]") != "Demo Hills pacing" || !strings.HasPrefix(created.Get("route[file]"), "data:application/vnd.fit;base64,") {
		t.Errorf("name %q file prefix %.40q", created.Get("route[name]"), created.Get("route[file]"))
	}
	id, ok, _ := pushes.Get(t.Context(), "wilant", "wahoo", "pacing:"+g.ID)
	if !ok || id == "" {
		t.Fatal("no remote id recorded")
	}

	// A second push updates that route in place instead of creating another.
	if got := h.as("wilant", "cyclists", http.MethodPost, path, body).StatusCode; got != http.StatusOK {
		t.Fatalf("second push: status = %d", got)
	}
	if len(h.upstream.createdRoutes) != 1 {
		t.Errorf("a second push created another route (%d)", len(h.upstream.createdRoutes))
	}
	var upd url.Values
	for gotID, v := range h.upstream.updatedRoutes {
		if gotID == id {
			upd = v
		}
	}
	if upd == nil || upd.Get("route[external_id]") != "pacing:"+g.ID {
		t.Errorf("updated routes = %v, want an update of %q", h.upstream.updatedRoutes, id)
	}
}
