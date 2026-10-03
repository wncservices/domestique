package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/crewplan"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/schedule"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Crew rides as fixed sessions. A rider who says "I'm going" gets a workout in
// their own plan, linked to nothing the crew owns: it points at the ride by id
// and carries its own copy of the estimate, so a ride that is deleted leaves the
// rider's plan as it was. See
// docs/superpowers/specs/2026-09-29-crew-planning-design.md.
//
// Three promises hold everywhere here:
//
//   - the rider comes from the session, never the body: nobody can mark someone
//     else going, and a crew action (delete a ride, remove a member) never
//     writes to a rider's workouts, it only orphans them on read;
//   - the fixed row is never scheduler.IsGenerated, so nothing automatic moves,
//     eases, replaces or deletes it;
//   - applying a join or a leave recomputes everything on the server, under the
//     scheduling advisory lock Replan uses; a preview is only ever a picture.

// crewRideOutdoorMessage is why a crew ride has no trainer version and no
// alternates: the day and the road are the crew's.
const crewRideOutdoorMessage = "A crew ride is ridden outdoors with the group, so it has no indoor version or alternates."

// crewRideNamePrefix starts the name of every fixed session.
const crewRideNamePrefix = "Crew ride: "

// ---------- DTOs ----------
//
// Mirrored by hand in apps/web/src/api/types.ts — change them together.

// crewRideRefDTO is workoutDTO.crewRide: what makes a session a crew ride.
// EstimatedTSS and Kind come from the stored session alone; the rest is filled
// by attachCrewRides, one batched read per response, and is absent when that
// read failed.
type crewRideRefDTO struct {
	RideID       string  `json:"rideId"`
	CrewID       string  `json:"crewId,omitempty"`
	CrewName     string  `json:"crewName,omitempty"`
	RouteName    string  `json:"routeName"`
	Going        bool    `json:"going"`
	EstimatedTSS float64 `json:"estimatedTss"`
	// Kind is long, endurance or short: what the plan treats the ride as.
	Kind string `json:"kind"`
	// GoingNames is who in the crew is going: names only, which is what a ride
	// is for.
	GoingNames []string `json:"goingNames,omitempty"`
	// Orphaned is "cancelled" when the ride was deleted and "left" when the
	// rider is no longer in the crew. The session is the rider's and is never
	// touched by either; they are offered the ordinary leave preview.
	Orphaned string `json:"orphaned,omitempty"`
}

// crewRideStub is the part of crewRideRefDTO the stored session alone gives.
func crewRideStub(w workout.Workout) *crewRideRefDTO {
	planned := workout.PlannedSeconds(w.Steps)
	return &crewRideRefDTO{
		RideID:       w.CrewRideID,
		RouteName:    strings.TrimPrefix(w.Name, crewRideNamePrefix),
		EstimatedTSS: crewplan.TSSForSeconds(planned),
		Kind:         string(crewplan.KindForSeconds(planned)),
	}
}

// crewRideListDTO is one row of GET /api/training/crew-rides: a ride in one of
// the caller's crews, who is going and what it is likely to take.
type crewRideListDTO struct {
	ID        string   `json:"id"`
	CrewID    string   `json:"crewId"`
	CrewName  string   `json:"crewName"`
	Slug      string   `json:"slug"`
	RouteName string   `json:"routeName"`
	Date      string   `json:"date"`
	Time      string   `json:"time,omitempty"`
	Going     []string `json:"going"`
	Mine      bool     `json:"mine"`
	// The estimate is the route's alone (see crewplan.EstimateRoute), so it is
	// the same for every member. Zero, and Kind empty, when the route is gone.
	Km      float64 `json:"km"`
	AscentM float64 `json:"ascentM"`
	Minutes float64 `json:"minutes"`
	TSS     float64 `json:"tss"`
	Kind    string  `json:"kind"`
}

// crewDiffDTO is the picture of what joining or leaving does to the rest of the
// plan: the changes the server would make, what it leaves alone, and warnings.
type crewDiffDTO struct {
	Changes   []crewChangeDTO    `json:"changes"`
	LeftAlone []crewLeftAloneDTO `json:"leftAlone"`
	Warnings  []string           `json:"warnings"`
}

type crewChangeDTO struct {
	ID        string `json:"id"`
	Op        string `json:"op"`
	WorkoutID string `json:"workoutId,omitempty"`
	Date      string `json:"date"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
}

type crewLeftAloneDTO struct {
	WorkoutID string `json:"workoutId"`
	Date      string `json:"date"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
}

type crewAppliedDTO struct {
	Removed   int `json:"removed"`
	Eased     int `json:"eased"`
	Shortened int `json:"shortened"`
	Added     int `json:"added"`
}

// goingResultDTO answers PUT .../going. Workout is the fixed session after a
// join and nil after a leave or a dry run.
type goingResultDTO struct {
	Going   bool            `json:"going"`
	Workout *workoutDTO     `json:"workout,omitempty"`
	Diff    crewDiffDTO     `json:"diff"`
	Applied *crewAppliedDTO `json:"applied,omitempty"`
}

