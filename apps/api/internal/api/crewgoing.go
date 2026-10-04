package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/crewplan"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/schedule"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Joining and leaving a crew ride change the rest of the rider's plan, so both
// are confirmed as a preview. The server computes the diff itself, always:
// a dry run is only a picture of it, the client never supplies a diff, and
// applying recomputes it under the scheduling lock, so a plan that changed in
// between (the tick, another tab) is never half-applied from a stale picture.
//
// The stores take no shared transaction, so a failed apply compensates: the
// updates are made first and the deletions last, and whatever was written is put
// back as it was (see rollBackGoing).

// handleSetGoing is PUT /api/training/crew-rides/{rideId}/going: the one write.
//
// Going: an approved member of the ride's crew, for a ride dated today or later
// whose route still exists. Not going: the rider's own fixed session is removed,
// which is also how a session orphaned by a cancelled ride or by leaving the
// crew is updated, so that case needs neither the ride nor the membership to
// still exist. The rider is always the session's. A non-member is told there is
// no such ride, the answer a ride in somebody else's crew gets everywhere.
func (s *Server) handleSetGoing(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	if !s.crewAvailable(w) || !s.scheduleAvailable(w) {
		return
	}
	ctx := r.Context()
	rider := auth.FromContext(ctx).User
	rideID := r.PathValue("rideId")

	var body goingBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGoingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	in, status, msg, err := s.resolveGoing(ctx, rider, rideID, body)
	switch {
	case err != nil:
		s.fail(w, err)
		return
	case status != 0:
		s.logger().Info("going refused", "rider", rider, "ride", rideID, "status", status)
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}

	var out goingOutcome
	if body.DryRun {
		out = s.planGoing(ctx, in)
	} else {
		ran := withDBLock(ctx, s.dbConn(), autoScheduleLockKey, func() {
			s.seasonMu.Lock()
			defer s.seasonMu.Unlock()
			out = s.doGoing(ctx, in)
		})
		if !ran {
			s.writeReplanLocked(w, rider)
			return
		}
	}
	if out.err != nil {
		// A write that should have landed did not.
		s.logger().Error("crew ride going failed", "rider", rider, "ride", rideID, "err", out.err)
		s.fail(w, out.err)
		return
	}

	res := goingResultDTO{Going: out.going, Diff: out.diff, Applied: out.applied}
	if out.workout != nil {
		one := []workoutDTO{workoutDTOFrom(*out.workout)}
		s.attachCrewRides(ctx, rider, one)
		res.Workout = &one[0]
	}
	if out.applied != nil {
		// The ride and the counts only: never the rider's plan.
		s.logger().Info("crew ride going set", "rider", rider, "ride", rideID, "going", out.going,
			"removed", out.applied.Removed, "eased", out.applied.Eased, "shortened", out.applied.Shortened, "added", out.applied.Added)
	}
	writeJSON(w, http.StatusOK, res)
}

// resolveGoing checks who may do what and gathers the ride. A non-zero status is
// a refusal with its message; an error is a failed read.
func (s *Server) resolveGoing(ctx context.Context, rider, rideID string, body goingBody) (goingInput, int, string, error) {
	in := goingInput{rider: rider, want: body.Going, skip: setOf(body.Skip)}
	notFound := func() (goingInput, int, string, error) {
		return in, http.StatusNotFound, schedule.ErrNotFound.Error(), nil
	}

	ride, err := s.Schedule.Get(ctx, rideID)
	rideGone := errors.Is(err, schedule.ErrNotFound)
	if err != nil && !rideGone {
		return in, 0, "", err
	}
	snap, err := s.Crew.Snapshot(ctx)
	if err != nil {
		return in, 0, "", err
	}
	member := !rideGone && snap.ApprovedRiders.Has(ride.CrewID, rider)

	if !body.Going {
		// Leaving needs only the rider's own fixed session: a cancelled ride and
		// a crew the rider has left must still be possible to update.
		existing, err := s.fixedSessionFor(ctx, rider, rideID)
		if err != nil {
			return in, 0, "", err
		}
		if existing == nil && !member {
			return notFound()
		}
		if rideGone {
			ride = schedule.Ride{ID: rideID, Date: existing.Date}
		}
		in.ride = ride
		in.existing = existing
		return in, 0, "", nil
	}

	if !member {
		return notFound()
	}
	in.ride = ride
	if in.crew, err = s.Crew.Get(ctx, ride.CrewID); err != nil {
		return in, 0, "", err
	}
	route, ok, err := s.rideRoute(ctx, ride.Slug)
	if err != nil {
		return in, 0, "", err
	}
	if !ok {
		return in, http.StatusConflict, "this ride's route no longer exists", nil
	}
	if ride.Date < s.now().Format(dateLayout) {
		return in, http.StatusConflict, "that ride has already passed", nil
	}
	// No distance, no estimate: the ride would be planned as a zero-length session.
	if route.Stats.DistanceM <= 0 {
		return in, http.StatusConflict, "this route has no distance yet", nil
	}
	in.route = route
	if in.existing, err = s.fixedSessionFor(ctx, rider, rideID); err != nil {
		return in, 0, "", err
	}
	return in, 0, "", nil
}

