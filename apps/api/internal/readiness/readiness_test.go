package readiness

import (
	"reflect"
	"testing"
	"time"
)

func date(s string) time.Time {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		panic(err)
	}
	return t
}

func assertAssessment(t *testing.T, got Assessment, wantVerdict Verdict, wantReasons ...string) {
	t.Helper()
	if got.Verdict != wantVerdict {
		t.Fatalf("verdict = %q, want %q (reasons: %v)", got.Verdict, wantVerdict, got.Reasons)
	}
	if len(wantReasons) == 0 {
		wantReasons = nil
	}
	if !reflect.DeepEqual(got.Reasons, wantReasons) {
		t.Fatalf("reasons = %v, want %v", got.Reasons, wantReasons)
	}
}

func TestReadyWithNoSignals(t *testing.T) {
	got := Assess(Day{Date: "2026-09-28", Present: true}, nil, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Ready)
}

func TestAbsentGarminRowSkipsWellnessRulesButFormAndLoadStillApply(t *testing.T) {
	// Present=false with garbage in the other fields must never trigger a
	// wellness rule; only form/load can.
	today := Day{Date: "2026-09-28", Present: false, ReadinessScore: 1, ReadinessLevel: "POOR", HRVStatus: "POOR", SleepScore: 1, RestingHR: 200}
	tsb := -40.0
	got := Assess(today, nil, &tsb, "2026-09-28", nil, date("2026-09-28"))
	assertAssessment(t, got, Rest, "your form is −40")
}

func TestZeroOrEmptyFieldsMeanNoReadingAndNeverFireOnTheirOwn(t *testing.T) {
	today := Day{Date: "2026-09-28", Present: true} // everything zero/empty
	got := Assess(today, nil, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Ready)
}

// --- Garmin readiness ---------------------------------------------------

func TestReadinessScoreBoundaries(t *testing.T) {
	cases := []struct {
		score   int
		verdict Verdict
		reason  string
	}{
		{24, Rest, "Garmin readiness is poor (24)"},
		{25, Caution, "Garmin readiness is low (25)"},
		{49, Caution, "Garmin readiness is low (49)"},
		{50, Ready, ""},
	}
	for _, c := range cases {
		today := Day{Date: "2026-09-28", Present: true, ReadinessScore: c.score, ReadinessLevel: "GOOD"}
		got := Assess(today, nil, nil, "", nil, date("2026-09-28"))
		if c.verdict == Ready {
			assertAssessment(t, got, Ready)
		} else {
			assertAssessment(t, got, c.verdict, c.reason)
		}
	}
}

func TestReadinessLevelPoorFiresRestEvenWithNoScore(t *testing.T) {
	today := Day{Date: "2026-09-28", Present: true, ReadinessLevel: "POOR"}
	got := Assess(today, nil, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Rest, "Garmin readiness is poor")
}

func TestReadinessLevelLowFiresCautionEvenWithNoScore(t *testing.T) {
	today := Day{Date: "2026-09-28", Present: true, ReadinessLevel: "LOW"}
	got := Assess(today, nil, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Caution, "Garmin readiness is low")
}

// --- HRV ------------------------------------------------------------------

func TestHRVLowTwoNightsFiresRest(t *testing.T) {
	today := Day{Date: "2026-09-28", Present: true, HRVStatus: "LOW"}
	history := []Day{{Date: "2026-09-27", HRVStatus: "POOR"}}
	got := Assess(today, history, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Rest, "HRV has been low for two nights")
}

func TestHRVLowOneNightOnlyFiresCaution(t *testing.T) {
	today := Day{Date: "2026-09-28", Present: true, HRVStatus: "LOW"}
	history := []Day{{Date: "2026-09-27", HRVStatus: "BALANCED"}}
	got := Assess(today, history, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Caution, "HRV is low today")
}

func TestHRVLowTodayWithNoYesterdayRowFiresCautionNotRest(t *testing.T) {
	today := Day{Date: "2026-09-28", Present: true, HRVStatus: "POOR"}
	got := Assess(today, nil, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Caution, "HRV is poor today")
}