type goingBody struct {
	Going  bool `json:"going"`
	DryRun bool `json:"dryRun"`
	// Skip names changes of the recomputed diff the rider unticked. A name the
	// diff no longer holds is ignored: the diff is built from what exists now.
	Skip []string `json:"skip"`
}

const maxGoingBodyBytes = 1 << 12

// ---------- the list ----------

// handleCrewRides is GET /api/training/crew-rides?from=: the upcoming rides of
// the caller's own crews with who is going and the estimate. from is the
// caller's own idea of today, as handleUpcomingRides takes it.
func (s *Server) handleCrewRides(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	if !s.crewAvailable(w) || !s.scheduleAvailable(w) {
		return
	}
	ctx := r.Context()
	rider := auth.FromContext(ctx).User

	from := r.URL.Query().Get("from")
	if from == "" {
		from = s.now().Format(dateLayout)
	} else if !isCalendarDate(from) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from must be a date in YYYY-MM-DD form"})
		return
	}

	snap, err := s.Crew.Snapshot(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	rides, err := s.Schedule.ListUpcoming(ctx, from)
	if err != nil {
		s.fail(w, err)
		return
	}
	going, err := s.Schedule.GoingFrom(ctx, from)
	if err != nil {
		s.fail(w, err)
		return
	}
	routes, err := s.routesBySlug(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	crewNames := make(map[string]string, len(snap.Crews))
	for _, c := range snap.Crews {
		crewNames[c.ID] = c.Name
	}

	out := make([]crewRideListDTO, 0, len(rides))
	for _, ride := range rides {
		if !snap.ApprovedRiders.Has(ride.CrewID, rider) {
			continue
		}
		dto := crewRideListDTO{
			ID: ride.ID, CrewID: ride.CrewID, CrewName: crewNames[ride.CrewID],
			Slug: ride.Slug, RouteName: ride.Slug, Date: ride.Date, Time: ride.Time,
			Going: orEmpty(going[ride.ID]),
		}
		for _, name := range dto.Going {
			if strings.EqualFold(name, rider) {
				dto.Mine = true
			}
		}
		if route, ok := routes[ride.Slug]; ok {
			e := crewplan.EstimateRoute(route.Stats)
			dto.RouteName = route.Name
			dto.Km, dto.AscentM, dto.Minutes, dto.TSS, dto.Kind = e.Km, e.AscentM, e.Hours*60, e.TSS, string(e.Kind)
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// routesBySlug is every route in the library, by slug.
func (s *Server) routesBySlug(ctx context.Context) (map[string]model.Route, error) {
	routes, _, err := s.Source.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]model.Route, len(routes))
	for _, rt := range routes {
		out[rt.Slug] = rt
	}
	return out, nil
}

// ---------- going ----------

// goingInput is one going or leaving request, resolved.
type goingInput struct {
	ride  schedule.Ride
	crew  crew.Crew
	route model.Route // set only when want
	rider string
	want  bool
	skip  map[string]bool
	// existing is the rider's fixed session for the ride, nil when they have none.
	existing *workout.Workout
}

type goingOutcome struct {
	going   bool
	workout *workout.Workout
	diff    crewDiffDTO
	applied *crewAppliedDTO
	err     error
}

// emptyCrewDiff is the diff of a request that changes nothing around the ride.
func emptyCrewDiff() crewDiffDTO {
	return crewDiffDTO{Changes: []crewChangeDTO{}, LeftAlone: []crewLeftAloneDTO{}, Warnings: []string{}}
}

// rideRoute finds a ride's route in the library.
func (s *Server) rideRoute(ctx context.Context, slug string) (model.Route, bool, error) {
	routes, err := s.routesBySlug(ctx)
	if err != nil {
		return model.Route{}, false, err
	}
	rt, ok := routes[slug]
	return rt, ok, nil
}

// fixedSessionFor is the rider's fixed session for a ride, if they have one.
func (s *Server) fixedSessionFor(ctx context.Context, rider, rideID string) (*workout.Workout, error) {
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return nil, err
	}
	for i := range workouts {
		if workouts[i].CrewRideID == rideID {
			return &workouts[i], nil
		}
	}
	return nil, nil
}

// fixedSessionRequest is the workout a rider gets for going: the ride's date,
// cycling, endurance, one step of the estimated length and no target, linked to
// the focus goal when there is one so the day reads as taken. Its description
// does not start with scheduler.GeneratedDescription, which is what keeps every
// automatic rule off it, and it names no rider.
func (s *Server) fixedSessionRequest(ctx context.Context, in goingInput) (workout.CreateWorkoutRequest, error) {
	est := crewplan.EstimateRoute(in.route.Stats)
	_, focus, _, err := s.focusPlan(ctx, in.rider, s.now())
	if err != nil {
		return workout.CreateWorkoutRequest{}, err
	}
	goalID := ""
	if focus != nil {
		goalID = focus.ID
	}
	return workout.CreateWorkoutRequest{
		Rider: in.rider, Sport: model.SportCycling, GoalID: goalID, Date: in.ride.Date,
		Name:        crewRideNamePrefix + in.route.Name,
		Description: crewRideDescription(in.crew.Name, in.route.Name, est),
		Zone:        workout.ZoneEndurance,
		CrewRideID:  in.ride.ID,
		Steps: []workout.WorkoutStep{{
			Name: "Crew ride", Intensity: workout.IntensityActive, Duration: workout.DurationTime,
			Seconds: est.Seconds(), Target: workout.TargetOpen,
		}},
	}, nil
}

// crewRideDescription is the session's own text. It names the crew and the
// route and carries the estimate; no other rider's name belongs in it.
func crewRideDescription(crewName, routeName string, e crewplan.Estimate) string {
	return fmt.Sprintf("Crew ride with %s: %s, %.0f km, %.0f m, about %s, estimated TSS %.0f.",
		crewName, routeName, e.Km, e.AscentM, aboutDuration(e.Hours), e.TSS)
}

// aboutDuration writes hours the way a rider says them: "4 h 21", "45 min".
func aboutDuration(hours float64) string {
	mins := int(math.Round(hours * 60))
	if mins < 60 {
		return fmt.Sprintf("%d min", mins)
	}
	return fmt.Sprintf("%d h %02d", mins/60, mins%60)
}

// ---------- reading a session as a crew ride ----------

// attachCrewRides completes crewRide on every workout in lists that has one:
// the crew, the route's name, who is going, and whether the session is
// orphaned. One batched read for all of them; a failed read leaves the stub
// (the estimate and the kind come from the stored session) and a Warn, never
// a failed page.
func (s *Server) attachCrewRides(ctx context.Context, rider string, lists ...[]workoutDTO) {
	var wanted bool
	for _, l := range lists {
		for _, d := range l {
			if d.CrewRide != nil {
				wanted = true
			}
		}
	}
	if !wanted || s.Schedule == nil || s.Crew == nil {
		return
	}
	rides, err := s.Schedule.ListUpcoming(ctx, "")
	if err != nil {
		s.logger().Warn("could not read crew rides for the plan", "rider", rider, "err", err)
		return
	}
	snap, err := s.Crew.Snapshot(ctx)
	if err != nil {
		s.logger().Warn("could not read crews for the plan", "rider", rider, "err", err)
		return
	}
	going, err := s.Schedule.GoingFrom(ctx, "")
	if err != nil {
		s.logger().Warn("could not read who is going for the plan", "rider", rider, "err", err)
		return
	}
	routes, err := s.routesBySlug(ctx)
	if err != nil {
		s.logger().Warn("could not read routes for the plan", "rider", rider, "err", err)
		return
	}
	byID := make(map[string]schedule.Ride, len(rides))
	for _, ride := range rides {
		byID[ride.ID] = ride
	}
	crewNames := make(map[string]string, len(snap.Crews))
	for _, c := range snap.Crews {
		crewNames[c.ID] = c.Name
	}

	for _, l := range lists {
		for i := range l {
			ref := l[i].CrewRide
			if ref == nil {
				continue
			}
			ride, ok := byID[ref.RideID]
			if !ok {
				ref.Orphaned = "cancelled"
				continue
			}
			ref.CrewID, ref.CrewName = ride.CrewID, crewNames[ride.CrewID]
			if rt, ok := routes[ride.Slug]; ok {
				ref.RouteName = rt.Name
			}
			names := append([]string(nil), going[ride.ID]...)
			sort.Strings(names)
			ref.GoingNames = names
			for _, n := range names {
				if strings.EqualFold(n, rider) {
					ref.Going = true
				}
			}
			if !snap.ApprovedRiders.Has(ride.CrewID, rider) {
				ref.Orphaned = "left"
			}
		}
	}
}

// fixedOption tells scheduler.WeekWorkouts about the rider's fixed crew rides in
// week: they take their days, reduce the volume and replace the long slot. It
// reads the stored rows, not the crew's ride, so a ride the crew has since
// cancelled still counts until the rider confirms an update.
func fixedOption(existing []workout.Workout, week periodization.Week) scheduler.Option {
	start, end := weekBounds(week)
	var fixed []scheduler.Fixed
	for _, wk := range existing {
		if wk.CrewRideID != "" && wk.Date >= start && wk.Date <= end {
			fixed = append(fixed, scheduler.Fixed{Date: wk.Date, Seconds: workout.PlannedSeconds(wk.Steps)})
		}
	}
	return scheduler.WithFixed(fixed)
}

// isCalendarDate reports whether s is a real YYYY-MM-DD date.
func isCalendarDate(s string) bool {
	d, err := time.Parse(dateLayout, s)
	return err == nil && d.Format(dateLayout) == s
}
