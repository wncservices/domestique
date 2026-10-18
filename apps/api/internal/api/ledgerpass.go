package api

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/ledger"
	"github.com/wncservices/domestique/apps/api/internal/lifeevents"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
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
	skipped, pushes := 0, 0
	for _, g := range goals {
		if seen[g.Rider] || ctx.Err() != nil {
			continue
		}
		seen[g.Rider] = true
		// A rider whose rides cannot be read right now would have them counted
		// as missed. Leave them untouched; nothing is claimed, so the next
		// tick of the morning slot tries again.
		if s.metricsFailedRider(g.Rider) || s.garminSessionExpired(g.Rider) {
			skipped++
			continue
		}
		if s.repayRider(ctx, g.Rider) {
			pushes++
		}
	}
	if pushes > 0 {
		s.logger().Info("load ledger: green-day pushes", "riders", pushes)
	}
	if skipped > 0 {
		s.logger().Warn("load ledger: riders skipped, their rides could not be read", "riders", skipped)
	}
}

// garminSessionExpired reports whether the rider has a stored Garmin session
// past its expected life, the same fact checkGarminExpiry counts. A rider with
// no Garmin link, or one that cannot be read, is not expired.
func (s *Server) garminSessionExpired(rider string) bool {
	if s.Links == nil {
		return false
	}
	link, err := s.Links.Get(garminProvider, rider)
	if err != nil {
		return false
	}
	return garmin.Session{ObtainedAt: link.UpdatedAt}.Expired(s.now())
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

// repayRider reports whether a green-day push was applied, so the pass can
// count them without naming the riders.
func (s *Server) repayRider(ctx context.Context, rider string) (pushed bool) {
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

	// Everything is read before the day is claimed, so a transient read error
	// costs this tick and not the whole day.
	events, err := s.lifeEventsFor(ctx, rider)
	if err != nil {
		s.logger().Warn("load ledger: could not read life events", "rider", rider, "err", err)
		return
	}
	// The return ramp after an illness is eased on purpose, so its days are
	// neither repaid onto nor part of what the week owes.
	blackout := lifeevents.NoTestDays(events)
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		s.logger().Warn("load ledger: could not read sessions", "rider", rider, "err", err)
		return
	}
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		s.logger().Warn("load ledger: could not read workouts", "rider", rider, "err", err)
		return
	}
	row, _, err := s.Training.GetLedgerWeek(ctx, rider, weekStart)
	if err != nil {
		s.logger().Warn("load ledger: could not read the week", "rider", rider, "err", err)
		return
	}
	var levels map[string]float64
	if levels, err = s.levelsFor(ctx, rider, profile, sport); err != nil {
		s.logger().Warn("load ledger: could not read levels", "rider", rider, "err", err)
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

	reduced := planWeek.Recovery || planWeek.Phase == periodization.PhaseTaper
	// Opened on the target not yet being set, not on the row being new: a crash
	// between the claim and the open must not skip opening forever.
	if row.TargetTSS == 0 {
		row, err = s.openLedgerWeek(ctx, rider, monday, planWeek, profile, blackout, sessions, workouts, reduced)
		if err != nil {
			s.logger().Warn("load ledger: could not open the week", "rider", rider, "err", err)
			return
		}
	}
	if reduced {
		return
	}

	var week []workout.Workout
	for _, w := range workouts {
		if w.Date >= weekStart && w.Date <= sunday {
			week = append(week, w)
		}
	}
	debt, err := s.weekDebt(ctx, rider, weekStart, row)
	if err != nil {
		s.logger().Warn("load ledger: could not work out the debt", "rider", rider, "err", err)
		return
	}
	if debt <= ledger.MinDebt {
		return
	}

	in := ledger.RepayInput{
		Debt: debt, Week: week, From: now.AddDate(0, 0, 1).Format(dateLayout), To: sunday,
		Profile: profile, Phase: planWeek.Phase, Recovery: planWeek.Recovery, Levels: levels, Blackout: blackout,
		UpgradedAlready: row.UpgradedAt != "",
	}
	byID := make(map[string]workout.Workout, len(week))
	for _, w := range week {
		byID[w.ID] = w
	}

	// A good morning is the cheapest place to pay load back: today's session is
	// raised first, and only what is still owed goes on to later days. A failure
	// here never stops the repayment below.
	if gain := s.greenPush(ctx, rider, today, in, byID, profile); gain > 0 {
		debt -= gain
		in.Debt = debt
		pushed = true
	}

	actions := ledger.PlanRepayment(in)
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
			if a.Kind == "upgrade" {
				if err := s.Training.MarkLedgerUpgraded(ctx, rider, weekStart, today); err != nil {
					s.logger().Warn("load ledger: could not record the upgrade", "rider", rider, "err", err)
				}
			}
		}
	}
	// Counts only: a TSS next to a rider's name is a fitness value, and a green
	// push beside a name would reveal the day's readiness verdict.
	s.logger().Info("load repaid", "rider", rider, "sessions", applied)
	return pushed
}

