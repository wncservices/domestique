package workout

import "testing"

// Rides analysed against an FTP test before the analysis knew what a test is
// read "struggled, 0 of 1 efforts on target". The start-up pass puts them
// right, and leaves every other ride's analysis alone.
func TestRescoreFTPTestAnalysesEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) *DB{
		"sqlite":   openTestDB,
		"postgres": openTestPostgres,
	}
	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()
			test, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Name: "Ramp", Steps: exampleSteps(), TestProtocol: "ramp"})
			if err != nil {
				t.Fatal(err)
			}
			plain, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Name: "Intervals", Steps: exampleSteps()})
			if err != nil {
				t.Fatal(err)
			}
			steps := []AnalysisStep{{Index: 1, Result: "missed", Hard: true}}
			for _, a := range []SessionAnalysis{
				{SessionID: "garmin:1", Rider: "wilant", WorkoutID: test.ID, Outcome: "struggled", Steps: steps},
				{SessionID: "garmin:2", Rider: "wilant", WorkoutID: plain.ID, Outcome: "struggled", Steps: steps},
			} {
				if err := db.SaveAnalysis(ctx, a); err != nil {
					t.Fatal(err)
				}
			}

			for range 2 { // idempotent
				if err := db.rescoreFTPTestAnalyses(); err != nil {
					t.Fatal(err)
				}
			}

			got, _, err := db.GetAnalysis(ctx, "garmin:1")
			if err != nil {
				t.Fatal(err)
			}
			if got.Outcome != "completed" || len(got.Steps) != 0 {
				t.Errorf("test ride = %q with %d steps, want completed with none", got.Outcome, len(got.Steps))
			}
			other, _, err := db.GetAnalysis(ctx, "garmin:2")
			if err != nil {
				t.Fatal(err)
			}
			if other.Outcome != "struggled" || len(other.Steps) != 1 {
				t.Errorf("ordinary ride = %q with %d steps, want it untouched", other.Outcome, len(other.Steps))
			}
		})
	}
}
