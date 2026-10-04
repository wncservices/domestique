package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/lifeevents"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Life events: a stored stretch of days (travel, illness, busy) and the plan
// changes it implies. The rules are internal/lifeevents; this file is the HTTP
// shape and the one place a diff is written.
//
// Three promises hold everywhere here:
//
//   - the rider comes from the session, never the body, and another rider's
//     event is a 404;
//   - the server computes the diff itself, always: a preview is only ever a
//     picture of it, the client never supplies one, and applying recomputes it,
//     so a plan that changed in between (the tick, another tab) is not
//     half-applied from a stale picture;
//   - applying runs under the scheduling advisory lock Replan uses.
//
// The rider's free-text note is stored for them and never logged.

// DTOs, mirrored by hand in apps/web/src/api/types.ts — change them together.

type lifeEventDTO struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Option    string `json:"option,omitempty"`
	Note      string `json:"note,omitempty"`
}

func lifeEventDTOFrom(e lifeevents.Event) lifeEventDTO {
	return lifeEventDTO{ID: e.ID, Kind: e.Kind, StartDate: e.Start, EndDate: e.End, Option: e.Option, Note: e.Note}
}

type lifeChangeDTO struct {
	ID        string `json:"id"`
	Op        string `json:"op"`
	WorkoutID string `json:"workoutId,omitempty"`
	Date      string `json:"date"`
	ToDate    string `json:"toDate,omitempty"`
	Name      string `json:"name"`
	Kind      string `json:"kind,omitempty"`
	Reason    string `json:"reason"`
	// Default is whether the preview ticks it.
	Default bool `json:"default"`
}

