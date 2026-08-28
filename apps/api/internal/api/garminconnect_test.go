package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/targets"
)

// fakeGarmin records what it was asked to sign in with, so a test can assert
// the password went to Garmin and nowhere else.
type fakeGarmin struct {
	email    string
	password string
	// consumer is what the server resolved and handed over — the tests that
	// care where a pair came from assert on this.
	consumer api.GarminConsumer
	// err, when set, is what Connect returns instead of a session — the three
	// sign-in failures are the point of most of these tests.
	err error

	// devices is what Devices hands back, and devicesErr what it fails with.
	devices    []garmin.Device
	devicesErr error

	// mu guards resumedSession: handleGarminCourseImport downloads several
	// courses at once (garmincourses.go's fetchGPX), so DownloadGPX is the
	// one method here a test can genuinely call from more than one goroutine
	// at a time. Recording into a plain field without a lock is exactly the
	// data race `go test -race` exists to catch — and did, in CI, before this.
	mu sync.Mutex
	// resumedSession records the session a resumed call was given, so a test
	// can assert the stored sign-in was used rather than a fresh one. Shared
	// by Devices and Courses, which resume identically.
	resumedSession garmin.Session

	// courses is what a push is handed, and coursesErr what resolving it
	// fails with.
	courses    targets.Courses
	coursesErr error

	// listCourses is what ListCourses hands back, and listCoursesErr what it
	// fails with.
	listCourses    []garmin.Course
	listCoursesErr error
	// gpxByID is what DownloadGPX returns for a given course id;
	// downloadGPXErr, when set, is what it fails with regardless of id.
	// Read-only once a test has set it up, before any concurrent call can
	// happen — safe without the lock that resumedSession needs.
	gpxByID        map[string][]byte
	downloadGPXErr error
}

func (f *fakeGarmin) setResumedSession(session garmin.Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumedSession = session
}

func (f *fakeGarmin) Courses(_ api.GarminConsumer, session garmin.Session) (targets.Courses, error) {
	f.setResumedSession(session)
	return f.courses, f.coursesErr
}

func (f *fakeGarmin) Devices(_ context.Context, _ api.GarminConsumer, session garmin.Session) ([]garmin.Device, error) {
	f.setResumedSession(session)
	return f.devices, f.devicesErr
}

func (f *fakeGarmin) ListCourses(_ context.Context, _ api.GarminConsumer, session garmin.Session) ([]garmin.Course, error) {
	f.setResumedSession(session)
	return f.listCourses, f.listCoursesErr
}

func (f *fakeGarmin) DownloadGPX(_ context.Context, _ api.GarminConsumer, session garmin.Session, courseID string) ([]byte, error) {
	f.setResumedSession(session)
	if f.downloadGPXErr != nil {
		return nil, f.downloadGPXErr
	}
	return f.gpxByID[courseID], nil
}

func (f *fakeGarmin) Connect(_ context.Context, consumer api.GarminConsumer, email, password string) (garmin.Session, error) {
	f.consumer = consumer
	f.email, f.password = email, password
	if f.err != nil {
		return garmin.Session{}, f.err
	}
	return garmin.Session{
		OAuth1Token:  "garmin-token-1",
		OAuth1Secret: "garmin-secret-1",
		DisplayName:  "Wilant N",
		ObtainedAt:   time.Now().UTC(),
	}, nil
}

// noConsumer makes the deployment one where Garmin has not been set up.
func noConsumer(t *testing.T) {
	t.Helper()
	t.Setenv(garmin.EnvConsumerKey, "")
	t.Setenv(garmin.EnvConsumerSecret, "")
}

