package applehealth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func brussels(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Skipf("no tz database: %v", err)
	}
	return loc
}

func sleep(start, end, value string) SleepSample {
	return SleepSample{Start: start, End: end, Value: value}
}

func only(t *testing.T, days []Day) Day {
	t.Helper()
	if len(days) != 1 {
		t.Fatalf("want one day, got %+v", days)
	}
	return days[0]
}

func TestSleepIsTheUnionOfAsleepIntervalsInTheNight(t *testing.T) {
	loc := brussels(t)
	p := Payload{Sleep: []SleepSample{
		sleep("2026-11-04T23:00:00+01:00", "2026-11-05T01:00:00+01:00", "Core"),
		sleep("2026-11-05T00:30:00+01:00", "2026-11-05T02:00:00+01:00", "Core"),
		sleep("2026-11-04T22:00:00+01:00", "2026-11-05T07:00:00+01:00", "In Bed"),
	}}
	d := only(t, Days(p, loc, "2026-11-05"))
	if d.Date != "2026-11-05" || d.SleepSeconds != 10800 {
		t.Fatalf("got %+v, want 10800 s on 2026-11-05", d)
	}
}

func TestAwakeAndInBedDoNotCount(t *testing.T) {
	p := Payload{Sleep: []SleepSample{
		sleep("2026-11-04T23:00:00+01:00", "2026-11-05T01:00:00+01:00", "Awake"),
		sleep("2026-11-04T22:00:00+01:00", "2026-11-05T07:00:00+01:00", "In Bed"),
	}}
	if got := Days(p, brussels(t), "2026-11-05"); len(got) != 0 {
		t.Fatalf("want no days, got %+v", got)
	}
}

func TestTheNightRunsFrom18To12Local(t *testing.T) {
	loc := brussels(t)
	// 30 minutes each, on the night of 2026-11-05.
	before := sleep("2026-11-04T17:30:00+01:00", "2026-11-04T18:00:00+01:00", "Deep")
	inside := sleep("2026-11-05T11:30:00+01:00", "2026-11-05T12:00:00+01:00", "Deep")
	after := sleep("2026-11-05T12:30:00+01:00", "2026-11-05T13:00:00+01:00", "Deep")

	if got := Days(Payload{Sleep: []SleepSample{before, after}}, loc, "2026-11-05"); len(got) != 0 {
		t.Fatalf("outside the night: got %+v", got)
	}
	d := only(t, Days(Payload{Sleep: []SleepSample{before, inside, after}}, loc, "2026-11-05"))
	if d.SleepSeconds != 1800 {
		t.Fatalf("got %d s, want 1800", d.SleepSeconds)
	}
}

func TestASleepIntervalIsClippedToTheNight(t *testing.T) {
	// 17:00 to 19:00 on D-1: only the hour after 18:00 is D's night.
	p := Payload{Sleep: []SleepSample{sleep("2026-11-04T17:00:00+01:00", "2026-11-04T19:00:00+01:00", "REM")}}
	d := only(t, Days(p, brussels(t), "2026-11-05"))
	if d.SleepSeconds != 3600 {
		t.Fatalf("got %d s, want 3600", d.SleepSeconds)
	}
}

