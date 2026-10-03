// Package routefit says how long a rider takes on a route and how well the
// route's terrain suits a session. It is pure: points and numbers in, numbers
// and a plain sentence out, no I/O.
//
// Two callers share it so they cannot disagree about a route's time:
// generating a loop for a planned ride (which also judges terrain, see Fit)
// and scheduling a library route as a ride. A route's time is one function,
// EstimateSeconds.
package routefit

import (
	"math"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/pacing"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const (
	// FallbackKph is the flat speed assumed for a rider with no FTP.
	FallbackKph = 25.0
	// FallbackSecondsPerMetreAscent is what each metre of climbing adds when
	// there is no FTP to run the physics with.
	FallbackSecondsPerMetreAscent = 1.4

	// defaultActive and defaultEasy are the shares of FTP a step with no
	// power target of its own is assumed to ride at: an easy step (warmup,
	// cooldown, recovery, rest) and everything else (active, interval, open).
	defaultActive = 0.65
	defaultEasy   = 0.50
)

// Rider is what the physics needs to know about a rider. Zero values mean
// "has not said": no FTP falls back to a fixed speed, no weight to the same
// assumed mass pacing uses.
type Rider struct {
	FTP, WeightKg float64
}

// AverageFraction is the time-weighted mean fraction of FTP over steps. A
// step with a power target counts at its midpoint; any other step at 0.50
// (warmup, cooldown, recovery, rest) or 0.65 (everything else). Repeat blocks
// are multiplied out, and only time steps count: a distance or open step has
// no honest number of seconds to weight by (workout.PlannedSeconds's rule),
// so a list with no timed step reports 0 and the caller treats it as
// unknown.
//
// Power targets are absolute watts, so a fraction needs the FTP; with none
// they are treated as untargeted.
func AverageFraction(steps []workout.WorkoutStep, ftp float64) float64 {
	sum, secs := weighted(steps, ftp)
	if secs <= 0 {
		return 0
	}
	return sum / secs
}

func weighted(steps []workout.WorkoutStep, ftp float64) (sum, secs float64) {
	for _, s := range steps {
		if s.Repeat >= 2 {
			ws, ts := weighted(s.Steps, ftp)
			sum += float64(s.Repeat) * ws
			secs += float64(s.Repeat) * ts
			continue
		}
		if s.Duration != workout.DurationTime || s.Seconds <= 0 {
			continue
		}
		sum += stepFraction(s, ftp) * s.Seconds
		secs += s.Seconds
	}
	return sum, secs
}

func stepFraction(s workout.WorkoutStep, ftp float64) float64 {
	if ftp > 0 && s.Target == workout.TargetPower && s.TargetHigh > 0 {
		return (s.TargetLow + s.TargetHigh) / 2 / ftp
	}
	switch s.Intensity {
	case workout.IntensityWarmup, workout.IntensityCooldown, workout.IntensityRecovery, workout.IntensityRest:
		return defaultEasy
	default:
		return defaultActive
	}
}

// FlatSpeed is the speed on flat road in m/s at fraction of the rider's FTP.
// assumed says the number leans on something the rider did not give: no FTP
// (the fixed 25 km/h) or no weight (pacing's assumed mass).
func FlatSpeed(r Rider, fraction float64) (mps float64, assumed bool) {
	if r.FTP <= 0 {
		return FallbackKph / 3.6, true
	}
	return pacing.DefaultPhysics(r.WeightKg).Speed(r.FTP*fraction, 0), r.WeightKg <= 0
}

// EstimateSeconds is how long a rider takes on a route at fraction of FTP:
// the pacing physics over the route's smoothed segments, so a hilly loop
// honestly takes longer than a flat one of equal length. With no FTP it is
// the distance at 25 km/h plus 1.4 s per metre of ascent, flagged assumed.
// A route with no elevation is paced as flat. This is the one function a
// route's time comes from.
func EstimateSeconds(points []gpx.Point, r Rider, fraction float64) (sec float64, assumed bool) {
	if len(points) < 2 {
		return 0, r.FTP <= 0 || r.WeightKg <= 0
	}
	stats := gpx.ComputeStats(points)
	if r.FTP <= 0 {
		return stats.DistanceM/(FallbackKph/3.6) + FallbackSecondsPerMetreAscent*stats.AscentM, true
	}
	if fraction <= 0 {
		fraction = defaultActive
	}
	ph := pacing.DefaultPhysics(r.WeightKg)
	watts := r.FTP * fraction
	assumed = r.WeightKg <= 0
	segs := pacing.Segments(points, segMinM, segMaxM)
	if len(segs) == 0 {
		return stats.DistanceM / ph.Speed(watts, 0), assumed
	}
	return pacing.TotalSeconds(segs, ph, watts), assumed
}

// segMinM and segMaxM are the segment bounds the route demands and pacing
// code cut a profile with, so a time here and a time there read the same
// grades.
const (
	segMinM = 100
	segMaxM = 500
)

// pace converts a stretch of road into seconds at one effort: the physics
// when the power is known, the no-FTP fallback otherwise.
type pace struct {
	ph    pacing.Physics
	watts float64
	known bool
}

func (p pace) seconds(lengthM, grade float64) float64 {
	if p.known {
		return lengthM / p.ph.Speed(p.watts, grade)
	}
	gain := math.Max(0, lengthM*grade/100)
	return lengthM/(FallbackKph/3.6) + FallbackSecondsPerMetreAscent*gain
}