// crewPlan is a computed diff with what applying it needs.
type crewPlan struct {
	diff    crewplan.Diff
	byID    map[string]workout.Workout
	profile workout.RiderProfile
}

// planGoing is the dry run: the picture of what applying would do, writing
// nothing.
func (s *Server) planGoing(ctx context.Context, in goingInput) goingOutcome {
	if in.want && in.existing != nil {
		return goingOutcome{going: true, diff: emptyCrewDiff(), workout: nil}
	}
	plan, err := s.planCrewRide(ctx, in)
	if err != nil {
		return goingOutcome{err: err}
	}
	return goingOutcome{going: in.want, diff: crewDiffDTOFrom(plan.diff)}
}

// planCrewRide computes the diff of joining or leaving, from the plan as it
// stands now.
func (s *Server) planCrewRide(ctx context.Context, in goingInput) (crewPlan, error) {
	now := s.now()
	today := now.Format(dateLayout)
	workouts, err := s.Training.ListWorkouts(ctx, in.rider)
	if err != nil {
		return crewPlan{}, err
	}
	profile, _, err := s.Training.GetProfile(ctx, in.rider)
	if err != nil {
		return crewPlan{}, err
	}
	ridden, err := s.riddenToday(ctx, in.rider, today, workouts)
	if err != nil {
		return crewPlan{}, err
	}
	blackout, err := s.blackoutFor(ctx, in.rider)
	if err != nil {
		return crewPlan{}, err
	}

	ride := crewplan.Ride{ID: in.ride.ID, Date: in.ride.Date}
	if in.want {
		ride.RouteName = in.route.Name
		ride.Seconds = crewplan.EstimateRoute(in.route.Stats).Seconds()
	} else if in.existing != nil {
		ride.RouteName = strings.TrimPrefix(in.existing.Name, crewRideNamePrefix)
		ride.Seconds = workout.PlannedSeconds(in.existing.Steps)
	}

	cin := crewplan.Input{
		Ride: ride, Leaving: !in.want, Workouts: workouts, Ridden: ridden,
		Profile: profile, Blackout: blackout, Now: now,
	}
	week, refill, err := s.rideWeek(ctx, in.rider, ride, workouts, !in.want)
	if err != nil {
		return crewPlan{}, err
	}
	if week != nil {
		cin.WeekTarget, cin.Recovery = week.TargetHours, week.Recovery
	}
	cin.Refill = refill

	byID := make(map[string]workout.Workout, len(workouts))
	for _, w := range workouts {
		byID[w.ID] = w
	}
	return crewPlan{diff: crewplan.Preview(cin), byID: byID, profile: profile}, nil
}

// rideWeek is the plan week the ride falls in, from the first of the rider's
// goals whose plan covers it, and, when asked, what that plan would make for the
// week without this ride (for the session put back on leaving). Nil, with no
// error, when no goal's plan covers the week.
func (s *Server) rideWeek(ctx context.Context, rider string, ride crewplan.Ride, workouts []workout.Workout, wantRefill bool) (*periodization.Week, []workout.CreateWorkoutRequest, error) {
	date, err := time.Parse(dateLayout, ride.Date)
	if err != nil {
		return nil, nil, nil
	}
	monday := periodization.MondayOf(date).Format(dateLayout)
	goals, err := s.Training.ListAllGoals(ctx)
	if err != nil {
		return nil, nil, err
	}
	// The goal the fixed session is linked to comes first, so a leave puts back
	// what that goal's plan would have made; any other covering goal is the fallback.
	preferred := ""
	for _, w := range workouts {
		if w.CrewRideID == ride.ID {
			preferred = w.GoalID
		}
	}
	sort.SliceStable(goals, func(i, j int) bool { return goals[i].ID == preferred && goals[j].ID != preferred })
	for _, g := range goals {
		if g.Rider != rider {
			continue
		}
		sc, err := s.seasonContext(ctx, g)
		if err != nil {
			if errors.Is(err, periodization.ErrNoEventDate) || errors.Is(err, periodization.ErrEventInThePast) {
				continue
			}
			return nil, nil, err
		}
		week, ok := sc.week(monday)
		if !ok {
			continue
		}
		if !wantRefill {
			return &week, nil, nil
		}
		// The other rides still stand; this one is what is being taken out.
		var others []workout.Workout
		for _, w := range workouts {
			if w.CrewRideID != ride.ID {
				others = append(others, w)
			}
		}
		reqs, err := scheduler.WeekWorkouts(week, sc.profile, sc.levels, rider, g.ID, g.Sport,
			append(sc.options(), fixedOption(others, week))...)
		if err != nil {
			return nil, nil, err
		}
		return &week, reqs, nil
	}
	return nil, nil, nil
}

