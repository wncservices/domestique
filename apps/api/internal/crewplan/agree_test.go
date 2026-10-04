package crewplan

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The preview says what joining removes; the fill, run on a week built after
// joining, says what the plan makes. They have to name the same days, or a rider
// confirms one picture and gets another. This runs both over a handful of week
// shapes and rides and compares.
func TestThePreviewRemovesExactlyTheDaysTheFillDrops(t *testing.T) {
	shapes := [][]string{
		{"tue", "thu", "sat", "sun"},
		{"wed", "fri", "sat"},
		{"mon", "tue", "wed", "thu", "fri", "sat", "sun"},
		{"tue", "thu", "sun"},
		{"thu", "sat", "sun"},
	}
	rides := map[string]float64{"long": 4 * 3600, "endurance": 90 * 60, "short": 30 * 60}
	rideDays := []string{sat, fri, thu, sun}
	week := periodization.Week{StartDate: mon, Phase: periodization.PhaseBase, TargetHours: 8}

	for _, days := range shapes {
		profile := workout.RiderProfile{AvailableDays: days}
		plain, err := scheduler.WeekWorkouts(week, profile, nil, "wilant", "goal", model.SportCycling)
		if err != nil {
			t.Fatal(err)
		}
		var existing []workout.Workout
		for _, r := range plain {
			existing = append(existing, workout.Workout{
				ID: r.Date, Rider: "wilant", Sport: r.Sport, Name: r.Name, GoalID: "goal", Date: r.Date,
				Description: r.Description, Zone: r.Zone, Steps: r.Steps,
			})
		}
		for kind, seconds := range rides {
			for _, rideDay := range rideDays {
				name := fmt.Sprintf("%v ride %s on %s", days, kind, rideDay)
				fixed, err := scheduler.WeekWorkouts(week, profile, nil, "wilant", "goal", model.SportCycling,
					scheduler.WithFixed([]scheduler.Fixed{{Date: rideDay, Seconds: seconds}}))
				if err != nil {
					t.Fatal(err)
				}
				kept := map[string]bool{}
				for _, r := range fixed {
					kept[r.Date] = true
				}
				var dropped []string
				for _, r := range plain {
					if !kept[r.Date] {
						dropped = append(dropped, r.Date)
					}
				}

				// WeekTarget 0: only the structural removals are compared, not the shortening.
				d := Preview(Input{Ride: Ride{ID: "r", Date: rideDay, RouteName: "R", Seconds: seconds}, Workouts: existing, Now: now})
				var removed []string
				for _, c := range d.Changes {
					if c.Op == OpRemove {
						removed = append(removed, c.Date)
					}
				}
				sort.Strings(dropped)
				sort.Strings(removed)
				if strings.Join(dropped, ",") != strings.Join(removed, ",") {
					t.Errorf("%s: the fill drops %v, the preview removes %v", name, dropped, removed)
				}
			}
		}
	}
}

// A 90-minute ride on Saturday with endurance sessions on Saturday and Thursday
// removes one session, not two: the Saturday one is the slot the ride replaces.
func TestAnEnduranceRideOnAnEnduranceDayRemovesExactlyOneSession(t *testing.T) {
	ride := Ride{ID: "r", Date: sat, Seconds: 90 * 60}
	d := Preview(input(ride,
		gen("sat-e", "Endurance ride", sat, workout.ZoneEndurance, 3600),
		gen("thu-e", "Endurance ride", thu, workout.ZoneEndurance, 3600),
	))
	n := 0
	for _, c := range d.Changes {
		if c.Op == OpRemove {
			n++
		}
	}
	if n != 1 || ops(d)["remove:sat-e"].ID == "" {
		t.Errorf("changes = %+v, want exactly the Saturday session removed", d.Changes)
	}
}
