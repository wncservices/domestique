package ledger

import (
	"sort"

	"github.com/wncservices/domestique/apps/api/internal/indoor"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
	"time"
)

// Action is one session change that pays back part of the debt. Steps are the
// outdoor steps: an indoor session is re-converted by the caller.
type Action struct {
	WorkoutID string
	Kind      string // "lengthen" | "upgrade"
	Name      string
	Steps     []workout.WorkoutStep
	Zone      workout.Zone
	Level     float64
	TSSAdded  float64
}

// RepayInput is everything PlanRepayment reads.
type RepayInput struct {
	Debt     float64
	Week     []workout.Workout // every workout of the rider dated in the week
	From     string            // tomorrow, YYYY-MM-DD
	To       string            // Sunday
	Profile  workout.RiderProfile
	Phase    periodization.Phase
	Recovery bool
	// Levels is the rider's levels as the scheduler reads them: keyed by zone
	// (levelFor), with "<sport>:<zone>" tried first.
	Levels   map[string]float64
	Blackout map[string]bool
}

const minGain = 0.5

// outdoorSteps is a session's steps before any trainer conversion.
func outdoorSteps(w workout.Workout) []workout.WorkoutStep {
	if w.OutdoorSteps != nil {
		return *w.OutdoorSteps
	}
	return w.Steps
}

// tssOf prices steps as the session would be ridden, on the trainer when w is
// indoor (the trainer conversion can cap what extra length adds).
func tssOf(w workout.Workout, steps []workout.WorkoutStep, p workout.RiderProfile) float64 {
	cand := w
	cand.Steps = steps
	if w.Indoor {
		cand.Indoor, cand.OutdoorSteps = false, nil
		if res, err := indoor.Convert(cand, p); err == nil {
			cand.Steps = res.Steps
		}
	}
	return workout.PlannedTSSOrHours(cand, p.FTPWatts)
}

func sortedRepayable(in RepayInput, keep func(workout.Workout) bool) []workout.Workout {
	var out []workout.Workout
	for _, w := range in.Week {
		if w.Date < in.From || w.Date > in.To || !Repayable(w, in.Blackout) || !keep(w) {
			continue
		}
		out = append(out, w)
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := workout.PlannedSeconds(outdoorSteps(out[i])), workout.PlannedSeconds(outdoorSteps(out[j]))
		if si != sj {
			return si > sj
		}
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func level(in RepayInput, sport model.Sport, zone string) float64 {
	if v, ok := in.Levels[string(sport)+":"+zone]; ok {
		return v
	}
	if v, ok := in.Levels[zone]; ok {
		return v
	}
	return 3.0
}

func dayBefore(date string, delta int) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	return t.AddDate(0, 0, delta).Format("2006-01-02")
}

// PlanRepayment chooses which sessions to lengthen, longest first, and at most
// one endurance session to upgrade to a structured one, until the debt is paid
// down to MinDebt. It changes nothing; the caller applies the actions.
func PlanRepayment(in RepayInput) []Action {
	if in.Recovery || in.Phase == periodization.PhaseTaper || in.Debt <= MinDebt {
		return nil
	}
	endurance := func(w workout.Workout) bool { return w.Zone == workout.ZoneEndurance }
	candidates := sortedRepayable(in, endurance)

	// The week's longest session of any kind is its long ride.
	var longestID string
	var longest float64
	for _, w := range in.Week {
		s := workout.PlannedSeconds(outdoorSteps(w))
		if s > longest || (s == longest && w.ID < longestID) {
			longest, longestID = s, w.ID
		}
	}

	debt := in.Debt
	var actions []Action
	touched := map[string]bool{}

	for _, w := range candidates {
		if debt <= MinDebt {
			break
		}
		old := outdoorSteps(w)
		cur := workout.PlannedSeconds(old)
		if cur <= 0 {
			continue
		}
		next := cur * (1 + LengthenFraction)
		if v := cur + LengthenMaxSeconds; v < next {
			next = v
		}
		if cap := in.Profile.HoursPerAvailableDay * 3600; cap > 0 && cap < next {
			next = cap
		}
		if next <= cur {
			continue
		}
		long := w.ID == longestID || scheduler.IsLongRideName(w.Name)
		req := scheduler.BuildEnduranceSession(next/3600, long, w.Sport, in.Profile)
		gain := tssOf(w, req.Steps, in.Profile) - tssOf(w, old, in.Profile)
		if gain <= minGain {
			continue
		}
		actions = append(actions, Action{
			WorkoutID: w.ID, Kind: "lengthen", Name: req.Name, Steps: req.Steps,
			Zone: workout.ZoneEndurance, TSSAdded: gain,
		})
		touched[w.ID] = true
		debt -= gain
	}

	if a, ok := planUpgrade(in, debt, longestID, touched); ok {
		actions = append(actions, a)
	}
	return actions
}

// planUpgrade turns one endurance session into the phase's first structured
// zone, when the week is short of structured sessions, no hard session sits
// the day before or after, and debt is still above UpgradeDebt.
func planUpgrade(in RepayInput, debt float64, longestID string, touched map[string]bool) (Action, bool) {
	if debt <= UpgradeDebt || in.Profile.FTPWatts <= 0 {
		return Action{}, false
	}
	structured := 0
	hardDays := map[string]bool{}
	for _, w := range in.Week {
		if workout.IsStructuredZone(w.Zone) {
			structured++
		}
		if scheduler.IsHardSession(w) {
			hardDays[w.Date] = true
		}
	}
	candidates := sortedRepayable(in, func(w workout.Workout) bool {
		return w.Zone == workout.ZoneEndurance && w.ID != longestID && !touched[w.ID] &&
			!scheduler.IsLongRideName(w.Name) && w.Sport == model.SportCycling
	})
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Date != candidates[j].Date {
			return candidates[i].Date < candidates[j].Date
		}
		return candidates[i].ID < candidates[j].ID
	})
	for _, w := range candidates {
		zones := scheduler.StructuredZones(in.Phase, w.Sport, len(in.Profile.AvailableDays))
		if len(zones) == 0 || structured >= len(zones) {
			return Action{}, false
		}
		if hardDays[dayBefore(w.Date, -1)] || hardDays[dayBefore(w.Date, 1)] {
			continue
		}
		zone := string(zones[0])
		ladder, ok := workoutlib.LadderFor(w.Sport, zone)
		if !ok {
			continue
		}
		old := outdoorSteps(w)
		rung, ok := workoutlib.Pick(ladder, level(in, w.Sport, zone), workout.PlannedSeconds(old))
		if !ok {
			continue
		}
		req := workoutlib.Instantiate(ladder, rung, in.Profile)
		gain := tssOf(w, req.Steps, in.Profile) - tssOf(w, old, in.Profile)
		if gain <= 0 {
			continue
		}
		return Action{
			WorkoutID: w.ID, Kind: "upgrade", Name: req.Name, Steps: req.Steps,
			Zone: req.Zone, Level: req.Level, TSSAdded: gain,
		}, true
	}
	return Action{}, false
}
