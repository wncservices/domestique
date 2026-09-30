package workoutlib

import (
	"fmt"
	"math"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// warmupSeconds and cooldownSeconds are the spec's fixed 10-minute warmup
// (ramping 50->65% across two 5-minute steps) and 10-minute cooldown
// (45-55%) — see docs/superpowers/specs's progression-levels design.
const (
	warmupSeconds   = 10 * minute
	cooldownSeconds = 10 * minute
)

// TotalSeconds is how long a rung takes end to end: the fixed warmup and
// cooldown plus every rep's work and rest. A single-rep rung's RestSeconds
// is always 0 in ladderTable, so this one formula covers both shapes — no
// separate "is this a repeat block" branch needed here the way Instantiate
// needs one.
func TotalSeconds(r Rung) float64 {
	return float64(warmupSeconds + cooldownSeconds + r.Reps*(r.WorkSeconds+r.RestSeconds))
}

// Pick returns the rung on l whose Level is closest to targetLevel, among
// rungs whose TotalSeconds fits within maxSeconds — ties broken toward the
// lower level, since a rider on the fence between two rungs is better
// undertrained than overtrained. false means no rung fits maxSeconds at all
// (every rung, even the ladder's easiest, is too long for the time budget).
func Pick(l Ladder, targetLevel float64, maxSeconds float64) (Rung, bool) {
	var best Rung
	found := false
	bestDist := math.Inf(1)
	for _, r := range l.Rungs {
		if TotalSeconds(r) > maxSeconds {
			continue
		}
		dist := math.Abs(float64(r.Level) - targetLevel)
		// Strict less-than: rungs are visited in increasing Level order, so
		// the first of any equal-distance pair already found is the lower
		// one, and a later equal-distance rung must not replace it.
		if !found || dist < bestDist {
			best, bestDist, found = r, dist, true
		}
	}
	return best, found
}

// restPercent is the spec's fixed rest intensity between reps: 50% FTP for
// cycling, 65% threshold speed for running (docs/superpowers/specs's
// progression-levels design, "Workout ladders"). A single value, not a
// range, unlike a rung's own LowPct/HighPct.
func restPercent(sport model.Sport) float64 {
	if sport == model.SportRunning {
		return 0.65
	}
	return 0.50
}

// zoneHRRange maps a structured zone to a fraction of max HR, for a rider
// with no FTP/threshold pace set and no threshold heart rate — a percentage
// of FTP or threshold speed has no fixed heart-rate equivalent, so heart rate
// needs its own table rather than reusing a rung's LowPct/HighPct. Mirrors
// scheduler.zoneTarget's own priority chain (power -> pace -> HR -> open);
// see this package's brief for the exact ruling on these fractions.
//
// "endurance" is not a ladder zone (no ladder is built for it); it is the key
// scheduler.enduranceZoneTarget asks HRRange for, carrying that function's
// long-standing 60-75% of max HR.
func zoneHRRange(zone string) (low, high float64) {
	switch zone {
	case "endurance":
		return 0.60, 0.75
	case "tempo", "sweet_spot":
		return 0.80, 0.88
	case "threshold":
		return 0.88, 0.93
	case "vo2max", "intervals":
		return 0.90, 0.97
	case "anaerobic":
		return 0.93, 1.00
	default:
		// Warmup, cooldown and the rest/recovery step between reps — all
		// the same easy bucket.
		return 0.50, 0.60
	}
}

// lthrHRRange maps a zone to a fraction of threshold heart rate (LTHR) from
// Joe Friel's %LTHR zones — one table per sport, the same edges the Fitness
// page and rideanalysis.HRZoneSecondsLTHR use. The %MaxHR bands above are not
// reused: their "tempo" (80-88% of max HR) sits far below Friel's tempo,
// which is 90-93% of LTHR. The easy bucket ("") is the top half of Z1, and
// vo2max/intervals/anaerobic are Z5b and Z5c.
func lthrHRRange(sport model.Sport, zone string) (low, high float64) {
	running := sport == model.SportRunning
	switch zone {
	case "endurance":
		if running {
			return 0.85, 0.89
		}
		return 0.81, 0.89
	case "tempo":
		if running {
			return 0.90, 0.94
		}
		return 0.90, 0.93
	case "sweet_spot":
		return 0.92, 0.96
	case "threshold":
		if running {
			return 0.95, 0.99
		}
		return 0.94, 0.99
	case "vo2max", "intervals":
		return 1.03, 1.06
	case "anaerobic":
		return 1.07, 1.10
	default:
		if running {
			return 0.72, 0.85
		}
		return 0.70, 0.81
	}
}

// HRRange is the heart-rate target, in bpm, for a zone: the rider's threshold
// heart rate times the Friel table when they have one (each end capped at
// MaxHR when that is known, so Z5c never asks for more than the rider's
// ceiling), otherwise today's percentage-of-max-HR bands unchanged. ok is
// false when the profile has neither, which callers treat as an open target.
// The basis switches per rider, never globally. Exported so
// scheduler.enduranceZoneTarget shares the one table.
func HRRange(sport model.Sport, profile workout.RiderProfile, zone string) (low, high float64, ok bool) {
	switch {
	case profile.ThresholdHR > 0:
		lowFrac, highFrac := lthrHRRange(sport, zone)
		low = float64(profile.ThresholdHR) * lowFrac
		high = float64(profile.ThresholdHR) * highFrac
		// A max HR below LTHR is stale or mistyped (LTHR cannot exceed max
		// HR), and capping at it would collapse every zone to one zero-width
		// value, so the cap only applies when the ceiling is plausible.
		if profile.MaxHR > 0 && profile.MaxHR >= profile.ThresholdHR {
			ceiling := float64(profile.MaxHR)
			low, high = math.Min(low, ceiling), math.Min(high, ceiling)
		}
		return low, high, true
	case profile.MaxHR > 0:
		lowFrac, highFrac := zoneHRRange(zone)
		return float64(profile.MaxHR) * lowFrac, float64(profile.MaxHR) * highFrac, true
	default:
		return 0, 0, false
	}
}

// target converts a percentage-of-FTP/threshold-speed range into an actual
// step target, following the same priority order scheduler.zoneTarget uses
// today: cycling power from FTP, running pace from threshold speed, heart
// rate (from threshold HR, else max HR — see HRRange), or an open target when
// none of a rider's profile has enough to go on. hrZone names the zone whose
// heart-rate table to use instead of lowPct/highPct when falling through to
// the HR case, since a percentage of FTP/pace does not translate directly to
// a heart-rate fraction.
func target(sport model.Sport, profile workout.RiderProfile, lowPct, highPct float64, hrZone string) (workout.TargetType, float64, float64) {
	switch {
	case sport == model.SportCycling && profile.FTPWatts > 0:
		return workout.TargetPower, profile.FTPWatts * lowPct, profile.FTPWatts * highPct
	case sport == model.SportRunning && profile.ThresholdPaceSecPerKM > 0:
		threshold := 1000 / profile.ThresholdPaceSecPerKM // m/s
		return workout.TargetPace, threshold * lowPct, threshold * highPct
	}
	if low, high, ok := HRRange(sport, profile, hrZone); ok {
		return workout.TargetHeartRate, low, high
	}
	return workout.TargetOpen, 0, 0
}

// workIntensity is the repeat block's (or single-rep step's) own intensity:
// "interval" for the zones that are genuinely intervals off the top end of
// the ladder, "active" for the steadier zones lower down — see this
// package's brief.
func workIntensity(zone string) workout.Intensity {
	switch zone {
	case "vo2max", "anaerobic", "intervals":
		return workout.IntensityInterval
	default:
		return workout.IntensityActive
	}
}

// WarmupCooldownSeconds is the library's fixed 10-minute warmup plus
// 10-minute cooldown, in seconds — every structured rung carries it (via
// Instantiate) and so does an endurance/long session built directly from
// the library (scheduler.BuildEnduranceSession), so the two warm up and cool
// down exactly the same way.
const WarmupCooldownSeconds = warmupSeconds + cooldownSeconds

// Warmup builds the library's 10-minute warmup: two 5-minute steps ramping
// 50->65% of FTP/threshold pace (or the equivalent heart-rate fraction),
// shared by Instantiate and by scheduler.BuildEnduranceSession — exported
// rather than duplicated so an endurance day warms up exactly the way a
// structured one does.
func Warmup(sport model.Sport, profile workout.RiderProfile) []workout.WorkoutStep {
	t1, low1, high1 := target(sport, profile, 0.50, 0.55, "")
	t2, low2, high2 := target(sport, profile, 0.60, 0.65, "")
	return []workout.WorkoutStep{
		{
			Name: "Warmup", Intensity: workout.IntensityWarmup, Duration: workout.DurationTime,
			Seconds: 5 * minute, Target: t1, TargetLow: low1, TargetHigh: high1,
		},
		{
			Name: "Warmup", Intensity: workout.IntensityWarmup, Duration: workout.DurationTime,
			Seconds: 5 * minute, Target: t2, TargetLow: low2, TargetHigh: high2,
		},
	}
}

// Cooldown builds the library's 10-minute cooldown at 45-55%. See Warmup's
// own comment on why this is exported rather than duplicated.
func Cooldown(sport model.Sport, profile workout.RiderProfile) workout.WorkoutStep {
	t, low, high := target(sport, profile, 0.45, 0.55, "")
	return workout.WorkoutStep{
		Name: "Cooldown", Intensity: workout.IntensityCooldown, Duration: workout.DurationTime,
		Seconds: cooldownSeconds, Target: t, TargetLow: low, TargetHigh: high,
	}
}

// Instantiate turns one rung into a structured workout request: the
// library's 10-minute warmup, the rung's work (a plain step for a
// single-rep rung, a repeat block otherwise), and its 10-minute cooldown.
// The caller sets GeneratedDescription, GoalID and Date — Instantiate only
// knows about the rung and the rider's profile, not why this workout is
// being built or when it lands.
func Instantiate(l Ladder, r Rung, profile workout.RiderProfile) workout.CreateWorkoutRequest {
	workType, workLow, workHigh := target(l.Sport, profile, r.LowPct, r.HighPct, l.Zone)
	rp := restPercent(l.Sport)
	restType, restLow, restHigh := target(l.Sport, profile, rp, rp, "")

	steps := append(Warmup(l.Sport, profile),
		workStep(l, r, workType, workLow, workHigh, restType, restLow, restHigh),
		Cooldown(l.Sport, profile),
	)

	return workout.CreateWorkoutRequest{
		Sport: l.Sport,
		Name:  rungName(l.Label, r),
		Zone:  workout.Zone(l.Zone),
		Level: float64(r.Level),
		Steps: steps,
	}
}

// workStep builds the rung's own work: a plain step for a single-rep rung
// (internal/workout requires Repeat >= 2 for a repeat block), a repeat block
// of work+recovery otherwise.
func workStep(l Ladder, r Rung, workType workout.TargetType, workLow, workHigh float64, restType workout.TargetType, restLow, restHigh float64) workout.WorkoutStep {
	if r.Reps < 2 {
		return workout.WorkoutStep{
			Name: l.Label, Intensity: workIntensity(l.Zone), Duration: workout.DurationTime,
			Seconds: float64(r.WorkSeconds), Target: workType, TargetLow: workLow, TargetHigh: workHigh,
		}
	}
	return workout.WorkoutStep{
		Name: l.Label, Repeat: r.Reps,
		Steps: []workout.WorkoutStep{
			{
				Name: "Work", Intensity: workIntensity(l.Zone), Duration: workout.DurationTime,
				Seconds: float64(r.WorkSeconds), Target: workType, TargetLow: workLow, TargetHigh: workHigh,
			},
			{
				Name: "Recovery", Intensity: workout.IntensityRecovery, Duration: workout.DurationTime,
				Seconds: float64(r.RestSeconds), Target: restType, TargetLow: restLow, TargetHigh: restHigh,
			},
		},
	}
}

// rungName is "<Label> <reps>×<work>" — "Threshold 3×12", "VO2max 5×4",
// "Anaerobic 6×30s" — matching the spec's own generated-name examples.
func rungName(label string, r Rung) string {
	return fmt.Sprintf("%s %d×%s", label, r.Reps, workLabel(r.WorkSeconds))
}

// workLabel formats a rep's work duration the way the spec's names do:
// whole minutes with no unit ("12", "60"), or seconds with an "s" suffix
// under a minute or not an even number of minutes ("30s", "90s" — 90 seconds
// is deliberately not shown as a minute and a half).
func workLabel(seconds int) string {
	if seconds%minute == 0 {
		return fmt.Sprintf("%d", seconds/minute)
	}
	return fmt.Sprintf("%ds", seconds)
}
