package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/climbs"
	"github.com/wncservices/domestique/apps/api/internal/pacing"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The pacing plan for a route: target watts (and heart rate as a fallback) per
// climb and segment, with the expected time from a steady-state physics model.
// Read-only and made of distances, elevations, watts and times: no latitude or
// longitude is ever returned. Coordinates leave the app only inside the pacing
// FIT, to the rider's own linked Garmin or Wahoo account.

// pacedRoute is a route ridden under a rider's profile: the plan, and the
// intensity it was built at.
type pacedRoute struct {
	plan    pacing.Plan
	phys    pacing.Physics
	ifv     float64
	derived bool
}

// paceRoute builds the pacing plan, the one place demands, the training bias
// and the pacing endpoint get their climb watts from. The intensity factor is
// the goal's override when it has one; otherwise two passes: ride the route at
// 0.85 FTP, take the band of that duration (pacing.EventIF), and build the plan
// at that band's IF. The band is not re-evaluated after the second pass.
func paceRoute(rp *routeProfile, profile workout.RiderProfile, override float64) pacedRoute {
	ph := pacing.DefaultPhysics(profile.WeightKG)
	pr := pacedRoute{phys: ph, ifv: override}
	if pr.ifv == 0 {
		pr.ifv, pr.derived = pacing.DerivedIF(rp.Segs, ph, profile.FTPWatts), true
	}
	pr.plan = pacing.Build(pacing.Input{
		Segs: rp.Segs, Climbs: rp.DeviceClimbs, FTP: profile.FTPWatts, IF: pr.ifv, Phys: ph, Profile: profile,
	})
	return pr
}

type pacingAssumptionsDTO struct {
	MassKG      float64 `json:"massKg"`
	MassAssumed bool    `json:"massAssumed"`
	CdA         float64 `json:"cdA"`
	Crr         float64 `json:"crr"`
	IF          float64 `json:"if"`
	// IFSource is "goal" when the rider set it on the goal, else "derived".
	IFSource string `json:"ifSource"`
	Wind     string `json:"wind"`
}

type pacingTotalsDTO struct {
	Seconds     float64 `json:"seconds"`
	NormalizedW float64 `json:"normalizedW"`
	AvgW        float64 `json:"avgW"`
	IF          float64 `json:"if"`
	AvgKph      float64 `json:"avgKph"`
	VI          float64 `json:"variabilityIndex"`
}

type pacingSegmentDTO struct {
	StartM     float64 `json:"startM"`
	EndM       float64 `json:"endM"`
	Kind       string  `json:"kind"`
	ClimbIndex *int    `json:"climbIndex,omitempty"`
	Gradient   float64 `json:"gradient"`
	WattsLow   int     `json:"wattsLow"`
	WattsHigh  int     `json:"wattsHigh"`
	HRLow      int     `json:"hrLow,omitempty"`
	HRHigh     int     `json:"hrHigh,omitempty"`
	SpeedKph   float64 `json:"speedKph"`
	Seconds    float64 `json:"seconds"`
}

type pacingClimbDTO struct {
	Index       int     `json:"index"`
	StartM      float64 `json:"startM"`
	EndM        float64 `json:"endM"`
	LengthM     float64 `json:"lengthM"`
	AvgGradient float64 `json:"avgGradient"`
	Category    string  `json:"category,omitempty"`
	Watts       float64 `json:"watts"`
	WattsLow    int     `json:"wattsLow"`
	WattsHigh   int     `json:"wattsHigh"`
	HRLow       int     `json:"hrLow,omitempty"`
	HRHigh      int     `json:"hrHigh,omitempty"`
	Seconds     float64 `json:"seconds"`
}

type pacingDTO struct {
	Available   bool                 `json:"available"`
	Route       routeRefDTO          `json:"route"`
	GoalID      string               `json:"goalId,omitempty"`
	EventDate   string               `json:"eventDate,omitempty"`
	Assumptions pacingAssumptionsDTO `json:"assumptions"`
	// Hint is "Assumed 75 kg; add your weight for a better time" when the
	// rider has not given a weight.
	Hint string `json:"hint,omitempty"`
	// HRNote qualifies the heart-rate ranges, when there are any.
	HRNote   string             `json:"hrNote,omitempty"`
	Totals   pacingTotalsDTO    `json:"totals"`
	Segments []pacingSegmentDTO `json:"segments"`
	Climbs   []pacingClimbDTO   `json:"climbs"`
	// Profile is the route's elevation by distance, for the chart.
	Profile []profilePointDTO `json:"profile"`
}

const (
	assumedWeightHint = "Assumed 75 kg; add your weight for a better time."
	hrLagNote         = "Steady-state; HR lags the first minutes of a climb."
)

// errGoalRouteMismatch is the answer when a pacing request names a goal that is
// not linked to the route being paced.
const errGoalRouteMismatch = "that goal is not linked to this route"

var pacingReasons = map[string]string{
	reasonNoElevation: demandReasons[reasonNoElevation],
	reasonNotCycling:  "A pacing plan is for cycling routes.",
	reasonNoFTP:       "Add your FTP to get a pacing plan.",
}

