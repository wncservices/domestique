package projection

import "testing"

func TestJudgeFeasibility(t *testing.T) {
	cases := []struct {
		name string
		in   FeasibilityInput
		key  FeasibilityKey
		tone string
		msg  string
	}{
		{"comfortable", FeasibilityInput{TargetCTL: 60, ReachedCTL: 70, Weeks: 20}, "comfortable", "success",
			"Comfortable: your plan reaches fitness 70 by race day; this event needs about 60."},
		{"exactly 5 percent over is comfortable", FeasibilityInput{TargetCTL: 100, ReachedCTL: 105, Weeks: 20}, "comfortable", "success",
			"Comfortable: your plan reaches fitness 105 by race day; this event needs about 100."},
		{"just under 5 percent is achievable", FeasibilityInput{TargetCTL: 100, ReachedCTL: 104, Weeks: 20}, "achievable", "success",
			"Achievable: your plan builds up to the fitness 100 this event needs."},
		{"reached below target with no shortfall never prints below it", FeasibilityInput{TargetCTL: 80, ReachedCTL: 70, Weeks: 20}, "achievable", "success",
			"Achievable: your plan builds up to the fitness 80 this event needs."},
		{"ratio 0.85 is achievable, just", FeasibilityInput{TargetCTL: 100, Shortfall: 15, Weeks: 12}, "achievable", "success",
			"Achievable, just: fitness 85 of about 100 by race day."},
		{"ratio under 0.85 is a stretch", FeasibilityInput{TargetCTL: 100, Shortfall: 16, Weeks: 12}, "stretch", "warning",
			"A stretch: fitness 84 of about 100 by race day. A later date would close it."},
		{"ratio 0.70 is a stretch", FeasibilityInput{TargetCTL: 100, Shortfall: 30, Weeks: 12}, "stretch", "warning",
			"A stretch: fitness 70 of about 100 by race day. A later date would close it."},
		{"ratio under 0.70 is unrealistic", FeasibilityInput{TargetCTL: 100, Shortfall: 31, Weeks: 9}, "unrealistic", "error",
			"Not realistic in 9 weeks: fitness can reach about 69, this event needs about 100."},
		{"hours cap explains a stretch", FeasibilityInput{TargetCTL: 100, Shortfall: 20, HoursCapped: true, Weeks: 9}, "stretch", "warning",
			"A stretch: your available hours cap the build at fitness 80 of about 100. More hours a week or a later date would close it."},
		// The cap changes the advice, never the verdict: half the fitness needed
		// is not realistic however it came about.
		{"hours cap does not soften an unrealistic goal", FeasibilityInput{TargetCTL: 100, Shortfall: 50, HoursCapped: true, Weeks: 9}, "unrealistic", "error",
			"Not realistic with your current hours: fitness can reach about 50, this event needs about 100. More hours a week or a later date would help."},
		{"hours cap does not demote a close plan", FeasibilityInput{TargetCTL: 100, Shortfall: 10, HoursCapped: true, Weeks: 9}, "achievable", "success",
			"Achievable, just: fitness 90 of about 100 by race day."},
		{"figures are rounded before comparing", FeasibilityInput{TargetCTL: 99.6, Shortfall: 14.6, Weeks: 9}, "achievable", "success",
			"Achievable, just: fitness 85 of about 100 by race day."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := JudgeFeasibility(c.in)
			if got.Key != c.key || got.Tone != c.tone || got.Message != c.msg {
				t.Errorf("got %+v, want key %s tone %s msg %q", got, c.key, c.tone, c.msg)
			}
		})
	}
}
