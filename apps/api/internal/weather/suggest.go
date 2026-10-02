package weather

import (
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// HorizonDays is how far ahead weather is judged: today and the next three
// days, which is what one forecast request (forecast_days=4) covers.
const HorizonDays = 4

const dateLayout = "2006-01-02"

// Options is everything Suggest needs to know about the rider and the moment.
// Today is the rider's own local date; Now is only used, with the forecast's UTC
// offset, to skip hours already past today.
type Options struct {
	SmartTrainer       bool
	AvailableDays      []string // "mon".."sun"; empty means any day
	StartHour, EndHour int      // the rider's configured ride window
	Today              string
	Now                time.Time
	// Blackout is the days the rider is away from training (a life event):
	// nothing is suggested for a session on one, and none is offered as an
	// alternative.
	Blackout map[string]bool
}

// Day is one forecast day judged over the rider's configured window.
type Day struct {
	Date    string
	Verdict Verdict
}

// Suggestion offers something for one planned session. It only ever offers:
// nothing here writes a workout.
type Suggestion struct {
	WorkoutID string
	Date      string
	Reasons   []string
	Codes     []string
	// CanSwitch is true only for a rider who has said they have a smart
	// trainer; everyone else gets information and, maybe, AltDate.
	CanSwitch bool
	// AltDate is a later dry, free, available day, offered only to riders who
	// cannot switch.
	AltDate string
}

func horizon(today string) []string {
	start, err := time.Parse(dateLayout, today)
	if err != nil {
		return nil
	}
	out := make([]string, HorizonDays)
	for i := range out {
		out[i] = start.AddDate(0, 0, i).Format(dateLayout)
	}
	return out
}

func inHorizon(days []string, date string) bool {
	for _, d := range days {
		if d == date {
			return true
		}
	}
	return false
}

// Days judges each day from today over the rider's configured window, skipping
// today's past hours, for the chip and for AltDate. A day the forecast does not
// cover is left out.
func Days(f Forecast, o Options) []Day {
	covered := map[string]bool{}
	for _, h := range f.Hours {
		covered[h.Date] = true
	}
	var out []Day
	for _, date := range horizon(o.Today) {
		if !covered[date] {
			continue
		}
		w := ClipToNow(Window{Date: date, StartHour: o.StartHour, EndHour: o.EndHour}, f, o.Now)
		out = append(out, Day{Date: date, Verdict: Assess(f, w)})
	}
	return out
}

// candidate is a session weather may comment on: planned, cycling, not already
// indoor, not an FTP test, not ridden, and inside the horizon (which is also
// what excludes the past).
func candidate(wk workout.Workout, days []string, ridden map[string]bool) bool {
	return wk.Sport == model.SportCycling &&
		!wk.Indoor &&
		wk.TestProtocol == "" &&
		!ridden[wk.ID] &&
		inHorizon(days, wk.Date)
}

// Suggest returns one Suggestion per candidate session whose ride window looks
// bad. The window is the rider's configured hours, extended by the session's own
// planned length (WindowFor), so a 4-hour ride is judged on four hours; today's
// past hours are skipped, and none left means no suggestion.
func Suggest(f Forecast, workouts []workout.Workout, ridden map[string]bool, o Options) []Suggestion {
	horizonDays := horizon(o.Today)
	days := Days(f, o)

	occupied := map[string]bool{}
	for d := range o.Blackout {
		occupied[d] = true
	}
	for _, wk := range workouts {
		if wk.Date != "" {
			occupied[wk.Date] = true
		}
	}

	var out []Suggestion
	for _, wk := range workouts {
		if o.Blackout[wk.Date] || !candidate(wk, horizonDays, ridden) {
			continue
		}
		end := WindowFor(o.StartHour, o.EndHour, workout.PlannedSeconds(wk.Steps))
		v := Assess(f, ClipToNow(Window{Date: wk.Date, StartHour: o.StartHour, EndHour: end}, f, o.Now))
		if !v.Bad {
			continue
		}
		s := Suggestion{
			WorkoutID: wk.ID, Date: wk.Date,
			Reasons: v.Reasons, Codes: v.Codes,
			CanSwitch: o.SmartTrainer,
		}
		if !o.SmartTrainer {
			s.AltDate = altDate(days, wk.Date, occupied, o.AvailableDays)
		}
		out = append(out, s)
	}
	return out
}

// altDate is the first day after date, inside the horizon, that is dry over the
// configured window, holds no workout at all, and (when the rider has stated
// which days they can train) is one of them.
func altDate(days []Day, date string, occupied map[string]bool, available []string) string {
	for _, d := range days {
		if d.Date <= date || d.Verdict.Bad || occupied[d.Date] {
			continue
		}
		if len(available) > 0 && !dayIn(available, d.Date) {
			continue
		}
		return d.Date
	}
	return ""
}

func dayIn(available []string, date string) bool {
	t, err := time.Parse(dateLayout, date)
	if err != nil {
		return false
	}
	abbr := strings.ToLower(t.Weekday().String()[:3])
	for _, a := range available {
		if strings.ToLower(strings.TrimSpace(a)) == abbr {
			return true
		}
	}
	return false
}