// greenPush raises today's session when readiness says push and the week owes
// load, returning the TSS it really added (0 when nothing was done). Reading the
// verdict writes nothing: only an applied push records a reason.
func (s *Server) greenPush(ctx context.Context, rider, today string, in ledger.RepayInput, byID map[string]workout.Workout, profile workout.RiderProfile) float64 {
	latest, err := s.latestFitness(ctx, rider)
	if err != nil {
		s.logger().Warn("green push: could not read fitness", "rider", rider, "err", err)
		return 0
	}
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		s.logger().Warn("green push: could not read sessions", "rider", rider, "err", err)
		return 0
	}
	if s.assessReadiness(ctx, rider, sessions, latest).Verdict != readiness.Push {
		return 0
	}
	a := ledger.PlanGreenPush(in, today)
	if a == nil {
		return 0
	}
	// A session already ridden has nothing left to raise: rewriting it would
	// change a record of what was done. The same test readiness easing uses.
	if adapter.WorkoutDone(byID[a.WorkoutID], sessions) {
		return 0
	}
	gain, err := s.applyRepayment(ctx, rider, byID[a.WorkoutID], profile, *a, in.Debt)
	if err != nil {
		s.logger().Warn("green push: could not raise the session", "rider", rider)
		return 0
	}
	return gain
}