func TestTheNightAcrossTheAutumnClockChange(t *testing.T) {
	loc := brussels(t)
	// Clocks go back on 2026-10-25 at 03:00, so the night's window is 19 elapsed hours (more
	// than the sleep cap, so it is checked through a shorter sleep that spans the change).
	// 01:00+02:00 to 04:00+01:00 is 3 wall-clock hours and 4 elapsed ones.
	p := Payload{Sleep: []SleepSample{
		sleep("2026-10-25T01:00:00+02:00", "2026-10-25T04:00:00+01:00", "Asleep"),
	}}
	d := only(t, Days(p, loc, "2026-10-25"))
	if d.SleepSeconds != 4*3600 {
		t.Fatalf("got %d s, want %d", d.SleepSeconds, 4*3600)
	}
	// Both ends of the window sit where wall-clock arithmetic puts them: time from 18:00+02:00 on
	// the 24th counts, a sample ending at 17:59+02:00 does not.
	p = Payload{Sleep: []SleepSample{
		sleep("2026-10-24T17:00:00+02:00", "2026-10-24T17:59:00+02:00", "Asleep"),
	}}
	if got := Days(p, loc, "2026-10-25"); len(got) != 0 {
		t.Fatalf("a sample ending at 17:59 should not count, got %+v", got)
	}
	p = Payload{Sleep: []SleepSample{
		sleep("2026-10-24T17:00:00+02:00", "2026-10-24T18:30:00+02:00", "Asleep"),
	}}
	d = only(t, Days(p, loc, "2026-10-25"))
	if d.SleepSeconds != 1800 {
		t.Fatalf("got %d s, want 1800", d.SleepSeconds)
	}
	// 11:00+01:00 is 11:00 local, inside the night.
	p = Payload{HRV: []Sample{{Start: "2026-10-25T11:00:00+01:00", Value: 45}}}
	d = only(t, Days(p, loc, "2026-10-25"))
	if d.Date != "2026-10-25" || d.HRV != 45 {
		t.Fatalf("got %+v", d)
	}
}

func TestHRVIsTheMeanOfTheNightsSamples(t *testing.T) {
	p := Payload{HRV: []Sample{
		{Start: "2026-11-05T03:00:00+01:00", Value: 40},
		{Start: "2026-11-05T05:00:00+01:00", Value: 60},
		{Start: "2026-11-05T15:00:00+01:00", Value: 200},
	}}
	d := only(t, Days(p, brussels(t), "2026-11-05"))
	if d.HRV != 50 {
		t.Fatalf("got %v, want 50", d.HRV)
	}
}

func TestRestingHRIsTheLatestSampleOfTheDay(t *testing.T) {
	p := Payload{RestingHR: []Sample{
		{Start: "2026-11-05T00:00:00+01:00", Value: 55},
		{Start: "2026-11-05T08:00:00+01:00", Value: 52},
	}}
	d := only(t, Days(p, brussels(t), "2026-11-05"))
	if d.RestingHR != 52 {
		t.Fatalf("got %d, want 52", d.RestingHR)
	}
}

func TestOutOfRangeValuesAreDroppedPerField(t *testing.T) {
	loc := brussels(t)
	p := Payload{
		HRV:   []Sample{{Start: "2026-11-05T03:00:00+01:00", Value: 400}},
		Sleep: []SleepSample{sleep("2026-11-04T23:00:00+01:00", "2026-11-05T05:00:00+01:00", "Core")},
	}
	d := only(t, Days(p, loc, "2026-11-05"))
	if d.HRV != 0 || d.SleepSeconds != 6*3600 {
		t.Fatalf("got %+v, want HRV dropped and sleep kept", d)
	}

	p = Payload{RestingHR: []Sample{{Start: "2026-11-05T08:00:00+01:00", Value: 20}}}
	if got := Days(p, loc, "2026-11-05"); len(got) != 0 {
		t.Fatalf("resting HR 20 should drop the day, got %+v", got)
	}

	// 17 hours asleep (a union that long is not a night).
	p = Payload{Sleep: []SleepSample{sleep("2026-11-04T18:00:00+01:00", "2026-11-05T11:00:00+01:00", "Core")}}
	if got := Days(p, loc, "2026-11-05"); len(got) != 0 {
		t.Fatalf("17 h of sleep should be dropped, got %+v", got)
	}
}

func TestDaysOutsideTheFortnightAreDropped(t *testing.T) {
	loc := brussels(t)
	hr := func(date string) Payload {
		return Payload{RestingHR: []Sample{{Start: date + "T08:00:00+01:00", Value: 50}}}
	}
	// today is 2026-11-20: minus 14 is 2026-11-06, minus 13 is 2026-11-07.
	for _, date := range []string{"2026-11-06", "2026-11-21"} {
		if got := Days(hr(date), loc, "2026-11-20"); len(got) != 0 {
			t.Fatalf("%s should be dropped, got %+v", date, got)
		}
	}
	d := only(t, Days(hr("2026-11-07"), loc, "2026-11-20"))
	if d.Date != "2026-11-07" {
		t.Fatalf("got %+v", d)
	}
}

