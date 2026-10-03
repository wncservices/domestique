package adapter

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func planned2(id, name, date string) []workout.Workout {
	return []workout.Workout{planned(id, name, date)}
}

// Adaptation never makes a session up on a day a life event covers, and a
// session a life event took away is never read as missed: it does not exist.

func blackout(days ...string) map[string]bool {
	out := map[string]bool{}
	for _, d := range days {
		out[d] = true
	}
	return out
}

func TestAMissedSessionIsNotMadeUpOnAnEventDay(t *testing.T) {
	ws := planned2("tue", "VO2max intervals", "2026-03-17")
	got := AdaptSessionsAround(ws, nil, profileAvailable("tue", "fri", "sat"), thursday, nil, readiness.Assessment{}, blackout("2026-03-20"))
	if len(got) != 1 || got[0].NewDate != "2026-03-21" {
		t.Fatalf("changes = %+v, want the make-up on Saturday: Friday is inside a life event", got)
	}
}

func TestAMissedSessionDoesNotTakeAnEasyDayInsideAnEvent(t *testing.T) {
	ws := append(planned2("tue", "Tempo ride", "2026-03-17"),
		planned("fri", "Endurance ride", "2026-03-20"),
		planned("sat", "Endurance ride", "2026-03-21"),
		planned("sun", "Long ride", "2026-03-22"),
	)
	got := AdaptSessionsAround(ws, nil, profileAvailable("tue", "fri", "sat", "sun"), thursday, nil, readiness.Assessment{}, blackout("2026-03-20"))
	if len(got) != 1 || got[0].ReplaceWorkoutID != "sat" {
		t.Fatalf("changes = %+v, want Tuesday's tempo to take Saturday's easy ride, not Friday's (inside an event)", got)
	}
}

func TestASessionOnAnEventDayIsNeverMissed(t *testing.T) {
	ws := planned2("tue", "VO2max intervals", "2026-03-17")
	got := AdaptSessionsAround(ws, nil, profileAvailable("fri"), thursday, nil, readiness.Assessment{}, blackout("2026-03-17"))
	if len(got) != 0 {
		t.Fatalf("changes = %+v, want none: Tuesday was an event day", got)
	}
}

func TestWithoutABlackoutNothingChanges(t *testing.T) {
	ws := planned2("tue", "VO2max intervals", "2026-03-17")
	a := AdaptSessions(ws, nil, profileAvailable("fri"), thursday, nil, readiness.Assessment{})
	b := AdaptSessionsAround(ws, nil, profileAvailable("fri"), thursday, nil, readiness.Assessment{}, nil)
	if len(a) != 1 || len(b) != 1 || a[0].WorkoutID != b[0].WorkoutID || a[0].NewDate != b[0].NewDate || a[0].Reason != b[0].Reason {
		t.Fatalf("AdaptSessions and AdaptSessionsAround(nil) disagree: %+v vs %+v", a, b)
	}
}
