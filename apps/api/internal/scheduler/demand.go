package scheduler

import (
	"sort"

	"github.com/wncservices/domestique/apps/api/internal/climbs"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
)

// RouteDemand is what a goal's route asks of the sessions that train for it,
// reduced to the one thing generation can use: how long an effort to favour,
// per kind of slot. Anaerobic slots are not biased: the training bar (1 km at
// 3 %) leaves no climb short enough to rehearse with 30 to 90 second efforts. It carries no route, no coordinates and no rider: distances
// and durations only.
//
// The route chooses the *shape* of a session (3 x 12 min rather than 4 x 8),
// never its zone or its difficulty: the phase's zone mix belongs to
// periodization and each zone's progression level is the rider's own, so
// swapping a Build vo2max slot for a threshold one on the strength of a route
// would move a level they have not earned. See WithRouteDemand.
type RouteDemand struct {
	// Sustained is the wanted work length, in seconds, for threshold and
	// sweet-spot slots: the longest climb of 4 to 30 minutes, capped at 20.
	Sustained int
	// Short is the wanted work length for vo2max slots: the median duration of
	// the climbs under 6 minutes.
	Short int
	// Climbing is whether the route is hilly enough (10 m/km or 1 500 m in
	// total) that the long ride should be done on a climbing route.
	Climbing bool
}

// ClimbingLongRideName is what the long endurance ride of a Build or Peak week
// is called for a climbing route. Its steps and hours are unchanged; the name
// tells the rider to choose a hilly road, since the app cannot schedule a
// route.
const ClimbingLongRideName = "Long ride, with climbing"

// IsLongRideName is whether name is a long ride's, plain or climbing.
func IsLongRideName(name string) bool {
	return name == "Long ride" || name == ClimbingLongRideName
}

// wantFor is the wanted effort length for a structured slot in zone, 0 when
// the demand has nothing to say about that zone.
func (d *RouteDemand) wantFor(zone string) int {
	if d == nil {
		return 0
	}
	switch zone {
	case "threshold", "sweet_spot":
		return d.Sustained
	case "vo2max":
		return d.Short
	default:
		return 0
	}
}

// Option adjusts how a week is generated.
type Option func(*options)

type options struct{ demand *RouteDemand }

// WithRouteDemand biases the Build and Peak weeks toward the demand. A nil
// demand, or one that says nothing, leaves generation exactly as it is without
// it. Base, taper and recovery weeks are never touched: only a Build or Peak
// week that is not a recovery week reads it.
func WithRouteDemand(d *RouteDemand) Option {
	return func(o *options) { o.demand = d }
}

func applyOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// biases reports whether a week is one a route demand applies to.
func biases(week periodization.Week) bool {
	return !week.Recovery && (week.Phase == periodization.PhaseBuild || week.Phase == periodization.PhasePeak)
}

const (
	minSustainedClimbSec = 4 * 60
	maxSustainedClimbSec = 30 * 60
	sustainedCapSec      = 20 * 60
	shortClimbSec        = 6 * 60
	climbingMPerKm       = 10.0
	climbingTotalM       = 1500.0
)

// DemandFromClimbs reduces a route's climbs to a RouteDemand. durations[i] is
// how long cs[i] takes the rider at race intensity, in seconds; a mismatched
// pair is a caller bug and gives no demand. ascentM and distanceM are the whole
// route's, for the climbing long ride. Returns nil when there is nothing to
// bias toward.
func DemandFromClimbs(cs []climbs.Climb, durations []float64, ascentM, distanceM float64) *RouteDemand {
	if len(cs) != len(durations) {
		return nil
	}
	var d RouteDemand

	var longest float64
	var shorts []float64
	for i := range cs {
		sec := durations[i]
		if sec >= minSustainedClimbSec && sec <= maxSustainedClimbSec && sec > longest {
			longest = sec
		}
		if sec < shortClimbSec {
			shorts = append(shorts, sec)
		}
	}
	if longest > 0 {
		d.Sustained = int(min(longest, sustainedCapSec) + 0.5)
	}
	if len(shorts) > 0 {
		sort.Float64s(shorts)
		mid := len(shorts) / 2
		median := shorts[mid]
		if len(shorts)%2 == 0 {
			median = (shorts[mid-1] + shorts[mid]) / 2
		}
		d.Short = int(median + 0.5)
	}
	d.Climbing = (distanceM > 0 && ascentM/(distanceM/1000) >= climbingMPerKm) || ascentM >= climbingTotalM

	if d == (RouteDemand{}) {
		return nil
	}
	return &d
}
