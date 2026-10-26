// Package intensity measures how a rider's recent power-scored riding splits
// across low, high and anaerobic intensity, and picks which structured zones
// a plan week should hold to close the gap to the phase's target split.
//
// It deliberately does not import scheduler (scheduler imports it), so the
// zone sets and today's ordering are restated here.
package intensity

import (
	"sort"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/rideanalysis"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// MinSeconds is the counted time under which there is no mix: a thin sample
// says more about one week than about the rider.
const MinSeconds = 10800

// tieMargin is how close the bucket gaps must be, to each other, for the
// current table to stand: the mix is already where the phase wants it.
const tieMargin = 0.01

// Mix is the share of counted time in each bucket, 0..1.
type Mix struct {
	Low, High, Anaerobic float64
	Seconds              int // counted time
}

type bucket int

const (
	bLow bucket = iota
	bHigh
	bAnaerobic
)

// FromAnalyses buckets the Coggan power-zone seconds of every power-scored
// analysis: low = zones 1-2 (< 75 % FTP), high = 3-4, anaerobic = 5-7.
// A ride scored on heart rate or an estimate, or without a normalised power,
// is not power-scored and does not count. ok is false under MinSeconds.
func FromAnalyses(as []workout.SessionAnalysis) (Mix, bool) {
	var low, high, ana int
	for _, a := range as {
		if a.LoadSource != string(rideanalysis.LoadSourceFITPower) || a.NormalizedPower <= 0 || len(a.PowerZoneSeconds) < 7 {
			continue
		}
		z := a.PowerZoneSeconds
		low += z[0] + z[1]
		high += z[2] + z[3]
		ana += z[4] + z[5] + z[6]
	}
	total := low + high + ana
	if total < MinSeconds {
		return Mix{}, false
	}
	t := float64(total)
	return Mix{Low: float64(low) / t, High: float64(high) / t, Anaerobic: float64(ana) / t, Seconds: total}, true
}

// Targets is the split a phase aims at. ok is false for taper and unknown
// phases, which have no mix.
func Targets(phase periodization.Phase) (low, high, anaerobic float64, ok bool) {
	switch phase {
	case periodization.PhaseBase:
		return .80, .18, .02, true
	case periodization.PhaseBuild:
		return .75, .20, .05, true
	case periodization.PhasePeak:
		return .75, .15, .10, true
	}
	return 0, 0, 0, false
}

type bucketZones struct {
	b     bucket
	zones []workout.Zone
}

// allowed is each phase's cycling zones by bucket, in today's order within a
// bucket and in the listed order between buckets.
func allowed(phase periodization.Phase) []bucketZones {
	switch phase {
	case periodization.PhaseBase:
		return []bucketZones{{bHigh, []workout.Zone{workout.ZoneSweetSpot, workout.ZoneTempo}}}
	case periodization.PhaseBuild:
		return []bucketZones{
			{bHigh, []workout.Zone{workout.ZoneThreshold, workout.ZoneSweetSpot}},
			{bAnaerobic, []workout.Zone{workout.ZoneVO2Max}},
		}
	case periodization.PhasePeak:
		return []bucketZones{
			{bAnaerobic, []workout.Zone{workout.ZoneVO2Max, workout.ZoneAnaerobic}},
			{bHigh, []workout.Zone{workout.ZoneThreshold}},
		}
	}
	return nil
}

// AllowedZones is the cycling zones a phase may hold, in today's order: the
// allowed buckets in listed order, each bucket's zones in order. It is nil for
// a phase with no targets.
func AllowedZones(phase periodization.Phase) []workout.Zone {
	var out []workout.Zone
	for _, g := range allowed(phase) {
		out = append(out, g.zones...)
	}
	return out
}

// ChooseZones returns k cycling zones for a week of the phase, the bucket
// with the largest shortfall first. A zone does not repeat until the allowed
// set is exhausted, then the order cycles. It returns nil when the phase has
// no targets or k < 1, or when the three bucket gaps are all within one
// percentage point of each other, so the caller keeps today's table.
func ChooseZones(phase periodization.Phase, mix Mix, k int) []workout.Zone {
	tl, th, ta, ok := Targets(phase)
	if !ok || k < 1 {
		return nil
	}
	gaps := map[bucket]float64{bLow: tl - mix.Low, bHigh: th - mix.High, bAnaerobic: ta - mix.Anaerobic}
	lo, hi := gaps[bLow], gaps[bLow]
	for _, g := range gaps {
		lo, hi = min(lo, g), max(hi, g)
	}
	if hi-lo <= tieMargin+1e-9 { // epsilon: shares are floats
		return nil
	}

	groups := allowed(phase)
	sort.SliceStable(groups, func(i, j int) bool { return gaps[groups[i].b] > gaps[groups[j].b] })

	var seq []workout.Zone
	for _, g := range groups {
		if gaps[g.b] > 0 {
			seq = append(seq, g.zones...)
		}
	}
	for _, g := range groups {
		if gaps[g.b] <= 0 {
			seq = append(seq, g.zones...)
		}
	}
	out := make([]workout.Zone, k)
	for i := range out {
		out[i] = seq[i%len(seq)]
	}
	return out
}
