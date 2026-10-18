package workout

import (
	"errors"
	"reflect"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/why"
)

func eachEngine(t *testing.T, fn func(t *testing.T, db *DB)) {
	t.Helper()
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) { fn(t, open(t)) })
	}
}

func newFlagWorkout(t *testing.T, db *DB, f Flags) Workout {
	t.Helper()
	w, err := db.CreateWorkout(t.Context(), CreateWorkoutRequest{
		Rider: "wilant", Name: "Tempo", Sport: model.SportCycling, Date: "2026-10-05", Steps: exampleSteps(), Flags: f,
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestFlagsRoundTrip(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		want := Flags{
			Origin: OriginPlan, AdaptedRules: []why.Rule{why.ReadinessRest, why.LifeEvent},
			Swapped: true, Kept: KeptCrew, MovedFrom: "2026-10-01", MovedBy: MovedByAuto,
		}
		w := newFlagWorkout(t, db, want)
		if !reflect.DeepEqual(w.Flags, want) {
			t.Fatalf("created = %+v, want %+v", w.Flags, want)
		}
		got, err := db.GetWorkout(ctx, w.ID)
		if err != nil || !reflect.DeepEqual(got.Flags, want) {
			t.Fatalf("read = %+v (%v), want %+v", got.Flags, err, want)
		}

		name := "Renamed"
		got, err = db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{Name: &name, Flags: nil})
		if err != nil || !reflect.DeepEqual(got.Flags, want) {
			t.Fatalf("nil Flags changed them: %+v (%v)", got.Flags, err)
		}

		next := Flags{Origin: OriginRider, Kept: KeptLife}
		got, err = db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{Flags: &next})
		if err != nil || !reflect.DeepEqual(got.Flags, next) {
			t.Fatalf("replaced = %+v (%v), want %+v", got.Flags, err, next)
		}
	})
}

func TestCreateDefaultsOriginToRider(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		w := newFlagWorkout(t, db, Flags{})
		if w.Flags.Origin != OriginRider {
			t.Fatalf("origin = %q, want rider", w.Flags.Origin)
		}
	})
}

func TestEditedAtFollowsUpdates(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		w := newFlagWorkout(t, db, Flags{})
		if w.EditedAt != "" {
			t.Fatalf("new row edited_at = %q, want empty", w.EditedAt)
		}
		name := "Edited"
		got, err := db.UpdateWorkout(t.Context(), w.ID, UpdateWorkoutRequest{Name: &name})
		if err != nil {
			t.Fatal(err)
		}
		if got.EditedAt == "" || got.EditedAt != got.UpdatedAt {
			t.Fatalf("edited_at = %q, updated_at = %q, want equal and set", got.EditedAt, got.UpdatedAt)
		}
	})
}

