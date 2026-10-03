package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/routefixture"
	"github.com/wncservices/domestique/apps/api/internal/schedule"
	"github.com/wncservices/domestique/apps/api/internal/settings"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Joining is refused with the replan wording while the tick holds the
// scheduling lock, and nothing is written. The advisory lock is PostgreSQL's.
func TestJoiningIsRefusedWhileTheSchedulingLockIsHeld(t *testing.T) {
	dsn := os.Getenv("DOMESTIQUE_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set DOMESTIQUE_TEST_POSTGRES to a PostgreSQL DSN to run this")
	}
	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	crews, err := crew.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	sched, err := schedule.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"workouts", "crew_ride_going", "crew_rides", "crew_members", "crews"} {
		if _, err := db.Conn().Exec(`DELETE FROM ` + table); err != nil {
			t.Fatal(err)
		}
	}
	appSettings, err := settings.UseDB(db.Conn(), db.DSN(), nil)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.New(auth.Config{Mode: auth.ModeProxy, Roles: auth.RoleMapping{Rider: []string{"cyclists"}}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := crews.Create(context.Background(), "Sunday Club", "wilant")
	if err != nil {
		t.Fatal(err)
	}
	targets := []string{c.ID}
	route, err := db.Create(context.Background(), source.CreateRequest{
		Name: "Hill Loop", UploadedBy: "wilant", Targets: &targets,
		GPX: routefixture.GPX("Hill Loop", true, 500, 0, routefixture.Piece{LengthM: 100000, Grade: 1}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ride, err := sched.Create(context.Background(), c.ID, route.Slug, cpSaturday, "", "wilant")
	if err != nil {
		t.Fatal(err)
	}
	srv := &api.Server{Source: db, Auth: authenticator, Training: store, Crew: crews, Schedule: sched, Settings: appSettings, Clock: cpClock}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	holding, release := make(chan struct{}), make(chan struct{})
	go api.HoldSchedulingLockForTest(context.Background(), db.Conn(), func() { close(holding); <-release })
	select {
	case <-holding:
	case <-time.After(5 * time.Second):
		t.Fatal("never took the lock")
	}
	defer close(release)

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/training/crew-rides/"+ride.ID+"/going", strings.NewReader(`{"going":true}`))
	req.Header.Set("Remote-User", "wilant")
	req.Header.Set("Remote-Groups", "cyclists")
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body["error"], "being updated right now") {
		t.Fatalf("status %d body %v, want 409 with the replan wording", resp.StatusCode, body)
	}
	if going, _ := sched.Going(context.Background(), ride.ID); len(going) != 0 {
		t.Errorf("going was written while the lock was held: %v", going)
	}
	if ws, _ := store.ListWorkouts(context.Background(), "wilant"); len(ws) != 0 {
		t.Errorf("a session was written while the lock was held")
	}
}
