package workout

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

func TestEffectiveOutcome(t *testing.T) {
	cases := []struct {
		name string
		a    SessionAnalysis
		want string
	}{
		{"all-out nailed planned ride is a struggle", SessionAnalysis{WorkoutID: "w", Outcome: "nailed", Feel: 5}, "struggled"},
		{"all-out completed planned ride is a struggle", SessionAnalysis{WorkoutID: "w", Outcome: "completed", Feel: 5}, "struggled"},
		{"very hard is not all-out", SessionAnalysis{WorkoutID: "w", Outcome: "nailed", Feel: 4}, "nailed"},
		{"an unplanned ride is left alone", SessionAnalysis{Outcome: "nailed", Feel: 5}, "nailed"},
		{"an incomplete ride stays incomplete", SessionAnalysis{WorkoutID: "w", Outcome: "incomplete", Feel: 5}, "incomplete"},
		{"a struggled ride stays struggled", SessionAnalysis{WorkoutID: "w", Outcome: "struggled", Feel: 5}, "struggled"},
		{"unrated is the stored outcome", SessionAnalysis{WorkoutID: "w", Outcome: "nailed"}, "nailed"},
		{"easy stays nailed", SessionAnalysis{WorkoutID: "w", Outcome: "nailed", Feel: 1}, "nailed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.EffectiveOutcome(); got != tc.want {
				t.Errorf("EffectiveOutcome = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSurveyEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) *DB{
		"sqlite":   openTestDB,
		"postgres": openTestPostgres,
	}
	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("the survey is a full replace and survives a re-analysis", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				if err := db.SaveAnalysis(ctx, SessionAnalysis{SessionID: "s1", Rider: "wilant", Outcome: "nailed"}); err != nil {
					t.Fatal(err)
				}
				if err := db.SetAnalysisSurvey(ctx, "s1", 4, "heavy", "high", 0.2); err != nil {
					t.Fatal(err)
				}
				a, _, _ := db.GetAnalysis(ctx, "s1")
				if a.Feel != 4 || a.Legs != "heavy" || a.Stress != "high" || a.LevelDelta != 0.2 {
					t.Fatalf("analysis = %+v", a)
				}

				// A re-analysis (a new FTP, a re-sync) must not wipe the survey.
				if err := db.SaveAnalysis(ctx, SessionAnalysis{SessionID: "s1", Rider: "wilant", Outcome: "completed"}); err != nil {
					t.Fatal(err)
				}
				a, _, _ = db.GetAnalysis(ctx, "s1")
				if a.Feel != 4 || a.Legs != "heavy" || a.Stress != "high" || a.Outcome != "completed" {
					t.Errorf("after re-analysis = %+v, want the survey kept", a)
				}

				// Sending less clears the rest.
				if err := db.SetAnalysisSurvey(ctx, "s1", 2, "", "", 0); err != nil {
					t.Fatal(err)
				}
				a, _, _ = db.GetAnalysis(ctx, "s1")
				if a.Feel != 2 || a.Legs != "" || a.Stress != "" {
					t.Errorf("after a full replace = %+v, want legs and stress cleared", a)
				}
			})

			t.Run("listing analyses carries the survey", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				if _, err := db.UpsertSession(ctx, UpsertSessionRequest{Rider: "wilant", Provider: "garmin", ExternalID: "x", Sport: "cycling", Date: "2026-03-20", DurationSeconds: 3600}); err != nil {
					t.Fatal(err)
				}
				sessions, _ := db.ListSessions(ctx, "wilant")
				id := sessions[0].ID
				if err := db.SaveAnalysis(ctx, SessionAnalysis{SessionID: id, Rider: "wilant", Outcome: "nailed"}); err != nil {
					t.Fatal(err)
				}
				if err := db.SetAnalysisSurvey(ctx, id, 3, "fresh", "low", 0); err != nil {
					t.Fatal(err)
				}
				list, err := db.ListAnalyses(ctx, "wilant", "2026-03-01")
				if err != nil || len(list) != 1 || list[0].Legs != "fresh" || list[0].Stress != "low" {
					t.Fatalf("list = %+v, %v", list, err)
				}
			})

			t.Run("a survey for an unknown session is an error", func(t *testing.T) {
				if err := open(t).SetAnalysisSurvey(t.Context(), "nope", 3, "", "", 0); err == nil {
					t.Error("expected an error")
				}
			})
		})
	}
}

// TestSessionAnalysesGainLegsAndStressOnAnOldDatabase: an analyses table that
// predates the survey columns gains them, keeps its rows, and reads them as
// unanswered.
func TestSessionAnalysesGainLegsAndStressOnAnOldDatabase(t *testing.T) {
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := src.Conn().Exec(`
CREATE TABLE session_analyses (
    session_id TEXT PRIMARY KEY, rider TEXT NOT NULL, workout_id TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL DEFAULT '', load_source TEXT NOT NULL DEFAULT '',
    normalized_power DOUBLE PRECISION NOT NULL DEFAULT 0, intensity_factor DOUBLE PRECISION NOT NULL DEFAULT 0,
    tss DOUBLE PRECISION NOT NULL DEFAULT 0, duration_ratio DOUBLE PRECISION NOT NULL DEFAULT 0,
    power_zone_seconds TEXT NOT NULL DEFAULT '', hr_zone_seconds TEXT NOT NULL DEFAULT '',
    power_curve TEXT NOT NULL DEFAULT '', steps TEXT NOT NULL DEFAULT '', analysed_at TEXT NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Conn().Exec(`INSERT INTO session_analyses (session_id, rider, outcome, analysed_at) VALUES ('old', 'wilant', 'nailed', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	db, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatalf("migrating: %v", err)
	}
	a, ok, err := db.GetAnalysis(t.Context(), "old")
	if err != nil || !ok || a.Legs != "" || a.Stress != "" || a.Outcome != "nailed" {
		t.Fatalf("old row = %+v ok=%v err=%v", a, ok, err)
	}
	if err := db.SetAnalysisSurvey(t.Context(), "old", 3, "heavy", "normal", 0); err != nil {
		t.Fatalf("survey on a migrated row: %v", err)
	}
	if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
		t.Errorf("second UseDB: %v", err)
	}
}
