package weather

import (
	"reflect"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The fixed "today" is Wednesday 2026-03-25; the horizon is Wed to Sat.
const (
	wed = "2026-03-25"
	thu = "2026-03-26"
	fri = "2026-03-27"
	sat = "2026-03-28"
	sun = "2026-03-29" // beyond the horizon
)

// nowAt is 08:00 local on the 25th in a +2 h forecast zone (06:00 UTC).
var nowAt = time.Date(2026, 3, 25, 6, 0, 0, 0, time.UTC)

func fourDays() Forecast {
	var hs []Hour
	for _, d := range []string{wed, thu, fri, sat, sun} {
		hs = append(hs, hoursFor(d, 15, 0, 0, 10, 15, 1)...)
	}
	return Forecast{Hours: hs, UTCOffsetSeconds: 7200}
}

// wet makes every hour of date rainy from hour from up to (not including) to.
func wet(f Forecast, date string, from, to int) {
	for i := range f.Hours {
		h := &f.Hours[i]
		if h.Date == date && h.Hour >= from && h.Hour < to {
			h.RainProb, h.Rain = 90, 1.5
		}
	}
}

func opts() Options {
	return Options{StartHour: 9, EndHour: 12, Today: wed, Now: nowAt}
}

func ride(id, date string, seconds float64) workout.Workout {
	return workout.Workout{
		ID: id, Rider: "wilant", Sport: model.SportCycling, Name: "Endurance ride", Date: date,
		Steps: []workout.WorkoutStep{{Name: "Ride", Duration: workout.DurationTime, Seconds: seconds, Target: workout.TargetOpen}},
	}
}

func ids(ss []Suggestion) []string {
	out := []string{}
	for _, s := range ss {
		out = append(out, s.WorkoutID)
	}
	return out
}

func TestDaysCoverTodayAndTheNextThree(t *testing.T) {
	f := fourDays()
	wet(f, fri, 9, 12)
	days := Days(f, opts())
	if len(days) != 4 {
		t.Fatalf("days = %d, want 4", len(days))
	}
	want := []string{wed, thu, fri, sat}
	for i, d := range days {
		if d.Date != want[i] {
			t.Errorf("day %d = %s, want %s", i, d.Date, want[i])
		}
		if d.Verdict.Bad != (d.Date == fri) {
			t.Errorf("%s bad = %v", d.Date, d.Verdict.Bad)
		}
	}
	if len(days[2].Verdict.Reasons) == 0 || days[2].Verdict.Codes[0] != CodeRain {
		t.Errorf("friday verdict = %+v", days[2].Verdict)
	}
}

func TestDaysSkipTodaysPastHours(t *testing.T) {
	f := fourDays()
	wet(f, wed, 9, 12)
	o := opts()
	o.Now = time.Date(2026, 3, 25, 11, 30, 0, 0, time.UTC) // 13:30 local
	if days := Days(f, o); days[0].Verdict.Bad {
		t.Fatalf("rain that already fell today counted: %+v", days[0].Verdict)
	}
}

func TestSuggestCandidates(t *testing.T) {
	f := fourDays()
	for _, d := range []string{wed, thu, fri, sat} {
		wet(f, d, 0, 24)
	}
	indoorWk := ride("indoor", thu, 3600)
	indoorWk.Indoor = true
	running := ride("run", thu, 3600)
	running.Sport = model.SportRunning
	test := ride("test", thu, 3600)
	test.TestProtocol = "ramp"
	undated := ride("undated", "", 3600)

	workouts := []workout.Workout{
		ride("today", wed, 3600),
		ride("thu", thu, 3600),
		ride("sat", sat, 3600),
		ride("yesterday", "2026-03-24", 3600),
		ride("beyond", sun, 3600),
		ride("ridden", fri, 3600),
		indoorWk, running, test, undated,
	}
	got := ids(Suggest(f, workouts, map[string]bool{"ridden": true}, opts()))
	want := []string{"today", "thu", "sat"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("suggested for %v, want %v", got, want)
	}
}

func TestSuggestGoodDayGivesNone(t *testing.T) {
	f := fourDays()
	wet(f, fri, 9, 12)
	got := ids(Suggest(f, []workout.Workout{ride("thu", thu, 3600), ride("sat", sat, 3600)}, nil, opts()))
	if len(got) != 0 {
		t.Fatalf("suggestions on dry days: %v", got)
	}
}

func TestSuggestCarriesReasonsAndCodes(t *testing.T) {
	f := fourDays()
	wet(f, fri, 9, 12)
	got := Suggest(f, []workout.Workout{ride("fri", fri, 3600)}, nil, opts())
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	s := got[0]
	if s.WorkoutID != "fri" || s.Date != fri {
		t.Errorf("suggestion = %+v", s)
	}
	if len(s.Reasons) != 1 || s.Reasons[0] == "" || !reflect.DeepEqual(s.Codes, []string{CodeRain}) {
		t.Errorf("reasons = %q codes = %v", s.Reasons, s.Codes)
	}
}

func TestSuggestUsesTheRidesOwnLength(t *testing.T) {
	// Rain only at 12:00, just after the default 9-12 window.
	f := fourDays()
	wet(f, thu, 12, 13)
	short := Suggest(f, []workout.Workout{ride("short", thu, 2*3600)}, nil, opts())
	if len(short) != 0 {
		t.Errorf("a 2 h ride is judged on 9-12 and must not see 12:00 rain: %v", ids(short))
	}
	long := Suggest(f, []workout.Workout{ride("long", thu, 4*3600)}, nil, opts())
	if len(long) != 1 {
		t.Errorf("a 4 h ride runs 9-13 and must see it: %v", ids(long))
	}
	// Six hours is the cap: rain at 16:00 is out of reach for a 9 h ride.
	g := fourDays()
	wet(g, thu, 15, 16)
	if got := Suggest(g, []workout.Workout{ride("huge", thu, 9*3600)}, nil, opts()); len(got) != 0 {
		t.Errorf("window not capped at 6 h: %v", ids(got))
	}
}

func TestSuggestTodayPastItsWindowGivesNone(t *testing.T) {
	f := fourDays()
	wet(f, wed, 9, 12)
	o := opts()
	o.Now = time.Date(2026, 3, 25, 11, 30, 0, 0, time.UTC) // 13:30 local
	if got := Suggest(f, []workout.Workout{ride("today", wed, 3600)}, nil, o); len(got) != 0 {
		t.Fatalf("suggested for rain that has already fallen: %v", ids(got))
	}
	// Earlier that morning the same rain is still ahead.
	o.Now = time.Date(2026, 3, 25, 6, 0, 0, 0, time.UTC)
	if got := Suggest(f, []workout.Workout{ride("today", wed, 3600)}, nil, o); len(got) != 1 {
		t.Fatalf("no suggestion while the rain is still ahead")
	}
}

func TestSuggestSwitchOnlyForSmartTrainerRiders(t *testing.T) {
	f := fourDays()
	wet(f, fri, 9, 12)
	w := []workout.Workout{ride("fri", fri, 3600)}

	o := opts()
	if got := Suggest(f, w, nil, o); got[0].CanSwitch {
		t.Error("canSwitch without the preference")
	}
	o.SmartTrainer = true
	got := Suggest(f, w, nil, o)
	if !got[0].CanSwitch {
		t.Error("canSwitch false for a smart-trainer rider")
	}
	if got[0].AltDate != "" {
		t.Errorf("a smart-trainer rider is offered a move (%s), not the switch", got[0].AltDate)
	}
}

func TestSuggestAltDate(t *testing.T) {
	bad := func() Forecast {
		f := fourDays()
		wet(f, thu, 9, 12)
		return f
	}
	cases := []struct {
		name      string
		forecast  func() Forecast
		workouts  []workout.Workout
		available []string
		want      string
	}{
		{"the next dry day", bad, []workout.Workout{ride("a", thu, 3600)}, nil, fri},
		{"skips a day that already has a workout", bad,
			[]workout.Workout{ride("a", thu, 3600), ride("b", fri, 3600)}, nil, sat},
		{"skips a day that is not an available day", bad,
			[]workout.Workout{ride("a", thu, 3600)}, []string{"sat", "sun"}, sat},
		{"an available day list containing the day allows it", bad,
			[]workout.Workout{ride("a", thu, 3600)}, []string{"fri"}, fri},
		{"never a day before the workout", func() Forecast { f := fourDays(); wet(f, fri, 9, 12); return f },
			[]workout.Workout{ride("a", fri, 3600)}, nil, sat},
		{"skips a wet day", func() Forecast { f := bad(); wet(f, fri, 9, 12); return f },
			[]workout.Workout{ride("a", thu, 3600)}, nil, sat},
		{"none inside the horizon", func() Forecast { f := bad(); wet(f, fri, 9, 12); wet(f, sat, 9, 12); return f },
			[]workout.Workout{ride("a", thu, 3600)}, nil, ""},
		{"none when the only dry days are not available", bad,
			[]workout.Workout{ride("a", thu, 3600)}, []string{"mon"}, ""},
		{"a workout on the dry day of another sport still occupies it", bad,
			[]workout.Workout{ride("a", thu, 3600), func() workout.Workout {
				r := ride("run", fri, 3600)
				r.Sport = model.SportRunning
				return r
			}()}, nil, sat},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := opts()
			o.AvailableDays = tc.available
			got := Suggest(tc.forecast(), tc.workouts, nil, o)
			var sug *Suggestion
			for i := range got {
				if got[i].WorkoutID == "a" {
					sug = &got[i]
				}
			}
			if sug == nil {
				t.Fatalf("no suggestion for a; got %v", ids(got))
			}
			if sug.AltDate != tc.want {
				t.Errorf("altDate = %q, want %q", sug.AltDate, tc.want)
			}
		})
	}
}