// insertLegacy writes a row the way it was before the flag columns: origin ”.
func insertLegacy(t *testing.T, db *DB, id, goal, crew, protocol, desc, created, updated string) {
	t.Helper()
	_, err := db.db.Exec(db.query(`
        INSERT INTO workouts (id, rider, sport, name, goal_id, date, description, steps, zone, level, test_protocol, crew_ride_id, created_at, updated_at)
        VALUES (?, 'wilant', 'cycling', ?, ?, '2026-10-05', ?, '[]', '', 0, ?, ?, ?, ?)`),
		id, id, goal, desc, protocol, crew, created, updated)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBackfillFlagsFromMarkers(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		gen := GeneratedDescription
		rows := []struct{ id, goal, crew, protocol, desc string }{
			{"plain", "g1", "", "", gen},
			{"crew", "g1", "r1", "", "x"},
			{"test", "g1", "", "ramp", "FTP ramp"},
			{"hand", "", "", "", "my own"},
			{"adjusted", "g1", "", "", gen + " " + AdjustedMarker + " Tired. " + AdjustedMarker + " Life event: illness."},
			{"swapped", "g1", "", "", gen + " " + SwappedMarker + " easier: X"},
			{"crewkept", "g1", "", "", gen + " " + CrewKeptMarker},
			{"lifekept", "g1", "", "", gen + " " + LifeKeptMarker},
			{"lifemove", "g1", "", "", gen + "\n\nRescheduled by a life event: moved from 2026-10-01."},
			{"ridermove", "g1", "", "", gen + "\n\nRescheduled by you: moved from 2026-10-03."},
			{"automove", "g1", "", "", gen + " " + AdjustedMarker + " missed, moved from 2026-10-04."},
		}
		for _, r := range rows {
			insertLegacy(t, db, r.id, r.goal, r.crew, r.protocol, r.desc, "2026-10-01T08:00:00Z", "2026-10-01T08:00:00Z")
		}
		ctx := t.Context()
		if err := db.backfillFlags(ctx); err != nil {
			t.Fatal(err)
		}
		snapshot := map[string]Workout{}
		for _, r := range rows {
			w, err := db.GetWorkout(ctx, r.id)
			if err != nil {
				t.Fatal(err)
			}
			legacy := LegacyFlags(w)
			if !w.Flags.Equivalent(legacy) || w.Flags.Origin == "" {
				t.Errorf("%s: stored %+v, legacy %+v", r.id, w.Flags, legacy)
			}
			for i := range w.Flags.AdaptedRules {
				if w.Flags.AdaptedRules[i] != legacy.AdaptedRules[i] {
					t.Errorf("%s: rule %d = %q, want %q", r.id, i, w.Flags.AdaptedRules[i], legacy.AdaptedRules[i])
				}
			}
			snapshot[r.id] = w
		}
		var left int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM workouts WHERE origin = ''`).Scan(&left); err != nil || left != 0 {
			t.Fatalf("rows with empty origin = %d (%v)", left, err)
		}
		if err := db.backfillFlags(ctx); err != nil {
			t.Fatal(err)
		}
		for id, before := range snapshot {
			after, _ := db.GetWorkout(ctx, id)
			if !reflect.DeepEqual(before, after) {
				t.Errorf("%s changed on the second run: %+v -> %+v", id, before, after)
			}
		}
	})
}

func TestBackfillSetsEditedAtOnlyForEditedRows(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		insertLegacy(t, db, "edited", "", "", "", "d", "2026-10-01T08:00:00Z", "2026-10-02T09:00:00Z")
		insertLegacy(t, db, "untouched", "", "", "", "d", "2026-10-01T08:00:00Z", "2026-10-01T08:00:00Z")
		if err := db.backfillFlags(t.Context()); err != nil {
			t.Fatal(err)
		}
		e, _ := db.GetWorkout(t.Context(), "edited")
		u, _ := db.GetWorkout(t.Context(), "untouched")
		if e.EditedAt != "2026-10-02T09:00:00Z" || e.UpdatedAt != "2026-10-02T09:00:00Z" {
			t.Errorf("edited: edited_at %q updated_at %q", e.EditedAt, e.UpdatedAt)
		}
		if u.EditedAt != "" || u.UpdatedAt != "2026-10-01T08:00:00Z" {
			t.Errorf("untouched: edited_at %q updated_at %q", u.EditedAt, u.UpdatedAt)
		}
	})
}

// The parity switch is process-wide and other tests in this package create rows
// without flags, so these tests must not run in parallel with anything and turn
// it off again when they finish.
func disableLegacyParityCheck() { parityCheck.Store(false) }

func withParityCheck(t *testing.T) {
	t.Helper()
	EnableLegacyParityCheck()
	t.Cleanup(disableLegacyParityCheck)
}

func TestParityCheckRejectsMismatch(t *testing.T) {
	withParityCheck(t)
	eachEngine(t, func(t *testing.T, db *DB) {
		_, err := db.CreateWorkout(t.Context(), CreateWorkoutRequest{
			Rider: "wilant", Name: "Tempo", Sport: model.SportCycling, Date: "2026-10-05", Steps: exampleSteps(),
			GoalID: "g", Description: GeneratedDescription, Flags: Flags{Origin: OriginRider},
		})
		if !errors.Is(err, ErrFlagsDisagree) {
			t.Fatalf("err = %v, want ErrFlagsDisagree", err)
		}
		all, err := db.ListWorkouts(t.Context(), "wilant")
		if err != nil || len(all) != 0 {
			t.Fatalf("rows = %d (%v), want none written", len(all), err)
		}
	})
}

func TestParityCheckAcceptsMatch(t *testing.T) {
	withParityCheck(t)
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		w, err := db.CreateWorkout(ctx, CreateWorkoutRequest{
			Rider: "wilant", Name: "Tempo", Sport: model.SportCycling, Date: "2026-10-05", Steps: exampleSteps(),
			GoalID: "g", Description: GeneratedDescription, Flags: Flags{Origin: OriginPlan},
		})
		if err != nil {
			t.Fatal(err)
		}

		noted := w.Description + " " + AdjustedMarker + " Tired."
		_, err = db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{Description: &noted})
		if !errors.Is(err, ErrFlagsDisagree) {
			t.Fatalf("update without flags: err = %v, want ErrFlagsDisagree", err)
		}
		got, _ := db.GetWorkout(ctx, w.ID)
		if got.Description != GeneratedDescription {
			t.Fatalf("a refused update wrote %q", got.Description)
		}

		next := w.Flags.WithAdaptation(why.ReadinessRest)
		if _, err = db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{Description: &noted, Flags: &next}); err != nil {
			t.Fatalf("update with flags: %v", err)
		}
	})
}
