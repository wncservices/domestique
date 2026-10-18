package readiness

import (
	"math"
	"testing"
	"time"
)

const v2Today = "2026-09-28"

func daysBefore(n int) string {
	return date(v2Today).AddDate(0, 0, -n).Format(dateLayout)
}

// hrvHistory is n nights of HRV before today (1..n days back), alternating
// around base so the SD is non-zero.
func hrvHistory(n int, base float64) []Day {
	var h []Day
	for i := 1; i <= n; i++ {
		v := base + 4
		if i%2 == 0 {
			v = base - 4
		}
		h = append(h, Day{Date: daysBefore(i), Present: true, HRVLastNight: v})
	}
	return h
}

func sleepHistory(n, seconds int) []Day {
	var h []Day
	for i := 1; i <= n; i++ {
		h = append(h, Day{Date: daysBefore(i), Present: true, SleepSeconds: seconds})
	}
	return h
}

func rhrHistory(n, bpm int) []Day {
	var h []Day
	for i := 1; i <= n; i++ {
		h = append(h, Day{Date: daysBefore(i), Present: true, RestingHR: bpm})
	}
	return h
}

func merge(a, b []Day) []Day {
	by := map[string]Day{}
	for _, d := range append(append([]Day{}, a...), b...) {
		x := by[d.Date]
		if d.HRVLastNight > 0 {
			x.HRVLastNight = d.HRVLastNight
		}
		if d.SleepSeconds > 0 {
			x.SleepSeconds = d.SleepSeconds
		}
		if d.RestingHR > 0 {
			x.RestingHR = d.RestingHR
		}
		x.Date, x.Present = d.Date, true
		by[d.Date] = x
	}
	var out []Day
	for _, d := range by {
		out = append(out, d)
	}
	return out
}

func eval(today Day, history []Day, mod func(*Input)) Assessment {
	in := Input{Today: today, History: history, Now: date(v2Today)}
	in.Today.Date = v2Today
	in.Today.Present = true
	if mod != nil {
		mod(&in)
	}
	return Evaluate(in)
}

func hasSignal(a Assessment, source, severity string) bool {
	for _, s := range a.Signals {
		if s.Source == source && s.Severity == severity {
			return true
		}
	}
	return false
}

func TestSignalsCarrySourceAndSeverity(t *testing.T) {
	a := eval(Day{ReadinessScore: 30, SleepScore: 20}, nil, nil)
	if !hasSignal(a, "garmin", "amber") || !hasSignal(a, "sleep", "severe") {
		t.Fatalf("signals = %+v", a.Signals)
	}
	if a.Verdict != Rest {
		t.Fatalf("verdict = %q", a.Verdict)
	}
}

func TestTwoSourcesMakeCaution(t *testing.T) {
	hist := rhrHistory(10, 50)
	a := eval(Day{SleepScore: 55, RestingHR: 54}, hist, nil)
	if a.Verdict != Caution {
		t.Fatalf("verdict = %q, reasons %v", a.Verdict, a.Reasons)
	}
	if len(a.Reasons) != 2 {
		t.Fatalf("reasons = %v", a.Reasons)
	}
}

func TestTwoAmbersFromOneSourceAreReady(t *testing.T) {
	// Score 55 is amber and so is a 3-night shortfall of 3 h: both are sleep.
	hist := sleepHistory(28, 8*3600)
	today := Day{SleepScore: 55, SleepSeconds: 7 * 3600}
	hist = merge(hist, []Day{{Date: daysBefore(1), SleepSeconds: 6 * 3600}, {Date: daysBefore(2), SleepSeconds: 6 * 3600}})
	a := eval(today, hist, nil)
	if a.Verdict != Ready {
		t.Fatalf("verdict = %q, reasons %v", a.Verdict, a.Reasons)
	}
	if len(a.Reasons) != 2 {
		t.Fatalf("both sleep reasons should be listed, got %v", a.Reasons)
	}
}

func TestSingleAmberIsReadyWithTheReasonListed(t *testing.T) {
	a := eval(Day{RestingHR: 54}, rhrHistory(10, 50), nil)
	if a.Verdict != Ready {
		t.Fatalf("verdict = %q", a.Verdict)
	}
	if len(a.Reasons) != 1 || len(a.Signals) != 1 || a.Signals[0].Source != "resting_hr" || a.Signals[0].Severity != "amber" {
		t.Fatalf("reasons %v signals %+v", a.Reasons, a.Signals)
	}
}

