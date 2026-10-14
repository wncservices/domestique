package adapter

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A rider who chose to keep a session as planned (around a crew ride, or
// through a return to training) is not overruled by readiness, fatigue or a
// step-down. The markers are written by other features, so each is tried.
func TestAKeptSessionIsNeverEasedSwappedOrSteppedDown(t *testing.T) {
	for name, marker := range map[string]string{"crew": scheduler.CrewKeptMarker, "life event": scheduler.LifeKeptMarker} {
		t.Run(name, func(t *testing.T) {
			kept := func(id, date string) workout.Workout {
				w := structured(id, "Threshold 3×8", date, workout.ZoneThreshold, 4)
				w.Description += " " + marker
				return w
			}
			rest := readiness.Assessment{Verdict: readiness.Rest, Reasons: []string{"your form is −34"}}
			if got := AdaptSessions([]workout.Workout{kept("today", "2026-03-19")}, nil, profileAvailable(weekdays...), thursday, nil, rest); len(got) != 0 {
				t.Errorf("fatigue changed a kept session: %+v", got)
			}
			caution := readiness.Assessment{Verdict: readiness.Caution, Reasons: []string{"HRV is unbalanced today"}}
			if got := AdaptSessions([]workout.Workout{kept("today", "2026-03-19")}, nil, workout.RiderProfile{}, thursday, nil, caution); len(got) != 0 {
				t.Errorf("caution stepped down a kept session: %+v", got)
			}
			ws := []workout.Workout{
				structured("tue-threshold", "Threshold 3×12", "2026-03-17", workout.ZoneThreshold, 5),
				kept("thu-threshold", "2026-03-19"),
			}
			analyses := map[string]workout.SessionAnalysis{"tue-threshold": struggled()}
			for _, c := range AdaptSessions(ws, nil, workout.RiderProfile{}, thursday, analyses, readiness.Assessment{}) {
				if c.WorkoutID == "thu-threshold" {
					t.Errorf("a struggled session stepped down a kept one: %+v", c)
				}
			}
			ws = []workout.Workout{kept("thu-hard", "2026-03-19"), {ID: "test", GoalID: "g", Date: "2026-03-20", TestProtocol: "ramp"}}
			if scheduler.NeedsEasingBeforeTest(ws[0], ws) {
				t.Error("a kept session was eased the day before an FTP test")
			}
		})
	}
}
