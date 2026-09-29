package weather

import (
	"reflect"
	"testing"
	"time"
)

const day = "2026-10-03"

// hoursFor builds a flat forecast for one day: every hour identical unless
// the caller then edits individual ones.
func hoursFor(date string, temp, rain, prob, wind, gust float64, code int) []Hour {
	hs := make([]Hour, 24)
	for h := range hs {
		hs[h] = Hour{Date: date, Hour: h, Temp: temp, Rain: rain, RainProb: prob, Wind: wind, Gust: gust, Code: code}
	}
	return hs
}

func calm() []Hour { return hoursFor(day, 15, 0, 0, 10, 15, 1) }

var win = Window{Date: day, StartHour: 9, EndHour: 12}

func assess(hs []Hour) Verdict { return Assess(Forecast{Hours: hs}, win) }

func TestAssessCalmDayIsGood(t *testing.T) {
	v := assess(calm())
	if v.Bad || len(v.Reasons) != 0 || len(v.Codes) != 0 {
		t.Fatalf("verdict = %+v", v)
	}
	if v.TempMin != 15 || v.TempMax != 15 || v.GustMax != 15 {
		t.Fatalf("summary = %+v", v)
	}
}

func TestAssessRainBoundaries(t *testing.T) {
	// Probability and total amount must both clear the bar.
	cases := []struct {
		name  string
		prob  float64
		mm    float64 // put in one hour
		isBad bool
	}{
		{"59% and enough rain", 59, 0.5, false},
		{"60% and enough rain", 60, 0.2, true},
		{"60% but 0.19 mm total", 60, 0.19, false},
		{"100% but dry", 100, 0, false},
		{"0.9 mm in an hour is not steady rain", 10, 0.9, false},
		{"1.0 mm in an hour is, whatever the probability", 10, 1.0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := calm()
			hs[10].RainProb = tc.prob
			hs[10].Rain = tc.mm
			if got := assess(hs).Bad; got != tc.isBad {
				t.Fatalf("bad = %v, want %v", got, tc.isBad)
			}
		})
	}
}

func TestAssessRainTotalSumsTheWindow(t *testing.T) {
	hs := calm()
	// 0.1 + 0.1 = 0.2 across two hours: sums, not maxima, and no float drift.
	hs[9].Rain, hs[10].Rain, hs[9].RainProb = 0.1, 0.1, 60
	if !assess(hs).Bad {
		t.Fatal("0.2 mm over the window at 60% should be bad")
	}
}

func TestAssessRainOutsideTheWindowIsIgnored(t *testing.T) {
	hs := calm()
	hs[8].Rain, hs[8].RainProb = 5, 100 // one hour before
	hs[12].Rain, hs[12].RainProb = 5, 100
	if assess(hs).Bad {
		t.Fatal("rain before or after the window must not count (end is exclusive)")
	}
}

func TestAssessWindBoundaries(t *testing.T) {
	cases := []struct {
		name       string
		wind, gust float64
		isBad      bool
	}{
		{"29 km/h sustained", 29, 30, false},
		{"30 km/h sustained", 30, 40, true},
		{"49 km/h gusts", 20, 49, false},
		{"50 km/h gusts", 20, 50, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := calm()
			hs[11].Wind, hs[11].Gust = tc.wind, tc.gust
			if got := assess(hs).Bad; got != tc.isBad {
				t.Fatalf("bad = %v, want %v", got, tc.isBad)
			}
		})
	}
}

func TestAssessTemperatureBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		temp  float64
		isBad bool
	}{
		{"2.9 C", 2.9, true},
		{"3 C", 3, false},
		{"33 C", 33, false},
		{"33.1 C", 33.1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := calm()
			hs[9].Temp = tc.temp
			if got := assess(hs).Bad; got != tc.isBad {
				t.Fatalf("bad = %v, want %v", got, tc.isBad)
			}
		})
	}
}

func TestAssessWeatherCodes(t *testing.T) {
	bad := map[int]string{
		95: "thunder", 96: "thunder", 99: "thunder",
		56: "wintry", 57: "wintry", 66: "wintry", 67: "wintry",
		71: "wintry", 72: "wintry", 73: "wintry", 74: "wintry", 75: "wintry", 76: "wintry", 77: "wintry",
		85: "wintry", 86: "wintry",
	}
	for code := 0; code <= 99; code++ {
		hs := calm()
		hs[10].Code = code
		v := assess(hs)
		want, isBad := bad[code]
		if v.Bad != isBad {
			t.Errorf("code %d: bad = %v, want %v", code, v.Bad, isBad)
			continue
		}
		if isBad && (len(v.Codes) != 1 || v.Codes[0] != want) {
			t.Errorf("code %d: codes = %v, want [%s]", code, v.Codes, want)
		}
	}
	// Just outside each range, spelled out.
	for _, code := range []int{55, 58, 65, 68, 70, 78, 84, 87, 94, 97, 98} {
		hs := calm()
		hs[10].Code = code
		if assess(hs).Bad {
			t.Errorf("code %d must not be bad", code)
		}
	}
}

