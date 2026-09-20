// Package autoprofile fills in a rider's fitness profile without asking:
// numbers Garmin's own account already holds (max heart rate, threshold
// pace, FTP), and the training pattern — which days, how many hours, how
// experienced — read off what the rider has actually been doing.
//
// It exists because the profile is what every downstream step needs:
// periodization sizes weeks from hours × days, the scheduler picks targets
// from FTP/pace/HR, and with nothing on file scheduling quietly returns no
// workouts at all. Asking a new rider to type seven fields before anything
// works is the biggest manual step left between "connected a device" and
// "workouts appear", and most of those answers are already sitting in their
// history.
//
// Two rules keep this safe, and both are the same rule
// workout.RiderProfile.FTPEstimated already established for FTP:
//
//   - Apply only ever writes a field that is unset or already an estimate. A
//     value the rider typed or saved is never touched — saving the profile
//     form clears the estimated list, which is what "the rider looked at it
//     and confirmed it" means.
//   - Everything it writes is labelled as an estimate, so the UI can say so
//     and a later sync can keep refining it.
//
// Pure functions of their inputs, like periodization.BuildPlan and
// fitnesstest.EstimateFTP: no store, no network, no clock but the one passed
// in.
package autoprofile

import (
	"math"
	"sort"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// windowWeeks is how much recent history the pattern is read from: long
// enough that one holiday or illness does not redefine "the days you
// train", short enough that a rider who changed routine two seasons ago is
// judged on the routine they have now.
const windowWeeks = 12

// Thresholds below which there is not enough history to say anything. A
// guess from three rides would set a rider's available days to whichever
// three weekdays they happened to ride, and the plan would then be built
// around noise.
const (
	minSessions = 8
	minWeeks    = 3
)

// Bounds on the hours-per-day budget: inference from a rider who did one
// huge weekend ride and nothing else should not produce an 8-hour day.
const (
	minHoursPerDay = 0.5
	maxHoursPerDay = 4.0
)

var weekdayNames = [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// weekdayOrder is Monday-first, matching how the rest of the training code
// (and a rider's calendar) reads a week.
var weekdayOrder = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// Inferred is a training pattern read from history.
type Inferred struct {
	AvailableDays        []string
	HoursPerAvailableDay float64
	ExperienceLevel      string
}

// Infer reads the last windowWeeks of sessions. ok is false when there is
// too little history to say anything — the caller's cue to leave the fields
// empty, not to guess.
func Infer(sessions []workout.CompletedSession, today time.Time) (Inferred, bool) {
	windowStart := periodization.MondayOf(today).AddDate(0, 0, -(windowWeeks-1)*7)

	perWeekday := map[string]map[string]bool{} // weekday -> set of distinct dates trained
	weekSeconds := map[string]float64{}        // Monday date -> seconds
	dates := map[string]bool{}
	count := 0

	for _, s := range sessions {
		day, err := time.Parse("2006-01-02", s.Date)
		if err != nil || day.Before(windowStart) || day.After(today) {
			continue
		}
		count++
		dates[s.Date] = true
		name := weekdayNames[day.Weekday()]
		if perWeekday[name] == nil {
			perWeekday[name] = map[string]bool{}
		}
		perWeekday[name][s.Date] = true
		weekSeconds[periodization.MondayOf(day).Format("2006-01-02")] += s.DurationSeconds
	}
	if count < minSessions || len(weekSeconds) < minWeeks {
		return Inferred{}, false
	}

	days := availableDays(perWeekday)
	weekly := topWeeklyHours(weekSeconds, 4)
	perDay := roundToQuarter(weekly / float64(len(days)))
	perDay = math.Min(maxHoursPerDay, math.Max(minHoursPerDay, perDay))

	// Average across the whole window, empty weeks included: a rider who
	// trains hard for three weeks a quarter is not an "advanced" rider.
	var total float64
	for _, secs := range weekSeconds {
		total += secs
	}
	avgWeeklyHours := total / 3600 / windowWeeks

	return Inferred{
		AvailableDays:        days,
		HoursPerAvailableDay: perDay,
		ExperienceLevel:      experience(avgWeeklyHours, len(dates)),
	}, true
}

// availableDays is every weekday the rider trains on in at least a quarter
// of the window's weeks — a day they did once in twelve weeks is an
// exception, not a habit — and never fewer than two, or more than six: a
// plan needs somewhere to put a hard day and an easy one, and a rider who
// trains seven days still needs a rest day the plan can respect.
func availableDays(perWeekday map[string]map[string]bool) []string {
	threshold := windowWeeks / 4
	type entry struct {
		name  string
		count int
		order int
	}
	var all []entry
	for i, name := range weekdayOrder {
		all = append(all, entry{name, len(perWeekday[name]), i})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].count > all[j].count })

	chosen := map[string]bool{}
	for _, e := range all {
		if len(chosen) >= 6 {
			break
		}
		if e.count >= threshold || len(chosen) < 2 {
			if e.count == 0 {
				break
			}
			chosen[e.name] = true
		}
	}
	out := make([]string, 0, len(chosen))
	for _, name := range weekdayOrder {
		if chosen[name] {
			out = append(out, name)
		}
	}
	return out
}

