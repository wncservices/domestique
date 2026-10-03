package loops

import (
	"math"
	"sort"

	"github.com/wncservices/domestique/apps/api/internal/routefit"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const (
	// TimeCalibrationSeeds and TimeRefinementSeeds are the two rounds of a
	// loop sized for a planned ride: ten engine calls, not the route
	// builder's fifteen. The routing quota is shared and a rider may plan a
	// whole week.
	TimeCalibrationSeeds = 3
	TimeRefinementSeeds  = 7

	// MaxTimeDeviation is how far a loop's estimated time may stray from the
	// planned time before it is dropped outright.
	MaxTimeDeviation = 0.15

	// KeepCandidates is how many distinct loops a time objective keeps.
	KeepCandidates = 3

	// distinctLength and distinctAscent say when two loops are the same shape:
	// within 3 percent of length and 10 percent of ascent of one already kept.
	distinctLength = 0.03
	distinctAscent = 0.10
)

// Detail is what a time objective knows about a loop it ranked.
type Detail struct {
	EstimatedSeconds float64
	TimeFit          float64
	TerrainFit       float64
	Score            float64
	Family           routefit.Family
	Note             string
}

// TimeObjective sizes loops by how long they take a rider on a planned ride,
// not by distance, and ranks them by how well their terrain suits the
// session. Not safe for concurrent use: the generator calls it from one
// goroutine.
type TimeObjective struct {
	wk       workout.Workout
	rider    routefit.Rider
	planned  float64
	fraction float64
	flatMps  float64
	assumed  bool
	family   routefit.Family

	detail map[int]Detail // by seed, so each loop is estimated once
}

// NewTimeObjective builds the objective for a workout and rider. The planned
// time is the workout's own (workout.PlannedSeconds); the effort is the
// time-weighted mean fraction of FTP over its steps. A workout with no timed
// step has no planned time; callers refuse it before generating.
func NewTimeObjective(wk workout.Workout, r routefit.Rider) *TimeObjective {
	fraction := routefit.AverageFraction(wk.Steps, r.FTP)
	mps, assumed := routefit.FlatSpeed(r, fraction)
	return &TimeObjective{
		wk: wk, rider: r,
		planned:  workout.PlannedSeconds(wk.Steps),
		fraction: fraction,
		flatMps:  mps,
		assumed:  assumed,
		family:   routefit.FamilyOf(wk),
		detail:   map[int]Detail{},
	}
}

// PlannedSeconds is the length of the ride the loop is for.
func (o *TimeObjective) PlannedSeconds() float64 { return o.planned }

// Workout is the session the loop is for.
func (o *TimeObjective) Workout() workout.Workout { return o.wk }

// Family is the session's terrain family; its engine profile and hilliness
// are what a caller asks the routing engine for.
func (o *TimeObjective) Family() routefit.Family { return o.family }

// SpeedMps is the flat-road speed the first guess used; SpeedAssumed says it
// leans on something the rider did not give (no FTP or no weight).
func (o *TimeObjective) SpeedMps() float64 { return o.flatMps }

// SpeedAssumed: see SpeedMps.
func (o *TimeObjective) SpeedAssumed() bool { return o.assumed }

// TargetLength is the first guess: flat speed times planned seconds.
func (o *TimeObjective) TargetLength() float64 { return o.flatMps * o.planned }

func (o *TimeObjective) seconds(l Loop) float64 {
	sec, _ := routefit.EstimateSeconds(l.Path.Points, o.rider, o.fraction)
	return sec
}

// Refine asks the engine, in the second round, for the planned time divided
// by the average seconds per metre the first round's loops actually took,
// divided by the overshoot the engine showed: the same correction the route
// builder makes for distance, with time in the middle.
func (o *TimeObjective) Refine(round1 []Loop) float64 {
	target := o.TargetLength()
	var sec, dist float64
	for _, l := range round1 {
		if l.DistanceM <= 0 {
			continue
		}
		sec += o.seconds(l)
		dist += l.DistanceM
	}
	ratio := OvershootRatio(round1, target)
	if sec <= 0 || dist <= 0 || ratio <= 0 {
		return target
	}
	return o.planned / (sec / dist) / ratio
}

// Keep drops a loop whose estimated time is more than 15 percent off the
// planned time.
func (o *TimeObjective) Keep(l Loop) bool {
	if o.planned <= 0 {
		return false
	}
	return math.Abs(o.seconds(l)-o.planned) <= MaxTimeDeviation*o.planned
}

// Detail scores a loop: 0.5 x timeFit + 0.5 x terrainFit, with the family's
// note. The time fit is 1 at the planned time and 0 at 15 percent off.
func (o *TimeObjective) Detail(l Loop) Detail {
	if d, ok := o.detail[l.Seed]; ok {
		return d
	}
	sec := o.seconds(l)
	timeFit := 0.0
	if o.planned > 0 {
		timeFit = clamp01(1 - math.Abs(sec-o.planned)/(MaxTimeDeviation*o.planned))
	}
	terrain, note := routefit.Fit(o.family, o.wk, l.Path.Points, o.rider)
	d := Detail{
		EstimatedSeconds: sec, TimeFit: timeFit, TerrainFit: terrain,
		Score: 0.5*timeFit + 0.5*terrain, Family: o.family, Note: note,
	}
	o.detail[l.Seed] = d
	return d
}

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }

// Rank orders by score, best first, and keeps the top three that differ: a
// loop within 3 percent of length and 10 percent of ascent of one already
// kept is skipped, so three candidates are three choices and not one choice
// shown three times.
func (o *TimeObjective) Rank(shortlist []Loop) []Loop {
	sorted := append([]Loop(nil), shortlist...)
	sort.SliceStable(sorted, func(i, j int) bool { return o.Detail(sorted[i]).Score > o.Detail(sorted[j]).Score })

	var kept []Loop
	for _, l := range sorted {
		if len(kept) == KeepCandidates {
			break
		}
		dup := false
		for _, k := range kept {
			sameLen := math.Abs(l.DistanceM-k.DistanceM) <= distinctLength*k.DistanceM
			sameClimb := math.Abs(l.AscentM-k.AscentM) <= math.Max(distinctAscent*k.AscentM, 1)
			if sameLen && sameClimb {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, l)
		}
	}
	return kept
}
