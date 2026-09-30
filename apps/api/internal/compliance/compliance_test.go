package compliance_test

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/compliance"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func planned(date string, seconds float64) workout.Workout {
	return workout.Workout{Date: date, Sport: "cycling", Steps: []workout.WorkoutStep{
		{Duration: workout.DurationTime, Seconds: seconds, Target: workout.TargetOpen},
	}}
}

func done(date, sport string, seconds float64) workout.CompletedSession {
	return workout.CompletedSession{Date: date, Sport: sport, DurationSeconds: seconds}
}

func TestDay(t *testing.T) {
	const today = "2026-09-29"
	openOnly := workout.Workout{Date: "2026-09-28", Sport: "cycling", Steps: []workout.WorkoutStep{{Duration: workout.DurationOpen, Target: workout.TargetOpen}}}

	// Planned for 31 minutes, ridden to failure at 24: always short by design.
	rampTest := planned("2026-09-28", 1860)
	rampTest.TestProtocol = "ramp"

	cases := []struct {
		name      string
		date      string
		planned   []workout.Workout
		completed []workout.CompletedSession
		want      compliance.Status
	}{
		{"nothing at all is rest", "2026-09-28", nil, nil, compliance.StatusRest},
		{"ride with no plan is unplanned", "2026-09-28", nil, []workout.CompletedSession{done("2026-09-28", "cycling", 3600)}, compliance.StatusUnplanned},
		{"80% is done", "2026-09-28", []workout.Workout{planned("2026-09-28", 3600)}, []workout.CompletedSession{done("2026-09-28", "cycling", 2880)}, compliance.StatusDone},
		{"just under 80% is partial", "2026-09-28", []workout.Workout{planned("2026-09-28", 3600)}, []workout.CompletedSession{done("2026-09-28", "cycling", 2870)}, compliance.StatusPartial},
		{"30% is partial", "2026-09-28", []workout.Workout{planned("2026-09-28", 3600)}, []workout.CompletedSession{done("2026-09-28", "cycling", 1080)}, compliance.StatusPartial},
		{"past day under 30% is missed", "2026-09-28", []workout.Workout{planned("2026-09-28", 3600)}, []workout.CompletedSession{done("2026-09-28", "cycling", 600)}, compliance.StatusMissed},
		{"past day with nothing is missed", "2026-09-28", []workout.Workout{planned("2026-09-28", 3600)}, nil, compliance.StatusMissed},
		{"today with nothing yet is upcoming", today, []workout.Workout{planned(today, 3600)}, nil, compliance.StatusUpcoming},
		{"future day is upcoming", "2026-10-01", []workout.Workout{planned("2026-10-01", 3600)}, nil, compliance.StatusUpcoming},
		{"an FTP test ridden short is done", "2026-09-28", []workout.Workout{rampTest}, []workout.CompletedSession{done("2026-09-28", "cycling", 1440)}, compliance.StatusDone},
		{"an FTP test not ridden is missed", "2026-09-28", []workout.Workout{rampTest}, nil, compliance.StatusMissed},
		{"other sport does not count", "2026-09-28", []workout.Workout{planned("2026-09-28", 3600)}, []workout.CompletedSession{done("2026-09-28", "running", 3600)}, compliance.StatusMissed},
		{"sessions add up", "2026-09-28", []workout.Workout{planned("2026-09-28", 3600)}, []workout.CompletedSession{done("2026-09-28", "cycling", 1800), done("2026-09-28", "cycling", 1200)}, compliance.StatusDone},
		{"open-ended plan with any matching ride is done", "2026-09-28", []workout.Workout{openOnly}, []workout.CompletedSession{done("2026-09-28", "cycling", 60)}, compliance.StatusDone},
		{"open-ended plan with nothing is missed", "2026-09-28", []workout.Workout{openOnly}, nil, compliance.StatusMissed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := compliance.Day(c.date, today, c.planned, c.completed); got != c.want {
				t.Errorf("Day() = %q, want %q", got, c.want)
			}
		})
	}
}
