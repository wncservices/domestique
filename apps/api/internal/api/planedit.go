package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wncservices/domestique/apps/api/internal/alternates"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/indoor"
	"github.com/wncservices/domestique/apps/api/internal/lifeevents"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/narration"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Natural-language plan edits: one typed sentence becomes a preview.
//
// The model proposes and this file disposes. Every field of every intent it
// returns is validated here, by the same rules as the form (lifeevents.Validate
// and Preview for an event, lifeevents.CheckMove for a move, the alternates and
// indoor rules for a swap or a conversion); an intent that fails is dropped
// with a visible reason, never repaired. Nothing is written: the response is a
// diff for the rider to confirm, and confirming is the ordinary apply call of
// each kind, which validates again. There is no path from model output to a
// stored change.
//
// The typed sentence is never logged or stored: the log lines here carry the
// rider, an outcome word and counts.

const (
	// maxPlanEditRunes bounds the typed sentence.
	maxPlanEditRunes = 300
	// planEditWindowDays is how many days of the plan the model sees, and
	// planEditHorizonDays how far ahead it may pick a date.
	planEditWindowDays  = 14
	planEditHorizonDays = 60

	planEditNoKeyMessage     = "this deployment has no narration configured"
	planEditConfusedMessage  = "I couldn't understand that — use the Life event form instead."
	planEditUnavailableMsg   = "could not reach the narration service"
	planEditSessionGoneNote  = "that session is not in the next two weeks of your plan"
	planEditNotPlanMadeNote  = "only a session the plan made can be changed this way"
	planEditRiddenNote       = "you have already ridden it"
	planEditFTPTestNote      = "an FTP test can be moved, not swapped or converted"
	planEditOutOfRangeFormat = "dates must be between today and %s"
)

// Mirrored by hand in apps/web/src/api/types.ts.

type planEditIntentDTO struct {
	Type      string `json:"type"`
	Kind      string `json:"kind,omitempty"`
	StartDate string `json:"startDate,omitempty"`
	EndDate   string `json:"endDate,omitempty"`
	Option    string `json:"option,omitempty"`
	// WorkoutID is the real session id the model's opaque handle resolved to,
	// for the apply call. The model never saw it.
	WorkoutID string `json:"workoutId,omitempty"`
	ToDate    string `json:"toDate,omitempty"`
	Alternate string `json:"alternate,omitempty"`
}

type planEditItemDTO struct {
	Intent planEditIntentDTO `json:"intent"`
	Diff   lifeDiffDTO       `json:"diff"`
}

type planEditDroppedDTO struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

type planEditResponseDTO struct {
	Items       []planEditItemDTO    `json:"items"`
	Dropped     []planEditDroppedDTO `json:"dropped"`
	Unsupported string               `json:"unsupported,omitempty"`
}

var planEditCaps = narration.Capabilities{SwapAlternate: true, ConvertIndoor: indoorAvailable}

// handleProposePlanEdit is POST /api/training/plan/propose-edit {text}.
func (s *Server) handleProposePlanEdit(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	if s.Narration == nil {
		s.logger().Warn("plan edit requested but no ANTHROPIC_API_KEY is configured", "rider", rider)
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": planEditNoKeyMessage})
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" || utf8.RuneCountInString(text) > maxPlanEditRunes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("write between 1 and %d characters", maxPlanEditRunes)})
		return
	}
	today, ok := parseTodayParam(w, r, s.now())
	if !ok {
		return
	}
	if !s.rateLimitNarration(w, rider) {
		return
	}

	ctx := r.Context()
	view, err := s.buildPlanView(ctx, rider, today)
	if err != nil {
		s.fail(w, err)
		return
	}
	edits, err := s.Narration.ProposePlanEdits(ctx, text, view.view)
	if err != nil {
		outcome := "model_error"
		switch {
		case errors.Is(err, narration.ErrRejected):
			outcome = "rejected"
			s.logger().Warn("plan edit proposal rejected", "rider", rider, "outcome", outcome)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": planEditConfusedMessage})
			return
		case errors.Is(err, narration.ErrNoToolCall):
			outcome = "no_tool_call"
		case errors.Is(err, context.DeadlineExceeded):
			outcome = "timeout"
		}
		// The outcome word only: an error can carry a fragment of the request.
		s.logger().Warn("plan edit proposal failed", "rider", rider, "outcome", outcome)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": planEditUnavailableMsg})
		return
	}

	out, err := s.validatePlanEdits(ctx, rider, today, view, edits)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("plan edit proposed", "rider", rider, "intents", len(edits.Intents),
		"kept", len(out.Items), "dropped", len(out.Dropped))
	writeJSON(w, http.StatusOK, out)
}

