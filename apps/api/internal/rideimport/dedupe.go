package rideimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Outcome is what saving one ride did.
type Outcome int

const (
	// Added means the ride is now a session with an analysis.
	Added Outcome = iota
	// AlreadyHere means a session for it exists, from a provider sync or from
	// an earlier import, and nothing was written.
	AlreadyHere
)

// The tolerances a provider's summary and an export file are allowed to
// differ by and still be the same ride. A provider keeps its own rounded
// duration and average, and a day rather than a start time.
const (
	durationFloorSeconds = 60
	durationFraction     = 0.02
	powerFraction        = 0.03
)

// Duplicate reports whether r is already among candidates: the rider's
// sessions of the same sport on r's day or the day either side (timezone
// slack, since a provider stores its own idea of the day). Same sport, and
// duration within max(60 s, 2 %), and, when both have power, average power
// within 3 %. The percentages are of the larger of the two numbers, so the
// answer does not depend on which side is asked.
//
// Two real rides of near-identical length and power on one day are the false
// positive this accepts: the cost is one under-counted ride in a bulk import,
// not corrupted data.
func Duplicate(r Ride, candidates []workout.CompletedSession) bool {
	day, err := time.Parse("2006-01-02", r.Date)
	if err != nil {
		return false
	}
	for _, c := range candidates {
		if c.Sport != r.Sport {
			continue
		}
		cd, err := time.Parse("2006-01-02", c.Date)
		if err != nil {
			continue
		}
		if gap := cd.Sub(day); gap < -24*time.Hour || gap > 24*time.Hour {
			continue
		}
		if !sameLength(r.Duration, c.DurationSeconds) && !sameLength(r.Elapsed, c.DurationSeconds) {
			continue
		}
		if r.AvgPower > 0 && c.AvgPowerWatts > 0 && math.Abs(r.AvgPower-c.AvgPowerWatts) > powerFraction*math.Max(r.AvgPower, c.AvgPowerWatts) {
			continue
		}
		return true
	}
	return false
}

func sameLength(a, b float64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	return math.Abs(a-b) <= math.Max(durationFloorSeconds, durationFraction*math.Max(a, b))
}

// NearbyDates is the day before, the day itself and the day after, the dates a
// provider might have filed the same ride under. Empty for a bad date.
func NearbyDates(date string) []string {
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil
	}
	return []string{
		day.AddDate(0, 0, -1).Format("2006-01-02"),
		date,
		day.AddDate(0, 0, 1).Format("2006-01-02"),
	}
}

// Provider is the completed_sessions provider an imported ride is filed
// under, and so the "source = import" marker: no new column needed.
const Provider = "import"

// ExternalID is a ride's key under Provider: its UTC start in seconds, which
// makes re-uploading the same file idempotent through the table's existing
// UNIQUE (provider, external_id). The rider is folded in because that key is
// global: two riders on one group ride whose head units started in the same
// second would otherwise be one row, and the second would take the first's.
// A short hash rather than the name, so an id says nothing about who rode.
func ExternalID(rider string, startUnix int64) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(rider))))
	return fmt.Sprintf("%d-%s", startUnix, hex.EncodeToString(sum[:4]))
}

// Store is what saving needs of the training store.
type Store interface {
	FindSimilarSession(ctx context.Context, rider, sport string, dates []string) ([]workout.CompletedSession, error)
	UpsertSession(ctx context.Context, req workout.UpsertSessionRequest) (workout.CompletedSession, error)
	SaveAnalysis(ctx context.Context, a workout.SessionAnalysis) error
}

// Save files one ride for rider: a completed session and its analysis, the
// same rows a synced ride ends up with, so readiness, threshold detection and
// the projection see it. It writes nothing for a ride that is already there,
// and never overwrites a provider's row (its summary numbers are better).
//
// An import is a record of the past: it is never matched to a planned workout
// and never moves a progression level, which would re-score last year's
// sessions against this year's plan. The analysis is saved for every ride, not
// only the recent ones sync analyses, because threshold detection's history
// rule reads older power curves, which is most of why anyone imports.
func Save(ctx context.Context, st Store, rider string, r Ride) (Outcome, error) {
	similar, err := st.FindSimilarSession(ctx, rider, r.Sport, NearbyDates(r.Date))
	if err != nil {
		return 0, err
	}
	// The exact key first: the same file again, or the same ride from another
	// export, is the same row whatever the tolerances say.
	id := ExternalID(rider, r.StartUnix)
	for _, c := range similar {
		if c.Provider == Provider && c.ExternalID == id {
			return AlreadyHere, nil
		}
	}
	if Duplicate(r, similar) {
		return AlreadyHere, nil
	}

	sess, err := st.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: rider, Provider: Provider, ExternalID: id, Sport: r.Sport, Date: r.Date,
		DurationSeconds: r.Duration, DistanceM: r.Distance, AvgHR: r.AvgHR, AvgPowerWatts: r.AvgPower,
		// The load a synced ride settles on once it is analysed, so the
		// fitness chart does not read an import differently from its neighbours.
		TrainingLoad: r.Analysis.Load,
	})
	if err != nil {
		return 0, err
	}

	a := r.Analysis
	if err := st.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: sess.ID, Rider: rider,
		Outcome: string(a.Outcome), LoadSource: string(a.LoadSource),
		NormalizedPower: a.NormalizedPower, IntensityFactor: a.IntensityFactor, TSS: a.TSS, DurationRatio: a.DurationRatio,
		MaxHR: a.MaxHR, BestHR1200: a.BestHR1200, BestSpeed1200: a.BestSpeed1200, BestSpeed1800: a.BestSpeed1800,
		PowerZoneSeconds: a.PowerZoneSeconds[:], HRZoneSeconds: a.HRZoneSeconds[:],
		PowerCurve: powerCurve(a.PowerCurve),
	}); err != nil {
		return 0, err
	}
	return Added, nil
}

// powerCurve converts Analyze's int-keyed curve to the string-keyed shape the
// store keeps, JSON object keys being strings.
func powerCurve(curve map[int]float64) map[string]float64 {
	if len(curve) == 0 {
		return nil
	}
	out := make(map[string]float64, len(curve))
	for seconds, watts := range curve {
		out[fmt.Sprint(seconds)] = watts
	}
	return out
}
