package projection

import "testing"

var band515 = Band{Low: 5, High: 15, IF: 0.75}

func judge(ctl, tsb, target float64) Verdict {
	return Judge(JudgeInput{CTL: ctl, TSB: tsb, Band: band515, TargetCTL: target, WeeksCovered: 6, WeeksTotal: 6})
}

func TestJudgeEveryKey(t *testing.T) {
	cases := []struct {
		name    string
		in      JudgeInput
		key     VerdictKey
		tone    string
		message string
	}{
		{"on track", JudgeInput{CTL: 70, TSB: 12, Band: band515, TargetCTL: 70}, KeyOnTrack, "success", "On track: form +12 on race day"},
		{"fatigued", JudgeInput{CTL: 70, TSB: -6, Band: band515, TargetCTL: 70}, KeyFatigued, "warning", "Too fatigued: form −6, consider a longer taper"},
		{"fresh", JudgeInput{CTL: 70, TSB: 31, Band: band515, TargetCTL: 70}, KeyFresh, "info", "Very fresh: form +31, a shorter taper keeps more fitness"},
		{"undertrained", JudgeInput{CTL: 48, TSB: 10, Band: band515, TargetCTL: 60}, KeyUndertrained, "warning", "Undertrained: fitness 48 vs 60 target"},
		{"incomplete", JudgeInput{CTL: 48, TSB: 10, Band: band515, TargetCTL: 60, WeeksCovered: 3, WeeksTotal: 7}, KeyIncomplete, "info", "Plan is still being built, projection covers 3 of 7 weeks"},
	}
	for _, c := range cases {
		if c.key != KeyIncomplete {
			c.in.WeeksCovered, c.in.WeeksTotal = 5, 5
		}
		v := Judge(c.in)
		if v.Key != c.key || v.Tone != c.tone || v.Message != c.message {
			t.Errorf("%s = %+v, want %s/%s %q", c.name, v, c.key, c.tone, c.message)
		}
	}
}

func TestJudgeBandEdgesAreInclusive(t *testing.T) {
	for _, tsb := range []float64{5, 15, 5.4, 14.6} {
		if v := judge(70, tsb, 70); v.Key != KeyOnTrack {
			t.Errorf("tsb %v = %s, want on_track (edges inclusive, judged as displayed)", tsb, v.Key)
		}
	}
	if v := judge(70, 4.4, 70); v.Key != KeyFatigued {
		t.Errorf("tsb 4.4 = %s, want fatigued", v.Key)
	}
	if v := judge(70, 15.6, 70); v.Key != KeyFresh {
		t.Errorf("tsb 15.6 = %s, want fresh", v.Key)
	}
}

func TestJudgeUndertrainedBoundaryIs85Percent(t *testing.T) {
	if v := judge(51, 10, 60); v.Key != KeyOnTrack {
		t.Errorf("CTL exactly 85%% = %s, want on_track", v.Key)
	}
	if v := judge(50, 10, 60); v.Key != KeyUndertrained {
		t.Errorf("CTL 50 of 60 = %s, want undertrained", v.Key)
	}
}

func TestJudgeUndertrainedOutranksFatiguedAndAppendsTheForm(t *testing.T) {
	v := judge(40, -6, 60)
	if v.Key != KeyUndertrained {
		t.Fatalf("key = %s, want undertrained", v.Key)
	}
	if want := "Undertrained: fitness 40 vs 60 target and form is −6"; v.Message != want {
		t.Errorf("message = %q, want %q", v.Message, want)
	}
	if v := judge(40, 10, 60); v.Message != "Undertrained: fitness 40 vs 60 target" {
		t.Errorf("in-band form should not be appended: %q", v.Message)
	}
}

func TestJudgeIncompleteOutranksTheRest(t *testing.T) {
	v := Judge(JudgeInput{CTL: 10, TSB: -20, Band: band515, TargetCTL: 60, WeeksCovered: 1, WeeksTotal: 2})
	if v.Key != KeyIncomplete {
		t.Errorf("key = %s, want incomplete", v.Key)
	}
}

func TestUnavailableCarriesItsReasonAndNoTone(t *testing.T) {
	v := Unavailable("Set your FTP to project your form")
	if v.Key != KeyUnavailable || v.Tone != "" || v.Message != "Set your FTP to project your form" {
		t.Errorf("verdict = %+v", v)
	}
}

func TestSignedRoundsToWholePointsWithATypographicMinus(t *testing.T) {
	for in, want := range map[float64]string{12.4: "+12", -5.6: "−6", 0.3: "0", -0.2: "0"} {
		if got := Signed(in); got != want {
			t.Errorf("Signed(%v) = %q, want %q", in, got, want)
		}
	}
}