type lifeLeftAloneDTO struct {
	WorkoutID string `json:"workoutId"`
	Date      string `json:"date"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
}

type lifeDiffDTO struct {
	Changes   []lifeChangeDTO    `json:"changes"`
	LeftAlone []lifeLeftAloneDTO `json:"leftAlone"`
	Advice    []string           `json:"advice"`
}

func lifeDiffDTOFrom(d lifeevents.Diff) lifeDiffDTO {
	out := lifeDiffDTO{Changes: []lifeChangeDTO{}, LeftAlone: []lifeLeftAloneDTO{}, Advice: []string{}}
	for _, c := range d.Changes {
		out.Changes = append(out.Changes, lifeChangeDTO{
			ID: c.ID, Op: c.Op, WorkoutID: c.WorkoutID, Date: c.Date, ToDate: c.ToDate,
			Name: c.Name, Kind: c.Kind, Reason: c.Reason, Default: c.Default,
		})
	}
	for _, n := range d.LeftAlone {
		out.LeftAlone = append(out.LeftAlone, lifeLeftAloneDTO{WorkoutID: n.WorkoutID, Date: n.Date, Name: n.Name, Reason: n.Reason})
	}
	out.Advice = append(out.Advice, d.Advice...)
	return out
}

type lifeAppliedDTO struct {
	Removed   int `json:"removed"`
	Moved     int `json:"moved"`
	Eased     int `json:"eased"`
	Shortened int `json:"shortened"`
	Indoor    int `json:"indoor"`
	Added     int `json:"added"`
}

type lifeResultDTO struct {
	Event   *lifeEventDTO   `json:"event,omitempty"`
	Diff    lifeDiffDTO     `json:"diff"`
	Applied *lifeAppliedDTO `json:"applied,omitempty"`
}

// lifeEventBody is the request of a create or an edit. Skip names changes of
// the recomputed diff the rider unticked; Include names the opt-in changes
// (the removal of a session they built) they ticked. A change that is neither
// ticked by default nor included is never applied.
type lifeEventBody struct {
	Kind      string   `json:"kind"`
	StartDate string   `json:"startDate"`
	EndDate   string   `json:"endDate"`
	Option    string   `json:"option"`
	Note      string   `json:"note"`
	DryRun    bool     `json:"dryRun"`
	Skip      []string `json:"skip"`
	Include   []string `json:"include"`
}

const maxLifeEventBodyBytes = 1 << 14

func (b lifeEventBody) event(rider string) lifeevents.Event {
	return lifeevents.Event{Rider: rider, Kind: b.Kind, Start: b.StartDate, End: b.EndDate, Option: b.Option, Note: b.Note}
}

func setOf(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// ---------- reads ----------

// handleListLifeEvents is GET /api/training/life-events: the rider's events
// that end a week ago or later.
func (s *Server) handleListLifeEvents(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	from := s.now().AddDate(0, 0, -lifeevents.MaxBackDays).Format(dateLayout)
	events, err := s.Training.ListLifeEvents(r.Context(), rider, from)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := struct {
		Events []lifeEventDTO `json:"events"`
	}{Events: []lifeEventDTO{}}
	for _, e := range events {
		out.Events = append(out.Events, lifeEventDTOFrom(e))
	}
	writeJSON(w, http.StatusOK, out)
}

// lifeEventsInWeek is the events overlapping start..start+6, for the week the
// Plan page draws.
func (s *Server) lifeEventsInWeek(ctx context.Context, rider string, start time.Time) ([]lifeEventDTO, error) {
	from := start.Format(dateLayout)
	to := start.AddDate(0, 0, 6).Format(dateLayout)
	events, err := s.Training.ListLifeEvents(ctx, rider, from)
	if err != nil {
		return nil, err
	}
	var out []lifeEventDTO
	for _, e := range events {
		if e.Start <= to {
			out = append(out, lifeEventDTOFrom(e))
		}
	}
	return out, nil
}

// ---------- the one pipeline ----------

// lifeVerb is what a request does to the events.
type lifeVerb int

const (
	lifeCreate lifeVerb = iota
	lifeUpdate
	lifeDelete
)

type lifeOp struct {
	verb          lifeVerb
	id            string
	cand          lifeevents.Event
	dryRun        bool
	skip, include map[string]bool
}

// lifeOutcome is the result of one op: a status and message on refusal, else
// the event (nil for a delete), the diff and, when it was applied, the counts.
type lifeOutcome struct {
	status  int
	message string
	err     error
	event   *lifeevents.Event
	diff    lifeevents.Diff
	applied *lifeAppliedDTO
}

func refuse(status int, err error) lifeOutcome {
	return lifeOutcome{status: status, message: err.Error()}
}

// doLifeOp validates the change, computes the diff from the plan as it stands
// now and, unless it is a dry run, writes the event and applies the diff. A
// non-dry run must be called under the scheduling lock.
func (s *Server) doLifeOp(ctx context.Context, rider string, op lifeOp) lifeOutcome {
	now := s.now()
	current, err := s.lifeEventsFor(ctx, rider)
	if err != nil {
		return lifeOutcome{err: err}
	}

	var old lifeevents.Event
	if op.verb != lifeCreate {
		if old, err = s.Training.GetLifeEvent(ctx, rider, op.id); err != nil {
			if errors.Is(err, workout.ErrLifeEventNotFound) {
				return lifeOutcome{status: http.StatusNotFound, message: err.Error()}
			}
			return lifeOutcome{err: err}
		}
	}

	var others []lifeevents.Event
	for _, e := range current {
		if e.ID != old.ID || op.verb == lifeCreate {
			others = append(others, e)
		}
	}
	// current may predate old (an event older than the lookback); previous
	// always holds it for an edit or a delete.
	previous := append([]lifeevents.Event(nil), others...)
	if op.verb != lifeCreate {
		previous = append(previous, old)
	}
	var next []lifeevents.Event
	cand := op.cand
	switch op.verb {
	case lifeCreate:
		cand = lifeevents.Normalize(cand)
		if err := lifeevents.Validate(cand, now); err != nil {
			return refuse(http.StatusBadRequest, err)
		}
	case lifeUpdate:
		cand = lifeevents.Normalize(cand)
		cand.ID = old.ID
		if err := lifeevents.ValidateUpdate(old, cand, now); err != nil {
			return refuse(http.StatusBadRequest, err)
		}
	}
	if op.verb != lifeDelete {
		if err := lifeevents.ValidateCaps(cand, lifeCaps); err != nil {
			return refuse(http.StatusBadRequest, err)
		}
		if err := lifeevents.CheckOverlap(cand, others); err != nil {
			return refuse(http.StatusConflict, err)
		}
		next = append(append(next, others...), cand)
	} else {
		next = others
	}

	plan, err := s.planLife(ctx, rider, previous, next)
	if err != nil {
		return lifeOutcome{err: err}
	}
	if op.dryRun {
		return lifeOutcome{diff: plan.diff}
	}

	// The event first: the blackout is what keeps a removal removed, so it must
	// exist before anything is removed. A delete goes the other way round (see
	// below): its diff only adds, and adding does not need the blackout.
	var saved *lifeevents.Event
	switch op.verb {
	case lifeCreate:
		e, err := s.Training.CreateLifeEvent(ctx, cand)
		if err != nil {
			return lifeOutcome{err: err}
		}
		saved = &e
	case lifeUpdate:
		e, err := s.Training.UpdateLifeEvent(ctx, rider, old.ID, cand)
		if err != nil {
			return lifeOutcome{err: err}
		}
		saved = &e
	}
	var applied lifeAppliedDTO
	err = s.afterLifeEventSaved()
	if err == nil {
		applied, err = s.applyLifeDiff(ctx, rider, plan, op.skip, op.include)
	}
	if err != nil {
		// The workout writes made so far stay (they are the diff, and a retry
		// recomputes it and skips what is already done), but the event row is put
		// back as it was, so a retry sees the same preview the rider confirmed
		// instead of an event that already exists. The stores take no shared
		// transaction, so this compensates; it does not commit atomically.
		s.rollBackLifeEvent(ctx, rider, op, old, saved)
		return lifeOutcome{err: err}
	}
	if op.verb == lifeDelete {
		if err := s.Training.DeleteLifeEvent(ctx, rider, old.ID); err != nil {
			return lifeOutcome{err: err}
		}
	}
	// What follows a plan change: the day-before-a-test easing follows a test
	// to its new day, and the return ramp reaches sessions this diff did not.
	s.adaptRider(ctx, rider)
	return lifeOutcome{event: saved, diff: plan.diff, applied: &applied}
}

// lifePlan is a computed diff with what applying it needs.
type lifePlan struct {
	diff    lifeevents.Diff
	byID    map[string]workout.Workout
	profile workout.RiderProfile
}

// planLife computes the diff of going from previous to next, from the plan as
// it stands now.
func (s *Server) planLife(ctx context.Context, rider string, previous, next []lifeevents.Event) (lifePlan, error) {
	now := s.now()
	today := now.Format(dateLayout)
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return lifePlan{}, err
	}
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		return lifePlan{}, err
	}
	ridden, err := s.riddenToday(ctx, rider, today, workouts)
	if err != nil {
		return lifePlan{}, err
	}
	refill, err := s.refillRequests(ctx, rider, lifeevents.FreedDays(previous, next, today))
	if err != nil {
		return lifePlan{}, err
	}
	byID := make(map[string]workout.Workout, len(workouts))
	for _, w := range workouts {
		byID[w.ID] = w
	}
	diff := lifeevents.Preview(lifeevents.Input{
		Events: next, Previous: previous, Workouts: workouts, Ridden: ridden, Profile: profile,
		Refill: refill, Now: now, Caps: lifeCaps,
	})
	return lifePlan{diff: diff, byID: byID, profile: profile}, nil
}

// refillRequests is what the plan would make for the weeks the freed days fall
// in, the same sessions the fill-once season built them from: scheduler's own
// WeekWorkouts. Preview picks the ones that fit.
func (s *Server) refillRequests(ctx context.Context, rider string, freed []string) ([]workout.CreateWorkoutRequest, error) {
	if len(freed) == 0 {
		return nil, nil
	}
	weeks := map[string]bool{}
	for _, d := range freed {
		t, err := time.Parse(dateLayout, d)
		if err != nil {
			continue
		}
		weeks[periodization.MondayOf(t).Format(dateLayout)] = true
	}
	goals, err := s.Training.ListAllGoals(ctx)
	if err != nil {
		return nil, err
	}
	// The rider's crew rides are fixed in their weeks: a refill built without
	// them would put a second long ride into a week a crew ride already holds.
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return nil, err
	}
	var out []workout.CreateWorkoutRequest
	for _, g := range goals {
		if g.Rider != rider {
			continue
		}
		sc, err := s.seasonContext(ctx, g)
		if err != nil {
			if errors.Is(err, periodization.ErrNoEventDate) || errors.Is(err, periodization.ErrEventInThePast) {
				continue
			}
			return nil, err
		}
		starts := make([]string, 0, len(weeks))
		for k := range weeks {
			starts = append(starts, k)
		}
		sort.Strings(starts)
		for _, start := range starts {
			week, ok := sc.week(start)
			if !ok {
				continue
			}
			reqs, err := scheduler.WeekWorkouts(week, sc.profile, sc.levels, rider, g.ID, g.Sport, fixedOption(workouts, week))
			if err != nil {
				return nil, err
			}
			out = append(out, reqs...)
		}
	}
	return out, nil
}

// applyLifeDiff writes the changes the rider ticked. A change that names a
// session that has gone is not in the recomputed diff, so there is nothing to
// ignore: the diff is built from what exists now.
func (s *Server) applyLifeDiff(ctx context.Context, rider string, plan lifePlan, skip, include map[string]bool) (lifeAppliedDTO, error) {
	var n lifeAppliedDTO
	for _, c := range plan.diff.Changes {
		wanted := (c.Default && !skip[c.ID]) || (!c.Default && include[c.ID])
		if !wanted {
			// A skipped ease or shortening is the rider keeping the session as it
			// is. Mark it, or the return ramp (which runs after every plan change,
			// and on every tick) would simply do it again.
			if c.Default && skip[c.ID] && (c.Op == lifeevents.OpEase || c.Op == lifeevents.OpShort) {
				if err := s.keepThroughReturn(ctx, plan.byID[c.WorkoutID]); err != nil {
					return n, err
				}
			}
			continue
		}
		wk := plan.byID[c.WorkoutID]
		switch c.Op {
		case lifeevents.OpRemove:
			s.removeWorkoutFromGarmin(ctx, wk)
			if err := s.Training.DeleteWorkout(ctx, wk.ID); err != nil && !errors.Is(err, workout.ErrWorkoutNotFound) {
				return n, err
			}
			n.Removed++
		case lifeevents.OpMove:
			// Its Garmin copy stays: a moved session is the same session, and the
			// next push moves its calendar entry like any date change. A life
			// event sends nothing to an account itself.
			if err := s.applyLifeUpdate(ctx, wk, plan.profile, c); err != nil {
				return n, err
			}
			n.Moved++
		case lifeevents.OpEase, lifeevents.OpShort, lifeevents.OpIndoor:
			if err := s.applyLifeUpdate(ctx, wk, plan.profile, c); err != nil {
				return n, err
			}
			switch c.Op {
			case lifeevents.OpEase:
				n.Eased++
			case lifeevents.OpShort:
				n.Shortened++
			default:
				n.Indoor++
			}
		case lifeevents.OpAdd:
			if c.Create == nil {
				continue
			}
			if _, err := s.Training.CreateWorkout(ctx, *c.Create); err != nil {
				return n, err
			}
			n.Added++
		}
	}
	return n, nil
}

func (s *Server) afterLifeEventSaved() error {
	if s.AfterLifeEventSaved != nil {
		return s.AfterLifeEventSaved()
	}
	return nil
}

// rollBackLifeEvent undoes the event write of a failed apply: a created event is
// deleted, an edited one restored. A delete never wrote before applying.
func (s *Server) rollBackLifeEvent(ctx context.Context, rider string, op lifeOp, old lifeevents.Event, saved *lifeevents.Event) {
	var err error
	switch op.verb {
	case lifeCreate:
		if saved != nil {
			err = s.Training.DeleteLifeEvent(ctx, rider, saved.ID)
		}
	case lifeUpdate:
		_, err = s.Training.UpdateLifeEvent(ctx, rider, old.ID, old)
	}
	if err != nil {
		s.logger().Error("could not put a life event back after a failed apply", "rider", rider, "err", err)
	}
}

// keepThroughReturn writes the kept marker into a session whose ease or
// shortening the rider unticked, once.
func (s *Server) keepThroughReturn(ctx context.Context, wk workout.Workout) error {
	if wk.ID == "" || strings.Contains(wk.Description, lifeevents.KeptMarker) {
		return nil
	}
	description := strings.TrimRight(wk.Description, " \n") + " " + lifeevents.KeptMarker
	_, err := s.Training.UpdateWorkout(ctx, wk.ID, workout.UpdateWorkoutRequest{Description: &description})
	return err
}

// ---------- handlers ----------

func (s *Server) handleCreateLifeEvent(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	var body lifeEventBody
	if !decodeLifeBody(w, r, &body) {
		return
	}
	s.runLifeOp(w, r, rider, lifeOp{
		verb: lifeCreate, cand: body.event(rider), dryRun: body.DryRun,
		skip: setOf(body.Skip), include: setOf(body.Include),
	}, http.StatusCreated)
}

func (s *Server) handleUpdateLifeEvent(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	var body lifeEventBody
	if !decodeLifeBody(w, r, &body) {
		return
	}
	op := lifeOp{
		verb: lifeUpdate, id: r.PathValue("id"), cand: body.event(rider), dryRun: body.DryRun,
		skip: setOf(body.Skip), include: setOf(body.Include),
	}
	// An end before the start is how "I'm back" on a one-day event, or an edit
	// that empties it, says it is over: the event goes.
	if body.EndDate != "" && body.StartDate != "" && body.EndDate < body.StartDate {
		op.verb = lifeDelete
	}
	s.runLifeOp(w, r, rider, op, http.StatusOK)
}

func (s *Server) handleDeleteLifeEvent(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	var body lifeEventBody
	// The body is optional on a delete: only the skip and include lists use it.
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLifeEventBodyBytes)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
	}
	dry := r.URL.Query().Get("dryRun") == "1" || body.DryRun
	s.runLifeOp(w, r, rider, lifeOp{
		verb: lifeDelete, id: r.PathValue("id"), dryRun: dry,
		skip: setOf(body.Skip), include: setOf(body.Include),
	}, http.StatusOK)
}

func decodeLifeBody(w http.ResponseWriter, r *http.Request, body *lifeEventBody) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLifeEventBodyBytes)).Decode(body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return false
	}
	return true
}

// runLifeOp runs op, under the scheduling lock unless it only previews, and
// writes the response.
func (s *Server) runLifeOp(w http.ResponseWriter, r *http.Request, rider string, op lifeOp, created int) {
	ctx := r.Context()
	var out lifeOutcome
	if op.dryRun {
		out = s.doLifeOp(ctx, rider, op)
	} else {
		ran := withDBLock(ctx, s.dbConn(), autoScheduleLockKey, func() { out = s.doLifeOp(ctx, rider, op) })
		if !ran {
			// The same collision, and the same wording, as Replan's.
			s.writeReplanLocked(w, rider)
			return
		}
	}
	switch {
	case out.err != nil:
		// A write that should have landed did not.
		s.logger().Error("life event failed", "rider", rider, "kind", op.cand.Kind, "err", out.err)
		s.fail(w, out.err)
		return
	case out.status != 0:
		s.logger().Info("life event refused", "rider", rider, "kind", op.cand.Kind, "status", out.status)
		writeJSON(w, out.status, map[string]string{"error": out.message})
		return
	}

	res := lifeResultDTO{Diff: lifeDiffDTOFrom(out.diff), Applied: out.applied}
	if out.event != nil {
		dto := lifeEventDTOFrom(*out.event)
		res.Event = &dto
	}
	if out.applied != nil {
		// Counts and the kind only: never the note, never a date next to a name.
		kind := op.cand.Kind
		if op.verb == lifeDelete {
			kind = "deleted"
		}
		s.logger().Info("life event applied", "rider", rider, "kind", kind, "verb", int(op.verb),
			"removed", out.applied.Removed, "moved", out.applied.Moved, "eased", out.applied.Eased,
			"shortened", out.applied.Shortened, "indoor", out.applied.Indoor, "added", out.applied.Added)
	}
	status := http.StatusOK
	if op.verb == lifeCreate && !op.dryRun {
		status = created
	}
	writeJSON(w, status, res)
}
