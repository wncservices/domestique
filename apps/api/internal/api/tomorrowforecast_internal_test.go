package api

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Every date in this file is explicit; nothing reads the wall clock or the
// process's local zone, which is what running the package under both
// TZ=UTC and TZ=Europe/Brussels proves.
var fcToday = time.Date(2026, 3, 19, 0, 0, 0, 0, time.UTC)

func fcDay(offset int) string { return fcToday.AddDate(0, 0, offset).Format("2006-01-02") }

// fcHard is a generated threshold session with a power target of powerWatts
// for an hour.
func fcHard(id, date string, powerWatts float64) workout.Workout {
	return workout.Workout{
		ID: id, Sport: model.SportCycling, Name: "Threshold 3x12", GoalID: "g", Date: date,
		Description: scheduler.GeneratedDescription, Zone: workout.ZoneThreshold, Level: 5,
		Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600,
			Target: workout.TargetPower, TargetLow: powerWatts, TargetHigh: powerWatts}},
	}
}

func fcEasy(id, date string) workout.Workout {
	w := fcHard(id, date, 120)
	w.Name, w.Zone, w.Level = "Endurance ride", workout.ZoneEndurance, 0
	return w
}

func TestProjectedTSBRollsTodaysLoadForward(t *testing.T) {
	latest := &workout.FitnessSnapshot{Date: fcDay(0), CTL: 40, ATL: 100}

	got, ok := projectedTSB(latest, nil, 0, fcToday)
	if !ok {
		t.Fatal("ok = false, want a fresh snapshot to project")
	}
	// A rest day: CTL decays by exp(-1/42), ATL by exp(-1/7).
	want := 40*math.Exp(-1.0/42) - 100*math.Exp(-1.0/7)
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("tsb = %v, want %v", got, want)
	}

	hard, _ := projectedTSB(latest, nil, 150, fcToday)
	if hard >= got {
		t.Errorf("a 150 TSS day gave %v, want lower than the rest-day %v", hard, got)
	}
}

func TestProjectedTSBFoldsInDaysBetweenSnapshotAndToday(t *testing.T) {
	latest := &workout.FitnessSnapshot{Date: fcDay(-1), CTL: 40, ATL: 100}
	withYesterday, ok := projectedTSB(latest, map[string]float64{fcDay(-1): 200}, 0, fcToday)
	if !ok {
		t.Fatal("ok = false for a snapshot one day old")
	}
	without, _ := projectedTSB(latest, nil, 0, fcToday)
	if withYesterday >= without {
		t.Errorf("yesterday's 200 TSS gave %v, want lower than %v without it", withYesterday, without)
	}
}

func TestProjectedTSBFreshness(t *testing.T) {
	cases := []struct {
		name string
		date string
		ok   bool
	}{
		{"today", fcDay(0), true},
		{"two days old", fcDay(-2), true},
		{"three days old", fcDay(-3), false},
		{"dated in the future", fcDay(1), false},
		{"unparseable", "not-a-date", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ok := projectedTSB(&workout.FitnessSnapshot{Date: c.date, CTL: 40, ATL: 100}, nil, 0, fcToday)
			if ok != c.ok {
				t.Errorf("ok = %v, want %v", ok, c.ok)
			}
		})
	}
	if _, ok := projectedTSB(nil, nil, 0, fcToday); ok {
		t.Error("ok = true with no snapshot")
	}
}

func TestTodayTrainingLoad(t *testing.T) {
	hardToday := []workout.Workout{fcHard("w", fcDay(0), 200)}
	session := []workout.CompletedSession{{Date: fcDay(0), TrainingLoad: 80}, {Date: fcDay(0), TrainingLoad: 10}, {Date: fcDay(-1), TrainingLoad: 500}}

	t.Run("actual sessions win over the estimate", func(t *testing.T) {
		got, ok := todayTrainingLoad(hardToday, session, 200, fcDay(0))
		if !ok || got != 90 {
			t.Errorf("got %v, %v, want 90, true", got, ok)
		}
	})
	t.Run("estimate from the planned hard session", func(t *testing.T) {
		// One hour at 200 W against a 200 W FTP is IF 1.0, TSS 100.
		got, ok := todayTrainingLoad(hardToday, nil, 200, fcDay(0))
		if !ok || math.Abs(got-100) > 1e-6 {
			t.Errorf("got %v, %v, want 100, true", got, ok)
		}
	})
	t.Run("a planned hard session with no FTP cannot be estimated", func(t *testing.T) {
		if _, ok := todayTrainingLoad(hardToday, nil, 0, fcDay(0)); ok {
			t.Error("ok = true, want false: no FTP means no estimate")
		}
	})
	t.Run("a day with nothing hard planned is a real zero, FTP or not", func(t *testing.T) {
		got, ok := todayTrainingLoad([]workout.Workout{fcEasy("e", fcDay(0))}, nil, 0, fcDay(0))
		if !ok || got != 0 {
			t.Errorf("got %v, %v, want 0, true", got, ok)
		}
	})
	t.Run("another day's hard session is not today's", func(t *testing.T) {
		got, ok := todayTrainingLoad([]workout.Workout{fcHard("w", fcDay(1), 200)}, nil, 200, fcDay(0))
		if !ok || got != 0 {
			t.Errorf("got %v, %v, want 0, true", got, ok)
		}
	})
}

