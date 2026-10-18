package workout

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/why"
)

func TestLegacyFlags(t *testing.T) {
	gen := GeneratedDescription
	cases := []struct {
		name string
		w    Workout
		want Flags
	}{
		{"plain plan", Workout{GoalID: "g1", Description: gen}, Flags{Origin: OriginPlan}},
		{"crew row", Workout{GoalID: "g1", CrewRideID: "r1", Description: "anything"}, Flags{Origin: OriginCrew}},
		{"test", Workout{GoalID: "g1", TestProtocol: "ramp", Description: "FTP ramp"}, Flags{Origin: OriginTest}},
		{"hand-built", Workout{Description: "my own"}, Flags{Origin: OriginRider}},
		{"adjusted twice", Workout{GoalID: "g1", Description: gen + " " + AdjustedMarker + " Tired. " + AdjustedMarker + " Life event: illness."},
			Flags{Origin: OriginPlan, AdaptedRules: []why.Rule{why.Legacy, why.LifeEvent}}},
		{"swapped", Workout{GoalID: "g1", Description: gen + " " + SwappedMarker + " easier: X"}, Flags{Origin: OriginPlan, Swapped: true}},
		{"crew kept", Workout{GoalID: "g1", Description: gen + " " + CrewKeptMarker}, Flags{Origin: OriginPlan, Kept: KeptCrew}},
		{"life kept", Workout{GoalID: "g1", Description: gen + " " + LifeKeptMarker}, Flags{Origin: OriginPlan, Kept: KeptLife}},
		{"life move", Workout{GoalID: "g1", Description: gen + "\n\nRescheduled by a life event: moved from 2026-10-01."},
			Flags{Origin: OriginPlan, MovedFrom: "2026-10-01", MovedBy: MovedByLife}},
		{"life move again", Workout{GoalID: "g1", Description: gen + "\n\nRescheduled by a life event: moved from 2026-10-01.\n\nRescheduled again by a life event (it was on 2026-10-02)."},
			Flags{Origin: OriginPlan, MovedFrom: "2026-10-01", MovedBy: MovedByLife}},
		{"rider move", Workout{GoalID: "g1", Description: gen + "\n\nRescheduled by you: moved from 2026-10-03."},
			Flags{Origin: OriginPlan, MovedFrom: "2026-10-03", MovedBy: MovedByRider}},
		{"auto move", Workout{GoalID: "g1", Description: gen + " " + AdjustedMarker + " missed, moved from 2026-10-04."},
			Flags{Origin: OriginPlan, AdaptedRules: []why.Rule{why.Legacy}, MovedFrom: "2026-10-04", MovedBy: MovedByAuto}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := LegacyFlags(c.w)
			if !got.Equivalent(c.want) || got.MovedFrom != c.want.MovedFrom || got.Origin != c.want.Origin {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			if len(got.AdaptedRules) != len(c.want.AdaptedRules) {
				t.Fatalf("rules got %v, want %v", got.AdaptedRules, c.want.AdaptedRules)
			}
			for i := range got.AdaptedRules {
				if got.AdaptedRules[i] != c.want.AdaptedRules[i] {
					t.Fatalf("rules got %v, want %v", got.AdaptedRules, c.want.AdaptedRules)
				}
			}
		})
	}
}

func TestFlagsEquivalent(t *testing.T) {
	f := func(r ...why.Rule) Flags { return Flags{Origin: OriginPlan, AdaptedRules: r} }
	if !f(why.Legacy).Equivalent(f(why.MissedMoved)) {
		t.Error("legacy should match missed_moved")
	}
	if !f(why.LifeEvent).Equivalent(f(why.IllnessRamp)) {
		t.Error("life_event should match illness_ramp")
	}
	if f(why.Legacy).Equivalent(f(why.LifeEvent)) {
		t.Error("legacy must not match life_event")
	}
	if f(why.Legacy).Equivalent(f(why.Legacy, why.Legacy)) {
		t.Error("different counts must not match")
	}
}

func TestWithAdaptationDoesNotAlias(t *testing.T) {
	base := Flags{AdaptedRules: make([]why.Rule, 1, 4)}
	base.AdaptedRules[0] = why.Legacy
	a := base.WithAdaptation(why.MissedMoved)
	b := base.WithAdaptation(why.LifeEvent)
	if len(base.AdaptedRules) != 1 {
		t.Fatalf("original changed: %v", base.AdaptedRules)
	}
	if a.AdaptedRules[1] != why.MissedMoved || b.AdaptedRules[1] != why.LifeEvent {
		t.Fatalf("copies share storage: %v %v", a.AdaptedRules, b.AdaptedRules)
	}
	if !a.Adapted() || (Flags{}).Adapted() {
		t.Error("Adapted wrong")
	}
}