// planView is the model's view and the key back from its handles.
type planView struct {
	view    narration.PlanView
	handles map[string]workout.Workout
	// workouts, events and profile are what validation reads, fetched once.
	workouts []workout.Workout
	events   []lifeevents.Event
	profile  workout.RiderProfile
	ridden   map[string]bool
}

// buildPlanView is the next 14 days of the rider's plan, each session under an
// opaque handle, in date order. It copies no id, no name the rider typed, and
// nothing about the rider.
func (s *Server) buildPlanView(ctx context.Context, rider string, today time.Time) (planView, error) {
	todayStr := today.Format(dateLayout)
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return planView{}, err
	}
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		return planView{}, err
	}
	events, err := s.lifeEventsFor(ctx, rider)
	if err != nil {
		return planView{}, err
	}
	ridden, err := s.riddenToday(ctx, rider, todayStr, workouts)
	if err != nil {
		return planView{}, err
	}

	sort.SliceStable(workouts, func(i, j int) bool {
		if workouts[i].Date != workouts[j].Date {
			return workouts[i].Date < workouts[j].Date
		}
		return workouts[i].ID < workouts[j].ID
	})
	pv := planView{
		handles: map[string]workout.Workout{}, workouts: workouts, events: events, profile: profile, ridden: ridden,
		view: narration.PlanView{
			Today: todayStr, AvailableDays: append([]string(nil), profile.AvailableDays...),
			Capabilities: planEditCaps,
		},
	}
	byDate := map[string][]narration.ViewSession{}
	n := 0
	lastDay := today.AddDate(0, 0, planEditWindowDays-1).Format(dateLayout)
	for _, wk := range workouts {
		if wk.Date < todayStr || wk.Date > lastDay {
			continue
		}
		n++
		handle := fmt.Sprintf("w%d", n)
		pv.handles[handle] = wk
		vs := narration.ViewSession{
			Handle: handle, Sport: string(wk.Sport), Zone: string(wk.Zone),
			Minutes: int(workout.PlannedSeconds(wk.Steps)/60 + 0.5),
			FTPTest: wk.TestProtocol != "", Indoor: wk.Indoor, Ridden: ridden[wk.ID],
			RiderBuilt: !isPlanMade(wk) && wk.TestProtocol == "",
		}
		if !vs.RiderBuilt {
			// The generated name; a name the rider typed is theirs.
			vs.Name = wk.Name
		}
		byDate[wk.Date] = append(byDate[wk.Date], vs)
	}
	for i := 0; i < planEditWindowDays; i++ {
		d := today.AddDate(0, 0, i).Format(dateLayout)
		pv.view.Days = append(pv.view.Days, narration.ViewDay{Date: d, Sessions: byDate[d]})
	}
	return pv, nil
}

// validatePlanEdits re-checks every intent, never trusting the model, and turns
// the survivors into diffs by the same code as the form.
func (s *Server) validatePlanEdits(ctx context.Context, rider string, today time.Time, pv planView, edits narration.PlanEdits) (planEditResponseDTO, error) {
	out := planEditResponseDTO{Items: []planEditItemDTO{}, Dropped: []planEditDroppedDTO{}, Unsupported: edits.Unsupported}
	todayStr := today.Format(dateLayout)
	horizon := today.AddDate(0, 0, planEditHorizonDays).Format(dateLayout)
	now := s.now()

	// Events accepted so far in this reply, so two of them cannot overlap.
	events := append([]lifeevents.Event(nil), pv.events...)

	drop := func(in narration.Intent, reason string) {
		out.Dropped = append(out.Dropped, planEditDroppedDTO{Type: in.Type, Reason: capitalizeFirst(reason) + "."})
	}
	inRange := func(date string) bool { return date >= todayStr && date <= horizon }

	for _, in := range edits.Intents {
		switch in.Type {
		case narration.IntentCreateLifeEvent:
			e := lifeevents.Event{Rider: rider, Kind: in.Kind, Start: in.StartDate, End: in.EndDate, Option: in.Option}
			if err := lifeevents.Validate(e, now); err != nil {
				drop(in, err.Error())
				continue
			}
			if err := lifeevents.ValidateCaps(e, lifeCaps); err != nil {
				drop(in, err.Error())
				continue
			}
			if e.Start > horizon {
				drop(in, fmt.Sprintf(planEditOutOfRangeFormat, horizon))
				continue
			}
			if err := lifeevents.CheckOverlap(e, events); err != nil {
				drop(in, "it overlaps a life event you already have of the same kind")
				continue
			}
			plan, err := s.planLife(ctx, rider, events, append(append([]lifeevents.Event(nil), events...), lifeevents.Normalize(e)))
			if err != nil {
				return out, err
			}
			events = append(events, lifeevents.Normalize(e))
			out.Items = append(out.Items, planEditItemDTO{
				Intent: planEditIntentDTO{Type: in.Type, Kind: e.Kind, StartDate: e.Start, EndDate: e.End, Option: lifeevents.Normalize(e).Option},
				Diff:   lifeDiffDTOFrom(plan.diff),
			})

		case narration.IntentMoveWorkout, narration.IntentSwapAlternate, narration.IntentConvertIndoor:
			wk, why := pv.resolve(in.Workout, in.Type)
			if why != "" {
				drop(in, why)
				continue
			}
			item, why, err := s.singleSessionItem(ctx, rider, today, pv, events, wk, in, inRange, horizon)
			if err != nil {
				return out, err
			}
			if why != "" {
				drop(in, why)
				continue
			}
			out.Items = append(out.Items, item)
		}
	}
	return out, nil
}