// doGoing joins or leaves. It runs under the scheduling lock.
func (s *Server) doGoing(ctx context.Context, in goingInput) goingOutcome {
	// Read again inside the lock: the session may have been made or deleted
	// while this waited.
	existing, err := s.fixedSessionFor(ctx, in.rider, in.ride.ID)
	if err != nil {
		return goingOutcome{err: err}
	}
	in.existing = existing
	if in.want && existing != nil {
		// Already going: only make sure the going row is there.
		if err := s.Schedule.Go(ctx, in.ride.ID, in.rider); err != nil {
			return goingOutcome{err: err}
		}
		return goingOutcome{going: true, workout: existing, diff: emptyCrewDiff(), applied: &crewAppliedDTO{}}
	}
	if !in.want && existing == nil {
		if err := s.Schedule.Leave(ctx, in.ride.ID, in.rider); err != nil {
			return goingOutcome{err: err}
		}
		return goingOutcome{going: false, diff: emptyCrewDiff(), applied: &crewAppliedDTO{}}
	}

	plan, err := s.planCrewRide(ctx, in)
	if err != nil {
		return goingOutcome{err: err}
	}
	journal := &goingJournal{}
	applied, fixed, err := s.applyCrewRide(ctx, in, plan, journal)
	if err != nil {
		s.rollBackGoing(ctx, in, journal)
		return goingOutcome{err: err}
	}
	// What follows a plan change: today's session may now be a different one.
	if plan.profile.AutoPushWorkouts {
		s.pushWorkoutsForRider(ctx, in.rider)
	}
	return goingOutcome{going: in.want, workout: fixed, diff: crewDiffDTOFrom(plan.diff), applied: &applied}
}

// goingJournal is what an apply wrote, so a failure can put it back.
type goingJournal struct {
	created string                     // the fixed session made on joining
	wentIn  bool                       // the going row was written
	updated map[string]workout.Workout // originals of updated sessions
	removed []workout.Workout          // originals of deleted sessions, the fixed one on leaving included
	added   []string                   // sessions made to fill a freed day
}