func TestHRVUnbalancedTodayFiresCaution(t *testing.T) {
	today := Day{Date: "2026-09-28", Present: true, HRVStatus: "UNBALANCED"}
	got := Assess(today, nil, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Caution, "HRV is unbalanced today")
}

func TestHRVBalancedNeverFires(t *testing.T) {
	today := Day{Date: "2026-09-28", Present: true, HRVStatus: "BALANCED"}
	history := []Day{{Date: "2026-09-27", HRVStatus: "POOR"}}
	got := Assess(today, history, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Ready)
}

// --- Sleep ------------------------------------------------------------

func TestSleepScoreBoundaries(t *testing.T) {
	cases := []struct {
		score   int
		verdict Verdict
		reason  string
	}{
		{39, Rest, "you slept 5h10 (sleep score 39)"},
		{40, Caution, "you slept 5h10 (sleep score 40)"},
		{59, Caution, "you slept 5h10 (sleep score 59)"},
		{60, Ready, ""},
	}
	for _, c := range cases {
		today := Day{Date: "2026-09-28", Present: true, SleepSeconds: 5*3600 + 10*60, SleepScore: c.score}
		got := Assess(today, nil, nil, "", nil, date("2026-09-28"))
		if c.verdict == Ready {
			assertAssessment(t, got, Ready)
		} else {
			assertAssessment(t, got, c.verdict, c.reason)
		}
	}
}

func TestSleepDurationFormatting(t *testing.T) {
	today := Day{Date: "2026-09-28", Present: true, SleepSeconds: 5*3600 + 10*60, SleepScore: 38}
	got := Assess(today, nil, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Rest, "you slept 5h10 (sleep score 38)")
}

// --- Resting HR ---------------------------------------------------------

func historyWithRHR(n int, rhr int) []Day {
	days := make([]Day, n)
	base := date("2026-09-27")
	for i := 0; i < n; i++ {
		days[i] = Day{Date: base.AddDate(0, 0, -i).Format(dateLayout), RestingHR: rhr}
	}
	return days
}

func TestRestingHRBaselineNeedsAtLeastSevenReadings(t *testing.T) {
	// Only 6 readings of 52 in the last 28 days: rule stays silent even
	// though today's RHR is way above.
	history := historyWithRHR(6, 52)
	today := Day{Date: "2026-09-28", Present: true, RestingHR: 70}
	got := Assess(today, history, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Ready)
}

func TestRestingHRDeltaBoundaries(t *testing.T) {
	history := historyWithRHR(7, 52)
	cases := []struct {
		rhr     int
		verdict Verdict
		reason  string
	}{
		{55, Ready, ""}, // +3
		{56, Caution, "resting heart rate is 4 above your usual 52"}, // +4
		{58, Caution, "resting heart rate is 6 above your usual 52"}, // +6
		{59, Rest, "resting heart rate is 7 above your usual 52"},    // +7
	}
	for _, c := range cases {
		today := Day{Date: "2026-09-28", Present: true, RestingHR: c.rhr}
		got := Assess(today, history, nil, "", nil, date("2026-09-28"))
		if c.verdict == Ready {
			assertAssessment(t, got, Ready)
		} else {
			assertAssessment(t, got, c.verdict, c.reason)
		}
	}
}

func TestRestingHRBaselineIgnoresZeroReadingsAndTodayItself(t *testing.T) {
	// 7 valid readings plus some zero (no-reading) days and today's own
	// (very high) reading, none of which should count toward the baseline.
	history := append(historyWithRHR(7, 52), Day{Date: "2026-09-20", RestingHR: 0})
	today := Day{Date: "2026-09-28", Present: true, RestingHR: 59}
	got := Assess(today, history, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Rest, "resting heart rate is 7 above your usual 52")
}

