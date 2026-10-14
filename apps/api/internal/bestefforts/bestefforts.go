// Package bestefforts decides what counts as a rider's best effort and when a
// ride has just set one. It is pure: no storage, no clock. The ride's date,
// the scope's stored values and whether a ride may be awarded at all are the
// caller's to supply, because they depend on when the ride was analysed and
// where it came from (see internal/api/besteffortsync.go).
//
// Design: docs/superpowers/specs/2026-09-29-season-review-design.md.
package bestefforts

import (
	"strconv"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/thresholds"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Scopes an Award can name. A season is a calendar year.
const (
	ScopeSeason  = "season"
	ScopeAllTime = "all_time"
)

// MinPrior is how many earlier values a scope needs before a new one can be a
// best in it. Without it a rider's first weeks, or a January ride against an
// empty year, would make every effort "a best".
const MinPrior = 3

// Metric is one thing a rider keeps a record of.
type Metric struct {
	Key string
	// Sport is the sport the record is kept for; "" means any sport.
	Sport string
	// Label and Unit are for display: "20-min power", "W".
	Label, Unit string
	// MinGain is how much a new value must beat the previous best by to count:
	// a flat watt for power, a percent of the previous best for speed and
	// heart rate. Smaller gains are noise, not progress.
	MinGain func(prev float64) float64
}

func onWatt(float64) float64          { return 1 }
func onePercent(prev float64) float64 { return prev * 0.01 }

// powerWindows are the seconds rideanalysis.PowerCurve reports, in display
// order. They are the keys of SessionAnalysis.PowerCurve.
var powerWindows = []struct {
	seconds int
	label   string
}{
	{5, "5-sec power"}, {60, "1-min power"}, {300, "5-min power"},
	{480, "8-min power"}, {1200, "20-min power"}, {3600, "60-min power"},
}

var metrics = func() []Metric {
	var out []Metric
	for _, w := range powerWindows {
		out = append(out, Metric{
			Key: "power_" + strconv.Itoa(w.seconds), Sport: string(model.SportCycling),
			Label: w.label, Unit: "W", MinGain: onWatt,
		})
	}
	out = append(out,
		Metric{Key: "speed_1200", Sport: string(model.SportRunning), Label: "20-min speed", Unit: "m/s", MinGain: onePercent},
		Metric{Key: "speed_1800", Sport: string(model.SportRunning), Label: "30-min speed", Unit: "m/s", MinGain: onePercent},
		Metric{Key: "max_hr", Label: "Max heart rate", Unit: "bpm", MinGain: onePercent},
	)
	return out
}()

// Metrics lists every record the app keeps, in display order. NP, best 20-min
// heart rate and cycling speed are deliberately not records: NP is not a peak,
// and road speed mostly measures wind and gradient.
func Metrics() []Metric { return append([]Metric(nil), metrics...) }

// Efforts is what one analysed ride contributes: metric key to value, for the
// metrics that apply to sport and that the ride has a value for. A zero or
// absent value is not an effort.
func Efforts(a workout.SessionAnalysis, sport string) map[string]float64 {
	out := map[string]float64{}
	if sport == string(model.SportCycling) {
		for _, w := range powerWindows {
			if v := a.PowerCurve[strconv.Itoa(w.seconds)]; v > 0 {
				out["power_"+strconv.Itoa(w.seconds)] = v
			}
		}
	}
	if sport == string(model.SportRunning) {
		if a.BestSpeed1200 > 0 {
			out["speed_1200"] = a.BestSpeed1200
		}
		if a.BestSpeed1800 > 0 {
			out["speed_1800"] = a.BestSpeed1800
		}
	}
	if (sport == string(model.SportCycling) || sport == string(model.SportRunning)) && a.MaxHR > 0 && a.MaxHR <= thresholds.MaxHRSpikeThreshold {
		out["max_hr"] = float64(a.MaxHR)
	}
	return out
}

// epsilon absorbs float rounding at the exact threshold (101 against
// 100 * 1.01), so "exactly one percent" is in.
const epsilon = 1e-9

// Award says whether value, a ride's new effort for m, is a best in a scope:
// "" for none, ScopeSeason or ScopeAllTime. season and allTime are every other
// stored value for the metric in that scope (the ride's own excluded), so a
// ride synced late cannot claim a record a newer ride already beats.
//
// A scope needs MinPrior values and the ride has to beat the best of them by
// MinGain. All-time wins over season: one marker, the stronger label. A tie
// never awards, so the earlier ride keeps the record.
func Award(m Metric, value float64, season, allTime []float64) string {
	if beats(m, value, allTime) {
		return ScopeAllTime
	}
	if beats(m, value, season) {
		return ScopeSeason
	}
	return ""
}

func beats(m Metric, value float64, prior []float64) bool {
	if len(prior) < MinPrior {
		return false
	}
	best := prior[0]
	for _, v := range prior[1:] {
		if v > best {
			best = v
		}
	}
	return value-best >= m.MinGain(best)-epsilon
}
