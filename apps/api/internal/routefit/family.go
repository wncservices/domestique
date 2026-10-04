package routefit

import (
	"fmt"
	"math"

	"github.com/wncservices/domestique/apps/api/internal/climbs"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/pacing"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Family is what a session is for, in terms of the road it wants. The table
// is a heuristic, not a coaching claim: the copy says "suited to", never
// "perfect for".
type Family string

const (
	FamilyRecovery  Family = "recovery"
	FamilyEndurance Family = "endurance"
	FamilyLong      Family = "long"
	FamilySteady    Family = "steady"
	FamilyClimb     Family = "climb"
)

// Engine is the routing profile and steepness_difficulty (the only terrain
// levers the routing engine has) a family asks for. The rest of the judgement
// is Fit's, after a loop comes back.
func (f Family) Engine() (profile string, hilliness int) {
	switch f {
	case FamilyRecovery:
		return "cycling-regular", 0
	case FamilyEndurance, FamilySteady:
		return "cycling-road", 1
	default: // long, climb
		return "cycling-road", 3
	}
}

const (
	recoveryBelowSec = 75 * 60
	longAboveSec     = 150 * 60

	// flatGrade is the grade within which a stretch counts as flat enough to
	// hold a steady effort on.
	flatGrade = 2.0
	// climbCoverage is how much of a work step a climb must last to host it.
	climbCoverage = 0.8
	// climbPenalty is what each climb of TrainingConfig size costs a
	// recovery ride.
	climbPenalty = 0.3
	// taperMPerKm is how far outside a family's band the fit falls to nothing.
	taperMPerKm = 8.0
)

// FamilyOf maps a session to its family.
func FamilyOf(wk workout.Workout) Family {
	switch wk.Zone {
	case workout.ZoneTempo, workout.ZoneSweetSpot, workout.ZoneThreshold:
		return FamilySteady
	case workout.ZoneVO2Max, workout.ZoneAnaerobic, workout.ZoneIntervals:
		return FamilyClimb
	}
	if wk.Name == scheduler.ClimbingLongRideName {
		return FamilyLong
	}
	secs := workout.PlannedSeconds(wk.Steps)
	switch {
	case secs > longAboveSec:
		return FamilyLong
	case secs > 0 && secs < recoveryBelowSec:
		return FamilyRecovery
	default:
		return FamilyEndurance
	}
}

// Fit scores how well a route's terrain suits the session, 0 to 1, with a
// plain note. A route below 0.5 is still worth showing; the note says what is
// missing. A route with no elevation cannot be judged and scores a neutral
// 0.5 saying so.
func Fit(f Family, wk workout.Workout, points []gpx.Point, r Rider) (score float64, note string) {
	stats := gpx.ComputeStats(points)
	for _, p := range points {
		if !p.HasEle {
			return 0.5, "This route has no elevation data, so its terrain could not be judged."
		}
	}
	if stats.DistanceM <= 0 {
		return 0.5, "This route has no elevation data, so its terrain could not be judged."
	}
	mPerKm := stats.AscentM / (stats.DistanceM / 1000)
	found := climbs.Detect(points, climbs.TrainingConfig)

	switch f {
	case FamilyRecovery:
		return fitRecovery(mPerKm, len(found))
	case FamilyEndurance:
		return fitEndurance(mPerKm)
	case FamilyLong:
		return fitLong(mPerKm)
	case FamilySteady:
		return fitSteady(wk, points, found, r)
	default:
		return fitClimb(wk, points, found, mPerKm, r)
	}
}

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }

func fitRecovery(mPerKm float64, nClimbs int) (float64, string) {
	score := 1.0
	if mPerKm > 4 {
		score = 1 - (mPerKm-4)/taperMPerKm
	}
	score = clamp01(score - climbPenalty*float64(nClimbs))
	switch {
	case nClimbs > 0:
		return score, "Has a climb of a kilometre or more, hillier than an easy ride wants."
	case mPerKm > 4:
		return score, "Hillier than an easy ride wants."
	default:
		return score, "Suited to an easy ride: flat."
	}
}

func fitEndurance(mPerKm float64) (float64, string) {
	switch {
	case mPerKm < 5:
		return clamp01(1 - (5-mPerKm)/taperMPerKm), "Flatter than an endurance ride usually is."
	case mPerKm > 10:
		return clamp01(1 - (mPerKm-10)/taperMPerKm), "Hillier than an endurance ride usually is."
	default:
		return 1, fmt.Sprintf("Suited to an endurance ride: about %.0f m of climbing per km.", mPerKm)
	}
}

func fitLong(mPerKm float64) (float64, string) {
	score := clamp01(mPerKm / 10)
	if mPerKm >= 10 {
		return score, fmt.Sprintf("Suited to a long ride with climbing: about %.0f m per km.", mPerKm)
	}
	return score, "Not much climbing for a long ride that wants some."
}