// openLedgerWeek fixes the week's target and settles last week's row into it,
// returning the row as stored.
//
// The target is the plan's, scaled to the days not blacked out, but never more
// than what the scheduler actually built for the week: a plan target the
// sessions fall short of, or days of a week that were never planned (a goal
// saved on Thursday), are not load the rider has lost. Debt then starts at
// what was carried in and grows only as planned sessions are missed, eased or
// cut short.
//
// Last week's own debt rolls in up to a tenth of this week's target (nothing
// into a recovery or taper week) and the rest of what it owed is forgiven.
func (s *Server) openLedgerWeek(ctx context.Context, rider string, monday time.Time, planWeek periodization.Week, profile workout.RiderProfile, blackout map[string]bool, sessions []workout.CompletedSession, workouts []workout.Workout, reduced bool) (workout.LedgerWeek, error) {
	weekStart := monday.Format(dateLayout)
	weekEnd := monday.AddDate(0, 0, 6).Format(dateLayout)
	target := ledger.Target(planWeek.TargetTSS, profile.AvailableDays, weekStart, blackout)
	var built float64
	for _, w := range workouts {
		if w.GoalID != "" && w.Date >= weekStart && w.Date <= weekEnd {
			built += workout.PlannedTSSOrHours(w, profile.FTPWatts)
		}
	}
	target = math.Min(target, built)
	openDays := ledger.OpenDays(profile.AvailableDays, weekStart, blackout)

	lastStart := monday.AddDate(0, 0, -7).Format(dateLayout)
	var rolled, forgiven float64
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
		lastTarget := ledger.EffectiveTarget(last.TargetTSS, last.OpenDays, ledger.OpenDays(profile.AvailableDays, lastStart, blackout))
		lastDebt := ledger.Debt(lastTarget, last.CarriedInTSS, done, 0)
		rolled, forgiven = ledger.Settle(lastDebt, last.CarriedInTSS, target, reduced, last.Reduced)
	}
	if err := s.Training.OpenLedgerWeek(ctx, rider, weekStart, target, rolled, openDays, reduced); err != nil {
		return workout.LedgerWeek{}, err
	}
	if found {
		if err := s.Training.SetLedgerForgiven(ctx, rider, lastStart, forgiven); err != nil {
			return workout.LedgerWeek{}, err
		}
		s.logger().Info("load ledger settled last week", "rider", rider, "rolled", rolled > 0, "forgiven", forgiven > 0)
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
	rule, reason := why.LoadRepaid, "Load made up."
	if a.Kind == "push" {
		rule, reason = why.GreenPush, "Pushed on a good day."
	}
	// A lengthened ride keeps its name ("Long ride, with climbing" must
	// survive); an upgrade is a different session and takes the new one, and so
	// does a structured session stepped up a rung (a.Level set).
	if a.Kind == "upgrade" || (a.Kind == "push" && a.Level > 0) {
		name = a.Name
		level = &a.Level
	}
	steps := append([]workout.WorkoutStep{}, a.Steps...)
	zone := a.Zone
	// Dual write: the flags and the description marker, which the parity check
	// holds together. The marker goes in before keepIndoor so the trainer note
	// lands in front of it, and the reason text after it, where the UI reads it.
	flags := workout.CurrentFlags(wk).WithAdaptation(rule)
	base := wk.Description
	if a.Kind == "upgrade" {
		// A different session now: the endurance ride's own text no longer
		// describes it.
		base = workout.GeneratedDescription
	}
	description := base + " " + scheduler.AdjustedMarker
	req := workout.UpdateWorkoutRequest{Name: &name, Steps: &steps, Zone: &zone, Level: level, Description: &description, Flags: &flags}
	s.keepIndoor(&req, wk, profile, zone)

	// What the session is now worth, as it will be ridden.
	after := wk
	after.Steps = *req.Steps
	gain := workout.PlannedTSSOrHours(after, profile.FTPWatts) - workout.PlannedTSSOrHours(wk, profile.FTPWatts)
	if gain <= minRepaidGain {
		return 0, nil
	}
	// No figure in the text: the description reaches the calendar feed and the
	// head unit. The Why popover keeps the number through LoadRepaidInputs.
	withReason := *req.Description + " " + reason
	req.Description = &withReason

	if _, err := s.Training.UpdateWorkout(ctx, wk.ID, req); err != nil {
		s.logger().Warn("load ledger: could not update a workout", "workout", wk.ID, "rider", rider, "err", err)
		return 0, err
	}
	var rec why.Record
	if rule == why.GreenPush {
		rec = why.NewRecord(rule, reason, why.GreenPushInputs{DebtBefore: debtBefore, TSSAdded: gain})
	} else {
		rec = why.NewRecord(rule, reason, why.LoadRepaidInputs{DebtBefore: debtBefore, TSSAdded: gain})
	}
	s.recordAdjustment(ctx, rider, wk.ID, rec, req.Indoor != nil && *req.Indoor)
	return gain, nil
}

// weekDebt is what the current week still owes: the one computation the
// morning pass and the week strip share, so the number a rider reads is the
// number the pass acts on. Load lost after the week opened, plus what was
// carried in, less what is done and what is still planned for today onward.
// A life event added since the week opened takes days away from what it owes,
// so the session it removed is not debt.
func (s *Server) weekDebt(ctx context.Context, rider, weekStart string, row workout.LedgerWeek) (float64, error) {
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		return 0, err
	}
	events, err := s.lifeEventsFor(ctx, rider)
	if err != nil {
		return 0, err
	}
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		return 0, err
	}
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return 0, err
	}
	start, err := time.ParseInLocation(dateLayout, weekStart, s.localNow().Location())
	if err != nil {
		return 0, err
	}
	sunday := start.AddDate(0, 0, 6).Format(dateLayout)
	today := s.localNow().Format(dateLayout)

	var completed, remaining float64
	for _, c := range sessions {
		if c.Date >= weekStart && c.Date <= today {
			completed += c.TrainingLoad
		}
	}
	for _, w := range workouts {
		if w.Date < weekStart || w.Date > sunday {
			continue
		}
		if w.GoalID != "" && w.Date >= today && !adapter.WorkoutDone(w, sessions) {
			remaining += workout.PlannedTSSOrHours(w, profile.FTPWatts)
		}
	}
	target := ledger.EffectiveTarget(row.TargetTSS, row.OpenDays, ledger.OpenDays(profile.AvailableDays, weekStart, lifeevents.NoTestDays(events)))
	return ledger.Debt(target, row.CarriedInTSS, completed, remaining), nil
}
