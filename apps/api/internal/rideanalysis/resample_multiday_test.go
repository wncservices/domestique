package rideanalysis

import (
	"testing"
	"time"

	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func steadyRecords(from time.Time, seconds, watts int) []*mesgdef.Record {
	out := make([]*mesgdef.Record, 0, seconds)
	for i := 0; i < seconds; i++ {
		out = append(out, mesgdef.NewRecord(nil).SetTimestamp(from.Add(time.Duration(i)*time.Second)).SetPower(uint16(watts)).SetHeartRate(140))
	}
	return out
}

// A bikepacking trip: four days of 6 hours' riding with the device off
// overnight. The 72-hour span cap used to read it as unanalysable and zero the
// ride.
func TestAFourDayFileWithOvernightGapsKeepsItsMetrics(t *testing.T) {
	var recs []*mesgdef.Record
	for day := 0; day < 4; day++ {
		recs = append(recs, steadyRecords(fixtureStart.Add(time.Duration(day)*24*time.Hour), 6*3600, 200)...)
	}
	samples := Resample(recs)
	if len(samples) == 0 {
		t.Fatal("a four-day trip resampled to nothing")
	}
	// 4 x 6 h ridden, plus a break second per night.
	if want := 4*6*3600 + 3; len(samples) < want-10 || len(samples) > want+10 {
		t.Errorf("%d samples, want about %d: nights must not be laid out second by second", len(samples), want)
	}

	a := Analyze(Input{
		Sport:    "cycling",
		Activity: &filedef.Activity{Records: recs},
		Summary:  Summary{DurationSeconds: 4 * 6 * 3600},
		Profile:  workout.RiderProfile{FTPWatts: 250},
	})
	if a.SamplesUnreadable || a.NormalizedPower < 190 || a.TSS <= 0 {
		t.Errorf("NP %.0f TSS %.0f unreadable=%v, want the trip's real figures", a.NormalizedPower, a.TSS, a.SamplesUnreadable)
	}
}

// One record with no clock set (zero, or before the activity) used to stretch
// the span back to year 1 and zero the whole ride.
func TestOneBadTimestampDoesNotKillTheRide(t *testing.T) {
	recs := steadyRecords(fixtureStart, 1800, 200)
	for name, bad := range map[string]time.Time{
		"zero":            {},
		"FIT epoch":       time.Date(1989, 12, 31, 0, 0, 0, 0, time.UTC),
		"decades out":     fixtureStart.Add(30 * 365 * 24 * time.Hour),
		"a month earlier": fixtureStart.Add(-30 * 24 * time.Hour),
	} {
		t.Run(name, func(t *testing.T) {
			withBad := append([]*mesgdef.Record{}, recs[:900]...)
			withBad = append(withBad, mesgdef.NewRecord(nil).SetTimestamp(bad).SetPower(900))
			withBad = append(withBad, recs[900:]...)

			samples := Resample(withBad)
			if len(samples) < 1790 || len(samples) > 1810 {
				t.Fatalf("%d samples, want the ride's 1800", len(samples))
			}
			a := Analyze(Input{Sport: "cycling", Activity: &filedef.Activity{Records: withBad}, Profile: workout.RiderProfile{FTPWatts: 250}})
			if a.NormalizedPower < 190 || a.NormalizedPower > 215 {
				t.Errorf("NP = %.0f, want about 200: the bad record's 900 W must not count", a.NormalizedPower)
			}
		})
	}
}

// When nothing usable comes out of the file, the provider summary stands and
// the duration is never overwritten with zero.
func TestAnUnreadableFileKeepsTheProviderSummary(t *testing.T) {
	recs := []*mesgdef.Record{
		mesgdef.NewRecord(nil).SetTimestamp(time.Time{}).SetPower(200),
		mesgdef.NewRecord(nil).SetTimestamp(time.Time{}).SetPower(210),
	}
	a := Analyze(Input{
		Sport:    "cycling",
		Activity: &filedef.Activity{Records: recs},
		Summary:  Summary{DurationSeconds: 3600, AvgPower: 180, TSS: 55},
		Profile:  workout.RiderProfile{FTPWatts: 250},
	})
	if !a.SamplesUnreadable {
		t.Error("SamplesUnreadable not set")
	}
	if a.LoadSource != LoadSourceProviderTSS || a.Load != 55 {
		t.Errorf("load %v from %v, want the provider's 55", a.Load, a.LoadSource)
	}

	// With no provider TSS either, the estimate uses the summary's duration.
	a = Analyze(Input{
		Sport:    "cycling",
		Activity: &filedef.Activity{Records: recs},
		Summary:  Summary{DurationSeconds: 3600, AvgPower: 180},
		Profile:  workout.RiderProfile{FTPWatts: 250},
	})
	if a.LoadSource != LoadSourceEstimate || a.Load <= 0 {
		t.Errorf("load %v from %v, want a positive estimate from the summary duration", a.Load, a.LoadSource)
	}
}

// Still no panic and no giant allocation: reversed, absurd, and tied clusters.
func TestResampleStillRefusesWhatCannotBeTold(t *testing.T) {
	far := fixtureStart.Add(20 * 365 * 24 * time.Hour)
	if got := Resample([]*mesgdef.Record{recAt(fixtureStart), recAt(far)}); got != nil {
		t.Errorf("two records 20 years apart laid out %d samples: which one is right is unknowable", len(got))
	}
	// Eight days of continuous 1 Hz would be 691k samples, past the cap.
	var many []*mesgdef.Record
	for d := 0; d < 8; d++ {
		many = append(many, steadyRecords(fixtureStart.Add(time.Duration(d)*24*time.Hour), 86400, 150)...)
	}
	if got := Resample(many); got != nil {
		t.Errorf("laid out %d samples past the cap", len(got))
	}
}
