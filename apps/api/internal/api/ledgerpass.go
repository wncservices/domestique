package api

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/ledger"
	"github.com/wncservices/domestique/apps/api/internal/lifeevents"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// minRepaidGain is the TSS a session must really gain, once it has been through
// the trainer conversion again, for the change to be worth writing.
const minRepaidGain = 0.5

// repayLoad is the load ledger's morning pass: once a day, in the day's first
// sync slot, it closes last week's books on a Monday and makes up what this
// week still owes by lengthening, or at most once upgrading, sessions later in
// the week. See docs/superpowers/specs/2026-10-18-load-ledger-design.md.
//
// It runs inside AutoScheduleTick, under the scheduling lock, and never fails
// the tick: a rider whose pass cannot run is a Warn naming the stage.
func (s *Server) repayLoad(ctx context.Context) {
	if s.Training == nil || !s.inMorningSlot(s.now()) {
		return
	}
	goals, err := s.Training.ListAllGoals(ctx)
	if err != nil {
		s.logger().Warn("load ledger: listing goals failed", "err", err)
		return
	}
	seen := map[string]bool{}
	for _, g := range goals {
		if seen[g.Rider] || ctx.Err() != nil {
			continue
		}
		seen[g.Rider] = true
		s.repayRider(ctx, g.Rider)
	}
}

// ledgerPlanWeek is the rider's focus goal's plan and its entry for the week
// beginning weekStart. ok is false when no goal can be planned or covers it.
func (s *Server) ledgerPlanWeek(ctx context.Context, rider, weekStart string) (plan periodization.Plan, week periodization.Week, sport model.Sport, ok bool, err error) {
	goals, err := s.Training.ListGoals(ctx, rider)
	if err != nil {
		return plan, week, "", false, err
	}
	sortGoalsForFocus(goals)
	for _, g := range goals {
		plan, _, err := s.reconciledPeriodizationPlan(ctx, g, rider)
		if err == periodization.ErrNoEventDate || err == periodization.ErrEventInThePast {
			continue
		}
		if err != nil {
			return plan, week, "", false, err
		}
		for _, wk := range plan.Weeks {
			if wk.StartDate == weekStart {
				sport = g.Sport
				if sport == "" {
					sport = model.SportCycling
				}
				return plan, wk, sport, true, nil
			}
		}
	}
	return plan, week, "", false, nil
}

func (s *Server) repayRider(ctx context.Context, rider string) {
	now := s.localNow()
	today := now.Format(dateLayout)
	monday := periodization.MondayOf(now)
	weekStart := monday.Format(dateLayout)
	sunday := monday.AddDate(0, 0, 6).Format(dateLayout)

	plan, planWeek, sport, ok, err := s.ledgerPlanWeek(ctx, rider, weekStart)
	if err != nil {
		s.logger().Warn("load ledger: could not read the plan", "rider", rider, "err", err)
		return
	}
	// Only a plan sized in TSS has a target to owe against; the hours basis is
	// adapter.Reconcile's and stays exactly as it was.
	if !ok || plan.LoadBasis != "ctl" {
		return
	}
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		s.logger().Warn("load ledger: could not read the profile", "rider", rider, "err", err)
		return
	}
	// A TSS needs an FTP to price a session by, whatever the plan was sized on.
	if profile.FTPWatts <= 0 {
		return
	}

	claimed, _, err := s.Training.ClaimLedgerDay(ctx, rider, weekStart, today)
	if err != nil {
		s.logger().Warn("load ledger: could not claim the day", "rider", rider, "err", err)
		return
	}
	if !claimed {
		return
	}

	events, err := s.lifeEventsFor(ctx, rider)
	if err != nil {
		s.logger().Warn("load ledger: could not read life events", "rider", rider, "err", err)
		return
	}
	blackout := lifeevents.Blackout(events)
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		s.logger().Warn("load ledger: could not read sessions", "rider", rider, "err", err)
		return
	}
	reduced := planWeek.Recovery || planWeek.Phase == periodization.PhaseTaper

	row, _, err := s.Training.GetLedgerWeek(ctx, rider, weekStart)
	if err != nil {
		s.logger().Warn("load ledger: could not read the week", "rider", rider, "err", err)
		return
	}
	// Opened on the target not yet being set, not on the row being new: a crash
	// between the claim and the open must not skip opening forever.
	if row.TargetTSS == 0 {
		row, err = s.openLedgerWeek(ctx, rider, monday, planWeek, profile, blackout, sessions, reduced)
		if err != nil {
			s.logger().Warn("load ledger: could not open the week", "rider", rider, "err", err)
			return
		}
	}
	if reduced {
		return
	}

	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		s.logger().Warn("load ledger: could not read workouts", "rider", rider, "err", err)
		return
	}
	var completed, remaining float64
	for _, c := range sessions {
		if c.Date >= weekStart && c.Date <= today {
			completed += c.TrainingLoad
		}
	}
	var week []workout.Workout
	for _, w := range workouts {
		if w.Date < weekStart || w.Date > sunday {
			continue
		}
		week = append(week, w)
		if w.GoalID != "" && w.Date >= today && !adapter.WorkoutDone(w, sessions) {
			remaining += workout.PlannedTSSOrHours(w, profile.FTPWatts)
		}
	}
	debt := ledger.Debt(row.TargetTSS, row.CarriedInTSS, completed, remaining)
	if debt <= ledger.MinDebt {
		return
	}

	levels, err := s.levelsFor(ctx, rider, profile, sport)
	if err != nil {
		s.logger().Warn("load ledger: could not read levels", "rider", rider, "err", err)
		return
	}
	actions := ledger.PlanRepayment(ledger.RepayInput{
		Debt: debt, Week: week, From: now.AddDate(0, 0, 1).Format(dateLayout), To: sunday,
		Profile: profile, Phase: planWeek.Phase, Recovery: planWeek.Recovery, Levels: levels, Blackout: blackout,
	})
	byID := make(map[string]workout.Workout, len(week))
	for _, w := range week {
		byID[w.ID] = w
	}
	var applied int
	for _, a := range actions {
		if ctx.Err() != nil {
			break
		}
		gain, err := s.applyRepayment(ctx, rider, byID[a.WorkoutID], profile, a, debt)
		if err != nil {
			continue
		}
		if gain > 0 {
			debt -= gain
			applied++
		}
	}
	// Counts only: a TSS next to a rider's name is a fitness value.
	s.logger().Info("load repaid", "rider", rider, "sessions", applied)
}

