package pacing

import (
	"math"

	"github.com/wncservices/domestique/apps/api/internal/climbs"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Why a plan could not be built.
const (
	ReasonNoFTP     = "no_ftp"
	ReasonNoProfile = "no_profile"
)

// Segment kinds.
const (
	KindClimb   = "climb"
	KindFlat    = "flat"
	KindDescent = "descent"
)

const (
	// descentGrade is where a descent starts: steeper than this, a rider
	// coasts and recovers instead of pedalling to a target.
	descentGrade = -3.0
	// descentFTP is the descent power, a share of FTP: soft pedal, recover.
	descentFTP = 0.55
	// targetBand is the +/- band around a segment's power.
	targetBand = 0.03
)

// Input is everything a plan is built from. Segs and Climbs describe the
// route (distances and grades only, no coordinates); the rest is the rider.
type Input struct {
	Segs []Seg
	// Climbs are from climbs.Detect at DeviceConfig, so short ramps are covered
	// too, not only the climbs training rehearses.
	Climbs  []climbs.Climb
	FTP     float64
	IF      float64
	Phys    Physics
	Profile workout.RiderProfile
}

// Segment is one line of the pacing plan: a stretch of one kind with a target.
type Segment struct {
	StartM, EndM float64
	Kind         string
	// ClimbIndex is which climb a climb segment belongs to (climbs.Climb.Index),
	// -1 for flat and descent.
	ClimbIndex int
	// Gradient is the average over the stretch, in percent.
	Gradient float64
	// Watts is the exact target the physics timeline was run at;
	// WattsLow and WattsHigh are what a rider reads: +/-3 %, rounded to 5 W.
	Watts               float64
	WattsLow, WattsHigh int
	// HRLow and HRHigh are the steady-state heart rate for that effort, 0 when
	// the rider has neither a threshold nor a max HR.
	HRLow, HRHigh int
	SpeedKph      float64
	Seconds       float64
}

// ClimbTarget is what the plan asks of one climb.
type ClimbTarget struct {
	Index          int
	StartM, EndM   float64
	LengthM        float64
	AvgGradient    float64
	Watts, Seconds float64
	// HRLow and HRHigh are the steady-state heart rate for Watts, 0 when the
	// rider has no HR data.
	HRLow, HRHigh int
}

// Plan is the pacing plan: where to ride at what, and what it adds up to.
type Plan struct {
	// Reason is set (and nothing else is) when no plan could be built.
	Reason   string
	Segments []Segment
	Climbs   []ClimbTarget
	// Seconds is the expected time; NormalizedW, AvgW, IF, AvgKph and VI are
	// the totals the timeline gives.
	Seconds     float64
	NormalizedW float64
	AvgW        float64
	IF          float64
	AvgKph      float64
	VI          float64
}

// leg is one profile segment with its role and power in a candidate plan.
type leg struct {
	seg   Seg
	kind  string
	climb int
	watts float64
	secs  float64
}

// Build makes a gradient-aware pacing plan.
//
// Relative to a flat-level power Pf: a climb is ridden at Pf x 1.10 under 5
// minutes, x 1.05 for 5 to 20, x 1.00 beyond (and never above FTP x the same
// factor); flat and rolling road at Pf; a descent steeper than -3 % at 0.55 x
// FTP. Pf is solved by bisection so the plan's normalised power equals
// FTP x IF: the same NP a rider would hold on a flat course, spent where it
// buys time. Constant power is the wrong answer on a hilly course because
// speed is low on a climb, so an extra watt there buys more seconds than on
// the flat, and coasting a descent loses almost nothing.
func Build(in Input) Plan {
	if in.FTP <= 0 {
		return Plan{Reason: ReasonNoFTP}
	}
	if len(in.Segs) == 0 || in.IF <= 0 {
		return Plan{Reason: ReasonNoProfile}
	}
	ph := in.Phys
	if ph.MassKg <= 0 {
		ph = DefaultPhysics(0)
	}
	legs := classify(in.Segs, in.Climbs)
	target := in.FTP * in.IF

	lo, hi := 0.2*in.FTP, 1.6*in.FTP
	if np, _ := evaluate(legs, in, ph, hi); np <= target {
		lo = hi
	} else {
		for i := 0; i < 50; i++ {
			mid := (lo + hi) / 2
			if np, _ := evaluate(legs, in, ph, mid); np < target {
				lo = mid
			} else {
				hi = mid
			}
		}
	}
	pf := lo
	np, cw := evaluate(legs, in, ph, pf)

	plan := Plan{NormalizedW: np}
	var dist, energy float64
	for _, p := range legs {
		plan.Seconds += p.secs
		energy += p.watts * p.secs
		dist += p.seg.EndM - p.seg.StartM
	}
	if plan.Seconds > 0 {
		plan.AvgW = energy / plan.Seconds
		plan.AvgKph = dist / plan.Seconds * 3.6
	}
	plan.IF = np / in.FTP
	if plan.AvgW > 0 {
		plan.VI = np / plan.AvgW
	}
	plan.Segments = merge(legs, in)
	for _, c := range in.Climbs {
		ct := ClimbTarget{
			Index: c.Index, StartM: c.StartM, EndM: c.EndM, LengthM: c.LengthM, AvgGradient: c.AvgGradient,
			Watts: cw[c.Index], Seconds: ClimbSeconds(c, in.Segs, ph, cw[c.Index]),
		}
		ct.HRLow, ct.HRHigh = hrFor(in.Profile, ct.Watts/in.FTP)
		plan.Climbs = append(plan.Climbs, ct)
	}
	return plan
}

// classify gives every profile segment its kind: part of a climb, a descent
// steeper than -3 %, or flat/rolling.
func classify(segs []Seg, cs []climbs.Climb) []leg {
	out := make([]leg, len(segs))
	for i, s := range segs {
		p := leg{seg: s, kind: KindFlat, climb: -1}
		mid := (s.StartM + s.EndM) / 2
		for _, c := range cs {
			if mid >= c.StartM && mid <= c.EndM {
				p.kind, p.climb = KindClimb, c.Index
				break
			}
		}
		if p.kind != KindClimb && s.Grade < descentGrade {
			p.kind = KindDescent
		}
		out[i] = p
	}
	return out
}

// evaluate runs the plan at flat-level power pf: it sets each leg's watts and
// time (mutating legs, since the next evaluation overwrites them), and
// returns the timeline's normalised power and each climb's watts.
func evaluate(legs []leg, in Input, ph Physics, pf float64) (float64, map[int]float64) {
	cw := make(map[int]float64, len(in.Climbs))
	for _, c := range in.Climbs {
		// The factor depends on how long the climb takes and that on the watts,
		// but there is always a consistent answer: more power only shortens a
		// climb. Ride it at the middle factor to find which band it is in.
		d1 := ClimbSeconds(c, in.Segs, ph, math.Min(pf*1.05, in.FTP*1.05))
		f := ClimbFactor(d1)
		cw[c.Index] = math.Min(pf*f, in.FTP*f)
	}
	var samples []float64
	for i := range legs {
		p := &legs[i]
		switch p.kind {
		case KindClimb:
			p.watts = cw[p.climb]
		case KindDescent:
			p.watts = descentFTP * in.FTP
		default:
			p.watts = pf
		}
		p.secs = (p.seg.EndM - p.seg.StartM) / ph.Speed(p.watts, p.seg.Grade)
		n := int(math.Round(p.secs))
		if n < 1 {
			n = 1
		}
		for j := 0; j < n; j++ {
			samples = append(samples, p.watts)
		}
	}
	return normalised(samples), cw
}

// normalised is normalised power: the 30 s rolling mean of one-second power,
// raised to the fourth, averaged, and rooted. Under 30 seconds it is just the
// mean.
func normalised(samples []float64) float64 {
	const window = 30
	if len(samples) < window {
		var sum float64
		for _, s := range samples {
			sum += s
		}
		if len(samples) == 0 {
			return 0
		}
		return sum / float64(len(samples))
	}
	var run, sum4 float64
	for i, s := range samples {
		run += s
		if i >= window {
			run -= samples[i-window]
		}
		if i >= window-1 {
			m := run / window
			sum4 += m * m * m * m
		}
	}
	return math.Pow(sum4/float64(len(samples)-window+1), 0.25)
}

// merge joins adjacent legs of one kind (and one climb), so a rider reads a
// handful of lines rather than the two hundred the profile is cut into.
func merge(legs []leg, in Input) []Segment {
	var out []Segment
	var cur leg
	var open bool
	var start, length, rise, secs float64
	flush := func() {
		if !open {
			return
		}
		seg := Segment{
			StartM: start, EndM: cur.seg.EndM, Kind: cur.kind, ClimbIndex: cur.climb,
			Watts: cur.watts, Seconds: secs,
			WattsLow:  roundTo5(cur.watts * (1 - targetBand)),
			WattsHigh: roundTo5(cur.watts * (1 + targetBand)),
		}
		if length > 0 && secs > 0 {
			seg.Gradient = rise / length
			seg.SpeedKph = length / secs * 3.6
		}
		seg.HRLow, seg.HRHigh = hrFor(in.Profile, cur.watts/in.FTP)
		out = append(out, seg)
	}
	for _, p := range legs {
		if open && cur.kind == p.kind && cur.climb == p.climb {
			cur.seg.EndM = p.seg.EndM
			length += p.seg.EndM - p.seg.StartM
			rise += (p.seg.EndM - p.seg.StartM) * p.seg.Grade
			secs += p.secs
			continue
		}
		flush()
		cur, open = p, true
		start = p.seg.StartM
		length = p.seg.EndM - p.seg.StartM
		rise = length * p.seg.Grade
		secs = p.secs
	}
	flush()
	return out
}

func roundTo5(w float64) int { return int(math.Round(w/5) * 5) }