// resolve maps a handle back to the session and says why it cannot be used.
func (pv planView) resolve(handle, intentType string) (workout.Workout, string) {
	wk, ok := pv.handles[handle]
	switch {
	case !ok:
		return workout.Workout{}, planEditSessionGoneNote
	case pv.ridden[wk.ID]:
		return workout.Workout{}, planEditRiddenNote
	case wk.TestProtocol != "" && intentType != narration.IntentMoveWorkout:
		return workout.Workout{}, planEditFTPTestNote
	case wk.TestProtocol == "" && !isPlanMade(wk):
		return workout.Workout{}, planEditNotPlanMadeNote
	}
	return wk, ""
}

// singleSessionItem validates one move, swap or conversion and builds its diff
// line. why is non-empty when the intent is dropped.
func (s *Server) singleSessionItem(ctx context.Context, rider string, today time.Time, pv planView, events []lifeevents.Event,
	wk workout.Workout, in narration.Intent, inRange func(string) bool, horizon string) (planEditItemDTO, string, error) {

	item := planEditItemDTO{Intent: planEditIntentDTO{Type: in.Type, WorkoutID: wk.ID}}
	change := lifeevents.Change{WorkoutID: wk.ID, Date: wk.Date, Name: wk.Name, Default: true}

	switch in.Type {
	case narration.IntentMoveWorkout:
		if !inRange(in.ToDate) {
			return item, fmt.Sprintf(planEditOutOfRangeFormat, horizon), nil
		}
		err := lifeevents.CheckMove(lifeevents.Input{
			Events: events, Workouts: pv.workouts, Profile: pv.profile, Now: s.now(),
		}, wk, in.ToDate)
		if err != nil {
			return item, err.Error(), nil
		}
		change.ID, change.Op, change.ToDate = lifeevents.OpMove+":"+wk.ID, lifeevents.OpMove, in.ToDate
		change.Reason = fmt.Sprintf("Move it to %s, as you asked.", dayLabel(in.ToDate))
		item.Intent.ToDate = in.ToDate

	case narration.IntentSwapAlternate:
		kind := alternates.Kind(in.Alternate)
		if !kind.Valid() {
			return item, "that is not an alternate I can offer", nil
		}
		set, err := s.alternatesFor(ctx, wk, today)
		if err != nil {
			return item, "", err
		}
		if set.refusal != "" {
			return item, strings.TrimSuffix(set.refusal, "."), nil
		}
		opt, ok := findAlternate(set.options, kind)
		if !ok {
			return item, "that session has no " + in.Alternate + " version", nil
		}
		minutes := int(workout.PlannedSeconds(shownSteps(wk, opt, set.profile))/60 + 0.5)
		change.ID, change.Op = "swap:"+wk.ID, lifeevents.OpSwap
		change.Reason = fmt.Sprintf("Swap it for the %s version: %s, %d min.", in.Alternate, opt.Name, minutes)
		item.Intent.Alternate = in.Alternate

	case narration.IntentConvertIndoor:
		if !indoorAvailable {
			return item, "indoor conversion is not available here", nil
		}
		if wk.Sport != model.SportCycling {
			return item, strings.TrimSuffix(indoorRunningMessage, "."), nil
		}
		if wk.Indoor {
			return item, "it is already the indoor version", nil
		}
		res, err := indoor.Convert(wk, pv.profile)
		if err != nil {
			return item, strings.TrimSuffix(indoorRunningMessage, "."), nil
		}
		change.ID, change.Op = lifeevents.OpIndoor+":"+wk.ID, lifeevents.OpIndoor
		change.Reason = "Ride it indoors. " + res.Note
	}

	item.Diff = lifeDiffDTOFrom(lifeevents.Diff{Changes: []lifeevents.Change{change}})
	return item, "", nil
}

// dayLabel is "Sat 10 Oct" for a YYYY-MM-DD date.
func dayLabel(date string) string {
	t, err := time.Parse(dateLayout, date)
	if err != nil {
		return date
	}
	return t.Format("Mon 2 Jan")
}

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