func TestGarminConnectStoresTheSessionAndNotThePassword(t *testing.T) {
	h := newConnectHarness(t, true)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"rider@example.com","password":"hunter2"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	body := decodeConnection(t, resp)
	if body["connected"] != true || body["displayName"] != "Wilant N" {
		t.Errorf("body = %v, want a connected account named Wilant N", body)
	}

	// The password reached Garmin...
	if h.garmin.password != "hunter2" {
		t.Errorf("connector saw password %q", h.garmin.password)
	}

	// ...and nowhere else. This is the reason the feature is built this way.
	var rows int
	if err := h.db.Conn().QueryRow(
		`SELECT COUNT(*) FROM provider_links WHERE CAST(secret AS TEXT) LIKE '%hunter2%'`).
		Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatal("the password appears in the stored session")
	}

	// What is stored is the token pair, and it comes back as it went in — the
	// push adapter will read it exactly this way.
	_, stored, err := h.links.Secret("garmin", "wilant")
	if err != nil {
		t.Fatal(err)
	}
	var session garmin.Session
	if err := json.Unmarshal([]byte(stored), &session); err != nil {
		t.Fatalf("the stored session is not readable as one: %v", err)
	}
	if session.OAuth1Token != "garmin-token-1" || session.OAuth1Secret != "garmin-secret-1" {
		t.Errorf("stored %+v, want the token pair Garmin returned", session)
	}
}

// Signing in is how a Garmin becomes a push target. Without this a rider
// signs in, sees "connected", and still has nowhere for routes to go.
func TestGarminConnectLinksTheHeadUnit(t *testing.T) {
	h := newConnectHarness(t, true)

	// "friend" has no seeded account, unlike wilant.
	resp := h.as("friend", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"friend@example.com","password":"pw"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if _, err := h.accounts.Get(t.Context(), accounts.ID(model.ProviderGarmin, "friend")); err != nil {
		t.Errorf("no head unit was linked after signing in: %v", err)
	}
}

// Signing in again must not fail because the head unit is already there.
func TestGarminConnectIsRepeatable(t *testing.T) {
	h := newConnectHarness(t, true)

	for i := range 2 {
		resp := h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
			`{"email":"rider@example.com","password":"pw"}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("attempt %d: status = %d, want 200", i+1, resp.StatusCode)
		}
	}
}

// An account with 2FA on cannot be signed in to this way, and saying "wrong
// password" would send the rider round in circles. The distinct status and the
// mfa flag are what the UI keys its own wording off.
func TestGarminMFAIsReportedAsItsOwnThing(t *testing.T) {
	h := newConnectHarness(t, true)
	h.garmin.err = garmin.ErrMFARequired

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r@example.com","password":"pw"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}

	body := decodeConnection(t, resp)
	if body["mfa"] != true {
		t.Errorf("body = %v, want mfa: true", body)
	}
	message, _ := body["error"].(string)
	if message == "" || message == "Garmin did not accept those details" {
		t.Errorf("error = %q, want it to say the account uses two-factor", message)
	}

	// Nothing was stored: there is no session to store.
	if _, err := h.links.Get("garmin", "wilant"); err == nil {
		t.Error("a connection was stored despite the MFA challenge")
	}
}

func TestGarminBadCredentialsAreReportedAsSuch(t *testing.T) {
	h := newConnectHarness(t, true)
	h.garmin.err = garmin.ErrBadCredentials

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r@example.com","password":"wrong"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}

	body := decodeConnection(t, resp)
	if body["mfa"] == true {
		t.Error("a wrong password was reported as an MFA challenge")
	}
}

func TestGarminConnectRefusedWithoutAnEncryptionKey(t *testing.T) {
	h := newConnectHarness(t, false)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r@example.com","password":"pw"}`)
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", resp.StatusCode)
	}
	// Refused before signing in: no point sending someone's password to
	// Garmin when the result cannot be kept.
	if h.garmin.password != "" {
		t.Error("the password was sent to Garmin despite the refusal")
	}
}

func TestGarminConnectRefusedWithoutAConsumer(t *testing.T) {
	h := newConnectHarness(t, true)
	noConsumer(t)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r@example.com","password":"pw"}`)
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", resp.StatusCode)
	}
	if h.garmin.password != "" {
		t.Error("the password was sent despite there being no way to complete the handshake")
	}
}