// applyCrewRide writes the ride's own rows and the changes the rider did not
// skip. Order matters for the rollback: the fixed session and the going row
// first, then updates, then deletions last, since a deletion is the one write a
// compensation can only approximate.
func (s *Server) applyCrewRide(ctx context.Context, in goingInput, plan crewPlan, j *goingJournal) (crewAppliedDTO, *workout.Workout, error) {
	var n crewAppliedDTO
	var fixed *workout.Workout
	j.updated = map[string]workout.Workout{}

	if in.want {
		req, err := s.fixedSessionRequest(ctx, in)
		if err != nil {
			return n, nil, err
		}
		wk, err := s.Training.CreateWorkout(ctx, req)
		if errors.Is(err, workout.ErrCrewRideExists) {
			// Raced another request for the same ride; theirs stands.
			again, ferr := s.fixedSessionFor(ctx, in.rider, in.ride.ID)
			if ferr != nil || again == nil {
				return n, nil, err
			}
			wk, err = *again, nil
		} else if err == nil {
			j.created = wk.ID
		}
		if err != nil {
			return n, nil, err
		}
		fixed = &wk
		if err := s.afterCrewWrite(); err != nil {
			return n, nil, err
		}
		if err := s.Schedule.Go(ctx, in.ride.ID, in.rider); err != nil {
			return n, nil, err
		}
		j.wentIn = true
		if err := s.afterCrewWrite(); err != nil {
			return n, nil, err
		}
	}

	var deletions []crewplan.Change
	for _, c := range plan.diff.Changes {
		skipped := in.skip[c.ID]
		if skipped {
			// A skipped ease is the rider keeping the session as it is. Mark it,
			// or the easing rule, which runs after every plan change, would simply
			// do it again.
			if c.Op == crewplan.OpEase {
				if err := s.keepAroundCrewRide(ctx, plan.byID[c.WorkoutID], j); err != nil {
					return n, fixed, err
				}
			}
			continue
		}
		switch c.Op {
		case crewplan.OpRemove:
			deletions = append(deletions, c)
		case crewplan.OpEase, crewplan.OpShorten:
			wk := plan.byID[c.WorkoutID]
			if err := s.applyCrewUpdate(ctx, wk, plan.profile, c, j); err != nil {
				return n, fixed, err
			}
			if c.Op == crewplan.OpEase {
				n.Eased++
			} else {
				n.Shortened++
			}
			if err := s.afterCrewWrite(); err != nil {
				return n, fixed, err
			}
		case crewplan.OpAdd:
			if c.Create == nil {
				continue
			}
			made, err := s.Training.CreateWorkout(ctx, *c.Create)
			if err != nil {
				return n, fixed, err
			}
			j.added = append(j.added, made.ID)
			n.Added++
		}
	}

	if !in.want && in.existing != nil {
		deletions = append(deletions, crewplan.Change{WorkoutID: in.existing.ID, Op: crewplan.OpRemove})
	}
	for _, c := range deletions {
		wk := plan.byID[c.WorkoutID]
		if wk.ID == "" {
			wk = *in.existing
		}
		s.removeWorkoutFromGarmin(ctx, wk)
		if err := s.Training.DeleteWorkout(ctx, wk.ID); err != nil && !errors.Is(err, workout.ErrWorkoutNotFound) {
			return n, fixed, err
		}
		j.removed = append(j.removed, wk)
		// The fixed session going is the leave itself, not a change around it.
		if in.existing == nil || in.existing.ID != wk.ID {
			n.Removed++
		}
		if err := s.afterCrewWrite(); err != nil {
			return n, fixed, err
		}
	}
	if !in.want {
		if err := s.Schedule.Leave(ctx, in.ride.ID, in.rider); err != nil {
			return n, fixed, err
		}
	}
	return n, fixed, nil
}

// afterCrewWrite runs the test hook, which stands in for a failed write.
func (s *Server) afterCrewWrite() error {
	if s.AfterCrewRideWrite != nil {
		return s.AfterCrewRideWrite()
	}
	return nil
}

// applyCrewUpdate writes one ease or shortening, keeping an indoor session
// indoors and recording why an eased session was eased.
func (s *Server) applyCrewUpdate(ctx context.Context, wk workout.Workout, profile workout.RiderProfile, c crewplan.Change, j *goingJournal) error {
	if c.Update == nil {
		return errors.New("crew ride change carries nothing to write")
	}
	req := *c.Update
	zone := wk.Zone
	if req.Zone != nil {
		zone = *req.Zone
	}
	s.keepIndoor(&req, wk, profile, zone)
	j.updated[wk.ID] = wk
	if _, err := s.Training.UpdateWorkout(ctx, wk.ID, req); err != nil {
		return err
	}
	if c.Op == crewplan.OpEase {
		rule := why.CrewRideEve
		if c.After {
			rule = why.CrewRideAfter
		}
		s.recordAdjustment(ctx, wk.Rider, wk.ID,
			why.NewRecord(rule, c.Reason, why.CrewRideInputs{RideDate: c.RideDate, Kind: c.RideKind}),
			req.Indoor != nil && *req.Indoor)
	}
	return nil
}

// keepAroundCrewRide writes the kept marker into a session whose ease the rider
// unticked, once.
func (s *Server) keepAroundCrewRide(ctx context.Context, wk workout.Workout, j *goingJournal) error {
	if wk.ID == "" || strings.Contains(wk.Description, crewplan.KeptMarker) {
		return nil
	}
	description := strings.TrimRight(wk.Description, " \n") + " " + crewplan.KeptMarker
	j.updated[wk.ID] = wk
	_, err := s.Training.UpdateWorkout(ctx, wk.ID, workout.UpdateWorkoutRequest{Description: &description})
	return err
}

