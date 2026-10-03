package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/routing"
)

const (
	// candidateTTL is how long a generated loop is held for its rider to
	// choose: long enough to look at three maps, short enough that nothing
	// sits in memory for a day.
	candidateTTL = 30 * time.Minute
	// maxCandidatesPerRider bounds what one rider can leave in memory: a
	// week of workouts' worth of three.
	maxCandidatesPerRider = 12
)

// heldCandidate is one generated loop, kept on the server so that saving it
// builds the GPX from the server's own path, with its elevation, and never
// from coordinates the browser posts back. It is the rider's own location
// data held in memory for a short while: never logged, never written to
// disk, dropped when the rider is removed.
type heldCandidate struct {
	ID               string
	WorkoutID        string
	Path             routing.Path
	EstimatedSeconds float64
	DistanceM        float64
	AscentM          float64
	At               time.Time
}

// candidateStore holds loops in memory keyed by rider and id. It is not
// shared between replicas and does not survive a restart: a lost candidate is
// a 410 and the rider generates again.
type candidateStore struct {
	now func() time.Time

	mu      sync.Mutex
	byRider map[string][]heldCandidate
}

func newCandidateStore(now func() time.Time) *candidateStore {
	return &candidateStore{now: now, byRider: map[string][]heldCandidate{}}
}

// candidateStore is the server's one store of generated loops.
func (s *Server) candidateStore() *candidateStore {
	s.candidatesOnce.Do(func() { s.candidates = newCandidateStore(s.now) })
	return s.candidates
}

func candidateKey(rider string) string { return strings.ToLower(strings.TrimSpace(rider)) }

func newCandidateID() string {
	b := make([]byte, 16)
	// crypto/rand: an id is the only thing standing between one rider's
	// location data and a guess.
	if _, err := rand.Read(b); err != nil {
		panic("candidates: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// pruneLocked drops what has expired.
func (s *candidateStore) pruneLocked(key string) {
	cutoff := s.now().Add(-candidateTTL)
	kept := s.byRider[key][:0]
	for _, c := range s.byRider[key] {
		if c.At.After(cutoff) {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		delete(s.byRider, key)
		return
	}
	s.byRider[key] = kept
}

// Hold stores items as the rider's candidates for workoutID, replacing any
// earlier generation for the same workout, and returns their ids in order.
// Past maxCandidatesPerRider the oldest go first.
func (s *candidateStore) Hold(rider, workoutID string, items []heldCandidate) []string {
	key := candidateKey(rider)
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneLocked(key)
	kept := s.byRider[key][:0]
	for _, c := range s.byRider[key] {
		if c.WorkoutID != workoutID {
			kept = append(kept, c)
		}
	}
	now := s.now()
	ids := make([]string, len(items))
	for i, c := range items {
		c.ID, c.WorkoutID, c.At = newCandidateID(), workoutID, now
		ids[i] = c.ID
		kept = append(kept, c)
	}
	if over := len(kept) - maxCandidatesPerRider; over > 0 {
		kept = append([]heldCandidate(nil), kept[over:]...)
	}
	s.byRider[key] = kept
	return ids
}

// Get returns the rider's candidate by id; ok is false for an unknown,
// expired or another rider's id, which the caller answers as 410.
func (s *candidateStore) Get(rider, id string) (heldCandidate, bool) {
	key := candidateKey(rider)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(key)
	for _, c := range s.byRider[key] {
		if c.ID == id {
			return c, true
		}
	}
	return heldCandidate{}, false
}

// Take is Get that also removes the candidate, under one lock: of two
// concurrent saves of the same candidate, exactly one gets it.
func (s *candidateStore) Take(rider, id string) (heldCandidate, bool) {
	key := candidateKey(rider)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(key)
	held := s.byRider[key]
	for i, c := range held {
		if c.ID == id {
			s.byRider[key] = append(held[:i:i], held[i+1:]...)
			if len(s.byRider[key]) == 0 {
				delete(s.byRider, key)
			}
			return c, true
		}
	}
	return heldCandidate{}, false
}

// Sweep drops every expired candidate of every rider and says how many went.
// Expiry is otherwise only checked when a rider's own candidates are read, so
// a rider who never comes back would leave their loops in memory.
func (s *candidateStore) Sweep() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	dropped := 0
	for key := range s.byRider {
		before := len(s.byRider[key])
		s.pruneLocked(key)
		dropped += before - len(s.byRider[key])
	}
	return dropped
}

// candidateSweepEvery is how often RunCandidateJanitor sweeps.
const candidateSweepEvery = 5 * time.Minute

// RunCandidateJanitor sweeps expired route candidates until ctx ends, so
// location data held for a rider who walked away does not linger.
func (s *Server) RunCandidateJanitor(ctx context.Context) {
	t := time.NewTicker(candidateSweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := s.candidateStore().Sweep(); n > 0 {
				s.logger().Info("expired route candidates dropped", "count", n)
			}
		}
	}
}

// Count is how many unexpired candidates the rider has.
func (s *candidateStore) Count(rider string) int {
	key := candidateKey(rider)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(key)
	return len(s.byRider[key])
}

// DropWorkout forgets the rider's candidates for one workout: once one is
// chosen the rest are not wanted, and a workout that went indoor or was
// deleted has no use for them.
func (s *candidateStore) DropWorkout(rider, workoutID string) {
	key := candidateKey(rider)
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.byRider[key][:0]
	for _, c := range s.byRider[key] {
		if c.WorkoutID != workoutID {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		delete(s.byRider, key)
		return
	}
	s.byRider[key] = kept
}

// Forget drops everything held for a rider: a removed rider's location data
// must not outlive them in memory.
func (s *candidateStore) Forget(rider string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byRider, candidateKey(rider))
}
