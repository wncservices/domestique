package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/ridestart"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// wrNow is a Saturday morning, 08:00 in Brussels, with the zone explicit so
// nothing here depends on the machine's TZ.
var wrNow = time.Date(2026, 10, 3, 8, 0, 0, 0, time.FixedZone("CEST", 2*3600))

// wrHarness runs the server with the stores the workout-route feature uses,
// and a clock and log capture a test can read.
type wrHarness struct {
	t      *testing.T
	client *http.Client
	base   string
	srv    *api.Server
	db     *source.DB

	training *workout.DB
	starts   *ridestart.Store
	logs     *syncBuffer

	mu  sync.Mutex
	now time.Time
}

func newWRHarness(t *testing.T, mutate ...func(*wrHarness, *api.Server)) *wrHarness {
	t.Helper()
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	training, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	starts, err := ridestart.UseDB(db.Conn(), db.DSN())
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

	h := &wrHarness{t: t, db: db, training: training, starts: starts, logs: &syncBuffer{}, now: wrNow}
	srv := &api.Server{
		Source:     db,
		Auth:       authenticator,
		Training:   training,
		RideStarts: starts,
		Clock:      h.clock,
		Log:        slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	for _, m := range mutate {
		m(h, srv)
	}
	h.srv = srv
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	h.client, h.base = server.Client(), server.URL
	return h
}

func (h *wrHarness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

// as sends a request as a rider in the "cyclists" group.
func (h *wrHarness) as(user, method, path, body string) *http.Response {
	h.t.Helper()
	return h.asGroup(user, "cyclists", method, path, body)
}

func (h *wrHarness) asGroup(user, groups, method, path, body string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Remote-User", user)
	req.Header.Set("Remote-Groups", groups)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (h *wrHarness) body(resp *http.Response) string {
	h.t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(raw)
}