func TestDaysAreSortedAndMerged(t *testing.T) {
	p := Payload{
		RestingHR: []Sample{
			{Start: "2026-11-05T08:00:00+01:00", Value: 52},
			{Start: "2026-11-03T08:00:00+01:00", Value: 54},
		},
		HRV: []Sample{{Start: "2026-11-05T03:00:00+01:00", Value: 48}},
	}
	got := Days(p, brussels(t), "2026-11-05")
	if len(got) != 2 || got[0].Date != "2026-11-03" || got[1].Date != "2026-11-05" {
		t.Fatalf("got %+v", got)
	}
	if got[1].HRV != 48 || got[1].RestingHR != 52 {
		t.Fatalf("fields not merged: %+v", got[1])
	}
}

func TestDecodeRefusesATimeWithoutAnOffset(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"hrv":[{"start":"2026-11-05T03:00:00","value":40}]}`))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
}

func TestDecodeAcceptsZ(t *testing.T) {
	p, err := Decode(strings.NewReader(`{"hrv":[{"start":"2026-11-05T03:00:00Z","value":40}]}`))
	if err != nil || len(p.HRV) != 1 {
		t.Fatalf("got %+v, %v", p, err)
	}
}

func TestDecodeRefusesASleepSampleThatEndsBeforeItStarts(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"sleep":[{"start":"2026-11-05T03:00:00Z","end":"2026-11-05T03:00:00Z","value":"Core"}]}`))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
}

func TestDecodeRefusesBadJSONAndTrailingData(t *testing.T) {
	for _, body := range []string{`{`, `{} {}`, `{"hrv":[]} x`} {
		if _, err := Decode(strings.NewReader(body)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: got %v, want ErrInvalid", body, err)
		}
	}
}

func TestDecodeRefusesUnknownFields(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"hrv":[],"steps":[]}`))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
}

func TestDecodeRefusesTooManySamples(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"hrv":[`)
	for i := 0; i < MaxSamples+1; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"start":"2026-11-05T03:00:00Z","value":40}`)
	}
	b.WriteString(`]}`)
	_, err := Decode(strings.NewReader(b.String()))
	if !errors.Is(err, ErrTooMany) {
		t.Fatalf("got %v, want ErrTooMany", err)
	}
}

func TestEmptyPayloadGivesNoDays(t *testing.T) {
	if got := Days(Payload{}, time.UTC, "2026-11-05"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAnOutOfRangeHRVSampleIsDroppedBeforeTheMean(t *testing.T) {
	p := Payload{HRV: []Sample{
		{Start: "2026-11-05T02:00:00+01:00", Value: 40},
		{Start: "2026-11-05T04:00:00+01:00", Value: 60},
		{Start: "2026-11-05T05:00:00+01:00", Value: 400},
	}}
	if d := only(t, Days(p, brussels(t), "2026-11-05")); d.HRV != 50 {
		t.Fatalf("got %v, want 50", d.HRV)
	}
}

func TestAnOutOfRangeRestingHRSampleDoesNotHideAnEarlierOne(t *testing.T) {
	p := Payload{RestingHR: []Sample{
		{Start: "2026-11-05T00:00:00+01:00", Value: 52},
		{Start: "2026-11-05T08:00:00+01:00", Value: 200},
	}}
	if d := only(t, Days(p, brussels(t), "2026-11-05")); d.RestingHR != 52 {
		t.Fatalf("got %d, want 52", d.RestingHR)
	}
}

func TestWhichSleepStagesCountAsAsleep(t *testing.T) {
	cases := map[string]bool{
		"Asleep": true, "Core": true, "Deep": true, "REM": true, "Unspecified": true,
		"core": true, "rem": true,
		"In Bed": false, "Awake": false, "Mystery": false, "": false,
	}
	for stage, counts := range cases {
		p := Payload{Sleep: []SleepSample{sleep("2026-11-05T01:00:00+01:00", "2026-11-05T02:00:00+01:00", stage)}}
		got := Days(p, brussels(t), "2026-11-05")
		if counts && (len(got) != 1 || got[0].SleepSeconds != 3600) {
			t.Errorf("%q should count, got %+v", stage, got)
		}
		if !counts && len(got) != 0 {
			t.Errorf("%q should not count, got %+v", stage, got)
		}
	}
}
