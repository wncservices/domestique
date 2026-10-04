package rideimport

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func ride(date string, secs, watts float64) Ride {
	return Ride{Sport: "cycling", Date: date, Duration: secs, Elapsed: secs, AvgPower: watts}
}

func existing(date string, secs, watts float64) workout.CompletedSession {
	return workout.CompletedSession{Provider: "garmin", Sport: "cycling", Date: date, DurationSeconds: secs, AvgPowerWatts: watts}
}

func TestDuplicateDurationTolerance(t *testing.T) {
	for name, tc := range map[string]struct {
		ride, other float64
		want        bool
	}{
		// 2 % of an hour is 72 s, over the 60 s floor.
		"long: just inside":  {3600, 3600 + 72, true},
		"long: just outside": {3600, 3600 + 80, false},
		// A short ride is governed by the 60 s floor.
		"short: just inside":  {600, 660, true},
		"short: just outside": {600, 661, false},
		"short: below":        {600, 540, true},
		"short: below out":    {600, 539, false},
		"identical":           {1800, 1800, true},
	} {
		t.Run(name, func(t *testing.T) {
			got := Duplicate(ride("2026-03-05", tc.ride, 0), []workout.CompletedSession{existing("2026-03-05", tc.other, 0)})
			if got != tc.want {
				t.Errorf("Duplicate = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDuplicatePowerToleranceOnlyWhenBothHavePower(t *testing.T) {
	for name, tc := range map[string]struct {
		ride, other float64
		want        bool
	}{
		"inside":                   {200, 206, true},
		"outside":                  {200, 207, false},
		"outside the other way":    {207, 200, false},
		"inside the other way":     {206, 200, true},
		"the rider has no meter":   {0, 230, true},
		"the provider has no data": {230, 0, true},
	} {
		t.Run(name, func(t *testing.T) {
			got := Duplicate(ride("2026-03-05", 3600, tc.ride), []workout.CompletedSession{existing("2026-03-05", 3600, tc.other)})
			if got != tc.want {
				t.Errorf("Duplicate = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDuplicateDateWindowIsPlusOrMinusOneDay(t *testing.T) {
	for date, want := range map[string]bool{
		"2026-03-03": false, "2026-03-04": true, "2026-03-05": true, "2026-03-06": true, "2026-03-07": false,
		// Month and year boundaries are calendar arithmetic, not string maths.
	} {
		if got := Duplicate(ride("2026-03-05", 3600, 200), []workout.CompletedSession{existing(date, 3600, 200)}); got != want {
			t.Errorf("existing on %s: Duplicate = %v, want %v", date, got, want)
		}
	}
	if !Duplicate(ride("2026-03-01", 3600, 200), []workout.CompletedSession{existing("2026-02-28", 3600, 200)}) {
		t.Error("1 March against 28 February must be a day apart")
	}
	if !Duplicate(ride("2026-01-01", 3600, 200), []workout.CompletedSession{existing("2025-12-31", 3600, 200)}) {
		t.Error("1 January against 31 December must be a day apart")
	}
}

func TestDuplicateNeverAcrossSports(t *testing.T) {
	other := existing("2026-03-05", 3600, 200)
	other.Sport = "running"
	if Duplicate(ride("2026-03-05", 3600, 200), []workout.CompletedSession{other}) {
		t.Error("a run is not a duplicate of a ride")
	}
}

func TestDuplicateMatchesAnyCandidate(t *testing.T) {
	cands := []workout.CompletedSession{existing("2026-03-05", 1000, 100), existing("2026-03-04", 3610, 201)}
	if !Duplicate(ride("2026-03-05", 3600, 200), cands) {
		t.Error("the second candidate matches")
	}
	if Duplicate(ride("2026-03-05", 3600, 200), nil) {
		t.Error("no candidates, no duplicate")
	}
}

func TestDuplicateComparesTheClockTimeToo(t *testing.T) {
	// A provider may report elapsed time where the file's timer excludes pauses.
	r := ride("2026-03-05", 3000, 200)
	r.Elapsed = 3600
	if !Duplicate(r, []workout.CompletedSession{existing("2026-03-05", 3600, 200)}) {
		t.Error("a provider row that carries the elapsed time is the same ride")
	}
}

func TestDuplicateGuardsAgainstBadDates(t *testing.T) {
	if Duplicate(ride("not a date", 3600, 200), []workout.CompletedSession{existing("2026-03-05", 3600, 200)}) {
		t.Error("an unparseable date must not match")
	}
}

func TestNearbyDatesAreTheDayBeforeTheDayAndTheDayAfter(t *testing.T) {
	got := NearbyDates("2026-03-01")
	want := []string{"2026-02-28", "2026-03-01", "2026-03-02"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("NearbyDates = %v, want %v", got, want)
	}
	if NearbyDates("garbage") != nil {
		t.Error("a bad date has no neighbours")
	}
}
