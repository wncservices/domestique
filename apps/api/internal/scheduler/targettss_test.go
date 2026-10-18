package scheduler

import (
	"reflect"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func tssWeek() (periodization.Week, workout.RiderProfile, map[string]float64) {
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBuild, TargetHours: 4}
	profile := workout.RiderProfile{AvailableDays: []string{"mon", "wed", "fri", "sun"}, FTPWatts: 250}
	levels := map[string]float64{"threshold": 3, "vo2max": 3}
	return week, profile, levels
}

func weekTSS(t *testing.T, reqs []workout.CreateWorkoutRequest, ftp float64) float64 {
	t.Helper()
	var sum float64
	for _, r := range reqs {
		tss, _ := workout.PlannedTSS(workout.Workout{Sport: r.Sport, Steps: r.Steps}, ftp)
		sum += tss
	}
	return sum
}

func structuredLevels(reqs []workout.CreateWorkoutRequest) []float64 {
	var out []float64
	for _, r := range reqs {
		if r.Zone != workout.ZoneEndurance {
			out = append(out, r.Level)
		}
	}
	return out
}

func TestWithTargetTSSRaisesStructuredRungs(t *testing.T) {
	week, profile, levels := tssWeek()
	base, err := WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	baseTSS := weekTSS(t, base, profile.FTPWatts)
	got, err := WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling, WithTargetTSS(1.3*baseTSS))
	if err != nil {
		t.Fatal(err)
	}
	if g := weekTSS(t, got, profile.FTPWatts); g <= baseTSS {
		t.Fatalf("boosted TSS %v not above unboosted %v", g, baseTSS)
	}
	bl, gl := structuredLevels(base), structuredLevels(got)
	if len(bl) != len(gl) || len(bl) == 0 {
		t.Fatalf("structured count changed: %v vs %v", bl, gl)
	}
	for i := range gl {
		if gl[i] > bl[i]+1.0 {
			t.Errorf("level %d = %v, more than 1.0 above %v", i, gl[i], bl[i])
		}
	}
}

func TestWithTargetTSSNeverLengthensAWeek(t *testing.T) {
	week, profile, levels := tssWeek()
	got, err := WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling, WithTargetTSS(10000))
	if err != nil {
		t.Fatal(err)
	}
	var secs float64
	for _, r := range got {
		secs += totalSeconds(r.Steps)
	}
	if limit := week.TargetHours * 3600 * 1.1; secs > limit {
		t.Errorf("week is %v s, over %v", secs, limit)
	}
}

func TestWithTargetTSSReachedIsUnchanged(t *testing.T) {
	week, profile, levels := tssWeek()
	base, _ := WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling)
	got, err := WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling, WithTargetTSS(1))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(base, got) {
		t.Error("a target already met changed the week")
	}
}

func TestWithTargetTSSIsANoOpForRunningAndWithoutFTP(t *testing.T) {
	week, profile, levels := tssWeek()
	base, _ := WeekWorkouts(week, profile, levels, "r", "g", model.SportRunning)
	got, _ := WeekWorkouts(week, profile, levels, "r", "g", model.SportRunning, WithTargetTSS(10000))
	if !reflect.DeepEqual(base, got) {
		t.Error("running changed")
	}
	profile.FTPWatts = 0
	base, _ = WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling)
	got, _ = WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling, WithTargetTSS(10000))
	if !reflect.DeepEqual(base, got) {
		t.Error("no-FTP week changed")
	}
	week, profile, levels = tssWeek()
	base, _ = WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling)
	got, _ = WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling, WithTargetTSS(0))
	if !reflect.DeepEqual(base, got) {
		t.Error("tss 0 changed")
	}
}
