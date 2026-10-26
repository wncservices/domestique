package scheduler

import (
	"reflect"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/intensity"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// highShort is a rider who has ridden much less high-intensity time than a
// Build week wants and too much anaerobic: the mix prefers threshold and
// sweet spot over today's vo2max.
var highShort = intensity.Mix{Low: .75, High: .05, Anaerobic: .20, Seconds: 40000}

func mixWeek(phase periodization.Phase, recovery bool, ctl float64, days []string) (periodization.Week, workout.RiderProfile, map[string]float64) {
	week := periodization.Week{StartDate: "2026-10-05", Phase: phase, Recovery: recovery, TargetHours: 12, PlannedCTL: ctl}
	profile := workout.RiderProfile{FTPWatts: 250, AvailableDays: days}
	levels := map[string]float64{"sweet_spot": 4, "tempo": 4, "threshold": 4, "vo2max": 4, "anaerobic": 4}
	return week, profile, levels
}

var allDays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

func mixBuild(t *testing.T, week periodization.Week, profile workout.RiderProfile, levels map[string]float64, opts ...Option) []workout.CreateWorkoutRequest {
	t.Helper()
	out, err := WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestZoneMixChangesBuildZones(t *testing.T) {
	week, profile, levels := mixWeek(periodization.PhaseBuild, false, 0, []string{"tue", "thu", "sat", "sun"})
	plain := structuredZones(mixBuild(t, week, profile, levels))
	mixed := structuredZones(mixBuild(t, week, profile, levels, WithZoneMix(highShort)))
	if !reflect.DeepEqual(plain, []workout.Zone{workout.ZoneThreshold, workout.ZoneVO2Max}) {
		t.Fatalf("plain = %v", plain)
	}
	if !reflect.DeepEqual(mixed, []workout.Zone{workout.ZoneThreshold, workout.ZoneSweetSpot}) {
		t.Errorf("mixed = %v, want threshold and sweet_spot", mixed)
	}
}

func TestNoMixIsTodaysTable(t *testing.T) {
	// A mix with no preference (every gap within a point) and no option at
	// all give the same week; taper and running ignore even a strong one.
	week, profile, levels := mixWeek(periodization.PhaseBuild, false, 0, []string{"tue", "thu", "sat", "sun"})
	even := intensity.Mix{Low: .75, High: .20, Anaerobic: .05, Seconds: 40000}
	if !reflect.DeepEqual(mixBuild(t, week, profile, levels), mixBuild(t, week, profile, levels, WithZoneMix(even))) {
		t.Error("an even mix changed the week")
	}
	taper := week
	taper.Phase = periodization.PhaseTaper
	if !reflect.DeepEqual(mixBuild(t, taper, profile, levels), mixBuild(t, taper, profile, levels, WithZoneMix(highShort))) {
		t.Error("taper read the mix")
	}
	plain, _ := WeekWorkouts(week, profile, levels, "r", "g", model.SportRunning)
	mixed, _ := WeekWorkouts(week, profile, levels, "r", "g", model.SportRunning, WithZoneMix(highShort))
	if !reflect.DeepEqual(plain, mixed) {
		t.Error("running read the mix")
	}
}

func TestThirdHardSessionNeedsSixDaysAndCTL60(t *testing.T) {
	count := func(phase periodization.Phase, ctl float64, days []string, opts ...Option) []workout.CreateWorkoutRequest {
		week, profile, levels := mixWeek(phase, false, ctl, days)
		out := mixBuild(t, week, profile, levels, opts...)
		var hard []workout.CreateWorkoutRequest
		for _, w := range out {
			if w.Zone != workout.ZoneEndurance {
				hard = append(hard, w)
			}
		}
		return hard
	}
	six := allDays[:6]
	for _, phase := range []periodization.Phase{periodization.PhaseBuild, periodization.PhasePeak} {
		hard := count(phase, 60, six)
		if len(hard) != 3 {
			t.Fatalf("%s: %d hard sessions at CTL 60 with 6 days, want 3", phase, len(hard))
		}
		seen := map[workout.Zone]bool{}
		var dates []time.Time
		for _, w := range hard {
			seen[w.Zone] = true
			d, _ := time.Parse("2006-01-02", w.Date)
			dates = append(dates, d)
		}
		if len(seen) != 3 {
			t.Errorf("%s: zones repeat: %v", phase, seen)
		}
		for i := range dates {
			for j := i + 1; j < len(dates); j++ {
				if diff := dates[i].Sub(dates[j]).Hours(); diff == 24 || diff == -24 {
					t.Errorf("%s: adjacent hard days %v %v", phase, dates[i], dates[j])
				}
			}
		}
		if got := len(count(phase, 59.9, six)); got != 2 {
			t.Errorf("%s: CTL 59.9 gave %d, want 2", phase, got)
		}
		if got := len(count(phase, 80, allDays[:5])); got != 2 {
			t.Errorf("%s: 5 days gave %d, want 2", phase, got)
		}
		if got := len(count(phase, 80, six, WithZoneMix(highShort))); got != 3 {
			t.Errorf("%s with a mix: %d hard, want 3", phase, got)
		}
	}
	if got := len(count(periodization.PhaseBase, 80, allDays)); got != 1 {
		t.Errorf("base gave %d, want 1", got)
	}
	if got := len(count(periodization.PhaseTaper, 80, allDays)); got != 1 {
		t.Errorf("taper gave %d, want 1", got)
	}
}

func TestRecoveryWeekHasOneShortStructuredSession(t *testing.T) {
	// Peak's first zone is vo2max, the one ladder whose easiest rungs fit
	// an hour. Level 4 gives a target of 3 (level - 1); rung 3 is 50 minutes and fits the hour.
	week, profile, levels := mixWeek(periodization.PhasePeak, true, 80, []string{"tue", "thu", "sat", "sun"})
	for _, opts := range [][]Option{nil, {WithZoneMix(highShort)}} {
		out := mixBuild(t, week, profile, levels, opts...)
		var hard []workout.CreateWorkoutRequest
		for _, w := range out {
			if w.Zone != workout.ZoneEndurance {
				hard = append(hard, w)
			}
			if w.Name == "Long ride" {
				t.Error("recovery week has a long ride")
			}
		}
		if len(hard) != 1 {
			t.Fatalf("%d structured sessions, want 1", len(hard))
		}
		h := hard[0]
		if h.Zone != workout.ZoneVO2Max || h.Level != 3 || h.Date != "2026-10-06" {
			t.Errorf("session = zone %s level %v on %s; want vo2max level 3 on the first day", h.Zone, h.Level, h.Date)
		}
		if s := totalSeconds(h.Steps); s > 3600 {
			t.Errorf("session lasts %v s, over 3600", s)
		}
	}

	// Base and build recovery weeks keep one session too: sweet spot and
	// threshold at level - 1 (4 -> 3, rung 3 or the longest that fits the hour).
	build1, p1, l1 := mixWeek(periodization.PhaseBuild, true, 0, []string{"tue", "thu", "sat", "sun"})
	for phase, zone := range map[periodization.Phase]workout.Zone{
		periodization.PhaseBase: workout.ZoneSweetSpot, periodization.PhaseBuild: workout.ZoneThreshold,
	} {
		wk := build1
		wk.Phase = phase
		out := mixBuild(t, wk, p1, l1)
		hard := 0
		for _, w := range out {
			if w.Zone == workout.ZoneEndurance {
				if w.Name != "Endurance ride" {
					t.Errorf("%s: easy day is %q", phase, w.Name)
				}
				continue
			}
			hard++
			if w.Zone != zone || w.Level > 3 || w.Date != "2026-10-06" || totalSeconds(w.Steps) > 3600 {
				t.Errorf("%s recovery session = %s level %v on %s (%v s)", phase, w.Zone, w.Level, w.Date, totalSeconds(w.Steps))
			}
		}
		if hard != 1 {
			t.Errorf("%s: %d structured sessions, want 1", phase, hard)
		}
	}
	l1 = map[string]float64{"vo2max": 1}
	peak := week
	if out := mixBuild(t, peak, p1, l1); structuredZones(out)[0] != workout.ZoneVO2Max || out[0].Level != 1 {
		t.Errorf("level 1 recovery = %+v, want vo2max rung 1", out[0])
	}
	// Under three days nothing is structured.
	if z := structuredZones(mixBuild(t, week, workout.RiderProfile{FTPWatts: 250, AvailableDays: []string{"tue", "sat"}}, levels)); len(z) != 0 {
		t.Errorf("two-day recovery week gave %v", z)
	}
}

func TestRouteDemandStillBiasesWithinTheChosenZone(t *testing.T) {
	week, profile, levels := mixWeek(periodization.PhaseBuild, false, 0, []string{"tue", "thu", "sat", "sun"})
	levels["threshold"] = 4
	plain := byZone(mixBuild(t, week, profile, levels, WithZoneMix(highShort)), workout.ZoneThreshold)
	biased := byZone(mixBuild(t, week, profile, levels, WithZoneMix(highShort), WithRouteDemand(testDemand)), workout.ZoneThreshold)
	if plain == nil || biased == nil {
		t.Fatal("the mix should keep a threshold session")
	}
	if plain.Name != "Threshold 4×8" || biased.Name != "Threshold 3×12" {
		t.Errorf("names = %q / %q, want Threshold 4×8 / Threshold 3×12", plain.Name, biased.Name)
	}
}

// The target-TSS retry lifts structured levels; a recovery week must never be
// lifted, so the option changes nothing there.
func TestTargetTSSLeavesARecoveryWeekAlone(t *testing.T) {
	week, profile, levels := mixWeek(periodization.PhasePeak, true, 80, []string{"tue", "thu", "sat", "sun"})
	week.LoadCapped = true
	if !reflect.DeepEqual(mixBuild(t, week, profile, levels), mixBuild(t, week, profile, levels, WithTargetTSS(900))) {
		t.Error("WithTargetTSS changed a recovery week")
	}
}