// topWeeklyHours is the mean of the n biggest weeks, in hours. periodization
// treats hours-per-day × days as the rider's *peak* week, so the budget is
// read from their best recent weeks rather than their average — averaging
// would build every plan around a week the rider is already capable of
// beating.
func topWeeklyHours(weekSeconds map[string]float64, n int) float64 {
	hours := make([]float64, 0, len(weekSeconds))
	for _, secs := range weekSeconds {
		hours = append(hours, secs/3600)
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(hours)))
	if len(hours) > n {
		hours = hours[:n]
	}
	var sum float64
	for _, h := range hours {
		sum += h
	}
	return sum / float64(len(hours))
}

func roundToQuarter(v float64) float64 { return math.Round(v*4) / 4 }

// experience is a deliberately rough three-way cut on how much the rider
// actually trains — volume and frequency, nothing cleverer. The field is a
// free-text label today, read by a human, not an input to any calculation.
func experience(avgWeeklyHours float64, trainingDays int) string {
	switch {
	case avgWeeklyHours >= 8 && trainingDays >= windowWeeks*3:
		return "advanced"
	case avgWeeklyHours >= 3:
		return "intermediate"
	default:
		return "beginner"
	}
}

// Suggestion is everything that can be filled in, from any source. A zero
// or empty field means "no suggestion for this one".
type Suggestion struct {
	FTPWatts              float64
	MaxHR                 int
	ThresholdPaceSecPerKM float64
	RestingHR             int
	AvailableDays         []string
	HoursPerAvailableDay  float64
	ExperienceLevel       string
}

// Apply merges s into p and returns the result with the names of the fields
// it changed. It writes a field only when the rider has not set it (zero /
// empty) or it is already an estimate; anything the rider has confirmed is
// left exactly as it is. Every field it writes is marked estimated.
//
// The caller owns p.Rider: a never-saved profile arrives as a zero value.
func Apply(p workout.RiderProfile, s Suggestion) (workout.RiderProfile, []string) {
	var changed []string

	if s.FTPWatts > 0 && (p.FTPWatts == 0 || p.FTPEstimated) && s.FTPWatts != p.FTPWatts {
		p.FTPWatts, p.FTPEstimated = s.FTPWatts, true
		changed = append(changed, "ftp")
	}
	if s.MaxHR > 0 && fillable(p.MaxHR == 0, p.IsEstimated(workout.FieldMaxHR)) && s.MaxHR != p.MaxHR {
		p.MaxHR = s.MaxHR
		p.MarkEstimated(workout.FieldMaxHR)
		changed = append(changed, workout.FieldMaxHR)
	}
	if s.ThresholdPaceSecPerKM > 0 && fillable(p.ThresholdPaceSecPerKM == 0, p.IsEstimated(workout.FieldThresholdPace)) &&
		s.ThresholdPaceSecPerKM != p.ThresholdPaceSecPerKM {
		p.ThresholdPaceSecPerKM = s.ThresholdPaceSecPerKM
		p.MarkEstimated(workout.FieldThresholdPace)
		changed = append(changed, workout.FieldThresholdPace)
	}
	if s.RestingHR > 0 && fillable(p.RestingHR == 0, p.IsEstimated(workout.FieldRestingHR)) && s.RestingHR != p.RestingHR {
		p.RestingHR = s.RestingHR
		p.MarkEstimated(workout.FieldRestingHR)
		changed = append(changed, workout.FieldRestingHR)
	}
	if len(s.AvailableDays) > 0 && fillable(len(p.AvailableDays) == 0, p.IsEstimated(workout.FieldAvailableDays)) &&
		!sameDays(s.AvailableDays, p.AvailableDays) {
		p.AvailableDays = s.AvailableDays
		p.MarkEstimated(workout.FieldAvailableDays)
		changed = append(changed, workout.FieldAvailableDays)
	}
	if s.HoursPerAvailableDay > 0 && fillable(p.HoursPerAvailableDay == 0, p.IsEstimated(workout.FieldHoursPerAvailableDay)) &&
		s.HoursPerAvailableDay != p.HoursPerAvailableDay {
		p.HoursPerAvailableDay = s.HoursPerAvailableDay
		p.MarkEstimated(workout.FieldHoursPerAvailableDay)
		changed = append(changed, workout.FieldHoursPerAvailableDay)
	}
	if s.ExperienceLevel != "" && fillable(p.ExperienceLevel == "", p.IsEstimated(workout.FieldExperienceLevel)) &&
		s.ExperienceLevel != p.ExperienceLevel {
		p.ExperienceLevel = s.ExperienceLevel
		p.MarkEstimated(workout.FieldExperienceLevel)
		changed = append(changed, workout.FieldExperienceLevel)
	}
	return p, changed
}

func fillable(unset, estimated bool) bool { return unset || estimated }

func sameDays(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
