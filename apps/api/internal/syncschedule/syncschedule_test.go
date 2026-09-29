package syncschedule_test

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/syncschedule"
)

func brussels(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func mustParse(t *testing.T) syncschedule.Schedule {
	t.Helper()
	s, err := syncschedule.Parse([]string{"21:00", "06:30"}, "Europe/Brussels") // unsorted on purpose
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNext(t *testing.T) {
	loc := brussels(t)
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, loc) }

	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"before the morning slot", at(2026, 6, 10, 5, 0), at(2026, 6, 10, 6, 30)},
		{"exactly on a slot moves to the next one", at(2026, 6, 10, 6, 30), at(2026, 6, 10, 21, 0)},
		{"between the slots", at(2026, 6, 10, 12, 0), at(2026, 6, 10, 21, 0)},
		{"after the evening slot rolls to tomorrow morning", at(2026, 6, 10, 21, 0), at(2026, 6, 11, 6, 30)},
		{"late night", at(2026, 6, 10, 23, 59), at(2026, 6, 11, 6, 30)},
		{"month boundary", at(2026, 6, 30, 22, 0), at(2026, 7, 1, 6, 30)},
		// Last Sunday of March 2026 is the 29th: 02:00 CET jumps to 03:00
		// CEST, so the day is 23 hours long and 06:30 is one hour earlier in
		// UTC than the day before.
		{"evening before spring forward", at(2026, 3, 28, 21, 30), at(2026, 3, 29, 6, 30)},
		{"spring forward day, morning to evening", at(2026, 3, 29, 7, 0), at(2026, 3, 29, 21, 0)},
		// Last Sunday of October 2026 is the 25th: 03:00 CEST falls back to
		// 02:00 CET, a 25 hour day.
		{"evening before fall back", at(2026, 10, 24, 21, 30), at(2026, 10, 25, 6, 30)},
		{"fall back day, after evening", at(2026, 10, 25, 22, 0), at(2026, 10, 26, 6, 30)},
	}
	s := mustParse(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := s.Next(c.now)
			if !got.Equal(c.want) {
				t.Errorf("Next(%s) = %s, want %s", c.now, got, c.want)
			}
		})
	}
}

// The wall-clock time must hold across a DST change, not the UTC offset:
// the morning slot is 06:30 local both sides of the switch.
func TestNextKeepsWallClockAcrossDST(t *testing.T) {
	loc := brussels(t)
	s := mustParse(t)

	before := s.Next(time.Date(2026, 3, 27, 22, 0, 0, 0, loc)) // 28 March, still CET
	after := s.Next(time.Date(2026, 3, 29, 7, 0, 0, 0, loc))   // 29 March 21:00, CEST
	after = s.Next(after)                                      // 30 March 06:30 CEST
	if before.In(loc).Hour() != 6 || before.In(loc).Minute() != 30 || after.In(loc).Hour() != 6 || after.In(loc).Minute() != 30 {
		t.Fatalf("slots = %s, %s, want 06:30 local both times", before.In(loc), after.In(loc))
	}
	if _, off := before.In(loc).Zone(); off != 3600 {
		t.Errorf("28 March offset = %d, want CET (3600)", off)
	}
	if _, off := after.In(loc).Zone(); off != 7200 {
		t.Errorf("30 March offset = %d, want CEST (7200)", off)
	}
}

func TestNextIsZoneNotMachineDependent(t *testing.T) {
	loc := brussels(t)
	s := mustParse(t)
	// The same instant expressed in UTC and in Tokyo gives the same answer.
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, loc)
	want := time.Date(2026, 6, 10, 21, 0, 0, 0, loc)
	for _, in := range []*time.Location{time.UTC, time.FixedZone("JST", 9*3600)} {
		if got := s.Next(now.In(in)); !got.Equal(want) {
			t.Errorf("Next in %s = %s, want %s", in, got, want)
		}
	}
}

func TestLast(t *testing.T) {
	loc := brussels(t)
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, loc) }
	s := mustParse(t)

	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"before the morning slot is last night's", at(2026, 6, 10, 5, 0), at(2026, 6, 9, 21, 0)},
		{"exactly on a slot counts", at(2026, 6, 10, 6, 30), at(2026, 6, 10, 6, 30)},
		{"between", at(2026, 6, 10, 12, 0), at(2026, 6, 10, 6, 30)},
		{"after the evening slot", at(2026, 6, 10, 23, 0), at(2026, 6, 10, 21, 0)},
		{"across the spring-forward night", at(2026, 3, 29, 5, 0), at(2026, 3, 28, 21, 0)},
		{"across the fall-back night", at(2026, 10, 25, 5, 0), at(2026, 10, 24, 21, 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := s.Last(c.now); !got.Equal(c.want) {
				t.Errorf("Last(%s) = %s, want %s", c.now, got, c.want)
			}
		})
	}
}

// The missed-run rule: on start, run once if the last sync is older than the
// most recent slot.
func TestMissed(t *testing.T) {
	loc := brussels(t)
	at := func(d, h, min int) time.Time { return time.Date(2026, 6, d, h, min, 0, 0, loc) }
	s := mustParse(t)

	cases := []struct {
		name      string
		last, now time.Time
		want      bool
	}{
		{"never ran", time.Time{}, at(10, 12, 0), true},
		{"process was down at 06:30", at(9, 21, 1), at(10, 8, 0), true},
		{"ran after the last slot", at(10, 6, 31), at(10, 8, 0), false},
		{"ran exactly on the slot", at(10, 6, 30), at(10, 8, 0), false},
		{"a 30 minute tick synced this morning", at(10, 7, 0), at(10, 12, 0), false},
		{"down through the whole evening slot", at(10, 12, 0), at(11, 5, 0), true},
		{"before any slot today, last night's was caught", at(9, 21, 5), at(10, 5, 0), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := s.Missed(c.last, c.now); got != c.want {
				t.Errorf("Missed(%s, %s) = %v, want %v", c.last, c.now, got, c.want)
			}
		})
	}
}

func TestParseValidation(t *testing.T) {
	bad := []struct {
		name  string
		times []string
		zone  string
	}{
		{"hour out of range", []string{"25:00"}, "Europe/Brussels"},
		{"minute out of range", []string{"06:61"}, "Europe/Brussels"},
		{"not a time", []string{"morning"}, "Europe/Brussels"},
		{"missing leading zero", []string{"6:30"}, "Europe/Brussels"},
		{"seconds", []string{"06:30:00"}, "Europe/Brussels"},
		{"unknown zone", []string{"06:30"}, "Mars/Olympus_Mons"},
		{"empty list", nil, "Europe/Brussels"},
		{"duplicate", []string{"06:30", "06:30"}, "Europe/Brussels"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := syncschedule.Parse(c.times, c.zone); err == nil {
				t.Errorf("Parse(%v, %q) succeeded, want an error", c.times, c.zone)
			}
		})
	}
}
