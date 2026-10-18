package api

import "github.com/wncservices/domestique/apps/api/internal/workout"

// legacyFlagged gives a fixture the flags its description markers say, which is
// what a row written before the typed columns existed carries. Flags a request
// already names are kept while they agree with its text; a fixture that builds
// a session with the scheduler and then appends a marker to the description by
// hand has changed the text, and the flags follow it. The parity check refuses
// a row whose flags disagree with its text, so fixtures go through here; the
// writers under test set theirs explicitly.
func legacyFlagged(req workout.CreateWorkoutRequest) workout.CreateWorkoutRequest {
	legacy := workout.LegacyFlags(workout.Workout{
		GoalID: req.GoalID, Description: req.Description,
		TestProtocol: req.TestProtocol, CrewRideID: req.CrewRideID,
	})
	if req.Flags.Origin == "" || !req.Flags.Equivalent(legacy) {
		req.Flags = legacy
	}
	return req
}

// legacyFlagsAfter is the flags a fixture that rewrites a description by hand
// should store with it: what the new text says. The parity check holds the
// writers under test to the same reading.
func legacyFlagsAfter(wk workout.Workout, description string) *workout.Flags {
	wk.Description = description
	f := workout.LegacyFlags(wk)
	return &f
}
