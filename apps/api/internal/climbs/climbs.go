// Package climbs finds sustained climbs in a track's elevation profile.
//
// It is pure: points in, climbs out, no I/O. It exists so two consumers with
// different bars share one algorithm: fitcourse writes device cues (Garmin
// ClimbPro's own thresholds), and training asks which climbs a rider's route
// demands (a stricter, longer bar). A Climb carries distances and indexes
// only, never a coordinate; a consumer that needs a point looks it up from
// the index, so nothing built on this leaks route geometry by accident.
package climbs

import "github.com/wncservices/domestique/apps/api/internal/gpx"

// Config is the set of thresholds one consumer wants from the shared
// algorithm.
type Config struct {
	// MinLengthM and MinGradient are the bars a climb must clear: length in
	// metres, average gradient in percent.
	MinLengthM, MinGradient float64
	// ClimbingGrade is the bar for a single smoothed point to count as
	// climbing at all, in percent.
	ClimbingGrade float64
	// MergeGapM is how far a dip, false flat or hairpin may sit inside one
	// climb without ending it.
	MergeGapM float64
	// SmoothRadiusM denoises elevation before grade is taken from it.
	// Elevation is far noisier than heading: one bad GPS or DEM sample
	// reads as a wall.
	SmoothRadiusM float64
}

var (
	// DeviceConfig mirrors Garmin ClimbPro's published thresholds (500 m and
	// 3 %), so a climb a course cue marks and one the device shows agree.
	// This is what fitcourse has always used; do not change it without
	// meaning to move every cue.
	DeviceConfig = Config{MinLengthM: 500, MinGradient: 3, ClimbingGrade: 1.5, MergeGapM: 200, SmoothRadiusM: 50}

	// TrainingConfig is the bar for a climb a workout can rehearse. A 700 m
	// ramp is a sprint, not something a rung of intervals trains for, so the
	// minimum length is 1 km.
	TrainingConfig = Config{MinLengthM: 1000, MinGradient: 3, ClimbingGrade: 1.5, MergeGapM: 200, SmoothRadiusM: 50}
)

// Climb is one detected climb.
type Climb struct {
	// Index is the climb's rank by position on the track, from 0.
	Index int
	// StartIdx and EndIdx index the points slice Detect was given.
	StartIdx, EndIdx int
	// StartM and EndM are distances along the track in metres.
	StartM, EndM float64
	LengthM      float64
	GainM        float64
	// AvgGradient is the average grade over the climb, in percent.
	AvgGradient float64
	// Score is length (m) times average gradient (%): Strava's own formula.
	// It is also the rank for "biggest climbs".
	Score float64
}

// Category score thresholds, Strava's length x gradient score. The Tour de
// France's own categorisation is openly discretionary and cannot be
// reproduced from a GPX file; this is the formula most cyclists already read
// their climbs by, not a more correct one.
const (
	cat4Score = 8_000.0
	cat3Score = 16_000.0
	cat2Score = 32_000.0
	cat1Score = 64_000.0
	hcScore   = 80_000.0
)

// Category maps a score to a category: 4 (easiest) down to 1, and 0 for
// hors categorie. ok is false below category 4: a climb can clear the
// length and gradient bars and still be too small to be categorised.
func Category(score float64) (cat int, ok bool) {
	switch {
	case score >= hcScore:
		return 0, true
	case score >= cat1Score:
		return 1, true
	case score >= cat2Score:
		return 2, true
	case score >= cat3Score:
		return 3, true
	case score >= cat4Score:
		return 4, true
	default:
		return 0, false
	}
}

// Detect finds the track's climbs under cfg.
//
// Rules, in order: smooth elevation; mark points climbing at ClimbingGrade
// or more; grow a climb until the last climbing point is more than
// MergeGapM behind it, so a short dip does not split it; end the climb at its
// highest smoothed point, so a trailing descent is not counted; keep it if
// its length and average gradient clear cfg.
//
// Every point must carry elevation, or this returns nothing: a climb derived
// from a mix of real and absent elevation is worse than none.
func Detect(points []gpx.Point, cfg Config) []Climb {
	if len(points) < 3 {
		return nil
	}
	for _, p := range points {
		if !p.HasEle {
			return nil
		}
	}
	distances := cumulativeDistances(points)
	elev := smoothElevation(points, distances, cfg.SmoothRadiusM)

	ascending := make([]bool, len(points))
	for i := 1; i < len(points); i++ {
		run := distances[i] - distances[i-1]
		if run <= 0 {
			continue
		}
		grade := (elev[i] - elev[i-1]) / run * 100
		ascending[i] = grade >= cfg.ClimbingGrade
	}

	var out []Climb
	for i := 1; i < len(points); i++ {
		if !ascending[i] {
			continue
		}

		start := i - 1
		peak := i
		lastAscendingAt := distances[i]

		j := i + 1
		for j < len(points) {
			if elev[j] > elev[peak] {
				peak = j
			}
			if ascending[j] {
				lastAscendingAt = distances[j]
			} else if distances[j]-lastAscendingAt > cfg.MergeGapM {
				break
			}
			j++
		}

		length := distances[peak] - distances[start]
		gain := elev[peak] - elev[start]
		if length >= cfg.MinLengthM && gain > 0 {
			avg := gain / length * 100
			if avg >= cfg.MinGradient {
				out = append(out, Climb{
					Index:       len(out),
					StartIdx:    start,
					EndIdx:      peak,
					StartM:      distances[start],
					EndM:        distances[peak],
					LengthM:     length,
					GainM:       gain,
					AvgGradient: avg,
					Score:       length * avg,
				})
			}
		}

		i = j
	}
	return out
}

// Distances is the cumulative distance in metres at each point.
func Distances(points []gpx.Point) []float64 { return cumulativeDistances(points) }

func cumulativeDistances(points []gpx.Point) []float64 {
	out := make([]float64, len(points))
	for i := 1; i < len(points); i++ {
		out[i] = out[i-1] + gpx.DistanceM(points[i-1], points[i])
	}
	return out
}

// smoothElevation averages each point's elevation with its neighbours within
// radiusM, as a sliding window over cumulative distance. Both window edges
// move only forward as i increases, so this is one pass over the points
// rather than one per point.
func smoothElevation(points []gpx.Point, distances []float64, radiusM float64) []float64 {
	out := make([]float64, len(points))
	lo, hi := 0, -1
	sum := 0.0
	for i := range points {
		for hi+1 < len(points) && distances[hi+1]-distances[i] <= radiusM {
			hi++
			sum += points[hi].Ele
		}
		for distances[i]-distances[lo] > radiusM {
			sum -= points[lo].Ele
			lo++
		}
		out[i] = sum / float64(hi-lo+1)
	}
	return out
}