// The status endpoint has to say *why* a form should not be offered, or the UI
// can only show a sign-in that will fail.
func TestGarminConnectionReportsWhySigningInIsUnavailable(t *testing.T) {
	t.Run("no key", func(t *testing.T) {
		h := newConnectHarness(t, false)
		body := decodeConnection(t, h.as("wilant", "cyclists", http.MethodGet, "/api/garmin/connection", ""))
		if body["canConnect"] != false {
			t.Errorf("canConnect = %v, want false", body["canConnect"])
		}
		if body["unavailable"] == nil {
			t.Error("nothing said about why")
		}
	})

	t.Run("no consumer", func(t *testing.T) {
		h := newConnectHarness(t, true)
		noConsumer(t)
		body := decodeConnection(t, h.as("wilant", "cyclists", http.MethodGet, "/api/garmin/connection", ""))
		if body["canConnect"] != false {
			t.Errorf("canConnect = %v, want false", body["canConnect"])
		}
	})

	t.Run("ready", func(t *testing.T) {
		h := newConnectHarness(t, true)
		body := decodeConnection(t, h.as("wilant", "cyclists", http.MethodGet, "/api/garmin/connection", ""))
		if body["canConnect"] != true {
			t.Errorf("canConnect = %v, want true", body["canConnect"])
		}
		if body["connected"] != false {
			t.Errorf("connected = %v before signing in", body["connected"])
		}
	})
}

// A stored session lasts about a year and then everything quietly stops. The
// date is reported so a deployment can say so before a ride rather than after.
func TestGarminConnectionReportsWhenTheSessionExpires(t *testing.T) {
	h := newConnectHarness(t, true)
	h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r@example.com","password":"pw"}`)

	body := decodeConnection(t, h.as("wilant", "cyclists", http.MethodGet, "/api/garmin/connection", ""))
	raw, _ := body["expiresAt"].(string)
	expires, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("expiresAt = %q, want an RFC3339 time", raw)
	}
	if want := time.Now().AddDate(1, 0, 0); expires.Sub(want).Abs() > time.Hour {
		t.Errorf("expiresAt = %s, want about a year from now (%s)", expires, want)
	}
	if body["expired"] == true {
		t.Error("a session made moments ago is reported as expired")
	}
}

// The rider is whoever is signed in. Letting the body decide would let one
// rider attach an account to another — the same rule as linking a head unit.
func TestGarminConnectIgnoresARiderInTheBody(t *testing.T) {
	h := newConnectHarness(t, true)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r@example.com","password":"pw","rider":"someone-else"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if _, err := h.links.Get("garmin", "wilant"); err != nil {
		t.Errorf("the connection was not stored against the session rider: %v", err)
	}
	if _, err := h.links.Get("garmin", "someone-else"); err == nil {
		t.Error("the body's rider was honoured")
	}
}

// handleGarminConnect proxies a password straight to Garmin — without a
// limit this server is an unlimited credential-stuffing proxy against
// whichever Garmin account a rider points it at.
func TestGarminConnectIsRateLimitedPerRider(t *testing.T) {
	h := newConnectHarness(t, true, func(s *api.Server) {
		s.ConnectLimiter = ratelimit.New(1, time.Hour)
	})

	first := h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r@example.com","password":"wrong"}`)
	if first.StatusCode == http.StatusTooManyRequests {
		t.Fatal("first attempt was already rate limited")
	}
	second := h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r@example.com","password":"wrong-again"}`)
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second attempt: status = %d, want 429", second.StatusCode)
	}

	// A different rider gets their own quota — this limits the caller, not
	// the endpoint globally.
	other := h.as("friend", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r2@example.com","password":"pw"}`)
	if other.StatusCode == http.StatusTooManyRequests {
		t.Fatal("a different rider was rate limited by another rider's attempts")
	}
}

func TestGarminDisconnectRemovesTheConnectionAndTheHeadUnit(t *testing.T) {
	h := newConnectHarness(t, true)
	h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r@example.com","password":"pw"}`)

	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/garmin/connection", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeConnection(t, resp)
	if body["connected"] != false {
		t.Errorf("connected = %v after disconnecting", body["connected"])
	}

	if _, err := h.links.Get("garmin", "wilant"); err == nil {
		t.Error("the connection survived being disconnected")
	}
	// A head unit with no sign-in behind it is a push target that can only
	// fail, so it goes too.
	if _, err := h.accounts.Get(t.Context(), accounts.ID(model.ProviderGarmin, "wilant")); err == nil {
		t.Error("the head unit is still linked with no way to reach it")
	}
}

