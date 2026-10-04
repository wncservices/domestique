package syncschedule_test

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/syncschedule"
)

func TestFirstOfDay(t *testing.T) {
	loc := brussels(t)
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, loc) }

	def := mustParse(t) // 06:30 and 21:00
	custom, err := syncschedule.Parse([]string{"18:00", "05:00", "12:00"}, "Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	single, err := syncschedule.Parse([]string{"07:15"}, "Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		s     syncschedule.Schedule
		slot  time.Time
		first bool
	}{
		{"morning slot", def, at(2026, 6, 10, 6, 30), true},
		{"evening slot is not the first", def, at(2026, 6, 10, 21, 0), false},
		{"a time that is no slot at all", def, at(2026, 6, 10, 6, 31), false},
		{"custom morning slot", custom, at(2026, 6, 10, 5, 0), true},
		{"custom midday slot", custom, at(2026, 6, 10, 12, 0), false},
		{"custom evening slot", custom, at(2026, 6, 10, 18, 0), false},
		{"the only slot is the first", single, at(2026, 6, 10, 7, 15), true},
		// Either side of both daylight-saving changes the first slot is still
		// 06:30 on the wall, whatever that is in UTC.
		{"the day before spring forward", def, at(2026, 3, 28, 6, 30), true},
		{"spring forward day", def, at(2026, 3, 29, 6, 30), true},
		{"spring forward evening", def, at(2026, 3, 29, 21, 0), false},
		{"the day before fall back", def, at(2026, 10, 24, 6, 30), true},
		{"fall back day", def, at(2026, 10, 25, 6, 30), true},
		{"fall back evening", def, at(2026, 10, 25, 21, 0), false},
		{"the same instant given in UTC", def, at(2026, 10, 25, 6, 30).UTC(), true},
		{"the zero time", def, time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.s.FirstOfDay(c.slot); got != c.first {
				t.Errorf("FirstOfDay(%s) = %v, want %v", c.slot, got, c.first)
			}
		})
	}
}

// A slot inside a daylight-saving gap does not exist on the wall; the schedule
// moves it forward, and FirstOfDay has to agree with Last about where.
func TestFirstOfDayAgreesWithLastInASpringForwardGap(t *testing.T) {
	loc := brussels(t)
	s, err := syncschedule.Parse([]string{"02:30", "21:00"}, "Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 29, 10, 0, 0, 0, loc)
	last := s.Last(now)
	if !s.FirstOfDay(last) {
		t.Errorf("Last(%s) = %s is the day's first slot but FirstOfDay says not", now, last)
	}
	evening := s.Last(time.Date(2026, 3, 29, 22, 0, 0, 0, loc))
	if s.FirstOfDay(evening) {
		t.Errorf("%s is not the first slot", evening)
	}
}
