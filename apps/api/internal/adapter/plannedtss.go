package adapter

import "github.com/wncservices/domestique/apps/api/internal/workout"

// PlannedTSS is workout.PlannedTSS, kept here for the callers that already
// reach it through the adapter.
func PlannedTSS(w workout.Workout, ftpWatts float64) (float64, bool) {
	return workout.PlannedTSS(w, ftpWatts)
}
