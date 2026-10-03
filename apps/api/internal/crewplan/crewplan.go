// Package crewplan is the pure half of crew-aware planning: how long a crew
// ride is likely to take, what that does to the week around it, and which of
// two crew mates' long rides could share a day. It reads no store and writes
// nothing; internal/api gathers the inputs and applies the results. See
// docs/superpowers/specs/2026-09-29-crew-planning-design.md.
package crewplan

import (
	"math"

	"github.com/wncservices/domestique/apps/api/internal/model"
)

// Kind is what a crew ride is to the week it falls in.
type Kind string

const (
	// Long is at least two hours: the week's long ride, and a key session.
	Long Kind = "long"
	// Endurance is one to two hours: fixed, but not a key session.
	Endurance Kind = "endurance"
	// Short is under an hour: fixed and it takes its day, nothing else moves.
	Short Kind = "short"
)

const (
	longSeconds      = 2 * 3600
	enduranceSeconds = 3600

	// groupSpeedKph is the pace a group ride averages on the flat, stops not
	// included: about 23 km/h for a 100 km, 1000 m ride once climbing is added.
	groupSpeedKph = 26.0
	// hoursPerMetreAscent is half an hour for every 1000 m climbed.
	hoursPerMetreAscent = 0.5 / 1000
	maxHours            = 8.0

	// IntensityFactor is what a group ride is priced at: mostly endurance with
	// surges. Fixed, so a rider with no FTP still gets an estimate: TSS is
	// FTP-relative by definition, so the FTP cancels out.
	IntensityFactor = 0.65
)

// Estimate is what a crew ride is expected to take and cost. It is rough on
// purpose and the UI says "about".
type Estimate struct {
	Km, AscentM float64
	Hours       float64
	TSS         float64
	Kind        Kind
}

// EstimateRoute estimates a ride on a route from its length and ascent alone.
//
// A pacing-model estimate (internal/pacing) would be better for a rider with
// an FTP and a weight, but it needs the route's whole profile, and this is
// read for every upcoming ride of every crew on every page load. The cheap
// stats-only formula is the one the spec fixes; swapping in the model is a
// follow-up for when a route profile is cheap to get.
func EstimateRoute(stats model.RouteStats) Estimate {
	km := stats.DistanceM / 1000
	hours := km/groupSpeedKph + stats.AscentM*hoursPerMetreAscent
	hours = math.Min(math.Max(hours, 0), maxHours)
	return Estimate{
		Km: km, AscentM: stats.AscentM, Hours: hours,
		TSS:  hours * IntensityFactor * IntensityFactor * 100,
		Kind: KindForSeconds(math.Round(hours * 3600)),
	}
}

// Seconds is the estimated duration as a whole number of seconds, the length
// of the fixed session's single step.
func (e Estimate) Seconds() float64 { return math.Round(e.Hours * 3600) }

// KindForSeconds classifies a planned duration.
func KindForSeconds(seconds float64) Kind {
	switch {
	case seconds >= longSeconds:
		return Long
	case seconds >= enduranceSeconds:
		return Endurance
	default:
		return Short
	}
}

// TSSForSeconds prices a crew ride of the given planned length the way
// EstimateRoute does: hours x 0.65^2 x 100. Readers that only hold the stored
// session use it, so the forecast and the DTO agree on one number.
func TSSForSeconds(seconds float64) float64 {
	return seconds / 3600 * IntensityFactor * IntensityFactor * 100
}