func TestRestingHRBoundaries(t *testing.T) {
	cases := []struct {
		bpm      int
		severity string
	}{{53, ""}, {54, "amber"}, {56, "amber"}, {57, "severe"}}
	for _, c := range cases {
		a := eval(Day{RestingHR: c.bpm}, rhrHistory(10, 50), nil)
		got := ""
		if len(a.Signals) == 1 {
			got = a.Signals[0].Severity
		}
		if got != c.severity {
			t.Errorf("rhr %d: severity %q, want %q", c.bpm, got, c.severity)
		}
	}
}

func TestSleepScoreSeverityBySource(t *testing.T) {
	cases := []struct {
		score    int
		severity string
	}{{0, ""}, {1, "severe"}, {39, "severe"}, {40, "amber"}, {59, "amber"}, {60, ""}}
	for _, c := range cases {
		a := eval(Day{SleepScore: c.score}, nil, nil)
		got := ""
		if len(a.Signals) == 1 && a.Signals[0].Source == "sleep" {
			got = a.Signals[0].Severity
		}
		if got != c.severity {
			t.Errorf("score %d: %q, want %q", c.score, got, c.severity)
		}
	}
}

func TestGarminBoundaries(t *testing.T) {
	cases := []struct {
		score    int
		level    string
		severity string
	}{{24, "", "severe"}, {25, "", "amber"}, {49, "", "amber"}, {50, "", ""}, {0, "POOR", "severe"}, {0, "LOW", "amber"}}
	for _, c := range cases {
		a := eval(Day{ReadinessScore: c.score, ReadinessLevel: c.level}, nil, nil)
		got := ""
		if len(a.Signals) == 1 && a.Signals[0].Source == "garmin" {
			got = a.Signals[0].Severity
		}
		if got != c.severity {
			t.Errorf("garmin %d/%s: %q, want %q", c.score, c.level, got, c.severity)
		}
	}
}

func TestSleepDebtBoundary(t *testing.T) {
	// Median 8 h; the shortfall over three nights is 3 x (8 h - night).
	base := sleepHistory(28, 8*3600)
	short := func(per int) Assessment {
		hist := merge(base, []Day{{Date: daysBefore(1), SleepSeconds: per}, {Date: daysBefore(2), SleepSeconds: per}})
		return eval(Day{SleepSeconds: per}, hist, nil)
	}
	// Each night 3000 s short => debt 9000 s exactly.
	if a := short(8*3600 - 3000); !hasSignal(a, "sleep", "amber") {
		t.Errorf("exactly 2.5 h short should be amber: %+v", a.Signals)
	}
	if a := short(8*3600 - 2999); hasSignal(a, "sleep", "amber") {
		t.Errorf("just under 2.5 h short should be silent: %+v", a.Signals)
	}
}

func TestSleepDebtNeedsSevenNightsOfMedian(t *testing.T) {
	hist := sleepHistory(6, 8*3600)
	a := eval(Day{SleepSeconds: 4 * 3600}, hist, nil)
	if hasSignal(a, "sleep", "amber") {
		t.Fatalf("6 nights is no median: %+v", a.Signals)
	}
	a = eval(Day{SleepSeconds: 4 * 3600}, sleepHistory(7, 8*3600), nil)
	if !hasSignal(a, "sleep", "amber") {
		t.Fatalf("7 nights should give a median: %+v", a.Signals)
	}
}

func TestLoadBoundaries(t *testing.T) {
	tsb := -30.0
	a := eval(Day{}, nil, func(in *Input) { in.TSB = &tsb; in.TSBDate = v2Today })
	if a.Verdict != Ready || len(a.Signals) != 0 {
		t.Fatalf("-30 is not below -30: %+v", a)
	}
	tsb = -30.1
	a = eval(Day{}, nil, func(in *Input) { in.TSB = &tsb; in.TSBDate = v2Today })
	if a.Verdict != Rest || !hasSignal(a, "load", "severe") {
		t.Fatalf("-30.1 should be rest: %+v", a)
	}
	stale := -40.0
	a = eval(Day{}, nil, func(in *Input) { in.TSB = &stale; in.TSBDate = daysBefore(3) })
	if a.Verdict != Ready {
		t.Fatalf("stale snapshot: %+v", a)
	}
}

