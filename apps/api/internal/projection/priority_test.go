package projection

import (
	"strings"
	"testing"
)

func TestJudgeWordsFormByPriority(t *testing.T) {
	in := JudgeInput{CTL: 70, TSB: -6, Band: band515, TargetCTL: 70, WeeksCovered: 5, WeeksTotal: 5}

	c := in
	c.Priority = "C"
	v := Judge(c)
	if strings.Contains(strings.ToLower(v.Message), "taper") && !strings.Contains(v.Message, "no taper") {
		t.Errorf("C message %q suggests a taper", v.Message)
	}
	if v.Message != "Form on race day: −6, no taper is planned for this race" || v.Tone != "info" {
		t.Errorf("C verdict = %+v", v)
	}
	c.TSB = 31
	if v := Judge(c); v.Message != "Form on race day: +31, no taper is planned for this race" {
		t.Errorf("C fresh verdict = %+v", v)
	}
	c.CTL, c.TargetCTL = 40, 70
	if v := Judge(c); v.Key != KeyUndertrained {
		t.Errorf("C undertrained = %+v: fitness still matters", v)
	}

	b := in
	b.Priority = "B"
	if v := Judge(b); v.Key != KeyFatigued || strings.Contains(v.Message, "longer taper") || !strings.Contains(v.Message, "last days") {
		t.Errorf("B fatigued = %+v, want the last days, not a longer taper", v)
	}
	b.TSB = 31
	if v := Judge(b); v.Key != KeyFresh || strings.Contains(v.Message, "shorter taper") {
		t.Errorf("B fresh = %+v", v)
	}

	a := in
	a.Priority = "A"
	if v := Judge(a); v.Message != "Too fatigued: form −6, consider a longer taper" {
		t.Errorf("A is unchanged: %+v", v)
	}
}

func TestSuggestByPriority(t *testing.T) {
	in, band := fatiguedInput()
	ramp := Ramp{MaxPerWeek: 11, Warnings: []RampWarning{{WeekStart: "2026-11-02", PerWeek: 11, ExcessTSS: 126}}}

	in.Priority = "C"
	s := Suggest(in, Verdict{Key: KeyFatigued}, band, ramp)
	if s == nil || strings.Contains(s.Text, "taper") || s.ExcessTSS != 126 {
		t.Errorf("C fatigued suggestion = %+v, want only the ramp hint", s)
	}
	if s := Suggest(in, Verdict{Key: KeyFatigued}, band, Ramp{}); s != nil {
		t.Errorf("C with no ramp: %+v, want none", s)
	}

	in.Priority = "B"
	s = Suggest(in, Verdict{Key: KeyFatigued}, band, ramp)
	if s == nil || s.Days == 0 || s.Days > 4 || !strings.Contains(s.Text, "last") {
		t.Errorf("B suggestion = %+v, want at most a mini-taper of 4 days", s)
	}
}
