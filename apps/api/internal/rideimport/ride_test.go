package rideimport

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/muktihari/fit/profile/typedef"

	"github.com/wncservices/domestique/apps/api/internal/rideanalysis"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// at is a fixed UTC instant. Nothing here reads the clock or time.Local, so
// the tests mean the same under TZ=UTC and TZ=Europe/Brussels.
func at(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

var profile = workout.RiderProfile{Rider: "wilant", FTPWatts: 250, MaxHR: 190}

func TestParseReadsTheRideSummary(t *testing.T) {
	raw := rideSpec{Start: at(2026, 3, 5, 9, 0), Seconds: 1800, Watts: 200, HR: 140, DistanceM: 15000, Pause: 120,
		WithActivity: true, Offset: time.Hour}.Build(t)

	r, err := Parse(raw, profile)
	if err != nil {
		t.Fatal(err)
	}
	if r.Sport != "cycling" || r.Date != "2026-03-05" || r.StartUTC != "2026-03-05T09:00:00Z" {
		t.Errorf("sport/date/start = %q %q %q", r.Sport, r.Date, r.StartUTC)
	}
	if r.StartUnix != at(2026, 3, 5, 9, 0).Unix() {
		t.Errorf("StartUnix = %d", r.StartUnix)
	}
	if r.Duration != 1800 || r.Elapsed != 1920 {
		t.Errorf("duration/elapsed = %v/%v, want the timer's 1800 and the clock's 1920", r.Duration, r.Elapsed)
	}
	if r.Distance != 15000 || r.AvgPower != 200 || r.AvgHR != 140 {
		t.Errorf("distance/power/hr = %v/%v/%v", r.Distance, r.AvgPower, r.AvgHR)
	}
}

func TestParseRunsAnalyzeWithNoPlan(t *testing.T) {
	raw := rideSpec{Start: at(2026, 3, 5, 9, 0), Seconds: 1500, Watts: 250, HR: 150}.Build(t)
	r, err := Parse(raw, profile)
	if err != nil {
		t.Fatal(err)
	}
	a := r.Analysis
	if a.Outcome != rideanalysis.OutcomeUnplanned {
		t.Errorf("outcome = %q, want unplanned: an import is never matched to a plan", a.Outcome)
	}
	if a.NormalizedPower < 249 || a.NormalizedPower > 251 {
		t.Errorf("NP = %v, want about 250", a.NormalizedPower)
	}
	if len(a.PowerCurve) == 0 || a.PowerCurve[300] < 249 {
		t.Errorf("power curve = %v, want a 5 minute entry near 250 W: detection reads it", a.PowerCurve)
	}
	if a.TSS <= 0 || a.Load <= 0 || a.LoadSource != rideanalysis.LoadSourceFITPower {
		t.Errorf("tss/load/source = %v/%v/%v, want a power load against the current FTP", a.TSS, a.Load, a.LoadSource)
	}
	if a.MaxHR != 150 {
		t.Errorf("max hr = %d", a.MaxHR)
	}
}

func TestParseSports(t *testing.T) {
	for name, tc := range map[string]struct {
		sport typedef.Sport
		sub   typedef.SubSport
		want  string
		err   error
	}{
		"road":            {typedef.SportCycling, typedef.SubSportRoad, "cycling", nil},
		"indoor":          {typedef.SportCycling, typedef.SubSportIndoorCycling, "cycling", nil},
		"virtual":         {typedef.SportCycling, typedef.SubSportVirtualActivity, "cycling", nil},
		"e-bike":          {typedef.SportEBiking, typedef.SubSportGeneric, "cycling", nil},
		"fitness bike":    {typedef.SportFitnessEquipment, typedef.SubSportIndoorCycling, "cycling", nil},
		"running":         {typedef.SportRunning, typedef.SubSportGeneric, "running", nil},
		"swimming":        {typedef.SportSwimming, typedef.SubSportGeneric, "", ErrSport},
		"fitness (other)": {typedef.SportFitnessEquipment, typedef.SubSportGeneric, "", ErrSport},
		"skiing":          {typedef.SportAlpineSkiing, typedef.SubSportGeneric, "", ErrSport},
	} {
		t.Run(name, func(t *testing.T) {
			raw := rideSpec{Start: at(2026, 3, 5, 9, 0), Seconds: 600, Watts: 180, Sport: tc.sport, SubSport: tc.sub}.Build(t)
			r, err := Parse(raw, profile)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if r.Sport != tc.want {
				t.Errorf("sport = %q, want %q", r.Sport, tc.want)
			}
		})
	}
}

func TestParseLocalDate(t *testing.T) {
	for name, tc := range map[string]struct {
		start  time.Time
		offset time.Duration
		active bool
		want   string
	}{
		// 22:30Z in summer Brussels (+2h) is 00:30 the next day.
		"after local midnight": {at(2026, 7, 1, 22, 30), 2 * time.Hour, true, "2026-07-02"},
		// 23:30 local in winter Brussels (+1h) starts 22:30Z: the same day.
		"23:30 local": {at(2026, 1, 10, 22, 30), time.Hour, true, "2026-01-10"},
		// 23:30 local in New York (-5h) is 04:30Z the next UTC day.
		"behind utc": {at(2026, 1, 11, 4, 30), -5 * time.Hour, true, "2026-01-10"},
		// No activity message, so no offset to read: the UTC day.
		"no offset": {at(2026, 7, 1, 22, 30), 0, false, "2026-07-01"},
		// A half-hour offset (Kolkata).
		"half hour": {at(2026, 3, 5, 19, 0), 5*time.Hour + 30*time.Minute, true, "2026-03-06"},
	} {
		t.Run(name, func(t *testing.T) {
			raw := rideSpec{Start: tc.start, Seconds: 600, Watts: 180, WithActivity: tc.active, Offset: tc.offset}.Build(t)
			r, err := Parse(raw, profile)
			if err != nil {
				t.Fatal(err)
			}
			if r.Date != tc.want {
				t.Errorf("date = %s, want %s", r.Date, tc.want)
			}
			if r.StartUTC != tc.start.Format(time.RFC3339) {
				t.Errorf("StartUTC = %s, want the UTC instant %s whatever the offset", r.StartUTC, tc.start.Format(time.RFC3339))
			}
		})
	}
}

func TestParseWithoutASessionMessage(t *testing.T) {
	// Records only, with power: a bike. Without power there is no way to say.
	r, err := Parse(rideSpec{Start: at(2026, 3, 5, 9, 0), Seconds: 600, Watts: 180, NoSession: true}.Build(t), profile)
	if err != nil || r.Sport != "cycling" || r.Duration != 600 || r.AvgPower != 180 {
		t.Errorf("records-only ride = %+v, %v", r, err)
	}
	if _, err := Parse(rideSpec{Start: at(2026, 3, 5, 9, 0), Seconds: 600, HR: 140, NoSession: true}.Build(t), profile); !errors.Is(err, ErrSport) {
		t.Errorf("no session and no power: err = %v, want ErrSport", err)
	}
}

func TestParseRefusesWhatIsNotARide(t *testing.T) {
	if _, err := Parse(rideSpec{Start: at(2026, 3, 5, 9, 0), Seconds: 600, NoRecords: true}.Build(t), profile); !errors.Is(err, ErrUnreadable) {
		t.Errorf("no records: err = %v, want ErrUnreadable", err)
	}
	if _, err := Parse([]byte("definitely not a FIT file, just text"), profile); !errors.Is(err, ErrUnreadable) {
		t.Errorf("garbage: err = %v, want ErrUnreadable", err)
	}
	good := rideSpec{Start: at(2026, 3, 5, 9, 0), Seconds: 600, Watts: 200}.Build(t)
	if _, err := Parse(good[:len(good)/2], profile); !errors.Is(err, ErrUnreadable) {
		t.Errorf("truncated: err = %v, want ErrUnreadable", err)
	}
}

func TestParseUsesTheProfileForLoadOnly(t *testing.T) {
	raw := rideSpec{Start: at(2026, 3, 5, 9, 0), Seconds: 3600, Watts: 250}.Build(t)
	a, _ := Parse(raw, workout.RiderProfile{FTPWatts: 250})
	b, _ := Parse(raw, workout.RiderProfile{FTPWatts: 200})
	if math.Abs(a.Analysis.TSS-100) > 1 {
		t.Errorf("an hour at FTP should score about 100 TSS, got %v", a.Analysis.TSS)
	}
	if b.Analysis.TSS <= a.Analysis.TSS {
		t.Errorf("a lower FTP must raise the TSS of the same ride: %v vs %v", b.Analysis.TSS, a.Analysis.TSS)
	}
	if a.Analysis.NormalizedPower != b.Analysis.NormalizedPower {
		t.Error("the profile must not change what the ride itself says")
	}
}
