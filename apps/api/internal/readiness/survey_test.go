package readiness

import "testing"

// Tuesday 2026-09-29; Monday is the 28th, Sunday the 27th.
const surveyToday = "2026-09-29"

func assessSurvey(days ...SurveyDay) Assessment {
	return AssessWithSurvey(Day{Date: surveyToday, Present: true}, nil, nil, "", nil, days, date(surveyToday))
}

func TestHeavyLegsTwoDaysRunningIsACaution(t *testing.T) {
	got := assessSurvey(SurveyDay{Date: "2026-09-28", Legs: "heavy"}, SurveyDay{Date: "2026-09-29", Legs: "heavy"})
	assertAssessment(t, got, Caution, "you reported heavy legs on Monday and Tuesday")
	if len(got.Signals) != 1 || got.Signals[0].Kind != "survey_legs" || got.Signals[0].Value != "heavy Monday and Tuesday" {
		t.Errorf("signals = %+v", got.Signals)
	}
}

func TestHeavyLegsEndingYesterdayStillCounts(t *testing.T) {
	got := assessSurvey(SurveyDay{Date: "2026-09-27", Legs: "heavy"}, SurveyDay{Date: "2026-09-28", Legs: "heavy"})
	assertAssessment(t, got, Caution, "you reported heavy legs on Sunday and Monday")
}

func TestHighStressOnBothHeavyDaysIsInTheWording(t *testing.T) {
	got := assessSurvey(
		SurveyDay{Date: "2026-09-28", Legs: "heavy", Stress: "high"},
		SurveyDay{Date: "2026-09-29", Legs: "heavy", Stress: "high"})
	assertAssessment(t, got, Caution, "you reported heavy legs on Monday and Tuesday, both high-stress days")
}

func TestHeavyLegsAfterTwoHighStressDaysIsACaution(t *testing.T) {
	got := assessSurvey(
		SurveyDay{Date: "2026-09-28", Legs: "normal", Stress: "high"},
		SurveyDay{Date: "2026-09-29", Legs: "heavy", Stress: "high"})
	assertAssessment(t, got, Caution, "heavy legs after two high-stress days")
}

func TestNothingElseInTheSurveyDoesAnything(t *testing.T) {
	cases := map[string][]SurveyDay{
		"a single heavy report": {{Date: "2026-09-29", Legs: "heavy"}},
		"heavy with a gap day between": {
			{Date: "2026-09-27", Legs: "heavy"}, {Date: "2026-09-29", Legs: "heavy"}},
		"heavy days that ended before yesterday": {
			{Date: "2026-09-26", Legs: "heavy"}, {Date: "2026-09-27", Legs: "heavy"}},
		"stress alone": {
			{Date: "2026-09-28", Stress: "high"}, {Date: "2026-09-29", Stress: "high"}},
		"high stress with legs not heavy": {
			{Date: "2026-09-28", Legs: "normal", Stress: "high"}, {Date: "2026-09-29", Legs: "fresh", Stress: "high"}},
		"heavy today with stress high only today": {
			{Date: "2026-09-28", Legs: "normal", Stress: "normal"}, {Date: "2026-09-29", Legs: "heavy", Stress: "high"}},
		"heavy yesterday but fresh today": {
			{Date: "2026-09-28", Legs: "heavy"}, {Date: "2026-09-29", Legs: "fresh"}},
		"no survey at all": nil,
	}
	for name, days := range cases {
		t.Run(name, func(t *testing.T) {
			assertAssessment(t, assessSurvey(days...), Ready)
		})
	}
}

// TestTheSurveyNeverProducesRest is the review-focus rule: however heavy the
// legs and however stressed, the survey is a caution reason and nothing more.
func TestTheSurveyNeverProducesRest(t *testing.T) {
	got := assessSurvey(
		SurveyDay{Date: "2026-09-27", Legs: "heavy", Stress: "high"},
		SurveyDay{Date: "2026-09-28", Legs: "heavy", Stress: "high"},
		SurveyDay{Date: "2026-09-29", Legs: "heavy", Stress: "high"})
	if got.Verdict != Caution {
		t.Errorf("verdict = %q, want caution", got.Verdict)
	}
}

// With a rest reason from elsewhere the verdict is still that rest, with the
// survey's caution carried after it exactly like any other caution reason.
func TestASurveyCautionRidesAlongsideARestReasonFromElsewhere(t *testing.T) {
	today := Day{Date: surveyToday, Present: true, ReadinessLevel: "POOR"}
	got := AssessWithSurvey(today, nil, nil, "", nil,
		[]SurveyDay{{Date: "2026-09-28", Legs: "heavy"}, {Date: "2026-09-29", Legs: "heavy"}}, date(surveyToday))
	assertAssessment(t, got, Rest, "Garmin readiness is poor", "you reported heavy legs on Monday and Tuesday")
	if len(got.Signals) != 2 || got.Signals[1].Kind != "survey_legs" {
		t.Errorf("signals = %+v", got.Signals)
	}
}

func TestAssessIsAssessWithNoSurvey(t *testing.T) {
	today := Day{Date: surveyToday, Present: true, ReadinessLevel: "LOW"}
	a := Assess(today, nil, nil, "", nil, date(surveyToday))
	b := AssessWithSurvey(today, nil, nil, "", nil, nil, date(surveyToday))
	if a.Verdict != b.Verdict || len(a.Reasons) != len(b.Reasons) {
		t.Errorf("Assess = %+v, AssessWithSurvey(nil) = %+v", a, b)
	}
}