// One rider's disconnect must not touch another's.
func TestGarminDisconnectOnlyAffectsTheCaller(t *testing.T) {
	h := newConnectHarness(t, true)
	h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r@example.com","password":"pw"}`)
	h.as("friend", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"f@example.com","password":"pw"}`)

	h.as("friend", "cyclists", http.MethodDelete, "/api/garmin/connection", "")

	if _, err := h.links.Get("garmin", "wilant"); err != nil {
		t.Errorf("another rider's disconnect took this one with it: %v", err)
	}
}

// Signing in to Garmin must not disturb the Komoot connection, which is the
// failure a single shared table invites.
func TestGarminAndKomootCoexist(t *testing.T) {
	h := newConnectHarness(t, true)

	h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/connection",
		`{"email":"k@example.com","password":"pw"}`)
	h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"g@example.com","password":"pw"}`)

	komoot := decodeConnection(t, h.as("wilant", "cyclists", http.MethodGet, "/api/komoot/connection", ""))
	if komoot["connected"] != true || komoot["email"] != "k@example.com" {
		t.Errorf("komoot connection = %v, want the one signed in to first", komoot)
	}

	h.as("wilant", "cyclists", http.MethodDelete, "/api/garmin/connection", "")

	komoot = decodeConnection(t, h.as("wilant", "cyclists", http.MethodGet, "/api/komoot/connection", ""))
	if komoot["connected"] != true {
		t.Error("disconnecting Garmin disconnected Komoot")
	}
}

// A viewer may not link head units, and signing in to Garmin is linking one.
func TestGarminConnectionNeedsAccountPermission(t *testing.T) {
	h := newConnectHarness(t, true)

	for _, tc := range []struct{ method, body string }{
		{http.MethodGet, ""},
		{http.MethodPost, `{"email":"r@example.com","password":"pw"}`},
		{http.MethodDelete, ""},
	} {
		resp := h.as("guest", "guests", tc.method, "/api/garmin/connection", tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s status = %d, want 403", tc.method, resp.StatusCode)
		}
	}
	if h.garmin.password != "" {
		t.Error("a viewer's password was sent to Garmin")
	}
}

// A Server with no connection store at all must refuse, not panic.
//
// providerlink.Store.CanStore has a nil-safe receiver on purpose, which is
// what lets every handler call it without a nil check first. That is subtle
// enough that two reviewers have now read it as a crash, so it is pinned here:
// if someone ever gives CanStore a body that dereferences, this fails rather
// than 500ing in production.
func TestGarminHandlersSurviveNoStore(t *testing.T) {
	srv := &api.Server{
		Auth:   noAuth(t),
		Garmin: &fakeGarmin{},
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	for _, tc := range []struct {
		method, body string
		want         int
	}{
		{http.MethodGet, "", http.StatusOK},
		{http.MethodPost, `{"email":"r@example.com","password":"pw"}`, http.StatusPreconditionFailed},
		{http.MethodDelete, "", http.StatusOK},
	} {
		req, err := http.NewRequest(tc.method, server.URL+"/api/garmin/connection",
			strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.method, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s status = %d, want %d", tc.method, resp.StatusCode, tc.want)
		}
	}
}

// noAuth is a server with authentication off, where every caller is an admin —
// the only way to reach these handlers without a store to seed a rider in.
func noAuth(t *testing.T) *auth.Authenticator {
	t.Helper()
	a, err := auth.New(auth.Config{Mode: auth.ModeNone})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Devices come from the session that was stored at sign-in. No password is
// involved, which is the whole reason a session is kept in its place.
func TestDevicesUseTheStoredSession(t *testing.T) {
	h := newConnectHarness(t, true)
	h.garmin.devices = []garmin.Device{
		{ID: "1", Name: "Edge 530"},
		{ID: "2", Name: "Forerunner 165"},
	}

	h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r@example.com","password":"pw"}`)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/garmin/devices", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var got []garmin.Device
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "Edge 530" || got[1].Name != "Forerunner 165" {
		t.Errorf("devices = %+v, want both units", got)
	}
	if h.garmin.resumedSession.OAuth1Token != "garmin-token-1" {
		t.Errorf("resumed with %q, want the stored token",
			h.garmin.resumedSession.OAuth1Token)
	}
}

// A rider who has not connected has no devices, and that is not an error —
// the panel simply has nothing to show.
func TestDevicesWithoutAConnectionAreEmpty(t *testing.T) {
	h := newConnectHarness(t, true)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/garmin/devices", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got []garmin.Device
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("devices = %+v, want none", got)
	}
}

// One rider's devices are not another's. Same rule as every other thing keyed
// by rider here.
func TestDevicesAreTheCallersOwn(t *testing.T) {
	h := newConnectHarness(t, true)
	h.garmin.devices = []garmin.Device{{ID: "1", Name: "Edge 530"}}

	h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r@example.com","password":"pw"}`)

	resp := h.as("someone", "cyclists", http.MethodGet, "/api/garmin/devices", "")
	var got []garmin.Device
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("another rider saw %+v", got)
	}
}

