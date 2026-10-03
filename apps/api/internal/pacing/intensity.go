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

// ClimbKind names a climb by how long it takes: short under 4 min, medium 4 to
// 8, sustained 8 to 20, long over 20. The training bias and the demands card
// both read it, so the words mean the same in both.
func ClimbKind(durationSec float64) string {
	switch {
	case durationSec < 4*60:
		return "short"
	case durationSec < 8*60:
		return "medium"
	case durationSec <= 20*60:
		return "sustained"
	default:
		return "long"
	}
}

// TotalSeconds is how long segs take at a constant watts through the physics.
func TotalSeconds(segs []Seg, ph Physics, watts float64) float64 {
	var sec float64
	for _, s := range segs {
		sec += (s.EndM - s.StartM) / ph.Speed(watts, s.Grade)
	}
	return sec
}

// DerivedIF is the intensity factor for a route when the rider has not set
// one. The event's duration is not known until it has been paced, so: pass 1
// rides the whole route at 0.85 of FTP, takes the band of that duration, and
// that band's IF is the answer. The band is not re-evaluated after pass 2
// (the plan built at this IF), so a route near a boundary does not flip
// between two intensities.
func DerivedIF(segs []Seg, ph Physics, ftp float64) float64 {
	return EventIF(TotalSeconds(segs, ph, 0.85*ftp) / 3600)
}