func TestACWRThroughTodayCountsTodaysLoad(t *testing.T) {
	var loads []readiness.Load
	for d := -27; d <= -1; d++ {
		loads = append(loads, readiness.Load{Date: fcDay(d), Load: 50})
	}

	retro, ok := readiness.ACWR(loads, fcToday)
	if !ok {
		t.Fatal("retrospective ratio unavailable with 28 days of coverage")
	}
	got, ok := acwrThroughToday(loads, 200, fcToday)
	if !ok {
		t.Fatal("ok = false with 28 days of coverage")
	}
	want := ((6*50.0 + 200) / 7) / ((27*50.0 + 200) / 28)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("ratio = %v, want %v", got, want)
	}
	if got <= retro {
		t.Errorf("ratio with a 200 TSS today = %v, want above the retrospective %v", got, retro)
	}
}

func TestACWRThroughTodayReplacesRatherThanAddsTodaysEntry(t *testing.T) {
	var loads []readiness.Load
	for d := -27; d <= -1; d++ {
		loads = append(loads, readiness.Load{Date: fcDay(d), Load: 50})
	}
	clean, _ := acwrThroughToday(loads, 200, fcToday)
	// today's actual session is already in loads (as dailyLoadsForReadiness
	// would have put it) — the same 200 must not be counted twice.
	dup, _ := acwrThroughToday(append(loads, readiness.Load{Date: fcDay(0), Load: 200}), 200, fcToday)
	if clean != dup {
		t.Errorf("ratio with today already in loads = %v, want %v (replaced, not added)", dup, clean)
	}
}

func TestACWRThroughTodayNeedsTwentyOneDaysOfCoverage(t *testing.T) {
	var loads []readiness.Load
	for d := -10; d <= -1; d++ {
		loads = append(loads, readiness.Load{Date: fcDay(d), Load: 50})
	}
	if _, ok := acwrThroughToday(loads, 200, fcToday); ok {
		t.Error("ok = true with 10 days of history, want the 21-day gate to hold")
	}
}

