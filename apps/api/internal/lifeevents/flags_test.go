package lifeevents

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// assertUpdateFlags checks the update's flags against what its final text says,
// the same relation the workout store's parity check applies on a write.
func assertUpdateFlags(t *testing.T, id string, before workout.Workout, u *workout.UpdateWorkoutRequest) workout.Flags {
	t.Helper()
	if u == nil || u.Flags == nil || u.Description == nil {
		t.Fatalf("%s: update %+v must carry Flags and Description", id, u)
	}
	final := workout.Workout{GoalID: before.GoalID, CrewRideID: before.CrewRideID, TestProtocol: before.TestProtocol, Description: *u.Description}
	if legacy := workout.LegacyFlags(final); !u.Flags.Equivalent(legacy) {
		t.Fatalf("%s: flags %+v do not match the text's %+v", id, *u.Flags, legacy)
	}
	return *u.Flags
}

func TestLifeEventRequestsCarryFlags(t *testing.T) {
	prior := hard("again", "2026-10-09", 90)
	prior.Description = scheduler.GeneratedDescription + "\n\nRescheduled by you: moved from 2026-10-01."

	cases := []struct {
		name   string
		events []Event
		ws     []workout.Workout
		id     string
		check  func(t *testing.T, f workout.Flags)
	}{
		{"move", []Event{travel("2026-10-08", "2026-10-09")},
			[]workout.Workout{hard("thu", "2026-10-08", 90)}, "move:thu",
			func(t *testing.T, f workout.Flags) {
				if f.MovedFrom != "2026-10-08" || f.MovedBy != workout.MovedByLife || f.AdaptedRules[len(f.AdaptedRules)-1] != why.LifeEvent {
					t.Errorf("flags = %+v", f)
				}
			}},
		{"move again keeps the first date", []Event{travel("2026-10-09", "2026-10-10")},
			[]workout.Workout{prior}, "move:again",
			func(t *testing.T, f workout.Flags) {
				if f.MovedFrom != "2026-10-01" || f.MovedBy != workout.MovedByLife || len(f.AdaptedRules) != 1 {
					t.Errorf("flags = %+v", f)
				}
			}},
		{"ease", []Event{ill("2026-10-08", "2026-10-11", OptionProper)},
			rampWeek(), "ease:d1",
			func(t *testing.T, f workout.Flags) {
				if len(f.AdaptedRules) != 1 || f.AdaptedRules[0] != why.LifeEvent {
					t.Errorf("flags = %+v", f)
				}
			}},
		{"shorten", []Event{ill("2026-10-08", "2026-10-11", OptionProper)},
			rampWeek(), "shorten:d2",
			func(t *testing.T, f workout.Flags) {
				if len(f.AdaptedRules) != 1 {
					t.Errorf("flags = %+v", f)
				}
			}},
		{"a test moves without an adaptation", []Event{ill("2026-10-08", "2026-10-11", OptionProper)},
			[]workout.Workout{testWorkout("test", "2026-10-14")}, "move:test",
			func(t *testing.T, f workout.Flags) {
				if f.Adapted() || f.Origin != workout.OriginTest || f.MovedBy != workout.MovedByLife {
					t.Errorf("flags = %+v", f)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := preview(c.events, nil, c.ws...)
			ch := mustFind(t, d, c.id)
			var before workout.Workout
			for _, w := range c.ws {
				if w.ID == ch.WorkoutID {
					before = w
				}
			}
			c.check(t, assertUpdateFlags(t, c.id, before, ch.Update))
		})
	}
}

func TestGymIndoorRequestCarriesFlags(t *testing.T) {
	gym := travel("2026-10-08", "2026-10-09")
	gym.Option = OptionGym
	ws := []workout.Workout{built("wed", "2026-10-07", 30), hard("thu", "2026-10-08", 90), easy("fri", "2026-10-09", 180)}
	d := preview([]Event{gym}, nil, ws...)
	ch := mustFind(t, d, "indoor:fri")
	f := assertUpdateFlags(t, "indoor:fri", ws[2], ch.Update)
	if len(f.AdaptedRules) != 1 || f.AdaptedRules[0] != why.LifeEvent {
		t.Errorf("flags = %+v", f)
	}
}

func TestFlagsNeverAliasTheBeforeSlice(t *testing.T) {
	w := easy("x", "2026-10-09", 60)
	a := lifeAdaptedFlags(w)
	b := lifeAdaptedFlags(w)
	a.AdaptedRules[0] = why.Legacy
	if b.AdaptedRules[0] != why.LifeEvent {
		t.Error("two derived flags share a backing array")
	}
}