func TestRestingHRBaselineExcludesReadingsOutsideTwentyEightDays(t *testing.T) {
	// 7 readings but 3 are 29+ days old: only 4 count, below the threshold.
	history := historyWithRHR(4, 52)
	old := date("2026-09-27").AddDate(0, 0, -29)
	for i := 0; i < 3; i++ {
		history = append(history, Day{Date: old.AddDate(0, 0, -i).Format(dateLayout), RestingHR: 52})
	}
	today := Day{Date: "2026-09-28", Present: true, RestingHR: 70}
	got := Assess(today, history, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Ready)
}

// --- Form (TSB) ---------------------------------------------------------

func TestFormRestBoundary(t *testing.T) {
	tsbNotQuite := -30.0
	got := Assess(Day{Date: "2026-09-28", Present: true}, nil, &tsbNotQuite, "2026-09-28", nil, date("2026-09-28"))
	assertAssessment(t, got, Ready)

	tsbFatigued := -30.1
	got = Assess(Day{Date: "2026-09-28", Present: true}, nil, &tsbFatigued, "2026-09-28", nil, date("2026-09-28"))
	assertAssessment(t, got, Rest, "your form is −30")
}

func TestFormReasonUsesUnicodeMinusAndRoundsToInteger(t *testing.T) {
	tsb := -33.6
	got := Assess(Day{Date: "2026-09-28", Present: true}, nil, &tsb, "2026-09-28", nil, date("2026-09-28"))
	assertAssessment(t, got, Rest, "your form is −34")
}

func TestFormSnapshotStaleness(t *testing.T) {
	tsb := -40.0
	now := date("2026-09-28")

	// Exactly 2 days old: still fresh.
	got := Assess(Day{Date: "2026-09-28", Present: true}, nil, &tsb, "2026-09-26", nil, now)
	assertAssessment(t, got, Rest, "your form is −40")

	// 3 days old: stale, rule stays silent.
	got = Assess(Day{Date: "2026-09-28", Present: true}, nil, &tsb, "2026-09-25", nil, now)
	assertAssessment(t, got, Ready)
}

func TestFormNilTSBNeverFires(t *testing.T) {
	got := Assess(Day{Date: "2026-09-28", Present: true}, nil, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Ready)
}

// --- Load (ACWR) ---------------------------------------------------------

func loadsForACWR(days int, load float64) []Load {
	out := make([]Load, days)
	base := date("2026-09-28")
	for i := 0; i < days; i++ {
		out[i] = Load{Date: base.AddDate(0, 0, -i).Format(dateLayout), Load: load}
	}
	return out
}

func TestACWRNeedsAtLeastTwentyOneDistinctDays(t *testing.T) {
	loads := loadsForACWR(20, 100) // only 20 distinct days
	got := Assess(Day{Date: "2026-09-28", Present: true}, nil, nil, "", loads, date("2026-09-28"))
	assertAssessment(t, got, Ready)
}

func TestACWRBoundary(t *testing.T) {
	// 21 days of load 50, but the last 7 days at 74.5 vs 28-day mean of 50
	// (with the other 21 days at 50 too) gives an approximate ratio.
	// Build precisely: 28 days all at load L28, last 7 days overridden to
	// L7 so mean7 = L7*7/7 = L7 and mean28 = (L7*7 + L28*21)/28.
	buildLoads := func(l7, l28 float64) []Load {
		loads := loadsForACWR(28, l28)
		for i := 0; i < 7; i++ {
			loads[i].Load = l7
		}
		return loads
	}

	// ratio just under 1.5
	l28 := 100.0
	// mean28 = (l7*7 + l28*21)/28; want mean7/mean28 = 1.49
	// mean7 = l7
	// l7 / ((l7*7 + 2100)/28) = 1.49
	// l7*28 = 1.49*(7*l7 + 2100)
	// 28 l7 = 10.43 l7 + 3129
	// 17.57 l7 = 3129
	l7Under := 3129.0 / 17.57
	loadsUnder := buildLoads(l7Under, l28)
	got := Assess(Day{Date: "2026-09-28", Present: true}, nil, nil, "", loadsUnder, date("2026-09-28"))
	if got.Verdict != Ready {
		t.Fatalf("just-under ratio: verdict = %q, reasons = %v", got.Verdict, got.Reasons)
	}

	// ratio at/above 1.5
	l7Over := 3150.0 / 17.5 // solves l7/((7l7+2100)/28) = 1.5 exactly
	loadsOver := buildLoads(l7Over, l28)
	got = Assess(Day{Date: "2026-09-28", Present: true}, nil, nil, "", loadsOver, date("2026-09-28"))
	if got.Verdict != Caution {
		t.Fatalf("at ratio 1.5: verdict = %q, reasons = %v", got.Verdict, got.Reasons)
	}
	if len(got.Reasons) != 1 || got.Reasons[0] != "your load this week is 1.5× your usual" {
		t.Fatalf("reasons = %v", got.Reasons)
	}
}