func TestAssessReasonsAreReadableAndInTableOrder(t *testing.T) {
	hs := calm()
	// Everything at once.
	for h := 9; h < 12; h++ {
		hs[h].RainProb, hs[h].Rain = 80, 1.0 // 3.0 mm total
		hs[h].Temp = 1.5
		hs[h].Code = 95
	}
	hs[10].Gust = 58
	v := assess(hs)
	wantCodes := []string{"rain", "wind", "cold", "thunder"}
	if !reflect.DeepEqual(v.Codes, wantCodes) {
		t.Fatalf("codes = %v, want %v", v.Codes, wantCodes)
	}
	wantReasons := []string{
		"Rain likely, 80% and 3 mm between 9 and 12",
		"Gusts up to 58 km/h",
		"Cold, down to 1.5 °C",
		"Thunderstorms forecast",
	}
	if !reflect.DeepEqual(v.Reasons, wantReasons) {
		t.Fatalf("reasons = %q, want %q", v.Reasons, wantReasons)
	}
	if v.RainProbMax != 80 || v.GustMax != 58 || v.TempMin != 1.5 || v.TempMax != 1.5 {
		t.Fatalf("summary = %+v", v)
	}
}

func TestAssessReasonTextForTheOtherRules(t *testing.T) {
	hs := calm()
	hs[9].Rain = 1.4
	hs[10].Wind = 34
	hs[11].Temp = 34.2
	hs[11].Code = 73
	v := assess(hs)
	want := []string{
		"Heavy rain, up to 1.4 mm an hour between 9 and 12",
		"Wind up to 34 km/h",
		"Hot, up to 34.2 °C",
		"Snow or ice forecast",
	}
	if !reflect.DeepEqual(v.Reasons, want) {
		t.Fatalf("reasons = %q, want %q", v.Reasons, want)
	}
}

func TestAssessEmptyWindowIsNotBad(t *testing.T) {
	hs := calm()
	hs[10].Code = 95
	for _, w := range []Window{
		{Date: day, StartHour: 12, EndHour: 12},
		{Date: day, StartHour: 14, EndHour: 9},
		{Date: "2026-10-09", StartHour: 9, EndHour: 12}, // not in the forecast
	} {
		if v := Assess(Forecast{Hours: hs}, w); v.Bad || len(v.Reasons) != 0 {
			t.Errorf("window %+v: %+v", w, v)
		}
	}
}

func TestAssessMatchesTheWindowsDateOnly(t *testing.T) {
	hs := append(calm(), hoursFor("2026-10-04", 15, 5, 100, 10, 15, 95)...)
	if assess(hs).Bad {
		t.Fatal("a storm on the next day must not count")
	}
}

func TestWindowFor(t *testing.T) {
	cases := []struct {
		name       string
		start, end int
		planned    float64
		want       int
	}{
		{"no plan keeps the configured window", 9, 12, 0, 12},
		{"short ride fits the window", 9, 12, 2 * 3600, 12},
		{"4 h ride extends to 13", 9, 12, 4 * 3600, 13},
		{"partial hour rounds up", 9, 12, 3*3600 + 1, 13},
		{"exactly the window", 9, 12, 3 * 3600, 12},
		{"capped at 6 h", 9, 12, 8 * 3600, 15},
		{"capped at the end of the day", 20, 22, 5 * 3600, 24},
		{"a long configured window is capped at 6 h too", 7, 15, 1 * 3600, 13},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WindowFor(tc.start, tc.end, tc.planned); got != tc.want {
				t.Fatalf("WindowFor(%d,%d,%v) = %d, want %d", tc.start, tc.end, tc.planned, got, tc.want)
			}
		})
	}
}

func TestClipToNowSkipsPastHoursToday(t *testing.T) {
	// 10:20 UTC with a +2 h forecast zone is 12:20 local on the 3rd.
	f := Forecast{UTCOffsetSeconds: 7200}
	now := time.Date(2026, 10, 3, 10, 20, 0, 0, time.UTC)

	got := ClipToNow(Window{Date: day, StartHour: 9, EndHour: 15}, f, now)
	if got.StartHour != 12 || got.EndHour != 15 {
		t.Fatalf("today = %+v, want start 12", got)
	}
	// Other days are untouched.
	other := Window{Date: "2026-10-04", StartHour: 9, EndHour: 15}
	if got := ClipToNow(other, f, now); got != other {
		t.Fatalf("other day changed: %+v", got)
	}
	// Nothing left today: an empty window, so Assess reports no suggestion.
	late := ClipToNow(Window{Date: day, StartHour: 9, EndHour: 12}, f, now)
	if v := Assess(Forecast{Hours: hoursFor(day, 1, 9, 100, 99, 99, 95)}, late); v.Bad {
		t.Fatalf("a window entirely in the past is bad: %+v (%+v)", v, late)
	}
	// The local date, not the server's or UTC's: 23:30 UTC is already the
	// 4th at +2 h, so a window on the 3rd is wholly past.
	night := time.Date(2026, 10, 3, 23, 30, 0, 0, time.UTC)
	if got := ClipToNow(Window{Date: day, StartHour: 9, EndHour: 15}, f, night); got.StartHour < got.EndHour {
		t.Fatalf("yesterday's window survived: %+v", got)
	}
}

func TestWorstCode(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"wind", "cold", "rain"}, "rain"},
		{[]string{"rain", "thunder"}, "thunder"},
		{[]string{"heat", "wintry"}, "wintry"},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := WorstCode(tc.in); got != tc.want {
			t.Errorf("WorstCode(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
