package scheduler

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestNeedsEasingBeforeTest(t *testing.T) {
	hard := func(id, date string) workout.Workout {
		return workout.Workout{ID: id, GoalID: "g", Date: date, Name: "Threshold intervals", Zone: workout.ZoneThreshold, Description: GeneratedDescription}
	}
	test := workout.Workout{ID: "t", GoalID: "g", Date: "2026-03-27", Name: "FTP Test (ramp)", TestProtocol: "ramp", Description: "The ramp test."}
	all := func(w ...workout.Workout) []workout.Workout { return append([]workout.Workout{test}, w...) }

	cases := []struct {
		name string
		w    workout.Workout
		want bool
	}{
		{"generated hard session the day before", hard("a", "2026-03-26"), true},
		{"two days before is left alone", hard("b", "2026-03-25"), false},
		{"the day of the test is left alone", hard("c", "2026-03-27"), false},
		{"the day after is left alone", hard("d", "2026-03-28"), false},
		{"a rider-built hard session is theirs", func() workout.Workout {
			w := hard("e", "2026-03-26")
			w.GoalID, w.Description = "", "my own intervals"
			return w
		}(), false},
		{"already adjusted is not adjusted twice", func() workout.Workout {
			w := hard("f", "2026-03-26")
			w.Description += " " + AdjustedMarker + " eased for readiness."
			return w
		}(), false},
		{"an easy generated session needs nothing", func() workout.Workout {
			w := hard("g", "2026-03-26")
			w.Zone, w.Name = workout.ZoneEndurance, "Endurance ride"
			return w
		}(), false},
		{"a long ride is key but not hard", func() workout.Workout {
			w := hard("h", "2026-03-26")
			w.Zone, w.Name = workout.ZoneEndurance, "Long ride"
			return w
		}(), false},
		{"a legacy tempo session by name", func() workout.Workout {
			w := hard("i", "2026-03-26")
			w.Zone, w.Name = "", "Tempo ride"
			return w
		}(), true},
		{"the test is never eased", func() workout.Workout {
			w := hard("j", "2026-03-26")
			w.TestProtocol = "twenty_minute"
			return w
		}(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NeedsEasingBeforeTest(c.w, all(c.w)); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
	if NeedsEasingBeforeTest(hard("k", "2026-03-26"), nil) {
		t.Error("no test on the calendar means nothing to ease for")
	}
	unscheduled := workout.Workout{ID: "u", TestProtocol: "ramp"} // no date
	if NeedsEasingBeforeTest(hard("l", ""), []workout.Workout{unscheduled}) {
		t.Error("an unscheduled test has no day before it")
	}
}
