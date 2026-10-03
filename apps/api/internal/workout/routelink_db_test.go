package workout

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

func TestGoalRouteLinkRoundTripsOnEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()

			g, err := db.CreateGoal(ctx, CreateGoalRequest{
				Rider: "wilant", Name: "Hilly Fondo", Sport: model.SportCycling, EventDate: "2026-06-01",
				RouteSlug: "demo-hills", PacingIF: 0.8,
			})
			if err != nil {
				t.Fatal(err)
			}
			if g.RouteSlug != "demo-hills" || g.PacingIF != 0.8 {
				t.Fatalf("created goal = %q / %v, want demo-hills / 0.8", g.RouteSlug, g.PacingIF)
			}
			listed, _ := db.ListGoals(ctx, "wilant")
			all, _ := db.ListAllGoals(ctx)
			if len(listed) != 1 || listed[0].RouteSlug != "demo-hills" || len(all) != 1 || all[0].RouteSlug != "demo-hills" {
				t.Fatalf("list lost the link: %+v / %+v", listed, all)
			}

			// An update that omits routeSlug and pacingIf keeps both.
			name := "Renamed"
			got, err := db.UpdateGoal(ctx, g.ID, UpdateGoalRequest{Name: &name})
			if err != nil {
				t.Fatal(err)
			}
			if got.RouteSlug != "demo-hills" || got.PacingIF != 0.8 {
				t.Errorf("an unrelated update changed the link: %q / %v", got.RouteSlug, got.PacingIF)
			}

			// Empty clears, zero resets the IF to derived.
			empty, zero := "", 0.0
			got, err = db.UpdateGoal(ctx, g.ID, UpdateGoalRequest{RouteSlug: &empty, PacingIF: &zero})
			if err != nil {
				t.Fatal(err)
			}
			if got.RouteSlug != "" || got.PacingIF != 0 {
				t.Errorf("clearing left %q / %v", got.RouteSlug, got.PacingIF)
			}

			other := "another"
			got, _ = db.UpdateGoal(ctx, g.ID, UpdateGoalRequest{RouteSlug: &other})
			if got.RouteSlug != "another" {
				t.Errorf("setting a slug on update gave %q", got.RouteSlug)
			}
		})
	}
}

func TestWeightRoundTripsAndNoOtherSaveZeroesIt(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()

			// No profile row yet: setting a weight creates one.
			if err := db.SetWeight(ctx, "wilant", 82.5); err != nil {
				t.Fatal(err)
			}
			p, ok, err := db.GetProfile(ctx, "wilant")
			if err != nil || !ok || p.WeightKG != 82.5 {
				t.Fatalf("profile = %+v ok=%v err=%v, want weight 82.5", p, ok, err)
			}

			// Every other writer of the profile (the form, a Garmin sync, an
			// accepted threshold) builds a RiderProfile that does not know the
			// weight; none may zero it.
			if _, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", FTPWatts: 250, MaxHR: 188}); err != nil {
				t.Fatal(err)
			}
			p, _, _ = db.GetProfile(ctx, "wilant")
			if p.WeightKG != 82.5 || p.FTPWatts != 250 {
				t.Errorf("SaveProfile left weight %v, FTP %v; want 82.5 and 250", p.WeightKG, p.FTPWatts)
			}

			if err := db.SetWeight(ctx, "WILANT", 0); err != nil {
				t.Fatal(err)
			}
			p, _, _ = db.GetProfile(ctx, "wilant")
			if p.WeightKG != 0 {
				t.Errorf("clearing left %v", p.WeightKG)
			}
			if p.FTPWatts != 250 {
				t.Errorf("SetWeight disturbed FTP: %v", p.FTPWatts)
			}
			if err := db.SetWeight(ctx, " ", 70); err == nil {
				t.Error("a weight for no rider must be refused")
			}
		})
	}
}

// An existing database has goals and rider_profiles tables with none of the
// new columns; opening it adds them, and opening it again changes nothing.
func TestOldTablesGainTheRouteAndWeightColumns(t *testing.T) {
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	for _, stmt := range []string{
		`CREATE TABLE goals (
    id TEXT PRIMARY KEY, rider TEXT NOT NULL, name TEXT NOT NULL, sport TEXT NOT NULL DEFAULT 'cycling',
    event_date TEXT NOT NULL DEFAULT '', priority TEXT NOT NULL DEFAULT 'B',
    target_distance_m DOUBLE PRECISION NOT NULL DEFAULT 0, target_elevation_m DOUBLE PRECISION NOT NULL DEFAULT 0,
    notes TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE rider_profiles (
    rider TEXT PRIMARY KEY, ftp_watts DOUBLE PRECISION NOT NULL DEFAULT 0,
    threshold_pace_sec_per_km DOUBLE PRECISION NOT NULL DEFAULT 0,
    max_hr INTEGER NOT NULL DEFAULT 0, resting_hr INTEGER NOT NULL DEFAULT 0,
    available_days TEXT NOT NULL DEFAULT '', hours_per_available_day DOUBLE PRECISION NOT NULL DEFAULT 0,
    experience_level TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL)`,
		`INSERT INTO goals (id, rider, name, created_at, updated_at) VALUES ('old', 'wilant', 'Old goal', 'x', 'x')`,
		`INSERT INTO rider_profiles (rider, ftp_watts, updated_at) VALUES ('wilant', 250, 'x')`,
	} {
		if _, err := src.Conn().Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}

	db, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatalf("UseDB (migrate): %v", err)
	}
	g, err := db.GetGoal(t.Context(), "old")
	if err != nil || g.RouteSlug != "" || g.PacingIF != 0 {
		t.Fatalf("old goal = %+v err %v, want empty route and derived IF", g, err)
	}
	p, _, _ := db.GetProfile(t.Context(), "wilant")
	if p.WeightKG != 0 || p.FTPWatts != 250 {
		t.Fatalf("old profile = %+v", p)
	}

	if err := db.SetWeight(t.Context(), "wilant", 71); err != nil {
		t.Fatal(err)
	}
	slug := "keep-me"
	if _, err := db.UpdateGoal(t.Context(), "old", UpdateGoalRequest{RouteSlug: &slug}); err != nil {
		t.Fatal(err)
	}
	if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
		t.Fatalf("second UseDB: %v", err)
	}
	g, _ = db.GetGoal(t.Context(), "old")
	p, _, _ = db.GetProfile(t.Context(), "wilant")
	if g.RouteSlug != "keep-me" || p.WeightKG != 71 {
		t.Errorf("a second UseDB changed data: goal %q, weight %v", g.RouteSlug, p.WeightKG)
	}
}
