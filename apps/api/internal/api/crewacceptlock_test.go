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
	"github.com/wncservices/domestique/apps/api/internal/schedule"
	"github.com/wncservices/domestique/apps/api/internal/settings"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Accepting runs under the scheduling lock: while the tick holds it, the answer
// is refused with the replan wording and nothing moves.
func TestAcceptingIsRefusedWhileTheSchedulingLockIsHeld(t *testing.T) {
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
	for _, table := range []string{"workouts", "ride_together_members", "ride_together_proposals", "crew_ride_together", "crew_members", "crews"} {
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
	p, _, err := sched.CreateProposal(context.Background(), schedule.Proposal{
		CrewID: c.ID, WeekStart: cpMonday, Day: cpSaturday, RouteSlug: "a-route", Riders: []string{"wilant", "sam"},
	})
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

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/training/ride-together/"+p.ID+"/accept", nil)
	req.Header.Set("Remote-User", "wilant")
	req.Header.Set("Remote-Groups", "cyclists")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if msg, _ := body["error"].(string); resp.StatusCode != http.StatusConflict || !strings.Contains(msg, "being updated right now") {
		t.Fatalf("status %d body %v, want 409 with the replan wording", resp.StatusCode, body)
	}
	got, _ := sched.GetProposal(context.Background(), p.ID)
	for _, m := range got.Members {
		if m.Status != schedule.MemberPending {
			t.Errorf("%s answered while the lock was held", m.Rider)
		}
	}
}
