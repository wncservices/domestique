// Package workoutshape turns a planned session into its canonical shape: the
// text that says what the session asks for, with no watts, heart-rate
// figures, paces, dates, names or rider. Two riders with the same session at
// different FTPs share one shape, so they can share one explanation.
package workoutshape

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/fitnesstest"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Text returns the canonical shape of w. The session name is left out: it is
// typed by the rider and the explanation cache is shared, so it must not reach
// the text. Sport and zone are written only from known values, so free text
// stored in either cannot reach it. Power targets become a percentage of ftp,
// rounded to 5 %; an FTP test adds a `test <protocol>` line (known protocols only); with no usable ftp (zero, negative, NaN or infinite) they
// read "power" with no number. Heart rate, pace and cadence read their name
// only, and an open target reads "open".
func Text(w workout.Workout, ftp float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "sport %s\n", sport(w.Sport))
	fmt.Fprintf(&b, "zone %s\n", zone(w.Zone))
	if w.TestProtocol != "" {
		// Test steps are open, so without this a test and a plain session of
		// the same steps would share one explanation.
		fmt.Fprintf(&b, "test %s\n", testProtocol(w.TestProtocol))
	}
	writeSteps(&b, w.Steps, ftp, 0)
	return b.String()
}

// testProtocol writes a protocol only from the known ids; the column is text.
func testProtocol(p string) string {
	switch p {
	case fitnesstest.ProtocolRamp, fitnesstest.ProtocolTwentyMinute, fitnesstest.ProtocolTwoByEight:
		return p
	default:
		return "unknown"
	}
}

// Key is the hex sha256 of a shape text, the cache key for its explanation.
func Key(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func writeSteps(b *strings.Builder, steps []workout.WorkoutStep, ftp float64, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, s := range steps {
		if len(s.Steps) > 0 {
			// A block is any step with children; Repeat below 2 is one pass.
			fmt.Fprintf(b, "%srepeat %dx\n", indent, max(s.Repeat, 1))
			writeSteps(b, s.Steps, ftp, depth+1)
			continue
		}
		fmt.Fprintf(b, "%s%s %s %s\n", indent, kind(s.Intensity), duration(s), target(s, ftp))
	}
}

// sport writes the sport the workout is stored under. An empty sport is
// cycling, as the database defaults it; anything else unknown reads "other".
func sport(s model.Sport) string {
	switch s {
	case "", model.SportCycling:
		return string(model.SportCycling)
	case model.SportRunning:
		return string(model.SportRunning)
	default:
		return "other"
	}
}

// zone writes the training-load bucket when it is one of the known zones, and
// "unset" for anything else, including the empty zone of an older row.
func zone(z workout.Zone) string {
	if z == workout.ZoneEndurance || workout.IsStructuredZone(z) {
		return string(z)
	}
	return "unset"
}

func kind(i workout.Intensity) string {
	switch i {
	case workout.IntensityWarmup:
		return "warmup"
	case workout.IntensityCooldown:
		return "cooldown"
	case workout.IntensityRecovery, workout.IntensityRest:
		return "recovery"
	case workout.IntensityActive, workout.IntensityInterval:
		return "work"
	default:
		return "other"
	}
}

func duration(s workout.WorkoutStep) string {
	switch s.Duration {
	case workout.DurationTime:
		if !finite(s.Seconds) {
			return "open"
		}
		if s.Seconds < 120 {
			return fmt.Sprintf("%ds", int(math.Round(s.Seconds)))
		}
		return fmt.Sprintf("%dmin", int(math.Round(s.Seconds/60)))
	case workout.DurationDistance:
		if !finite(s.Meters) {
			return "open"
		}
		return fmt.Sprintf("%dm", int(math.Round(s.Meters)))
	default:
		return "open"
	}
}

func target(s workout.WorkoutStep, ftp float64) string {
	switch s.Target {
	case workout.TargetPower:
		if !usableFTP(ftp) || !finite(s.TargetLow) || !finite(s.TargetHigh) {
			return "power"
		}
		lo := roundTo5(s.TargetLow / ftp * 100)
		hi := roundTo5(s.TargetHigh / ftp * 100)
		if lo > hi {
			lo, hi = hi, lo
		}
		if lo == hi {
			return fmt.Sprintf("%d%% FTP", lo)
		}
		return fmt.Sprintf("%d-%d%% FTP", lo, hi)
	case workout.TargetHeartRate:
		return "heart rate"
	case workout.TargetPace:
		return "pace"
	case workout.TargetCadence:
		return "cadence"
	default:
		return "open"
	}
}

func usableFTP(ftp float64) bool {
	return ftp > 0 && finite(ftp)
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func roundTo5(pct float64) int {
	return int(math.Round(pct/5)) * 5
}
