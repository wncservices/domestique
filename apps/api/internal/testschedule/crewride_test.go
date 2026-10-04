package testschedule

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func crewRideOn(date string, seconds float64) workout.Workout {
	return workout.Workout{
		ID: "crew-" + date, Rider: "wilant", Sport: model.SportCycling, Name: "Crew ride: Hill Loop", Date: date, GoalID: "g",
		CrewRideID: "ride-" + date, Zone: workout.ZoneEndurance, Description: "Crew ride with Sunday Club.",
		Steps: []workout.WorkoutStep{{Name: "Crew ride", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: seconds, Target: workout.TargetOpen}},
	}
}

// With no plan, the suggestion takes the first available day from tomorrow. A
// crew ride on that day takes it, and a long one makes the day after it a
// key-session follow-up that a hard test must not land on.
func TestASuggestionNeverLandsOnACrewRideDayOrTheDayAfterALongOne(t *testing.T) {
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, time.FixedZone("CEST", 2*3600)) // Wednesday
	profile := workout.RiderProfile{FTPWatts: 250}                            // no FTP check, nag-free: stale below
	profile.FTPVerifiedAt = "2026-06-01"                                      // a stale FTP makes a test worth suggesting
	base := Input{Profile: profile, HasPower: true, Now: now}

	plain := Suggest(base)
	if plain == nil {
		t.Fatal("setup: no suggestion for a stale FTP")
	}
	// Put a long crew ride on the day the plain suggestion would have used.
	in := base
	in.Upcoming = []workout.Workout{crewRideOn(plain.Date, 4*3600)}
	got := Suggest(in)
	if got == nil {
		t.Fatal("no suggestion at all once a day is taken")
	}
	after := dayAfter(plain.Date)
	if got.Date == plain.Date || got.Date == after {
		t.Errorf("suggested %s: that is the crew ride's day (%s) or the day after a long ride (%s)", got.Date, plain.Date, after)
	}

	// An endurance-length ride takes its day but does not make the next one off limits.
	in.Upcoming = []workout.Workout{crewRideOn(plain.Date, 90*60)}
	got = Suggest(in)
	if got == nil || got.Date == plain.Date {
		t.Errorf("suggested %+v, want a day other than the ride's", got)
	}
}

func dayAfter(date string) string {
	d, _ := time.Parse("2006-01-02", date)
	return d.AddDate(0, 0, 1).Format("2006-01-02")
}
