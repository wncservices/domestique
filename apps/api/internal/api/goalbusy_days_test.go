package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/pgtest"
	"github.com/wncservices/domestique/apps/api/internal/settings"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Saving the days again and again while scheduling is busy leaves one rebuild
// waiting, not one per save: each would clear and rebuild the same plan.
func TestRepeatedDaysSavesWhileSchedulingIsBusyRebuildOnce(t *testing.T) {
	db, err := source.OpenDB(pgtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	appSettings, err := settings.UseDB(db.Conn(), db.DSN(), nil)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.New(auth.Config{Mode: auth.ModeProxy, Roles: auth.RoleMapping{Rider: []string{"cyclists"}}})
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	now := utcNoon(2026, time.October, 7)
	srv := &api.Server{Source: db, Auth: authenticator, Training: store, Settings: appSettings,
		Clock: func() time.Time { return now }, SeasonFillRetry: 20 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(logs, nil))}
	followServerClock(srv, store)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	do := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Remote-User", "wilant")
		req.Header.Set("Remote-Groups", "cyclists")
		req.Header.Set("Content-Type", "application/json")
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	if _, err := store.SaveProfile(context.Background(), workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 3, AvailableDays: []string{"tue", "thu", "sat", "sun"},
	}); err != nil {
		t.Fatal(err)
	}
	if resp := do(http.MethodPost, "/api/training/goals", `{"name":"Race","eventDate":"2027-04-14"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create goal: status %d", resp.StatusCode)
	}
	srv.WaitForBackground()

	holding, release := make(chan struct{}), make(chan struct{})
	go api.HoldSchedulingLockForTest(context.Background(), db.Conn(), func() { close(holding); <-release })
	select {
	case <-holding:
	case <-time.After(5 * time.Second):
		t.Fatal("never took the lock")
	}
	for _, hours := range []string{"2", "2.5", "3.5"} {
		body := `{"hoursPerAvailableDay":` + hours + `,"availableDays":["tue","thu","sat","sun"]}`
		if resp := do(http.MethodPut, "/api/training/profile", body); resp.StatusCode != http.StatusOK {
			t.Fatalf("save profile: status %d", resp.StatusCode)
		}
	}
	close(release)
	srv.WaitForBackground()

	// Each retry ends in a "goal changed" line, rebuilt or gave up.
	n := 0
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, `msg="goal changed`) && strings.Contains(line, "reason=days") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d days rebuilds were attempted after three saves, want 1\n%s", n, logs.String())
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("a rebuild gave up:\n%s", logs.String())
	}
	// The one rebuild built the last save's hours.
	p, _, err := store.GetProfile(context.Background(), "wilant")
	if err != nil || p.HoursPerAvailableDay != 3.5 {
		t.Fatalf("profile = %+v, %v", p, err)
	}
}
