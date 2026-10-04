package api

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/routing"
)

var candidatesT0 = time.Date(2026, 10, 3, 8, 0, 0, 0, time.FixedZone("CEST", 2*3600))

func testPath() routing.Path {
	return routing.Path{Points: []gpx.Point{{Lat: 50, Lon: 4, HasEle: true}, {Lat: 50.01, Lon: 4, HasEle: true}}}
}

func heldOf(workoutID string, n int) []heldCandidate {
	out := make([]heldCandidate, n)
	for i := range out {
		out[i] = heldCandidate{WorkoutID: workoutID, Path: testPath(), EstimatedSeconds: float64(1000 + i)}
	}
	return out
}

func newTestCandidates(now *time.Time) *candidateStore {
	return newCandidateStore(func() time.Time { return *now })
}

func TestCandidatesAreHeldPerRiderAndIdentified(t *testing.T) {
	now := candidatesT0
	s := newTestCandidates(&now)
	ids := s.Hold("wilant", "w1", heldOf("w1", 3))
	if len(ids) != 3 || ids[0] == ids[1] || ids[0] == "" {
		t.Fatalf("ids = %v, want three distinct non-empty ids", ids)
	}
	got, ok := s.Get("wilant", ids[1])
	if !ok || got.WorkoutID != "w1" || got.EstimatedSeconds != 1001 {
		t.Fatalf("Get = %+v, %v, want the second candidate", got, ok)
	}
	if _, ok := s.Get("marie", ids[1]); ok {
		t.Error("another rider could read the candidate")
	}
	if _, ok := s.Get("WILANT ", ids[1]); !ok {
		t.Error("the rider key is not normalised the way every other rider key is")
	}
}

func TestCandidateIDsCannotBeGuessed(t *testing.T) {
	now := candidatesT0
	s := newTestCandidates(&now)
	a := s.Hold("wilant", "w1", heldOf("w1", 1))[0]
	b := s.Hold("wilant", "w2", heldOf("w2", 1))[0]
	if len(a) < 16 || a == b {
		t.Errorf("ids %q and %q: want long, unique", a, b)
	}
}

func TestCandidatesExpireAfterThirtyMinutes(t *testing.T) {
	now := candidatesT0
	s := newTestCandidates(&now)
	id := s.Hold("wilant", "w1", heldOf("w1", 1))[0]

	now = candidatesT0.Add(29*time.Minute + 59*time.Second)
	if _, ok := s.Get("wilant", id); !ok {
		t.Error("expired before thirty minutes")
	}
	now = candidatesT0.Add(30*time.Minute + time.Second)
	if _, ok := s.Get("wilant", id); ok {
		t.Error("still held after thirty minutes")
	}
}

func TestCandidatesAreCappedAtTwelvePerRiderOldestFirst(t *testing.T) {
	now := candidatesT0
	s := newTestCandidates(&now)
	var first []string
	for i := 0; i < 5; i++ {
		ids := s.Hold("wilant", string(rune('a'+i)), heldOf(string(rune('a'+i)), 3))
		if i == 0 {
			first = ids
		}
		now = now.Add(time.Minute)
	}
	if n := s.Count("wilant"); n != 12 {
		t.Errorf("held %d, want the cap of 12", n)
	}
	if _, ok := s.Get("wilant", first[0]); ok {
		t.Error("the oldest candidate survived the cap")
	}
}

func TestHoldingAgainForTheSameWorkoutReplacesItsEarlierCandidates(t *testing.T) {
	now := candidatesT0
	s := newTestCandidates(&now)
	old := s.Hold("wilant", "w1", heldOf("w1", 3))
	fresh := s.Hold("wilant", "w1", heldOf("w1", 3))
	if _, ok := s.Get("wilant", old[0]); ok {
		t.Error("an earlier generation for the same workout is still held")
	}
	if _, ok := s.Get("wilant", fresh[0]); !ok {
		t.Error("the new generation is not held")
	}
	if n := s.Count("wilant"); n != 3 {
		t.Errorf("held %d, want 3", n)
	}
}

func TestForgetDropsEverythingOfOneRider(t *testing.T) {
	now := candidatesT0
	s := newTestCandidates(&now)
	mine := s.Hold("wilant", "w1", heldOf("w1", 3))
	theirs := s.Hold("marie", "w2", heldOf("w2", 3))
	s.Forget(" Wilant")
	if _, ok := s.Get("wilant", mine[0]); ok {
		t.Error("a forgotten rider's candidate is still held")
	}
	if _, ok := s.Get("marie", theirs[0]); !ok {
		t.Error("forgetting one rider dropped another's")
	}
}

func TestDropForWorkoutKeepsTheRidersOthers(t *testing.T) {
	now := candidatesT0
	s := newTestCandidates(&now)
	a := s.Hold("wilant", "w1", heldOf("w1", 2))
	b := s.Hold("wilant", "w2", heldOf("w2", 2))
	s.DropWorkout("wilant", "w1")
	if _, ok := s.Get("wilant", a[0]); ok {
		t.Error("candidates for the dropped workout survived")
	}
	if _, ok := s.Get("wilant", b[0]); !ok {
		t.Error("candidates for another workout were dropped")
	}
}
