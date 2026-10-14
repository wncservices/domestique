package testschedule

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A plan-made session is a day a test may replace. One the rider chose to keep
// as planned is theirs: the suggester must not offer its day, or the apply
// (which refuses to delete it) would be offered something it cannot do.
func TestASuggestionSkipsADayHoldingAKeptSession(t *testing.T) {
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	profile := workout.RiderProfile{FTPWatts: 250, FTPVerifiedAt: "2026-06-01"}
	base := Input{Profile: profile, HasPower: true, Now: now}
	plain := Suggest(base)
	if plain == nil {
		t.Fatal("setup: no suggestion for a stale FTP")
	}

	for name, marker := range map[string]string{"crew": scheduler.CrewKeptMarker, "life event": scheduler.LifeKeptMarker} {
		t.Run(name, func(t *testing.T) {
			session := func(desc string) workout.Workout {
				return workout.Workout{
					ID: "s", Rider: "wilant", Sport: model.SportCycling, Name: "Endurance ride", Date: plain.Date, GoalID: "g",
					Zone: workout.ZoneEndurance, Description: desc,
				}
			}
			// Control: the same session, not kept, is replaceable on that day.
			in := base
			in.Upcoming = []workout.Workout{session(scheduler.GeneratedDescription)}
			if got := Suggest(in); got == nil || got.Date != plain.Date || got.ReplacesWorkoutID != "s" {
				t.Fatalf("control: suggestion = %+v, want the plan-made session's day replaced", got)
			}

			in.Upcoming = []workout.Workout{session(scheduler.GeneratedDescription + " " + marker)}
			got := Suggest(in)
			if got == nil {
				t.Fatal("no suggestion at all")
			}
			if got.Date == plain.Date || got.ReplacesWorkoutID == "s" {
				t.Errorf("suggested %+v: the kept session's day", got)
			}
		})
	}
}
