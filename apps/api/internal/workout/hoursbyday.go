package workout

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
)

// MaxHoursPerDay is the most a rider can offer for one day. It matches the
// ceiling the old single figure was validated against.
const MaxHoursPerDay = 12.0

// weekdayKeys is the canonical order of RiderProfile.AvailableDays and the
// keys of HoursByDay: Monday first, like every week in the app.
var weekdayKeys = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// availabilityTolerance is how far two mean figures may differ and still be
// the same availability: the old figure was typed with at most two decimals.
const availabilityTolerance = 0.01

// HoursOn is the hours the rider offers on a weekday ("mon".."sun"). With a
// per-day map it is that map's value; without one, the old single figure on
// each available day, so a profile saved before per-day hours reads the same.
func (p RiderProfile) HoursOn(day string) float64 {
	if len(p.HoursByDay) > 0 {
		return p.HoursByDay[day]
	}
	if slices.Contains(p.AvailableDays, day) {
		return p.HoursPerAvailableDay
	}
	return 0
}

// WeekHours is the hours the rider offers across the week.
func (p RiderProfile) WeekHours() float64 {
	var sum float64
	for _, d := range weekdayKeys {
		sum += p.HoursOn(d)
	}
	return sum
}

// MaxDayHours is the most the rider offers on any one day.
func (p RiderProfile) MaxDayHours() float64 {
	var most float64
	for _, d := range weekdayKeys {
		most = max(most, p.HoursOn(d))
	}
	return most
}

// UniformHours is the map that gives every day the same hours; nil when there
// are none to give, because a day with no hours is a day left out.
func UniformHours(days []string, hours float64) map[string]float64 {
	if hours <= 0 || len(days) == 0 {
		return nil
	}
	m := make(map[string]float64, len(days))
	for _, d := range days {
		m[d] = hours
	}
	return m
}

// ValidateHoursByDay refuses unknown weekdays and hours outside 0..MaxHoursPerDay
// (NaN included) and returns the map without its zero days: 0 means "off", and
// an off day is simply absent so the map and AvailableDays cannot disagree.
func ValidateHoursByDay(m map[string]float64) (map[string]float64, error) {
	out := make(map[string]float64, len(m))
	for day, h := range m {
		if !slices.Contains(weekdayKeys, day) {
			return nil, fmt.Errorf("workout: %q is not a weekday", day)
		}
		if math.IsNaN(h) || h < 0 || h > MaxHoursPerDay {
			return nil, fmt.Errorf("workout: hours for %s must be between 0 and %g", day, MaxHoursPerDay)
		}
		if h > 0 {
			out[day] = h
		}
	}
	return out, nil
}

// SameAvailability reports whether days and hours describe what the profile
// already stores in the old columns: the same day set and the same mean.
// Callers that still speak the old shape use it to tell "nothing changed"
// from a real edit without clobbering a per-day map.
func SameAvailability(stored RiderProfile, days []string, hours float64) bool {
	return sameDaySet(stored.AvailableDays, days) &&
		math.Abs(hours-stored.HoursPerAvailableDay) <= availabilityTolerance
}

func sameDaySet(a, b []string) bool {
	as, bs := slices.Clone(a), slices.Clone(b)
	slices.Sort(as)
	slices.Sort(bs)
	return slices.Equal(slices.Compact(as), slices.Compact(bs))
}

// NormaliseAvailability makes the three availability fields agree. The map is
// authoritative when it has any day: AvailableDays is its keys and
// HoursPerAvailableDay its mean. The old columns are still written that way so
// every reader that has not learned per-day hours, and a rollback to an older
// image, sees the same weekly total (days x mean). With no map the old figure
// is spread over its days; with days but no figure the days stay and the map
// stays empty, meaning "available, no per-day cap".
func (p *RiderProfile) NormaliseAvailability() {
	if len(p.HoursByDay) > 0 {
		days := make([]string, 0, len(p.HoursByDay))
		var sum float64
		for _, d := range weekdayKeys {
			if h, ok := p.HoursByDay[d]; ok {
				days = append(days, d)
				sum += h
			}
		}
		p.AvailableDays = days
		p.HoursPerAvailableDay = sum / float64(len(days))
		return
	}
	p.HoursByDay = UniformHours(p.AvailableDays, p.HoursPerAvailableDay)
}

// encodeHoursByDay is the column value: a JSON object, ” for none.
func encodeHoursByDay(m map[string]float64) (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("workout: encode hours by day: %w", err)
	}
	return string(b), nil
}

// decodeHoursByDay reads the column; ” is no map.
func decodeHoursByDay(raw string) (map[string]float64, error) {
	if raw == "" {
		return nil, nil
	}
	var m map[string]float64
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("workout: decode hours by day: %w", err)
	}
	return m, nil
}

// reconcileStoredHours drops a stored map that the old columns contradict. A
// rolled-back image writes only available_days and hours_per_available_day and
// leaves hours_by_day behind, so the old columns are the newer truth whenever
// they disagree with it.
func reconcileStoredHours(m map[string]float64, days []string, perDay float64) map[string]float64 {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	var sum float64
	for d, h := range m {
		keys = append(keys, d)
		sum += h
	}
	if !sameDaySet(keys, days) || math.Abs(sum/float64(len(keys))-perDay) > availabilityTolerance {
		return nil
	}
	return m
}