// openLedgerWeek fixes the week's target and settles last week's row into it,
// returning the row as stored. Last week's own debt rolls in up to a tenth of
// this week's target (nothing into a recovery or taper week) and the rest of
// what it owed is forgiven.
func (s *Server) openLedgerWeek(ctx context.Context, rider string, monday time.Time, planWeek periodization.Week, profile workout.RiderProfile, blackout map[string]bool, sessions []workout.CompletedSession, reduced bool) (workout.LedgerWeek, error) {
	weekStart := monday.Format(dateLayout)
	target := ledger.Target(planWeek.TargetTSS, profile.AvailableDays, weekStart, blackout)
	lastStart := monday.AddDate(0, 0, -7).Format(dateLayout)
	var rolled float64
	last, found, err := s.Training.GetLedgerWeek(ctx, rider, lastStart)
	if err != nil {
		return workout.LedgerWeek{}, err
	}
	if found {
		lastEnd := monday.AddDate(0, 0, -1).Format(dateLayout)
		var done float64
		for _, c := range sessions {
			if c.Date >= lastStart && c.Date <= lastEnd {
				done += c.TrainingLoad
			}
		}
		lastDebt := ledger.Debt(last.TargetTSS, last.CarriedInTSS, done, 0)
		var forgiven float64
		rolled, forgiven = ledger.Settle(lastDebt, last.CarriedInTSS, target, reduced)
		if err := s.Training.SetLedgerForgiven(ctx, rider, lastStart, forgiven); err != nil {
			return workout.LedgerWeek{}, err
		}
		s.logger().Info("load ledger settled last week", "rider", rider, "rolled", rolled > 0, "forgiven", forgiven > 0)
	}
	if err := s.Training.OpenLedgerWeek(ctx, rider, weekStart, target, rolled); err != nil {
		return workout.LedgerWeek{}, err
	}
	row, _, err := s.Training.GetLedgerWeek(ctx, rider, weekStart)
	return row, err
}

// applyRepayment writes one repayment and records why. It returns the TSS the
// session really gained once re-converted for a trainer, 0 when the change was
// not worth making, and an error only when a write failed (already logged).
//
// The session is updated first and the reason recorded after, because the why
// popover is shown only while the session still matches what the reason
// describes (see recordAdjustment).
func (s *Server) applyRepayment(ctx context.Context, rider string, wk workout.Workout, profile workout.RiderProfile, a ledger.Action, debtBefore float64) (float64, error) {
	if wk.ID == "" {
		return 0, fmt.Errorf("workout %s is not in the week", a.WorkoutID)
	}
	name := wk.Name
	var level *float64
	if a.Kind == "upgrade" {
		// A lengthened ride keeps its name ("Long ride, with climbing" must
		// survive); an upgrade is a different session and takes the new one.
		name = a.Name
		level = &a.Level
	}
	steps := append([]workout.WorkoutStep{}, a.Steps...)
	zone := a.Zone
	// Dual write: the flags and the description marker, which the parity check
	// holds together. The marker goes in before keepIndoor so the trainer note
	// lands in front of it, and the reason text after it, where the UI reads it.
	flags := workout.CurrentFlags(wk).WithAdaptation(why.LoadRepaid)
	description := wk.Description + " " + scheduler.AdjustedMarker
	req := workout.UpdateWorkoutRequest{Name: &name, Steps: &steps, Zone: &zone, Level: level, Description: &description, Flags: &flags}
	s.keepIndoor(&req, wk, profile, zone)

	// What the session is now worth, as it will be ridden.
	after := wk
	after.Steps = *req.Steps
	gain := workout.PlannedTSSOrHours(after, profile.FTPWatts) - workout.PlannedTSSOrHours(wk, profile.FTPWatts)
	if gain <= minRepaidGain {
		return 0, nil
	}
	reason := fmt.Sprintf("Load made up: +%d TSS.", int(math.Round(gain)))
	withReason := *req.Description + " " + reason
	req.Description = &withReason

	if _, err := s.Training.UpdateWorkout(ctx, wk.ID, req); err != nil {
		s.logger().Warn("load ledger: could not update a workout", "workout", wk.ID, "rider", rider, "err", err)
		return 0, err
	}
	rec := why.NewRecord(why.LoadRepaid, reason, why.LoadRepaidInputs{DebtBefore: debtBefore, TSSAdded: gain})
	s.recordAdjustment(ctx, rider, wk.ID, rec, req.Indoor != nil && *req.Indoor)
	return gain, nil
}
