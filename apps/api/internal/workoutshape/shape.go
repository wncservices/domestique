// Package workoutshape turns a planned session into its canonical shape: the
// text that says what the session asks for, with no watts, heart-rate
// figures, paces, dates or rider. Two riders with the same session at
// different FTPs share one shape, so they can share one explanation.
package workoutshape

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Text returns the canonical shape of w. Power targets become a percentage
// of ftp, rounded to 5 %; with no ftp (0 or less) they read "power" with no
// number. Heart rate, pace and cadence read their name only, and an open
// target reads "open".
func Text(w workout.Workout, ftp float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "sport %s\n", oneLine(string(w.Sport)))
	fmt.Fprintf(&b, "session %s\n", oneLine(w.Name))
	writeSteps(&b, w.Steps, ftp, 0)
	return b.String()
}

// Key is the hex sha256 of a shape text, the cache key for its explanation.
func Key(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func writeSteps(b *strings.Builder, steps []workout.WorkoutStep, ftp float64, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, s := range steps {
		if s.Repeat >= 2 {
			fmt.Fprintf(b, "%srepeat %dx\n", indent, s.Repeat)
			writeSteps(b, s.Steps, ftp, depth+1)
			continue
		}
		fmt.Fprintf(b, "%s%s %s %s\n", indent, kind(s.Intensity), duration(s), target(s, ftp))
	}
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
		if s.Seconds < 120 {
			return fmt.Sprintf("%ds", int(math.Round(s.Seconds)))
		}
		return fmt.Sprintf("%dmin", int(math.Round(s.Seconds/60)))
	case workout.DurationDistance:
		return fmt.Sprintf("%dm", int(math.Round(s.Meters)))
	default:
		return "open"
	}
}

func target(s workout.WorkoutStep, ftp float64) string {
	switch s.Target {
	case workout.TargetPower:
		if ftp <= 0 {
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

func roundTo5(pct float64) int {
	return int(math.Round(pct/5)) * 5
}

// oneLine keeps a free-text name from breaking the line structure.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