func TestACWRIsAmberFromLoad(t *testing.T) {
	var loads []Load
	for i := 1; i <= 28; i++ {
		l := 50.0
		if i <= 7 {
			l = 100
		}
		loads = append(loads, Load{Date: daysBefore(i), Load: l})
	}
	a := eval(Day{}, nil, func(in *Input) { in.Loads = loads })
	if a.Verdict != Ready || !hasSignal(a, "load", "amber") {
		t.Fatalf("%+v", a)
	}
}

func TestSurveyIsSubjectiveAmber(t *testing.T) {
	survey := []SurveyDay{
		{Date: daysBefore(1), Legs: "heavy"},
		{Date: v2Today, Legs: "heavy"},
	}
	a := eval(Day{}, nil, func(in *Input) { in.Survey = survey })
	if !hasSignal(a, "subjective", "amber") || a.Verdict != Ready {
		t.Fatalf("%+v", a)
	}
}

// --- HRV band ---------------------------------------------------------------

func TestBandNeedsFourteenNights(t *testing.T) {
	// 13 nights: no band.
	today := Day{Date: v2Today, HRVLastNight: 30}
	if _, _, _, ok := HRVBand(hrvHistory(13, 60), today); ok {
		t.Fatal("13 nights must not make a band")
	}
	if _, _, _, ok := HRVBand(hrvHistory(14, 60), today); !ok {
		t.Fatal("14 nights must make a band")
	}
	// Ten nights: the old status rules still apply, and no Push is possible.
	a := eval(Day{HRVStatus: "LOW", HRVLastNight: 30, SleepSeconds: 9 * 3600}, merge(hrvHistory(10, 60), sleepHistory(28, 8*3600)), nil)
	if !hasSignal(a, "hrv", "amber") || a.Verdict != Ready {
		t.Fatalf("status rule should give an amber with 10 nights: %+v", a)
	}
	a = eval(Day{HRVStatus: "BALANCED", HRVLastNight: 70, SleepSeconds: 9 * 3600}, merge(hrvHistory(10, 60), sleepHistory(28, 8*3600)), nil)
	if a.Verdict == Push {
		t.Fatal("no Push without a band")
	}
}

func TestBandNightsOlderThanSixtyDaysDoNotCount(t *testing.T) {
	var h []Day
	for i := 61; i < 90; i++ {
		h = append(h, Day{Date: daysBefore(i), Present: true, HRVLastNight: 60})
	}
	if _, _, _, ok := HRVBand(h, Day{Date: v2Today, HRVLastNight: 60}); ok {
		t.Fatal("nights outside the 60 must not count")
	}
}

func TestHRVBandValues(t *testing.T) {
	h := hrvHistory(20, 60) // alternating 64 / 56
	mean7, baseMean, baseSD, ok := HRVBand(h, Day{Date: v2Today, HRVLastNight: 60})
	if !ok {
		t.Fatal("band expected")
	}
	want := (math.Log(64) + math.Log(56)) / 2
	if math.Abs(baseMean-want) > 1e-9 {
		t.Errorf("baseMean = %v, want %v", baseMean, want)
	}
	if baseSD <= 0 || mean7 == 0 {
		t.Errorf("sd %v mean7 %v", baseSD, mean7)
	}
}

func TestStatusRulesDoNotFireWhenBandIsAvailable(t *testing.T) {
	h := hrvHistory(20, 60)
	a := eval(Day{HRVStatus: "POOR", HRVLastNight: 60}, h, nil)
	if hasSignal(a, "hrv", "amber") || hasSignal(a, "hrv", "severe") {
		t.Fatalf("status must not decide when the band exists: %+v", a.Signals)
	}
}

func TestHRVBelowBandSevenNightMeanIsAmber(t *testing.T) {
	h := hrvHistory(40, 60)
	// Nights 2-6 low, yesterday and today just inside the band: the 7-night
	// mean is under 60 - 0.5 SD but the two-night severe rule does not hold.
	for i := range h {
		d, _ := parseDate(h[i].Date)
		age := date(v2Today).Sub(d)
		if age > 24*time.Hour && age <= 6*24*time.Hour {
			h[i].HRVLastNight = 45
		} else if age == 24*time.Hour {
			h[i].HRVLastNight = 60
		}
	}
	a := eval(Day{HRVLastNight: 60}, h, nil)
	if !hasSignal(a, "hrv", "amber") {
		t.Fatalf("%+v", a.Signals)
	}
}

