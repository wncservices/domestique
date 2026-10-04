package api

import (
	"sync"
	"testing"
	"time"
)

func TestTakeGivesACandidateToExactlyOneOfManyConcurrentSavers(t *testing.T) {
	now := candidatesT0
	s := newTestCandidates(&now)
	id := s.Hold("wilant", "w1", heldOf("w1", 1))[0]

	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := s.Take("wilant", id); ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("%d savers got the candidate, want exactly 1", wins)
	}
	if _, ok := s.Get("wilant", id); ok {
		t.Error("a taken candidate is still held")
	}
}

func TestTakeLeavesTheRidersOtherCandidatesAndHonoursExpiry(t *testing.T) {
	now := candidatesT0
	s := newTestCandidates(&now)
	ids := s.Hold("wilant", "w1", heldOf("w1", 3))
	if _, ok := s.Take("wilant", ids[1]); !ok {
		t.Fatal("could not take a held candidate")
	}
	if s.Count("wilant") != 2 {
		t.Errorf("held %d after one take, want the other 2", s.Count("wilant"))
	}
	if _, ok := s.Take("marie", ids[0]); ok {
		t.Error("another rider took the candidate")
	}
	now = candidatesT0.Add(31 * time.Minute)
	if _, ok := s.Take("wilant", ids[0]); ok {
		t.Error("an expired candidate was taken")
	}
}

func TestSweepDropsExpiredCandidatesOfRidersWhoNeverComeBack(t *testing.T) {
	now := candidatesT0
	s := newTestCandidates(&now)
	s.Hold("wilant", "w1", heldOf("w1", 3))
	now = candidatesT0.Add(20 * time.Minute)
	s.Hold("marie", "w2", heldOf("w2", 2))

	now = candidatesT0.Add(35 * time.Minute) // wilant's are 35 minutes old, marie's 15
	if n := s.Sweep(); n != 3 {
		t.Errorf("swept %d, want wilant's 3", n)
	}
	s.mu.Lock()
	_, wilantHeld := s.byRider["wilant"]
	_, marieHeld := s.byRider["marie"]
	s.mu.Unlock()
	if wilantHeld || !marieHeld {
		t.Errorf("after the sweep wilant held=%v marie held=%v, want false and true: the map itself must not keep an expired rider", wilantHeld, marieHeld)
	}
}
