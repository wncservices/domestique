package projection

import "math"

// MediumHours is the duration assumed for a goal that states no distance, so
// it gets the medium band rather than none.
const MediumHours = 3.0

// Band is the form (TSB) a rider typically wants at the start line, and the
// intensity factor used to size the event for the target-CTL rule of thumb.
// It is a default, not a finding: no source validates one universal number.
type Band struct {
	Low  float64
	High float64
	IF   float64
}

// EventDuration estimates how long the event takes: distance at 28 km/h plus
// an hour per 1000 m of climbing. stated is false when the goal gives no
// distance (elevation alone says too little), and the medium default is used.
func EventDuration(distanceM, elevationM float64) (hours float64, stated bool) {
	if distanceM <= 0 {
		return MediumHours, false
	}
	return distanceM/1000/28 + math.Max(elevationM, 0)/1000, true
}

// BandFor picks the typical race-day form for an event of that length: short
// punchy events want more freshness, long ones are ridden at lower intensity
// and tolerate less of it.
func BandFor(hours float64) Band {
	switch {
	case hours < 2:
		return Band{Low: 10, High: 25, IF: 0.95}
	case hours <= 4:
		return Band{Low: 5, High: 20, IF: 0.85}
	default:
		return Band{Low: 5, High: 15, IF: 0.75}
	}
}

// TargetCTL is the rule of thumb eventTSS/4 clamped to [30, 120]. It exists
// only to give "undertrained" a yardstick; it is not a goal the rider set.
func TargetCTL(hours float64) float64 {
	tss := hours * math.Pow(BandFor(hours).IF, 2) * 100
	return math.Min(120, math.Max(30, tss/4))
}
