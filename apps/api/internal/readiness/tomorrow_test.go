package readiness

import (
	"reflect"
	"testing"
)

func TestForecastTomorrow(t *testing.T) {
	const (
		tsbRest   = "tomorrow's form is projected at −34"
		todayRest = "you needed to rest today, and tomorrow is a hard session too"
		acwrWord  = "with today's session counted, your load this week is 1.6× your usual"
		thirdDay  = "tomorrow would be your third hard day in a row"
	)
	cases := []struct {
		name string
		in   TomorrowInput
		want Assessment
	}{
		{"zero value is ready", TomorrowInput{}, Assessment{Verdict: Ready}},
		{"today rest alone is caution, never rest",
			TomorrowInput{TodayVerdict: Rest},
			Assessment{Verdict: Caution, Reasons: []string{todayRest}}},
		{"today caution alone is ready",
			TomorrowInput{TodayVerdict: Caution},
			Assessment{Verdict: Ready}},
		{"TSB -29 is ready",
			TomorrowInput{ProjectedTSB: -29, HaveProjectedTSB: true},
			Assessment{Verdict: Ready}},
		{"TSB exactly -30 is not below the threshold",
			TomorrowInput{ProjectedTSB: -30, HaveProjectedTSB: true},
			Assessment{Verdict: Ready}},
		{"TSB -34 is rest",
			TomorrowInput{ProjectedTSB: -34, HaveProjectedTSB: true},
			Assessment{Verdict: Rest, Reasons: []string{tsbRest}}},
		{"TSB just below -30 rounds for display",
			TomorrowInput{ProjectedTSB: -30.4, HaveProjectedTSB: true},
			Assessment{Verdict: Rest, Reasons: []string{"tomorrow's form is projected at −30"}}},
		{"TSB ignored without HaveProjectedTSB",
			TomorrowInput{ProjectedTSB: -50},
			Assessment{Verdict: Ready}},
		{"ACWR 1.49 is ready",
			TomorrowInput{ACWR: 1.49, HaveACWR: true},
			Assessment{Verdict: Ready}},
		{"ACWR 1.5 is caution",
			TomorrowInput{ACWR: 1.5, HaveACWR: true},
			Assessment{Verdict: Caution, Reasons: []string{"with today's session counted, your load this week is 1.5× your usual"}}},
		{"ACWR ignored without HaveACWR",
			TomorrowInput{ACWR: 2.5},
			Assessment{Verdict: Ready}},
		{"1 hard day is ready", TomorrowInput{ConsecutiveHardDays: 1}, Assessment{Verdict: Ready}},
		{"2 hard days is caution",
			TomorrowInput{ConsecutiveHardDays: 2},
			Assessment{Verdict: Caution, Reasons: []string{thirdDay}}},
		{"3 hard days is caution",
			TomorrowInput{ConsecutiveHardDays: 3},
			Assessment{Verdict: Caution, Reasons: []string{thirdDay}}},
		{"caution reasons keep rule order",
			TomorrowInput{TodayVerdict: Rest, ACWR: 1.6, HaveACWR: true, ConsecutiveHardDays: 2},
			Assessment{Verdict: Caution, Reasons: []string{todayRest, acwrWord, thirdDay}}},
		{"rest wins and lists rest reasons before caution reasons",
			TomorrowInput{TodayVerdict: Rest, ProjectedTSB: -34, HaveProjectedTSB: true, ConsecutiveHardDays: 2},
			Assessment{Verdict: Rest, Reasons: []string{tsbRest, todayRest, thirdDay}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ForecastTomorrow(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
