package api

import (
	"context"
	"database/sql"

	"github.com/muktihari/fit/profile/filedef"

	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/rideanalysis"
)

// SetRideAnalysisForTest swaps the ride scorer and the FIT decoder, so a test
// can make either panic the way a malformed file could. It returns the
// function that puts the real ones back.
func SetRideAnalysisForTest(analyze func(rideanalysis.Input) rideanalysis.Analysis, decode func([]byte) (*filedef.Activity, error)) (restore func()) {
	prevA, prevD := analyzeRide, decodeRideFIT
	if analyze != nil {
		analyzeRide = analyze
	}
	if decode != nil {
		decodeRideFIT = decode
	}
	return func() { analyzeRide, decodeRideFIT = prevA, prevD }
}

// HoldSchedulingLockForTest runs fn while holding the scheduling advisory lock
// the tick and Replan share, so an external test can make a request meet it
// held. It reports whether the lock was taken.
func HoldSchedulingLockForTest(ctx context.Context, db *sql.DB, fn func()) bool {
	return withDBLock(ctx, db, autoScheduleLockKey, fn)
}

// PurgeRiderDataForTest runs the rider purge, so an external test can check what
// it leaves behind.
func (s *Server) PurgeRiderDataForTest(ctx context.Context, rider string) (int, error) {
	sum, err := s.purgeRiderData(ctx, rider)
	return sum.CrewMemberships, err
}

// Limits the tests assert against.
const (
	MaxCrewsPerCreator = maxCrewsPerCreator
	MaxTogetherCrews   = maxTogetherCrews
)

// CrewsToRefreshForTest exposes which crews one ride-together poll refreshes.
func CrewsToRefreshForTest(snap crew.Snapshot, rider string) []crew.Crew {
	return crewsToRefresh(snap, rider)
}
