package api

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestLoadInputUnit(t *testing.T) {
	// Wednesday 2026-10-07: the last 4 complete weeks start 2026-09-07.
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	snap := &workout.FitnessSnapshot{Date: "2026-10-07", CTL: 40, ATL: 50}
	ses := func(date string, hours, load float64) workout.CompletedSession {
		return workout.CompletedSession{Date: date, DurationSeconds: hours * 3600, TrainingLoad: load}
	}
	cases := []struct {
		name     string
		sessions []workout.CompletedSession
		exp      string
		goal     workout.Goal
		wantIF   float64
		wantHist int
		wantBest float64
		wantRamp float64
	}{
		{"no history: default IF", nil, "", workout.Goal{}, 0.70, 0, 0, 6},
		{"under 3 h keeps 0.70", []workout.CompletedSession{ses("2026-10-01", 2.9, 200)}, "beginner", workout.Goal{}, 0.70, 0, 200, 4},
		{"IF from load", []workout.CompletedSession{ses("2026-10-01", 4, 4*0.64*100)}, "advanced", workout.Goal{}, 0.80, 0, 256, 8},
		{"IF clamps high", []workout.CompletedSession{ses("2026-10-01", 4, 4*1.5*100)}, "intermediate", workout.Goal{}, 0.85, 0, 600, 6},
		{"IF clamps low", []workout.CompletedSession{ses("2026-10-01", 4, 4*0.1*100)}, "", workout.Goal{}, 0.55, 0, 40, 6},
		{"sessions older than 42 days are ignored for IF", []workout.CompletedSession{ses("2026-08-01", 10, 10*0.5*100)}, "", workout.Goal{}, 0.70, 9, 0, 6},
		{
			"history weeks and best of the last 4 complete weeks",
			[]workout.CompletedSession{
				ses("2026-08-05", 1, 10),                            // 9 weeks back; outside the 4 weeks
				ses("2026-09-02", 1, 999),                           // the week of 08-31: 5 weeks back, outside
				ses("2026-09-08", 1, 100), ses("2026-09-10", 1, 50), // week of 09-07: 150
				ses("2026-09-30", 1, 120), // week of 09-28: 120
				ses("2026-10-05", 1, 500), // this week: not complete
			},
			"", workout.Goal{}, 0.55, 9, 150, 6,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := loadInputFrom(tc.goal, workout.RiderProfile{ExperienceLevel: tc.exp}, snap, tc.sessions, now)
			if in.CTL != 40 || in.ATL != 50 {
				t.Errorf("a snapshot from today must not be rolled: %v/%v", in.CTL, in.ATL)
			}
			if tc.name != "history weeks and best of the last 4 complete weeks" && math.Abs(in.TypicalIF-tc.wantIF) > 0.005 {
				t.Errorf("TypicalIF = %v, want %v", in.TypicalIF, tc.wantIF)
			}
			if in.HistoryWeeks != tc.wantHist {
				t.Errorf("HistoryWeeks = %d, want %d", in.HistoryWeeks, tc.wantHist)
			}
			if in.BestRecentWeekTSS != tc.wantBest {
				t.Errorf("BestRecentWeekTSS = %v, want %v", in.BestRecentWeekTSS, tc.wantBest)
			}
			if in.MaxRamp != tc.wantRamp {
				t.Errorf("MaxRamp = %v, want %v", in.MaxRamp, tc.wantRamp)
			}
			if in.TargetCTL != 0 || in.TaperTSB != 0 {
				t.Errorf("an undated goal carries a target: %v %v", in.TargetCTL, in.TaperTSB)
			}
		})
	}

	t.Run("dated goal carries target and taper form", func(t *testing.T) {
		in := loadInputFrom(workout.Goal{EventDate: "2027-04-14", TargetDistanceM: 120000, TargetElevationM: 1500}, workout.RiderProfile{}, snap, nil, now)
		if in.TargetCTL <= 0 || in.TaperTSB <= 0 {
			t.Errorf("dated goal: TargetCTL %v TaperTSB %v", in.TargetCTL, in.TaperTSB)
		}
	})
	t.Run("stale snapshot decays", func(t *testing.T) {
		old := &workout.FitnessSnapshot{Date: "2026-09-07", CTL: 40, ATL: 50}
		in := loadInputFrom(workout.Goal{}, workout.RiderProfile{}, old, nil, now)
		if !(in.CTL < 40) || !(in.ATL < in.CTL+10) || in.ATL >= 50 {
			t.Errorf("rolled %v/%v", in.CTL, in.ATL)
		}
	})
	_ = fmt.Sprint
}
