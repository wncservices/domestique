// Package pacing turns a route's profile and a rider's FTP into a race-day
// pacing plan. It is pure: numbers in, numbers out. It sees a route only as
// a list of (length, grade) pieces, never a coordinate, so nothing built on
// it can leak where a route goes.
package pacing

import (
	"math"
	"sort"

	"github.com/wncservices/domestique/apps/api/internal/climbs"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
)

// Physics is the steady-state power balance every "watts to speed"
// calculator shares: power at the wheel equals the force of gravity, rolling
// resistance and drag, times speed. Defaults are for a road bike on good
// asphalt with no wind; a rider's weight is the only per-rider input today.
type Physics struct {
	// MassKg is the rider; BikeKg the bike and kit, added to it.
	MassKg, BikeKg float64
	// CdA is drag area in m^2 (road bike, hands on the hoods).
	CdA float64
	// Crr is the rolling resistance coefficient.
	Crr float64
	// Rho is air density in kg/m^3 (about 20 C at low altitude).
	Rho float64
	// Eta is drivetrain efficiency: the share of pedal power that reaches the
	// wheel.
	Eta float64
	// MaxDescentKph caps speed on a descent: braking and corners mean a real
	// descent is not free-running.
	MaxDescentKph float64
}

// AssumedMassKg is used when the rider has not entered a weight. The plan
// says so wherever it shows a time.
const AssumedMassKg = 75.0

// DefaultPhysics returns the defaults for a rider of massKg. Zero (or less)
// means the rider has not said, and uses AssumedMassKg.
func DefaultPhysics(massKg float64) Physics {
	if massKg <= 0 {
		massKg = AssumedMassKg
	}
	return Physics{
		MassKg: massKg, BikeKg: 8, CdA: 0.32, Crr: 0.005,
		Rho: 1.20, Eta: 0.975, MaxDescentKph: 60,
	}
}

const (
	gravity = 9.80665
	// minSpeed keeps a stalled climb finite: a rider with no power on a
	// climb would otherwise have infinite time. 1 m/s is walking pace.
	minSpeed = 1.0
)

// Speed returns the steady speed in m/s at watts on a constant grade in
// percent, solved by bisection (the balance is monotonic in v once past the
// coasting speed of a descent). It is always finite and positive: power at or
// below zero coasts on a descent and crawls on a climb.
func (p Physics) Speed(watts, grade float64) float64 {
	theta := math.Atan(grade / 100)
	force := (p.MassKg + p.BikeKg) * gravity * (math.Sin(theta) + p.Crr*math.Cos(theta))
	drag := 0.5 * p.Rho * p.CdA
	wheel := math.Max(watts, 0) * p.Eta

	lo, hi := 0.0, 60.0
	for i := 0; i < 80; i++ {
		mid := (lo + hi) / 2
		if (force+drag*mid*mid)*mid < wheel {
			lo = mid
		} else {
			hi = mid
		}
	}
	v := (lo + hi) / 2
	if grade < 0 {
		if limit := p.MaxDescentKph / 3.6; v > limit {
			v = limit
		}
	}
	return math.Max(v, minSpeed)
}

// Seg is a stretch of the route at a constant grade: where it starts and
// ends along the track in metres, and the grade in percent. Distances and a
// grade only, no coordinates.
type Seg struct {
	StartM, EndM, Grade float64
}

// Segments cuts the smoothed profile into pieces of at least minM and at most
// maxM, each at one grade. A piece ends early where the next stretch of road
// differs by more than a couple of percentage points, so a rider reads where
// a climb really starts. Every point must carry elevation, else nothing is
// returned.
func Segments(points []gpx.Point, minM, maxM float64) []Seg {
	if len(points) < 2 || minM <= 0 || maxM < 2*minM {
		return nil
	}
	for _, pt := range points {
		if !pt.HasEle {
			return nil
		}
	}
	dist := climbs.Distances(points)
	total := dist[len(dist)-1]
	if total <= 0 {
		return nil
	}
	ele := climbs.SmoothedElevation(points, 50)

	eleAt := func(x float64) float64 {
		i := sort.SearchFloat64s(dist, x)
		switch {
		case i <= 0:
			return ele[0]
		case i >= len(dist):
			return ele[len(ele)-1]
		}
		span := dist[i] - dist[i-1]
		if span <= 0 {
			return ele[i]
		}
		f := (x - dist[i-1]) / span
		return ele[i-1] + f*(ele[i]-ele[i-1])
	}
	grade := func(a, b float64) float64 { return (eleAt(b) - eleAt(a)) / (b - a) * 100 }

	const step, split = 50.0, 2.0
	var out []Seg
	for start := 0.0; start < total-1e-9; {
		var end float64
		if total-start <= maxM {
			end = total
		} else {
			end = start + minM
			run := grade(start, end)
			for end+step <= start+maxM {
				if math.Abs(grade(end, end+step)-run) > split {
					break
				}
				end += step
				run = grade(start, end)
			}
			if total-end < minM {
				end = total - minM
			}
		}
		out = append(out, Seg{StartM: start, EndM: end, Grade: grade(start, end)})
		start = end
	}
	return out
}

// ClimbSeconds is how long climb c takes at a constant watts through the
// physics, reading the grade of each segment the climb overlaps.
func ClimbSeconds(c climbs.Climb, seg []Seg, ph Physics, watts float64) float64 {
	var sec float64
	for _, s := range seg {
		lo, hi := math.Max(s.StartM, c.StartM), math.Min(s.EndM, c.EndM)
		if hi <= lo {
			continue
		}
		sec += (hi - lo) / ph.Speed(watts, s.Grade)
	}
	return sec
}
