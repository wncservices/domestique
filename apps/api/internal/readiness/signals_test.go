package readiness

import (
	"reflect"
	"testing"
	"time"
)

func TestHRVRestSignalCarriesBothNightsAndTheUsualValue(t *testing.T) {
	today := Day{Date: "2026-09-28", Present: true, HRVStatus: "LOW", HRVLastNight: 41, HRVWeeklyAvg: 52}
	history := []Day{{Date: "2026-09-27", Present: true, HRVStatus: "POOR", HRVLastNight: 38}}
	got := Assess(today, history, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Rest, "HRV has been low for two nights")
	want := []Signal{{Kind: "hrv", Label: "HRV low two nights", Value: "38, 41 ms vs usual 52", Numbers: []float64{38, 41, 52}}}
	if !reflect.DeepEqual(got.Signals, want) {
		t.Errorf("signals = %#v, want %#v", got.Signals, want)
	}
}

func TestHRVCautionSignalShowsTonightsReading(t *testing.T) {
	today := Day{Date: "2026-09-28", Present: true, HRVStatus: "UNBALANCED", HRVLastNight: 41, HRVWeeklyAvg: 52}
	got := Assess(today, nil, nil, "", nil, date("2026-09-28"))
	assertAssessment(t, got, Caution, "HRV is unbalanced today")
	want := []Signal{{Kind: "hrv", Label: "HRV unbalanced", Value: "41 ms vs usual 52", Numbers: []float64{41, 52}}}
	if !reflect.DeepEqual(got.Signals, want) {
		t.Errorf("signals = %#v, want %#v", got.Signals, want)
	}
}

func TestHRVSignalFallsBackToTheStatusWhenThereAreNoReadings(t *testing.T) {
	today := Day{Date: "2026-09-28", Present: true, HRVStatus: "UNBALANCED"}
	got := Assess(today, nil, nil, "", nil, date("2026-09-28"))
	if len(got.Signals) != 1 || got.Signals[0].Value != "unbalanced" || len(got.Signals[0].Numbers) != 0 {
		t.Errorf("signals = %#v, want the status word and no invented zeros", got.Signals)
	}
}

// TestEveryReasonHasASignalInTheSameOrder fires every rule at once. The two
// slices are read side by side, so a reason with no signal, or a signal out
// of order, would put someone's numbers next to the wrong sentence.
func TestEveryReasonHasASignalInTheSameOrder(t *testing.T) {
	now := date("2026-09-28")
	today := Day{
		Date: "2026-09-28", Present: true,
		ReadinessScore: 20, ReadinessLevel: "POOR",
		HRVStatus: "LOW", HRVLastNight: 41, HRVWeeklyAvg: 52,
		SleepScore: 35, SleepSeconds: 5*3600 + 10*60,
		RestingHR: 60,
	}
	history := []Day{{Date: "2026-09-27", Present: true, HRVStatus: "LOW", HRVLastNight: 38}}
	for i := 2; i <= 10; i++ {
		history = append(history, Day{Date: now.AddDate(0, 0, -i).Format(dateLayout), Present: true, RestingHR: 50})
	}
	tsb := -34.0
	var loads []Load
	for i := 0; i < 28; i++ {
		l := 30.0
		if i < 7 {
			l = 90
		}
		loads = append(loads, Load{Date: now.AddDate(0, 0, -i).Format(dateLayout), Load: l})
	}
	got := Assess(today, history, &tsb, "2026-09-28", loads, now)
	if got.Verdict != Rest {
		t.Fatalf("verdict = %q", got.Verdict)
	}
	if len(got.Signals) != len(got.Reasons) {
		t.Fatalf("%d signals for %d reasons: %v / %#v", len(got.Signals), len(got.Reasons), got.Reasons, got.Signals)
	}
	var kinds []string
	for _, s := range got.Signals {
		if s.Label == "" || s.Value == "" {
			t.Errorf("signal %+v has no label or value", s)
		}
		kinds = append(kinds, s.Kind)
	}
	wantKinds := []string{"readiness", "hrv", "sleep", "resting_hr", "form", "load"}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Errorf("kinds = %v, want %v", kinds, wantKinds)
	}
	byKind := map[string]Signal{}
	for _, s := range got.Signals {
		byKind[s.Kind] = s
	}
	checks := map[string]string{
		"readiness":  "poor (20)",
		"sleep":      "5h10, score 35",
		"resting_hr": "60 bpm, 10 above your usual 50",
		"form":       "−34",
	}
	for kind, want := range checks {
		if byKind[kind].Value != want {
			t.Errorf("%s value = %q, want %q", kind, byKind[kind].Value, want)
		}
	}
	if !reflect.DeepEqual(byKind["sleep"].Numbers, []float64{18600, 35}) {
		t.Errorf("sleep numbers = %v", byKind["sleep"].Numbers)
	}
}

func TestAReadyDayHasNoSignals(t *testing.T) {
	got := Assess(Day{Date: "2026-09-28", Present: true}, nil, nil, "", nil, time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC))
	if got.Signals != nil {
		t.Errorf("signals = %#v", got.Signals)
	}
}
