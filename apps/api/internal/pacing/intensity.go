package pacing

// EventIF is the race-average intensity factor for an event of the given
// duration in hours: 0.95 under 2 h, 0.85 from 2 to 4 h inclusive, 0.75 over
// 4 h. These are the three bands the race-day projection uses for the same
// reason (a short event is ridden harder than a long one), so the two features
// agree, and this is the single implementation: the projection should import
// it rather than keep a second copy.
func EventIF(hours float64) float64 {
	switch {
	case hours < 2:
		return 0.95
	case hours <= 4:
		return 0.85
	default:
		return 0.75
	}
}

// ClimbFactor is how far above the flat-level power a climb is ridden, by how
// long it lasts: 1.10 under 5 min, 1.05 from 5 to 20 min, 1.00 beyond. Short
// climbs tolerate more over target; a long one is held at the target. Climb
// watts are also capped at FTP times this factor, so a low flat power never
// pushes a long climb past threshold. One function, shared by the route
// demands and the pacing plan.
func ClimbFactor(durationSec float64) float64 {
	switch {
	case durationSec < 5*60:
		return 1.10
	case durationSec <= 20*60:
		return 1.05
	default:
		return 1.00
	}
}
