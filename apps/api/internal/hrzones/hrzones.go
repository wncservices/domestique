// Package hrzones turns a rider's heart-rate zone setting into the four bpm
// boundaries their zones are drawn with, and a ride's heart-rate histogram
// into seconds per zone. It is pure: no I/O, no clock, nothing stored. The
// boundaries are recomputed on every read, so changing a setting re-draws
// every ride's zones without re-analysing it.
//
// Every caller that needs a zone boundary or a target range goes through
// here, so the rider's own choice and the automatic one cannot drift apart.
package hrzones

import (
	"fmt"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// lthrFractions are the Friel threshold-HR fractions for the four zone
// boundaries, per sport. Mirrored by LTHR_CYCLING_EDGES and LTHR_RUNNING_EDGES
// in apps/web/src/utils/fitnessMath.ts; a test reads that file and checks.
var lthrFractions = map[string][]float64{
	"cycling": {0.81, 0.90, 0.94, 1.00},
	"running": {0.85, 0.90, 0.95, 1.00},
}

// maxFractions are the %-of-max-HR boundaries. Mirrored by HR_ZONE_EDGES in
// fitnessMath.ts (its middle four), checked by a test.
var maxFractions = []float64{0.6, 0.7, 0.8, 0.9}

// reserveFractions are the Karvonen fractions of heart-rate reserve, applied
// as resting + (max - resting) x fraction.
var reserveFractions = []float64{0.6, 0.7, 0.8, 0.9}

// minCustomBPM and maxCustomBPM bound a rider's own boundaries. Outside this
// range a value is a typo or a sensor fault, not a zone.
const (
	minCustomBPM = 40
	maxCustomBPM = 230
)

func sportKey(sport string) string {
	if sport == "running" {
		return "running"
	}
	return "cycling"
}

// Setting is the rider's zone setting for one sport. Anything other than
// running reads the cycling setting, the same default the rest of the app
// uses when a sport is not stated.
func Setting(p workout.RiderProfile, sport string) workout.HRZoneSetting {
	if sportKey(sport) == "running" {
		return p.HRZonesRunning
	}
	return p.HRZonesCycling
}

// Bounds returns the four boundaries b[0..3] in bpm: a heart rate below b[i]
// is in zone i+1, at or above b[3] is Z5. ok is false when the method's
// inputs are missing, so the caller shows no zones rather than wrong ones.
// Automatic uses the threshold table when threshold HR is known, else max HR,
// which is exactly what every rider got before they could choose.
func Bounds(p workout.RiderProfile, sport string) (b [4]float64, ok bool) {
	s := Setting(p, sport)
	method := s.Method
	if method == workout.HRZonesAuto {
		switch {
		case p.ThresholdHR > 0:
			method = workout.HRZonesThreshold
		case p.MaxHR > 0:
			method = workout.HRZonesMax
		default:
			return b, false
		}
	}
	switch method {
	case workout.HRZonesThreshold:
		if p.ThresholdHR <= 0 {
			return b, false
		}
		f := lthrFractions[sportKey(sport)]
		for i := range b {
			b[i] = float64(p.ThresholdHR) * f[i]
		}
		return b, true
	case workout.HRZonesMax:
		if p.MaxHR <= 0 {
			return b, false
		}
		for i := range b {
			b[i] = float64(p.MaxHR) * maxFractions[i]
		}
		return b, true
	case workout.HRZonesReserve:
		// Reserve needs a resting HR below max; with resting at or above max
		// the reserve is zero or negative and the boundaries are nonsense.
		if p.MaxHR <= 0 || p.RestingHR <= 0 || p.RestingHR >= p.MaxHR {
			return b, false
		}
		reserve := float64(p.MaxHR - p.RestingHR)
		for i := range b {
			b[i] = float64(p.RestingHR) + reserve*reserveFractions[i]
		}
		return b, true
	case workout.HRZonesCustom:
		if len(s.Bounds) != 4 {
			return b, false
		}
		for i := range b {
			b[i] = float64(s.Bounds[i])
		}
		return b, true
	}
	return b, false
}

// Valid reports whether a setting can be stored. Only a custom setting takes
// bounds, and they must be four strictly ascending bpm values in 40..230, so
// a zone cannot come out empty or inverted.
func Valid(s workout.HRZoneSetting) error {
	switch s.Method {
	case workout.HRZonesAuto, workout.HRZonesThreshold, workout.HRZonesMax, workout.HRZonesReserve:
		if len(s.Bounds) != 0 {
			return fmt.Errorf("heart-rate zone method %q takes no bounds", s.Method)
		}
		return nil
	case workout.HRZonesCustom:
		if len(s.Bounds) != 4 {
			return fmt.Errorf("custom heart-rate zones need exactly four bounds, got %d", len(s.Bounds))
		}
		for i, v := range s.Bounds {
			if v < minCustomBPM || v > maxCustomBPM {
				return fmt.Errorf("custom heart-rate bound %d is outside %d-%d", v, minCustomBPM, maxCustomBPM)
			}
			if i > 0 && v <= s.Bounds[i-1] {
				return fmt.Errorf("custom heart-rate bounds must be strictly ascending")
			}
		}
		return nil
	}
	return fmt.Errorf("unknown heart-rate zone method %q", s.Method)
}

// Seconds sorts a ride's histogram into the five zones for boundaries b. A
// second at bpm x goes to the highest zone whose boundary is at or below x,
// so a value exactly on a boundary is in the higher zone. Seconds with no
// heart rate (h.NoHR) are never counted. ok is false when there is no heart
// rate at all, so the caller shows no zones rather than five zeros.
func Seconds(h workout.HRHistogram, b [4]float64) (z [5]int, ok bool) {
	total := 0
	for i, s := range h.Seconds {
		if s <= 0 {
			continue
		}
		bpm := float64(h.Min + i)
		zone := 0
		for _, edge := range b {
			if bpm >= edge {
				zone++
			}
		}
		z[zone] += s
		total += s
	}
	if total == 0 {
		return [5]int{}, false
	}
	return z, true
}

// Range is the heart-rate target range for a session in one zone, read from
// the rider's own boundaries. It is answered only for an explicit method: an
// automatic setting returns ok false so the caller keeps its own table, which
// is what makes an automatic rider's targets exactly what they were.
//
// The zones are recovery, endurance, tempo, sweet_spot, threshold, and the
// top band (vo2max, intervals, anaerobic) that runs to max HR when it is above
// b[3], else to b[3] plus the width of the threshold zone.
func Range(p workout.RiderProfile, sport, zone string) (low, high float64, ok bool) {
	if Setting(p, sport).Method == workout.HRZonesAuto {
		return 0, 0, false
	}
	b, ok := Bounds(p, sport)
	if !ok {
		return 0, 0, false
	}
	switch zone {
	case "recovery":
		// A wide custom Z2 would put the mirrored floor below zero.
		return max(0, b[0]-(b[1]-b[0])), b[0], true
	case "endurance":
		return b[0], b[1], true
	case "tempo":
		return b[1], b[2], true
	case "sweet_spot":
		return (b[1] + b[2]) / 2, (b[2] + b[3]) / 2, true
	case "threshold":
		return b[2], b[3], true
	case "vo2max", "intervals", "anaerobic":
		ceiling := b[3] + (b[3] - b[2])
		if float64(p.MaxHR) > b[3] {
			ceiling = float64(p.MaxHR)
		}
		return b[3], ceiling, true
	}
	return 0, 0, false
}