// rollBackGoing puts back what a failed apply wrote: the sessions it deleted are
// made again, the ones it updated are restored, and the fixed session and going
// row are removed again. The stores take no shared transaction, so this
// compensates; it does not commit atomically.
//
// Two things a compensation cannot give back: a re-made session has a new id and
// a fresh updated_at (so it no longer reads as "untouched" to the season refresh),
// and its copy on a Garmin account, which was deleted with the original, is not
// re-sent until the next push.
func (s *Server) rollBackGoing(ctx context.Context, in goingInput, j *goingJournal) {
	log := func(what string, err error) {
		if err != nil {
			s.logger().Error("could not put a crew ride change back after a failed apply", "rider", in.rider, "ride", in.ride.ID, "step", what, "err", err)
		}
	}
	for i := len(j.removed) - 1; i >= 0; i-- {
		o := j.removed[i]
		made, err := s.Training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
			Rider: o.Rider, Sport: o.Sport, Name: o.Name, GoalID: o.GoalID, Date: o.Date,
			Description: o.Description, Steps: o.Steps, Zone: o.Zone, Level: o.Level,
			TestProtocol: o.TestProtocol, CrewRideID: o.CrewRideID,
		})
		log("restore a deleted session", err)
		// An indoor session stays indoor, with the outdoor steps it reverts to.
		if err == nil && o.Indoor {
			var outdoor []workout.WorkoutStep
			if o.OutdoorSteps != nil {
				outdoor = append([]workout.WorkoutStep{}, *o.OutdoorSteps...)
			}
			yes := true
			_, err = s.Training.UpdateWorkout(ctx, made.ID, workout.UpdateWorkoutRequest{Indoor: &yes, OutdoorSteps: &outdoor})
			log("restore a deleted session's indoor state", err)
		}
	}
	ids := make([]string, 0, len(j.updated))
	for id := range j.updated {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		o := j.updated[id]
		var outdoor []workout.WorkoutStep
		if o.OutdoorSteps != nil {
			outdoor = append([]workout.WorkoutStep{}, *o.OutdoorSteps...)
		}
		_, err := s.Training.UpdateWorkout(ctx, id, workout.UpdateWorkoutRequest{
			Name: &o.Name, Steps: &o.Steps, Zone: &o.Zone, Level: &o.Level, Description: &o.Description,
			Indoor: &o.Indoor, OutdoorSteps: &outdoor,
		})
		log("restore an updated session", err)
	}
	for _, id := range j.added {
		log("remove a session added for the freed day", s.Training.DeleteWorkout(ctx, id))
	}
	if j.wentIn {
		log("remove the going row", s.Schedule.Leave(ctx, in.ride.ID, in.rider))
	}
	if j.created != "" {
		log("remove the fixed session", s.Training.DeleteWorkout(ctx, j.created))
	}
}

// crewDiffDTOFrom is the wire shape of a preview.
func crewDiffDTOFrom(d crewplan.Diff) crewDiffDTO {
	out := emptyCrewDiff()
	for _, c := range d.Changes {
		out.Changes = append(out.Changes, crewChangeDTO{
			ID: c.ID, Op: c.Op, WorkoutID: c.WorkoutID, Date: c.Date, Name: c.Name, Reason: c.Reason,
		})
	}
	for _, n := range d.LeftAlone {
		out.LeftAlone = append(out.LeftAlone, crewLeftAloneDTO{WorkoutID: n.WorkoutID, Date: n.Date, Name: n.Name, Reason: n.Reason})
	}
	out.Warnings = append(out.Warnings, d.Warnings...)
	return out
}

// easeAroundCrewRides is the automatic half of the same rule: after every
// schedule and replan the day before a crew ride, and the day after a long one,
// is eased, through the function the preview uses, so the two cannot disagree.
// It runs inside adaptRider, only touches generated, unadjusted sessions and
// writes the adjusted marker, so a session is changed automatically at most once.
// alreadyChanged is the ids this pass has already changed.
func (s *Server) easeAroundCrewRides(ctx context.Context, rider string, workouts []workout.Workout, profile workout.RiderProfile, alreadyChanged map[string]bool) {
	var any bool
	for _, w := range workouts {
		if w.CrewRideID != "" {
			any = true
			break
		}
	}
	if !any {
		return
	}
	today := s.now().Format(dateLayout)
	ridden, err := s.riddenToday(ctx, rider, today, workouts)
	if err != nil {
		s.logger().Warn("adapt: could not tell what was ridden today, crew ride easing was skipped", "rider", rider, "err", err)
		return
	}
	byID := make(map[string]workout.Workout, len(workouts))
	for _, w := range workouts {
		byID[w.ID] = w
	}
	for _, c := range crewplan.Eases(workouts, profile, ridden, today, "") {
		if alreadyChanged[c.WorkoutID] {
			continue
		}
		wk := byID[c.WorkoutID]
		if err := s.applyCrewUpdate(ctx, wk, profile, c, &goingJournal{updated: map[string]workout.Workout{}}); err != nil {
			s.logger().Warn("adapt: could not ease around a crew ride", "workout", wk.ID, "rider", rider, "err", err)
			continue
		}
		alreadyChanged[c.WorkoutID] = true
		s.logger().Info("workout adapted automatically", "workout", wk.ID, "rider", rider, "change", "eased around a crew ride")
	}
}
