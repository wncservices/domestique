package pacing

import (
	"fmt"
	"math"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

// zoneForPct names the training zone whose percent-of-FTP range contains pct
// (a fraction of FTP): the ladder zones' own ranges, with endurance below
// tempo and an easy bucket under it.
func zoneForPct(pct float64) string {
	switch {
	case pct < 0.55:
		return ""
	case pct < 0.76:
		return "endurance"
	case pct < 0.88:
		return "tempo"
	case pct < 0.95:
		return "sweet_spot"
	case pct < 1.06:
		return "threshold"
	case pct < 1.21:
		return "vo2max"
	default:
		return "anaerobic"
	}
}

// hrFor is the heart-rate range for a target at pct of FTP, from the same
// table the workout library uses (threshold HR when the rider has one, else
// max HR). Zero, zero when the rider has neither: watts only. It is a
// steady-state figure; heart rate lags the first minutes of a climb.
func hrFor(profile workout.RiderProfile, pct float64) (low, high int) {
	l, h, ok := workoutlib.HRRange(model.SportCycling, profile, zoneForPct(pct))
	if !ok {
		return 0, 0
	}
	return int(math.Round(l)), int(math.Round(h))
}

// HRForWatts is the heart-rate range for riding at watts, for a rider whose
// FTP is ftp: the table hrFor uses for every segment of the plan.
func HRForWatts(profile workout.RiderProfile, watts, ftp float64) (low, high int) {
	if ftp <= 0 {
		return 0, 0
	}
	return hrFor(profile, watts/ftp)
}

// maxCueName is how many characters a course point name may have: older Garmin
// Edges show about 10, newer ones about 15.
const maxCueName = 15

// CueName is the short ASCII label for a climb's course point: "C3 250-265W",
// or "C3 148-156bpm" when hr. n is the climb's number (1-based). It never
// exceeds 15 characters and never uses an en dash: it drops the unit, and then
// the range, before it would run over.
func CueName(n, low, high int, hr bool) string {
	unit := "W"
	if hr {
		unit = "bpm"
	}
	for _, name := range []string{
		fmt.Sprintf("C%d %d-%d%s", n, low, high, unit),
		fmt.Sprintf("C%d %d-%d", n, low, high),
		fmt.Sprintf("C%d", n),
	} {
		if len(name) <= maxCueName {
			return name
		}
	}
	return "C"
}