func TestHRVBelowBandTwoDaysIsRest(t *testing.T) {
	h := hrvHistory(40, 60)
	for i := range h {
		if h[i].Date == daysBefore(1) {
			h[i].HRVLastNight = 40
		}
	}
	a := eval(Day{HRVLastNight: 40}, h, nil)
	if a.Verdict != Rest || !hasSignal(a, "hrv", "severe") {
		t.Fatalf("%+v", a)
	}
	// Only today low: not severe.
	h2 := hrvHistory(40, 60)
	a = eval(Day{HRVLastNight: 40}, h2, nil)
	if hasSignal(a, "hrv", "severe") {
		t.Fatalf("one night is not severe: %+v", a.Signals)
	}
}

// --- check-in -------------------------------------------------------------------

func TestCheckInLegsOneAloneIsRest(t *testing.T) {
	// A Wahoo-only rider: no wellness rows at all.
	in := Input{Today: Day{Date: v2Today}, Now: date(v2Today), CheckIn: &CheckIn{Legs: 1}}
	a := Evaluate(in)
	if a.Verdict != Rest || !hasSignal(a, "subjective", "severe") {
		t.Fatalf("%+v", a)
	}
}

func TestCheckInTwoIsAmberOnceEvenIfSeveralAreTwo(t *testing.T) {
	a := Evaluate(Input{Today: Day{Date: v2Today}, Now: date(v2Today), CheckIn: &CheckIn{Legs: 2, Mood: 2, SleepQuality: 2}})
	n := 0
	for _, s := range a.Signals {
		if s.Source == "subjective" {
			n++
		}
	}
	if n != 1 || a.Verdict != Ready {
		t.Fatalf("%+v", a)
	}
}

func TestUnansweredCheckInIsNoSignal(t *testing.T) {
	for _, c := range []*CheckIn{nil, {}, {Legs: 3, Mood: 0, SleepQuality: 0}, {Mood: 3}} {
		a := Evaluate(Input{Today: Day{Date: v2Today}, Now: date(v2Today), CheckIn: c})
		if len(a.Signals) != 0 || a.Verdict != Ready {
			t.Errorf("%+v: %+v", c, a)
		}
	}
}

func TestCheckInAmberWithAnotherSourceIsCaution(t *testing.T) {
	a := eval(Day{SleepScore: 50}, nil, func(in *Input) { in.CheckIn = &CheckIn{Mood: 2} })
	if a.Verdict != Caution {
		t.Fatalf("%+v", a)
	}
}

// --- push ----------------------------------------------------------------------

func pushFixture() (Day, []Day) {
	h := merge(hrvHistory(30, 60), sleepHistory(28, 8*3600))
	return Day{HRVLastNight: 70, SleepSeconds: 8 * 3600}, h
}

func TestPushNeedsEveryCondition(t *testing.T) {
	today, h := pushFixture()
	if a := eval(today, h, nil); a.Verdict != Push || len(a.Reasons) != 0 {
		t.Fatalf("baseline should be Push with no reasons: %+v", a)
	}
	checkin := func(c *CheckIn) func(*Input) { return func(in *Input) { in.CheckIn = c } }
	if a := eval(today, h, checkin(&CheckIn{Legs: 3})); a.Verdict != Push {
		t.Errorf("legs 3 keeps push: %+v", a)
	}
	if a := eval(today, h, checkin(&CheckIn{Mood: 4})); a.Verdict != Push {
		t.Errorf("legs unanswered keeps push: %+v", a)
	}

	t.Run("no band", func(t *testing.T) {
		if a := eval(today, merge(hrvHistory(10, 60), sleepHistory(28, 8*3600)), nil); a.Verdict != Ready {
			t.Errorf("%+v", a)
		}
	})
	t.Run("7-night mean below baseline", func(t *testing.T) {
		low := Day{HRVLastNight: 58, SleepSeconds: 8 * 3600} // inside the band, under the mean
		hh := hrvHistory(30, 60)
		for i := range hh {
			d, _ := parseDate(hh[i].Date)
			if date(v2Today).Sub(d) <= 6*24*time.Hour {
				hh[i].HRVLastNight = 59
			}
		}
		if a := eval(low, merge(hh, sleepHistory(28, 8*3600)), nil); a.Verdict != Ready {
			t.Errorf("%+v", a)
		}
	})
	t.Run("short night", func(t *testing.T) {
		if a := eval(Day{HRVLastNight: 70, SleepSeconds: 8*3600 - 1}, h, nil); a.Verdict != Ready {
			t.Errorf("%+v", a)
		}
	})
	t.Run("no sleep median", func(t *testing.T) {
		if a := eval(today, hrvHistory(30, 60), nil); a.Verdict != Ready {
			t.Errorf("%+v", a)
		}
	})
	t.Run("no sleep reading today", func(t *testing.T) {
		if a := eval(Day{HRVLastNight: 70}, h, nil); a.Verdict != Ready {
			t.Errorf("%+v", a)
		}
	})
	t.Run("an amber signal", func(t *testing.T) {
		d := today
		d.SleepScore = 55
		if a := eval(d, h, nil); a.Verdict != Ready || len(a.Reasons) != 1 {
			t.Errorf("%+v", a)
		}
	})
	t.Run("legs 2", func(t *testing.T) {
		if a := eval(today, h, checkin(&CheckIn{Legs: 2})); a.Verdict != Ready {
			t.Errorf("%+v", a)
		}
	})
}