func TestConsecutiveHardDays(t *testing.T) {
	gen := func(id string, offset int) workout.Workout { return fcHard(id, fcDay(offset), 200) }
	cases := []struct {
		name string
		ws   []workout.Workout
		want int
	}{
		{"nothing planned", nil, 0},
		{"only today", []workout.Workout{gen("a", 0)}, 1},
		{"today and yesterday", []workout.Workout{gen("a", 0), gen("b", -1)}, 2},
		{"a run of three", []workout.Workout{gen("a", 0), gen("b", -1), gen("c", -2)}, 3},
		{"an easy day breaks the run", []workout.Workout{gen("a", 0), fcEasy("e", fcDay(-1)), gen("c", -2)}, 1},
		{"a day with no workout at all breaks the run", []workout.Workout{gen("a", 0), gen("c", -2)}, 1},
		{"today easy is zero however hard yesterday was", []workout.Workout{fcEasy("e", fcDay(0)), gen("b", -1)}, 0},
		{"tomorrow is not counted", []workout.Workout{gen("t", 1), gen("a", 0)}, 1},
		{"capped at seven", func() []workout.Workout {
			var ws []workout.Workout
			for d := 0; d >= -9; d-- {
				ws = append(ws, gen("x"+fcDay(d), d))
			}
			return ws
		}(), 7},
		{"a rider's own hard session is not a generated one", func() []workout.Workout {
			own := gen("own", -1)
			own.GoalID = ""
			return []workout.Workout{gen("a", 0), own}
		}(), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := consecutiveHardDays(c.ws, fcToday); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

func fcSnap(date string, ctl, atl float64) *workout.FitnessSnapshot {
	return &workout.FitnessSnapshot{Date: date, CTL: ctl, ATL: atl, TSB: ctl - atl}
}

func TestForecastTomorrowEligibility(t *testing.T) {
	adjusted := fcHard("t", fcDay(1), 200)
	adjusted.Description += " " + scheduler.AdjustedMarker + " already eased"
	ownWorkout := fcHard("t", fcDay(1), 200)
	ownWorkout.GoalID = ""
	ownWorkout.Description = "mine"
	// The 2-day run makes tomorrow a caution whenever it is eligible at all.
	twoDays := []workout.Workout{fcHard("a", fcDay(0), 200), fcHard("b", fcDay(-1), 200)}

	cases := []struct {
		name     string
		tomorrow []workout.Workout
		sessions []workout.CompletedSession
	}{
		{"no workout tomorrow", nil, nil},
		{"only an easy workout tomorrow", []workout.Workout{fcEasy("t", fcDay(1))}, nil},
		{"rider-authored workout", []workout.Workout{ownWorkout}, nil},
		{"already adjusted", []workout.Workout{adjusted}, nil},
		{"already done", []workout.Workout{fcHard("t", fcDay(1), 200)},
			[]workout.CompletedSession{{Date: fcDay(1), Sport: "cycling", DurationSeconds: 3600}}},
		{"only a workout the day after tomorrow", []workout.Workout{fcHard("t", fcDay(2), 200)}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := append(append([]workout.Workout{}, twoDays...), c.tomorrow...)
			_, _, ok := forecastTomorrow(fcToday, ws, c.sessions, nil, readiness.Assessment{Verdict: readiness.Ready}, workout.RiderProfile{FTPWatts: 200})
			if ok {
				t.Error("ok = true, want no forecast without an eligible tomorrow workout")
			}
		})
	}
}

func TestForecastTomorrowVerdicts(t *testing.T) {
	tomorrow := fcHard("t", fcDay(1), 200)
	profile := workout.RiderProfile{FTPWatts: 200}
	ready := readiness.Assessment{Verdict: readiness.Ready}

	t.Run("every input clean is ready", func(t *testing.T) {
		got, w, ok := forecastTomorrow(fcToday, []workout.Workout{tomorrow}, nil, nil, ready, profile)
		if !ok || got.Verdict != readiness.Ready || w.ID != "t" {
			t.Errorf("got %+v %q %v, want ready for workout t", got, w.ID, ok)
		}
	})

	t.Run("today's rest is caution for tomorrow", func(t *testing.T) {
		got, _, ok := forecastTomorrow(fcToday, []workout.Workout{tomorrow}, nil, nil, readiness.Assessment{Verdict: readiness.Rest}, profile)
		if !ok || got.Verdict != readiness.Caution {
			t.Errorf("got %+v %v, want caution", got, ok)
		}
	})

	// With a rest day today the projection is CTL*exp(-1/42) - ATL*exp(-1/7);
	// CTL 0 makes that exactly -ATL*exp(-1/7), so ATL picks the projected TSB.
	atlFor := func(projected float64) float64 { return projected / math.Exp(-1.0/7) }
	t.Run("projected TSB -29 is ready, -31 is rest", func(t *testing.T) {
		got, _, _ := forecastTomorrow(fcToday, []workout.Workout{tomorrow}, nil, fcSnap(fcDay(0), 0, atlFor(29)), ready, profile)
		if got.Verdict != readiness.Ready {
			t.Errorf("-29: got %+v, want ready", got)
		}
		got, _, _ = forecastTomorrow(fcToday, []workout.Workout{tomorrow}, nil, fcSnap(fcDay(0), 0, atlFor(31)), ready, profile)
		if got.Verdict != readiness.Rest || !strings.HasPrefix(got.Reasons[0], "tomorrow's form is projected at −31") {
			t.Errorf("-31: got %+v, want rest with the projected form", got)
		}
	})

	t.Run("a stale snapshot gives no TSB reason", func(t *testing.T) {
		got, _, _ := forecastTomorrow(fcToday, []workout.Workout{tomorrow}, nil, fcSnap(fcDay(-3), 0, 200), ready, profile)
		if got.Verdict != readiness.Ready {
			t.Errorf("got %+v, want ready", got)
		}
	})

	t.Run("no FTP with a hard session planned today gives no TSB reason, and leaves other reasons alone", func(t *testing.T) {
		ws := []workout.Workout{tomorrow, fcHard("today", fcDay(0), 200), fcHard("yday", fcDay(-1), 200)}
		got, _, _ := forecastTomorrow(fcToday, ws, nil, fcSnap(fcDay(0), 0, 200), ready, workout.RiderProfile{})
		if got.Verdict != readiness.Caution || len(got.Reasons) != 1 || !strings.Contains(got.Reasons[0], "third hard day") {
			t.Errorf("got %+v, want caution from consecutive days alone", got)
		}
	})

	t.Run("caution on a zone-less hard workout has no rung to step down to", func(t *testing.T) {
		legacy := tomorrow
		legacy.Zone, legacy.Level, legacy.Name = "", 0, "Tempo ride"
		_, _, ok := forecastTomorrow(fcToday, []workout.Workout{legacy}, nil, nil, readiness.Assessment{Verdict: readiness.Rest}, profile)
		if ok {
			t.Error("ok = true, want none: a caution cannot be applied to it")
		}
		got, _, ok := forecastTomorrow(fcToday, []workout.Workout{legacy}, nil, fcSnap(fcDay(0), 0, atlFor(40)), ready, profile)
		if !ok || got.Verdict != readiness.Rest {
			t.Errorf("rest on a legacy workout: got %+v %v, want rest (a swap needs no ladder)", got, ok)
		}
	})
}
