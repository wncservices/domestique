// Package syncschedule answers "when does the next fixed-time sync run?" — a
// pure calculation over a list of wall-clock times in one time zone, so the
// daylight-saving edge cases can be tested without a clock or a ticker.
//
// Slots are built from the local calendar date (time.Date in the zone), never
// by adding 24 hours to an instant: a day is 23 or 25 hours long twice a year,
// and "06:30" has to stay 06:30 on the wall either side of the change.
package syncschedule

import (
	"fmt"
	"sort"
	"time"
)

// Schedule is a set of daily wall-clock times in one zone.
type Schedule struct {
	slots []slot
	loc   *time.Location
}

type slot struct{ hour, minute int }

// Location is the zone the schedule's times are local to — also what "today"
// means for anything else that is a rider's morning rather than the server's.
func (s Schedule) Location() *time.Location { return s.loc }

// Parse builds a Schedule from strict "HH:MM" strings and an IANA zone name.
// An empty list, a duplicate, a malformed time or an unknown zone is an error,
// so a typo is caught by `domestique validate` instead of at 06:30.
func Parse(times []string, zone string) (Schedule, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return Schedule{}, fmt.Errorf("unknown time zone %q: %w", zone, err)
	}
	if len(times) == 0 {
		return Schedule{}, fmt.Errorf("no sync times given")
	}

	seen := map[slot]bool{}
	slots := make([]slot, 0, len(times))
	for _, raw := range times {
		// Re-format and compare so "6:30" is rejected too: the layout alone
		// is more forgiving than the documented HH:MM.
		parsed, err := time.Parse("15:04", raw)
		if err != nil || parsed.Format("15:04") != raw {
			return Schedule{}, fmt.Errorf("sync time %q is not HH:MM (24-hour)", raw)
		}
		sl := slot{parsed.Hour(), parsed.Minute()}
		if seen[sl] {
			return Schedule{}, fmt.Errorf("sync time %q is listed twice", raw)
		}
		seen[sl] = true
		slots = append(slots, sl)
	}
	sort.Slice(slots, func(i, j int) bool {
		if slots[i].hour != slots[j].hour {
			return slots[i].hour < slots[j].hour
		}
		return slots[i].minute < slots[j].minute
	})
	return Schedule{slots: slots, loc: loc}, nil
}

// around lists every slot on the local dates from two days before now to two
// days after, in ascending order — enough to bracket now whatever the
// schedule, however a DST change shifts the instants.
func (s Schedule) around(now time.Time) []time.Time {
	local := now.In(s.loc)
	var out []time.Time
	for d := -2; d <= 2; d++ {
		for _, sl := range s.slots {
			out = append(out, time.Date(local.Year(), local.Month(), local.Day()+d, sl.hour, sl.minute, 0, 0, s.loc))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// Next is the first slot strictly after now.
func (s Schedule) Next(now time.Time) time.Time {
	for _, at := range s.around(now) {
		if at.After(now) {
			return at
		}
	}
	return time.Time{} // unreachable for a Parse-built schedule
}

// Last is the latest slot at or before now.
func (s Schedule) Last(now time.Time) time.Time {
	var last time.Time
	for _, at := range s.around(now) {
		if at.After(now) {
			break
		}
		last = at
	}
	return last
}

// Missed reports whether a slot has passed since lastRun — the rule for
// "the process was down at 06:30": on start, sync once if the last sync is
// older than the most recent slot. A zero lastRun (never synced) is missed.
func (s Schedule) Missed(lastRun, now time.Time) bool {
	return lastRun.Before(s.Last(now))
}

// FirstOfDay reports whether slot is the earliest slot of its local date: the
// pass that follows the night, and so the one that may send a morning summary.
// A time that is not a slot at all is not the first of anything. Compared by
// building the day's first slot the same way around() does, so a slot moved
// forward out of a daylight-saving gap still matches.
func (s Schedule) FirstOfDay(slot time.Time) bool {
	if slot.IsZero() || len(s.slots) == 0 {
		return false
	}
	local := slot.In(s.loc)
	first := s.slots[0]
	return slot.Equal(time.Date(local.Year(), local.Month(), local.Day(), first.hour, first.minute, 0, 0, s.loc))
}