func TestBandWithAnEmptySevenNightWindowGivesNoHRVSignalAndNoPush(t *testing.T) {
	// 20 nights of history, all between 8 and 27 days back: the band exists
	// but none of the last 7 nights has a reading.
	var h []Day
	for i := 8; i <= 27; i++ {
		v := 64.0
		if i%2 == 0 {
			v = 56
		}
		h = append(h, Day{Date: daysBefore(i), Present: true, HRVLastNight: v})
	}
	h = merge(h, sleepHistory(28, 8*3600))
	if _, _, _, ok := HRVBand(h, Day{Date: v2Today}); !ok {
		t.Fatal("the band depends on the 60 nights only")
	}
	// A LOW status must not be read by the old rules while a band exists.
	a := eval(Day{HRVStatus: "LOW", SleepSeconds: 9 * 3600}, h, nil)
	if a.Verdict != Ready || len(a.Signals) != 0 {
		t.Fatalf("no HRV signal and no push expected: %+v", a)
	}
}

func TestHRVSevenNightMeanAtTheEdgeIsNotAmberAndJustBelowIs(t *testing.T) {
	// Make every window night read the same value x; the 7-night mean of ln
	// is then ln(x), so x = exp(edge) puts it on the line. The baseline moves
	// slightly when the window is overwritten, so solve for the edge with the
	// window already in place.
	build := func(x float64) ([]Day, Day) {
		h := hrvHistory(40, 60)
		for i := range h {
			d, _ := parseDate(h[i].Date)
			if date(v2Today).Sub(d) <= 6*24*time.Hour {
				h[i].HRVLastNight = x
			}
		}
		return h, Day{Date: v2Today, HRVLastNight: x}
	}
	edge := func(x float64) float64 {
		h, td := build(x)
		_, bm, sd, _ := HRVBand(h, td)
		return math.Exp(bm - hrvBandSDs*sd)
	}
	x := 58.0
	for i := 0; i < 60; i++ { // fixed point: x == edge(x)
		x = edge(x)
	}
	h, td := build(x)
	m7, bm, sd, _ := HRVBand(h, td)
	if d := math.Abs(m7 - (bm - hrvBandSDs*sd)); d > 1e-6 {
		t.Fatalf("setup did not reach the edge: %g", d)
	}
	// Exactly on the line (to within float noise, nudged up) is not amber.
	h, td = build(x * 1.000001)
	if a := eval(td, h, nil); hasSignal(a, "hrv", "amber") {
		t.Errorf("at the edge: %+v", a.Signals)
	}
	h, td = build(x * 0.999)
	if a := eval(td, h, nil); !hasSignal(a, "hrv", "amber") && !hasSignal(a, "hrv", "severe") {
		t.Errorf("just below the edge: %+v", a.Signals)
	}
}

func TestCheckInMoodOrSleepQualityOneAloneIsOneAmber(t *testing.T) {
	for _, c := range []*CheckIn{{Mood: 1}, {SleepQuality: 1}} {
		a := Evaluate(Input{Today: Day{Date: v2Today}, Now: date(v2Today), CheckIn: c})
		if a.Verdict != Ready || len(a.Reasons) != 1 || len(a.Signals) != 1 ||
			a.Signals[0].Source != "subjective" || a.Signals[0].Severity != "amber" {
			t.Errorf("%+v: %+v", c, a)
		}
	}
}