// pacingFor builds the response for a visible route and a rider's profile, or
// an unavailable reason code. Shared by the JSON endpoint and the FIT export,
// so the course a rider downloads carries exactly the targets they were shown.
func (s *Server) pacingFor(rp *routeProfile, profile workout.RiderProfile, goal *workout.Goal) (pacedRoute, pacingDTO, string) {
	if profile.FTPWatts <= 0 {
		return pacedRoute{}, pacingDTO{}, reasonNoFTP
	}
	override := 0.0
	if goal != nil {
		override = goal.PacingIF
	}
	pr := paceRoute(rp, profile, override)
	if pr.plan.Reason != "" {
		return pr, pacingDTO{}, reasonNoFTP
	}

	dto := pacingDTO{
		Available: true,
		Route:     routeRefDTO{Slug: rp.Route.Slug, Name: rp.Route.Name},
		Assumptions: pacingAssumptionsDTO{
			MassKG: pr.phys.MassKg, MassAssumed: profile.WeightKG <= 0, CdA: pr.phys.CdA, Crr: pr.phys.Crr,
			IF: pr.ifv, IFSource: "derived", Wind: "none",
		},
		Totals: pacingTotalsDTO{
			Seconds: math.Round(pr.plan.Seconds), NormalizedW: math.Round(pr.plan.NormalizedW),
			AvgW: math.Round(pr.plan.AvgW), IF: math.Round(pr.plan.IF*100) / 100,
			AvgKph: math.Round(pr.plan.AvgKph*10) / 10, VI: math.Round(pr.plan.VI*100) / 100,
		},
		Segments: make([]pacingSegmentDTO, 0, len(pr.plan.Segments)),
		Climbs:   make([]pacingClimbDTO, 0, len(pr.plan.Climbs)),
		Profile:  rp.Profile,
	}
	if !pr.derived {
		dto.Assumptions.IFSource = "goal"
	}
	if goal != nil {
		dto.GoalID, dto.EventDate = goal.ID, goal.EventDate
	}
	if dto.Assumptions.MassAssumed {
		dto.Hint = assumedWeightHint
	}

	hasHR := false
	for _, sg := range pr.plan.Segments {
		d := pacingSegmentDTO{
			StartM: math.Round(sg.StartM), EndM: math.Round(sg.EndM), Kind: sg.Kind,
			Gradient: math.Round(sg.Gradient*10) / 10, WattsLow: sg.WattsLow, WattsHigh: sg.WattsHigh,
			HRLow: sg.HRLow, HRHigh: sg.HRHigh,
			SpeedKph: math.Round(sg.SpeedKph*10) / 10, Seconds: math.Round(sg.Seconds),
		}
		if sg.Kind == pacing.KindClimb {
			idx := sg.ClimbIndex
			d.ClimbIndex = &idx
		}
		hasHR = hasHR || sg.HRHigh > 0
		dto.Segments = append(dto.Segments, d)
	}
	if hasHR {
		dto.HRNote = hrLagNote
	}
	byIndex := map[int]climbs.Climb{}
	for _, c := range rp.DeviceClimbs {
		byIndex[c.Index] = c
	}
	for _, ct := range pr.plan.Climbs {
		c := pacingClimbDTO{
			Index: ct.Index, StartM: math.Round(ct.StartM), EndM: math.Round(ct.EndM), LengthM: math.Round(ct.LengthM),
			AvgGradient: math.Round(ct.AvgGradient*10) / 10, Watts: math.Round(ct.Watts),
			WattsLow: roundTo5(ct.Watts * 0.97), WattsHigh: roundTo5(ct.Watts * 1.03), Seconds: math.Round(ct.Seconds),
		}
		c.HRLow, c.HRHigh = climbHR(profile, ct.Watts)
		if cat, ok := climbs.Category(byIndex[ct.Index].Score); ok {
			c.Category = "HC"
			if cat > 0 {
				c.Category = fmt.Sprint(cat)
			}
		}
		dto.Climbs = append(dto.Climbs, c)
	}
	return pr, dto, ""
}

func roundTo5(w float64) int { return int(math.Round(w/5) * 5) }

// climbHR is the heart-rate range for a climb target, from the plan's own
// table (pacing.HR), zero when the rider has no HR data.
func climbHR(profile workout.RiderProfile, watts float64) (low, high int) {
	return pacing.HRForWatts(profile, watts, profile.FTPWatts)
}

// handlePacing answers GET /api/routes/{slug}/pacing?goal=<id>.
func (s *Server) handlePacing(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermReadRoutes) {
		return
	}
	slug := cleanSlug(r.PathValue("slug"))
	rider := auth.FromContext(r.Context()).User

	var goal *workout.Goal
	var profile workout.RiderProfile
	if s.Training != nil {
		if id := r.URL.Query().Get("goal"); id != "" {
			g, err := s.Training.GetGoal(r.Context(), id)
			if err != nil && !errors.Is(err, workout.ErrGoalNotFound) {
				s.fail(w, err)
				return
			}
			if err != nil || !isOwnTraining(auth.FromContext(r.Context()), g.Rider) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": workout.ErrGoalNotFound.Error()})
				return
			}
			goal = &g
		}
		p, _, err := s.Training.GetProfile(r.Context(), rider)
		if err != nil {
			s.fail(w, err)
			return
		}
		profile = p
	}

	if goal != nil && goal.RouteSlug != slug {
		// A plan for one route with another route's goal would carry the wrong
		// intensity and event: refuse it rather than guess.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": errGoalRouteMismatch})
		return
	}

	rp, code, err := s.loadRouteProfile(r.Context(), rider, slug)
	if err != nil {
		s.fail(w, err)
		return
	}
	if code == reasonRouteUnavailable {
		// A route that is missing and one that is not visible answer alike.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such route"})
		return
	}
	unavailable := func(code string) {
		s.logger().Info("pacing", "rider", rider, "route", slug, "outcome", code)
		writeJSON(w, http.StatusOK, routeDemandsUnavailableDTO{Available: false, Reason: pacingReasons[code], ReasonCode: code})
	}
	if code != "" {
		unavailable(code)
		return
	}
	_, dto, code := s.pacingFor(rp, profile, goal)
	if code != "" {
		unavailable(code)
		return
	}
	s.logger().Debug("pacing", "rider", rider, "route", slug, "outcome", "available")
	writeJSON(w, http.StatusOK, dto)
}
