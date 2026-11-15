package workout

import (
	"math"
	"reflect"
	"testing"
)

func TestNormaliseDerivesDaysAndMeanFromTheMap(t *testing.T) {
	p := RiderProfile{HoursByDay: map[string]float64{"sat": 3, "mon": 1, "sun": 3}}
	p.NormaliseAvailability()
	if want := []string{"mon", "sat", "sun"}; !reflect.DeepEqual(p.AvailableDays, want) {
		t.Errorf("days = %v, want %v", p.AvailableDays, want)
	}
	if math.Abs(p.HoursPerAvailableDay-7.0/3) > 1e-9 {
		t.Errorf("mean = %v, want 7/3", p.HoursPerAvailableDay)
	}
}

func TestNormaliseSpreadsTheOldFigure(t *testing.T) {
	p := RiderProfile{AvailableDays: []string{"tue", "sat"}, HoursPerAvailableDay: 1.5}
	p.NormaliseAvailability()
	want := map[string]float64{"tue": 1.5, "sat": 1.5}
	if !reflect.DeepEqual(p.HoursByDay, want) {
		t.Errorf("map = %v, want %v", p.HoursByDay, want)
	}
}

func TestDaysWithoutHoursKeepTheirDays(t *testing.T) {
	p := RiderProfile{AvailableDays: []string{"tue", "sat"}}
	p.NormaliseAvailability()
	if len(p.HoursByDay) != 0 {
		t.Errorf("map = %v, want empty", p.HoursByDay)
	}
	if want := []string{"tue", "sat"}; !reflect.DeepEqual(p.AvailableDays, want) {
		t.Errorf("days = %v, want %v", p.AvailableDays, want)
	}
	if p.HoursOn("tue") != 0 || p.WeekHours() != 0 {
		t.Errorf("HoursOn(tue)=%v WeekHours=%v, want 0", p.HoursOn("tue"), p.WeekHours())
	}
}

func TestWeekHoursIsTheSum(t *testing.T) {
	p := RiderProfile{HoursByDay: map[string]float64{"mon": 1, "tue": 1, "wed": 1, "thu": 1, "fri": 1, "sat": 3, "sun": 3}}
	if got := p.WeekHours(); got != 11 {
		t.Errorf("WeekHours = %v, want 11", got)
	}
	if got := p.MaxDayHours(); got != 3 {
		t.Errorf("MaxDayHours = %v, want 3", got)
	}
}

func TestValidateHoursByDay(t *testing.T) {
	for name, m := range map[string]map[string]float64{
		"unknown day": {"xyz": 1},
		"negative":    {"mon": -1},
		"nan":         {"mon": math.NaN()},
		"too many":    {"mon": 12.5},
	} {
		if _, err := ValidateHoursByDay(m); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	got, err := ValidateHoursByDay(map[string]float64{"mon": 0, "sat": 3})
	if err != nil || !reflect.DeepEqual(got, map[string]float64{"sat": 3}) {
		t.Errorf("zero dropped: got %v, %v", got, err)
	}
	if _, err := ValidateHoursByDay(map[string]float64{"mon": 12}); err != nil {
		t.Errorf("12 is allowed: %v", err)
	}
}

func TestSameAvailability(t *testing.T) {
	stored := RiderProfile{HoursByDay: map[string]float64{"mon": 1, "sat": 3}}
	stored.NormaliseAvailability()
	for name, c := range map[string]struct {
		days  []string
		hours float64
		want  bool
	}{
		"same within tolerance": {[]string{"sat", "mon"}, 2.005, true},
		"fewer days":            {[]string{"mon"}, 2, false},
		"different mean":        {[]string{"mon", "sat"}, 2.1, false},
	} {
		if got := SameAvailability(stored, c.days, c.hours); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}
