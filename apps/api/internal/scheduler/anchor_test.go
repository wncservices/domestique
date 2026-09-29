package scheduler

import (
	"math"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// End to end through the real plan: the plan is rebuilt from "today" on every
// tick, so a recovery week only reaches the scheduler if the periodization
// keeps each calendar week's index stable across those rebuilds.
func TestRecoveryWeekIsScheduledEasyWhenItArrivesFromARebuiltPlan(t *testing.T) {
	goal := workout.Goal{ID: "g", EventDate: "2026-03-29", CreatedAt: "2026-01-05T09:00:00Z"}
	profile := workout.RiderProfile{HoursPerAvailableDay: 2, AvailableDays: []string{"tue", "thu", "sat", "sun"}}

	for _, zone := range []string{"UTC", "Europe/Brussels"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		schedule := func(day time.Time) (seconds float64, hard int) {
			plan, err := periodization.BuildPlan(goal, profile, day)
			if err != nil {
				t.Fatal(err)
			}
			out, err := NextWorkouts(plan, profile, nil, "w", "g", model.SportCycling, day)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range out {
				seconds += totalSeconds(w.Steps)
				if w.Zone != workout.ZoneEndurance {
					hard++
				}
			}
			return seconds, hard
		}

		// Week 3 (Wed 2026-01-21) is a full Base week: 8 h x 0.70 = 5.6 h, with
		// its sweet-spot session. Week 4 (Wed 2026-01-28) is Base's recovery
		// week: 60% of that, all easy.
		s3, hard3 := schedule(time.Date(2026, 1, 21, 6, 0, 0, 0, loc))
		s4, hard4 := schedule(time.Date(2026, 1, 28, 6, 0, 0, 0, loc))
		if math.Abs(s3-5.6*3600) > 1 || hard3 == 0 {
			t.Errorf("%s week 3: %.0fs with %d hard sessions, want 20160s and at least one hard session", zone, s3, hard3)
		}
		if math.Abs(s4-5.6*0.6*3600) > 1 || hard4 != 0 {
			t.Errorf("%s week 4 (recovery): %.0fs with %d hard sessions, want 12096s and none", zone, s4, hard4)
		}
	}
}