// stepFor is the longest work step of a session and the power it is ridden at.
type stepFor struct {
	seconds float64
	pace    pace
	ok      bool
}

// zoneFraction is the share of FTP a work step in zone is assumed to ride at
// when it names no power of its own.
func zoneFraction(z workout.Zone) float64 {
	switch z {
	case workout.ZoneTempo:
		return 0.80
	case workout.ZoneSweetSpot:
		return 0.90
	case workout.ZoneThreshold:
		return 1.0
	case workout.ZoneVO2Max:
		return 1.15
	case workout.ZoneAnaerobic:
		return 1.3
	case workout.ZoneIntervals:
		return 1.1
	default:
		return defaultActive
	}
}

func isEasy(i workout.Intensity) bool {
	switch i {
	case workout.IntensityWarmup, workout.IntensityCooldown, workout.IntensityRecovery, workout.IntensityRest:
		return true
	}
	return false
}

func longestWork(steps []workout.WorkoutStep, best *workout.WorkoutStep) {
	for i := range steps {
		s := steps[i]
		if s.Repeat >= 2 {
			longestWork(s.Steps, best)
			continue
		}
		if s.Duration != workout.DurationTime || s.Seconds <= 0 || isEasy(s.Intensity) {
			continue
		}
		if s.Seconds > best.Seconds {
			*best = s
		}
	}
}

func workStep(wk workout.Workout, r Rider) stepFor {
	var best workout.WorkoutStep
	longestWork(wk.Steps, &best)
	if best.Seconds <= 0 {
		return stepFor{}
	}
	out := stepFor{seconds: best.Seconds, ok: true}
	switch {
	case best.Target == workout.TargetPower && best.TargetHigh > 0:
		out.pace = pace{ph: pacing.DefaultPhysics(r.WeightKg), watts: (best.TargetLow + best.TargetHigh) / 2, known: true}
	case r.FTP > 0:
		out.pace = pace{ph: pacing.DefaultPhysics(r.WeightKg), watts: r.FTP * zoneFraction(wk.Zone), known: true}
	}
	return out
}

func minutes(sec float64) int { return int(math.Round(sec / 60)) }

func climbSeconds(c climbs.Climb, segs []pacing.Seg, p pace) float64 {
	var sec float64
	for _, s := range segs {
		lo, hi := math.Max(s.StartM, c.StartM), math.Min(s.EndM, c.EndM)
		if hi > lo {
			sec += p.seconds(hi-lo, s.Grade)
		}
	}
	return sec
}

const noWork = "This session has no work step to fit a route to."

func fitSteady(wk workout.Workout, points []gpx.Point, found []climbs.Climb, r Rider) (float64, string) {
	work := workStep(wk, r)
	segs := pacing.Segments(points, segMinM, segMaxM)
	if !work.ok || len(segs) == 0 {
		return 0.5, noWork
	}

	// The longest run of road within flatGrade, timed at the work step's
	// own effort.
	var best, run float64
	for _, s := range segs {
		if math.Abs(s.Grade) <= flatGrade {
			run += work.pace.seconds(s.EndM-s.StartM, s.Grade)
			best = math.Max(best, run)
		} else {
			run = 0
		}
	}
	coverage := clamp01(best / work.seconds)
	for _, c := range found {
		coverage = math.Max(coverage, clamp01(climbSeconds(c, segs, work.pace)/(climbCoverage*work.seconds)))
	}
	if coverage >= 0.5 {
		return coverage, fmt.Sprintf("Suited to %d-minute efforts: a long enough steady stretch.", minutes(work.seconds))
	}
	return coverage, fmt.Sprintf("No stretch long enough for %d-minute efforts; expect to ride them in pieces.", minutes(work.seconds))
}

func fitClimb(wk workout.Workout, points []gpx.Point, found []climbs.Climb, mPerKm float64, r Rider) (float64, string) {
	work := workStep(wk, r)
	segs := pacing.Segments(points, segMinM, segMaxM)
	if !work.ok || len(segs) == 0 {
		return 0.5, noWork
	}
	short := fmt.Sprintf("No climb long enough for %d-minute intervals; do them on the flatter stretches.", minutes(work.seconds))
	if len(found) == 0 {
		// Rolling road is not a climb, but it is not nothing either: it
		// counts half, up to the "climbing" bar's own 10 m/km.
		return 0.5 * clamp01(mPerKm/10), short
	}
	var longest float64
	for _, c := range found {
		longest = math.Max(longest, climbSeconds(c, segs, work.pace))
	}
	coverage := clamp01(longest / (climbCoverage * work.seconds))
	if coverage >= 0.5 {
		return coverage, fmt.Sprintf("Suited to %d-minute intervals: a climb that long is on the route.", minutes(work.seconds))
	}
	return coverage, short
}
