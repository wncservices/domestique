package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/typedef"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestCLIFitWorkoutMarksAnIndoorWorkout(t *testing.T) {
	dir := workspace(t)
	dbPath := filepath.Join(dir, "data", "routes.db")

	src, err := source.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := workout.UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	w, err := store.CreateWorkout(t.Context(), workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "Long ride",
		Steps: []workout.WorkoutStep{{Name: "Ride", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 1800, Target: workout.TargetOpen}},
	})
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if _, err := store.UpdateWorkout(t.Context(), w.ID, workout.UpdateWorkoutRequest{Indoor: &yes}); err != nil {
		t.Fatal(err)
	}
	src.Close()

	out := filepath.Join(dir, "indoor.fit")
	mustRun(t, "fit-workout", "--db", dbPath, "--out", out, w.ID)

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	fit, err := decoder.New(bytes.NewReader(raw)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	wkt := filedef.NewWorkout(fit.Messages...)
	if wkt.Workout.WktName != "Long ride (indoor)" || wkt.Workout.SubSport != typedef.SubSportIndoorCycling {
		t.Errorf("name %q sub_sport %v, want the indoor name and sub_sport", wkt.Workout.WktName, wkt.Workout.SubSport)
	}
}