// Garmin moving the endpoint is an upstream problem. It must not read as a
// broken connection: courses still sync, and the rider can do nothing about
// a URL.
func TestDeviceListFailureIsUpstream(t *testing.T) {
	h := newConnectHarness(t, true)
	h.garmin.devicesErr = errors.New("garmin: the device list returned 404")

	h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r@example.com","password":"pw"}`)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/garmin/devices", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

// recordingCourses is a provider that remembers what it was sent.
type recordingCourses struct {
	filenames []string
	sizes     []int
	deleted   []string
}

func (r *recordingCourses) ImportCourse(_ context.Context, filename string, data []byte) (string, error) {
	r.filenames = append(r.filenames, filename)
	r.sizes = append(r.sizes, len(data))
	return "garmin-course-1", nil
}

func (r *recordingCourses) DeleteCourse(_ context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	return nil
}

// The whole path, through HTTP: a rider connects Garmin, uploads a route
// targeting their account, and pushes. What proves the wiring is that a FIT
// file reaches the provider — the server resolving the session, the factory
// handing it to the adapter, the adapter rendering the course.
func TestPushSendsACourseToGarmin(t *testing.T) {
	h := newConnectHarness(t, true)
	courses := &recordingCourses{}
	h.garmin.courses = courses

	h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		`{"email":"r@example.com","password":"pw"}`)

	if _, err := h.db.Create(t.Context(), source.CreateRequest{
		Filename:   "ride.gpx",
		Name:       "Kluisbergen",
		Targets:    &[]string{"garmin:wilant"},
		UploadedBy: "wilant",
		GPX:        []byte(aTestGPX),
	}); err != nil {
		t.Fatal(err)
	}

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/push", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var out struct {
		Applied  int      `json:"applied"`
		Failures []string `json:"failures"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Failures) != 0 {
		t.Fatalf("push failed: %v", out.Failures)
	}
	if len(courses.filenames) != 1 {
		t.Fatalf("uploaded %d courses, want 1", len(courses.filenames))
	}
	if !strings.HasSuffix(courses.filenames[0], ".fit") {
		t.Errorf("uploaded %q, want a .fit", courses.filenames[0])
	}
	if courses.sizes[0] == 0 {
		t.Error("an empty course reached the provider")
	}

	// The session it pushed with is the stored one, not a fresh sign-in.
	if h.garmin.resumedSession.OAuth1Token != "garmin-token-1" {
		t.Errorf("pushed with %q, want the stored token", h.garmin.resumedSession.OAuth1Token)
	}
}

// A rider who has not connected must fail their own push and nobody else's,
// with a sentence that says what to do.
func TestPushWithoutAGarminConnectionFailsThatAccountOnly(t *testing.T) {
	h := newConnectHarness(t, true)

	if _, err := h.db.Create(t.Context(), source.CreateRequest{
		Filename:   "ride.gpx",
		Name:       "Kluisbergen",
		Targets:    &[]string{"garmin:wilant"},
		UploadedBy: "wilant",
		GPX:        []byte(aTestGPX),
	}); err != nil {
		t.Fatal(err)
	}

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/push", "")
	var out struct {
		Failures []string `json:"failures"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Failures) != 1 {
		t.Fatalf("failures = %v, want exactly the unconnected account", out.Failures)
	}
	if !strings.Contains(out.Failures[0], "has not connected Garmin") {
		t.Errorf("failure = %q, want it to say the account is not connected", out.Failures[0])
	}
}

const aTestGPX = `<?xml version="1.0"?>
<gpx version="1.1" creator="test"><trk><trkseg>
<trkpt lat="50.85" lon="4.35"><ele>20</ele></trkpt>
<trkpt lat="50.86" lon="4.36"><ele>25</ele></trkpt>
<trkpt lat="50.87" lon="4.37"><ele>30</ele></trkpt>
</trkseg></trk></gpx>`