func TestACWRMissingDaysCountAsZeroLoad(t *testing.T) {
	// 21 distinct days present (out of 28), all at a high load in the last
	// 7 days and nothing else — missing days count as 0, still enough to
	// cross the ratio given a high enough recent load.
	loads := []Load{}
	base := date("2026-09-28")
	for i := 0; i < 7; i++ {
		loads = append(loads, Load{Date: base.AddDate(0, 0, -i).Format(dateLayout), Load: 200})
	}
	for i := 7; i < 21; i++ {
		loads = append(loads, Load{Date: base.AddDate(0, 0, -i).Format(dateLayout), Load: 10})
	}
	got := Assess(Day{Date: "2026-09-28", Present: true}, nil, nil, "", loads, date("2026-09-28"))
	if got.Verdict != Caution {
		t.Fatalf("verdict = %q, reasons = %v", got.Verdict, got.Reasons)
	}
}

// --- Combined / ordering --------------------------------------------------

func TestRestVerdictAppendsCautionReasonsThatAlsoFired(t *testing.T) {
	today := Day{Date: "2026-09-28", Present: true, ReadinessLevel: "POOR"}
	tsb := -40.0
	// 21 days of load crossing the ACWR caution threshold.
	loads := []Load{}
	base := date("2026-09-28")
	for i := 0; i < 7; i++ {
		loads = append(loads, Load{Date: base.AddDate(0, 0, -i).Format(dateLayout), Load: 200})
	}
	for i := 7; i < 21; i++ {
		loads = append(loads, Load{Date: base.AddDate(0, 0, -i).Format(dateLayout), Load: 10})
	}
	got := Assess(today, nil, &tsb, "2026-09-28", loads, date("2026-09-28"))
	assertAssessment(t, got, Rest,
		"Garmin readiness is poor",
		"your form is −40",
		"your load this week is 3.6× your usual",
	)
}

func TestReasonOrderMatchesSpecOrderRestThenCaution(t *testing.T) {
	today := Day{
		Date:           "2026-09-28",
		Present:        true,
		ReadinessScore: 20,
		ReadinessLevel: "POOR",
		HRVStatus:      "POOR",
		SleepScore:     30,
		SleepSeconds:   4 * 3600,
		RestingHR:      70,
	}
	history := append(historyWithRHR(7, 52), Day{Date: "2026-09-27", HRVStatus: "LOW"})
	tsb := -35.0
	got := Assess(today, history, &tsb, "2026-09-28", nil, date("2026-09-28"))
	assertAssessment(t, got, Rest,
		"Garmin readiness is poor (20)",
		"HRV has been low for two nights",
		"you slept 4h00 (sleep score 30)",
		"resting heart rate is 18 above your usual 52",
		"your form is −35",
	)
}

func TestCautionVerdictListsOnlyCautionReasons(t *testing.T) {
	today := Day{
		Date:           "2026-09-28",
		Present:        true,
		ReadinessScore: 30,
		HRVStatus:      "UNBALANCED",
		SleepScore:     50,
		SleepSeconds:   6 * 3600,
		RestingHR:      57,
	}
	history := historyWithRHR(7, 52)
	got := Assess(today, history, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Caution,
		"Garmin readiness is low (30)",
		"HRV is unbalanced today",
		"you slept 6h00 (sleep score 50)",
		"resting heart rate is 5 above your usual 52",
	)
}
