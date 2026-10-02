package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/climbs"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/pacing"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Route demands: what a goal's route asks of a rider, set against what their
// plan trains. Read-only, owner-only, and made of distances, elevations, watts
// and times: it never carries a latitude or a longitude, and nothing here
// writes. See docs/superpowers/specs/2026-09-29-route-training-and-pacing-design.md.

// Why a goal's route demands cannot be shown. The code is for the UI to key
// off; the reason is the sentence a rider reads, with the fix in it.
const (
	reasonNoRoute          = "no_route"
	reasonRouteUnavailable = "route_unavailable"
	reasonNoElevation      = "no_elevation"
	reasonNotCycling       = "not_cycling"
	reasonNoFTP            = "no_ftp"
)

var demandReasons = map[string]string{
	reasonNoRoute:          "This goal has no route. Pick one on the goal to see what it asks of you.",
	reasonRouteUnavailable: "This goal's route is no longer available to you.",
	reasonNoElevation:      "This route has no elevation data. Recalculate this route's elevation from its page.",
	reasonNotCycling:       "Route demands are for cycling goals and cycling routes.",
	reasonNoFTP:            "Add your FTP to see what this route asks of you.",
}

type routeRefDTO struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// demandClimbDTO is one climb: where, how big, and how long it takes at the
// intensity the race is ridden at. Distances only, no position.
type demandClimbDTO struct {
	Index       int     `json:"index"`
	StartM      float64 `json:"startM"`
	EndM        float64 `json:"endM"`
	LengthM     float64 `json:"lengthM"`
	GainM       float64 `json:"gainM"`
	AvgGradient float64 `json:"avgGradient"`
	// Category is "4" to "1" or "HC" (Strava's length x gradient score);
	// absent for a climb too small to be rated.
	Category    string  `json:"category,omitempty"`
	DurationSec float64 `json:"durationSec"`
	Watts       float64 `json:"watts"`
	PctFTP      float64 `json:"pctFtp"`
	Kind        string  `json:"kind"`
	// Covered: the plan's longest sustained effort is at least 80 % of this
	// climb's duration.
	Covered bool `json:"covered"`
}

type coverageDTO struct {
	LongestSustainedSec float64 `json:"longestSustainedSec"`
	Uncovered           int     `json:"uncovered"`
	Message             string  `json:"message,omitempty"`
}

// biasDTO says whether generation favours effort lengths for this route (only
// Build and Peak weeks do), the phase the rider is in now, and the lengths, so
// the card can say "Build and Peak sessions favour 12-minute efforts".
type biasDTO struct {
	Active       bool   `json:"active"`
	Phase        string `json:"phase,omitempty"`
	SustainedSec int    `json:"sustainedSec,omitempty"`
	ShortSec     int    `json:"shortSec,omitempty"`
}

type routeDemandsDTO struct {
	Available   bool             `json:"available"`
	Route       routeRefDTO      `json:"route"`
	Assumptions []string         `json:"assumptions"`
	Climbs      []demandClimbDTO `json:"climbs"`
	Coverage    coverageDTO      `json:"coverage"`
	Bias        biasDTO          `json:"bias"`
}

type routeDemandsUnavailableDTO struct {
	Available  bool   `json:"available"`
	Reason     string `json:"reason"`
	ReasonCode string `json:"reasonCode"`
}

// routeProfile is a route's shape as the training features see it: its
// climbs at the training bar and its profile cut into grades. It holds the
// points only long enough to compute those; nothing returned from here is a
// position.
type routeProfile struct {
	Route  model.Route
	Segs   []pacing.Seg
	Climbs []climbs.Climb
}

// loadRouteProfile reads a rider's route for training use. The reason is ""
// when the profile is usable, and otherwise one of the reason codes above:
// the route is missing or not visible (the same answer, so nothing confirms
// another rider's route exists), has no usable elevation, or is not a
// cycling route. An error is a read failure, not a reason.
func (s *Server) loadRouteProfile(ctx context.Context, rider, slug string) (*routeProfile, string, error) {
	rt, ok, err := s.routeVisibleTo(ctx, rider, slug)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, reasonRouteUnavailable, nil
	}
	if rt.EffectiveSport() != model.SportCycling {
		return nil, reasonNotCycling, nil
	}
	points, err := s.Source.Track(ctx, slug)
	if err != nil {
		if errors.Is(err, source.ErrNotFound) {
			return nil, reasonRouteUnavailable, nil
		}
		return nil, "", err
	}
	if !hasElevation(points) {
		return nil, reasonNoElevation, nil
	}
	return &routeProfile{
		Route:  rt,
		Segs:   pacing.Segments(points, 100, 500),
		Climbs: climbs.Detect(points, climbs.TrainingConfig),
	}, "", nil
}

// hasElevation is true when every point carries elevation and it is not the
// all-zero placeholder some planners write (gpx.NeedsElevation).
func hasElevation(points []gpx.Point) bool {
	if len(points) < 3 || gpx.NeedsElevation(points) {
		return false
	}
	for _, p := range points {
		if !p.HasEle {
			return false
		}
	}
	return true
}

// demandClimbs rides each climb at FTP x IF x the climb factor for how long
// it lasts, through the physics. The factor depends on the duration and the
// duration on the watts, but there is always a consistent answer, because
// more power only shortens a climb: if the climb is under 5 minutes at the
// 1.05 factor it is under 5 minutes at 1.10 too, and so on up. Watts are
// capped at FTP x the same factor, so an override IF above 1.0 never asks for
// more than the plan's own climb ceiling.
func demandClimbs(rp *routeProfile, ftp float64, ph pacing.Physics, ifv float64) []demandClimbDTO {
	out := make([]demandClimbDTO, 0, len(rp.Climbs))
	for _, c := range rp.Climbs {
		d1 := pacing.ClimbSeconds(c, rp.Segs, ph, ftp*ifv*1.05)
		factor := pacing.ClimbFactor(d1)
		watts := math.Min(ftp*ifv*factor, ftp*factor)
		dur := pacing.ClimbSeconds(c, rp.Segs, ph, watts)

		dto := demandClimbDTO{
			Index: c.Index, StartM: math.Round(c.StartM), EndM: math.Round(c.EndM),
			LengthM: math.Round(c.LengthM), GainM: math.Round(c.GainM),
			AvgGradient: math.Round(c.AvgGradient*10) / 10,
			DurationSec: math.Round(dur), Watts: math.Round(watts),
			PctFTP: math.Round(watts/ftp*1000) / 10,
			Kind:   pacing.ClimbKind(dur),
		}
		if cat, ok := climbs.Category(c.Score); ok {
			dto.Category = "HC"
			if cat > 0 {
				dto.Category = strconv.Itoa(cat)
			}
		}
		out = append(out, dto)
	}
	return out
}

// climbCovered: a climb is covered when the plan's longest sustained effort
// is at least 80 % of how long the climb takes.
func climbCovered(longestSec, climbSec float64) bool {
	return longestSec >= 0.8*climbSec
}

// zoneLabel is how the coverage message names the zone of the plan's longest
// effort.
func zoneLabel(z workout.Zone) string {
	switch z {
	case workout.ZoneSweetSpot:
		return "sweet-spot"
	case workout.ZoneVO2Max:
		return "VO2max"
	case workout.ZoneAnaerobic:
		return "anaerobic"
	default:
		return "threshold"
	}
}

// coverageFor summarises how the plan answers the climbs, in a sentence.
// climbs carry their own Covered mark. Silent when there are no climbs.
func coverageFor(cl []demandClimbDTO, longestSec float64, longestZone workout.Zone) coverageDTO {
	cov := coverageDTO{LongestSustainedSec: longestSec}
	if len(cl) == 0 {
		return cov
	}
	minUncovered := math.Inf(1)
	for _, c := range cl {
		if !c.Covered {
			cov.Uncovered++
			minUncovered = math.Min(minUncovered, c.DurationSec)
		}
	}
	if cov.Uncovered == 0 {
		cov.Message = "Your plan trains for your route's climbs."
		return cov
	}
	noun := "climbs"
	if cov.Uncovered == 1 {
		noun = "climb"
	}
	head := fmt.Sprintf("Your route has %d %s over %d minutes; ", cov.Uncovered, noun, int(minUncovered/60))
	if longestSec <= 0 {
		cov.Message = head + "your plan has no sweet-spot or harder effort scheduled yet."
		return cov
	}
	cov.Message = head + fmt.Sprintf("your plan's longest %s effort is %d minutes.", zoneLabel(longestZone), int(math.Round(longestSec/60)))
	return cov
}

// sweetSpotOrHarder are the zones whose work steps count as a sustained
// effort for coverage: the ones a rehearsal of a climb is made of.
var sweetSpotOrHarder = map[workout.Zone]bool{
	workout.ZoneSweetSpot: true, workout.ZoneThreshold: true,
	workout.ZoneVO2Max: true, workout.ZoneAnaerobic: true,
}

// longestWorkStep is the longest single time-based work step in steps,
// looking inside repeat blocks. Work is an interval or active step: not a
// warmup, a cooldown or a recovery.
func longestWorkStep(steps []workout.WorkoutStep) float64 {
	var longest float64
	for _, st := range steps {
		if st.Repeat >= 2 {
			longest = math.Max(longest, longestWorkStep(st.Steps))
			continue
		}
		if st.Duration == workout.DurationTime && (st.Intensity == workout.IntensityInterval || st.Intensity == workout.IntensityActive) {
			longest = math.Max(longest, st.Seconds)
		}
	}
	return longest
}

// longestSustained is the longest single sweet-spot-or-harder work step in
// this goal's workouts from today to the event, and the zone it sits in. The
// workouts table holds planned and ridden sessions alike, so both count.
func longestSustained(workouts []workout.Workout, g workout.Goal, today string) (float64, workout.Zone) {
	var best float64
	var zone workout.Zone
	for _, wk := range workouts {
		if wk.GoalID != g.ID || wk.Date < today || (g.EventDate != "" && wk.Date > g.EventDate) || !sweetSpotOrHarder[wk.Zone] {
			continue
		}
		if l := longestWorkStep(wk.Steps); l > best {
			best, zone = l, wk.Zone
		}
	}
	return best, zone
}

// routeAssumptions are the inputs behind the numbers, said plainly.
func routeAssumptions(weightKG, ifv float64, derived bool) []string {
	out := []string{}
	if weightKG <= 0 {
		out = append(out, "Assumed 75 kg; add your weight for a better time.")
	} else {
		out = append(out, fmt.Sprintf("Your weight, %s kg, plus 8 kg for the bike and kit.", strconv.FormatFloat(weightKG, 'f', -1, 64)))
	}
	src := "your setting"
	if derived {
		src = "derived from how long the event takes"
	}
	out = append(out,
		fmt.Sprintf("Race intensity factor %.2f (%s).", ifv, src),
		"Steady power on each climb, no wind, no drafting, road bike on good asphalt. Times are estimates.")
	return out
}

// demandDurations is each climb's duration, for scheduler.DemandFromClimbs.
func demandDurations(cl []demandClimbDTO) []float64 {
	out := make([]float64, len(cl))
	for i, c := range cl {
		out[i] = c.DurationSec
	}
	return out
}

// routeDemandFor is what a goal's route asks of generated sessions, or nil.
// Anything that stops it being computed is nil, never an error: a missing or
// invisible route, no elevation, no FTP, a running goal or a library that
// cannot be read must not block planning, which then goes on as it would
// without a route. The one line it logs carries the goal, the route slug and
// the outcome, never a number about the rider.
func (s *Server) routeDemandFor(ctx context.Context, g workout.Goal, profile workout.RiderProfile) *scheduler.RouteDemand {
	if g.RouteSlug == "" {
		return nil
	}
	outcome := "unavailable"
	defer func() {
		s.logger().Debug("route demand", "goal", g.ID, "route", g.RouteSlug, "outcome", outcome)
	}()
	if profile.FTPWatts <= 0 || (g.Sport != "" && g.Sport != model.SportCycling) {
		return nil
	}
	rp, code, err := s.loadRouteProfile(ctx, g.Rider, g.RouteSlug)
	if err != nil {
		outcome = "unreadable"
		s.logger().Warn("route demand: reading the route failed", "goal", g.ID, "route", g.RouteSlug, "err", err)
		return nil
	}
	if code != "" {
		outcome = code
		return nil
	}
	ph := pacing.DefaultPhysics(profile.WeightKG)
	ifv := g.PacingIF
	if ifv == 0 {
		ifv = pacing.DerivedIF(rp.Segs, ph, profile.FTPWatts)
	}
	cl := demandClimbs(rp, profile.FTPWatts, ph, ifv)
	demand := scheduler.DemandFromClimbs(rp.Climbs, demandDurations(cl), rp.Route.Stats.AscentM, rp.Route.Stats.DistanceM)
	outcome = "none"
	if demand != nil {
		outcome = "biased"
	}
	return demand
}

// biasFor reports what generation does with this route. The phase is best
// effort: a goal with no event date or one in the past has no plan to read it
// from, and the card then just leaves it out.
func (s *Server) biasFor(ctx context.Context, g workout.Goal, rp *routeProfile, cl []demandClimbDTO) biasDTO {
	demand := scheduler.DemandFromClimbs(rp.Climbs, demandDurations(cl), rp.Route.Stats.AscentM, rp.Route.Stats.DistanceM)
	out := biasDTO{Active: demand != nil && (demand.Sustained > 0 || demand.Short > 0 || demand.Punchy > 0 || demand.Climbing)}
	if demand != nil {
		out.SustainedSec, out.ShortSec = demand.Sustained, demand.Short
	}
	if plan, _, err := s.reconciledPeriodizationPlan(ctx, g, g.Rider); err == nil {
		today := s.now().Format(dateLayout)
		for _, wk := range plan.Weeks {
			if start, end := weekBounds(wk); today >= start && today <= end {
				out.Phase = string(wk.Phase)
			}
		}
	}
	return out
}

// goalForOwner loads goal id for its owner, or writes the 404 every
// not-yours-or-not-there case gets: the same answer for both, so a goal's
// existence is not confirmed to another rider.
func (s *Server) goalForOwner(w http.ResponseWriter, r *http.Request) (workout.Goal, bool) {
	g, err := s.Training.GetGoal(r.Context(), r.PathValue("id"))
	if err != nil && !errors.Is(err, workout.ErrGoalNotFound) {
		s.fail(w, err)
		return workout.Goal{}, false
	}
	if err != nil || !isOwnTraining(auth.FromContext(r.Context()), g.Rider) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": workout.ErrGoalNotFound.Error()})
		return workout.Goal{}, false
	}
	return g, true
}

// handleRouteDemands answers GET /api/training/goals/{id}/route-demands.
func (s *Server) handleRouteDemands(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	g, ok := s.goalForOwner(w, r)
	if !ok {
		return
	}
	rider := g.Rider

	unavailable := func(code string) {
		s.logger().Info("route-demands", "rider", rider, "goal", g.ID, "route", g.RouteSlug, "outcome", code)
		writeJSON(w, http.StatusOK, routeDemandsUnavailableDTO{Available: false, Reason: demandReasons[code], ReasonCode: code})
	}

	if g.RouteSlug == "" {
		unavailable(reasonNoRoute)
		return
	}
	if g.Sport != "" && g.Sport != model.SportCycling {
		unavailable(reasonNotCycling)
		return
	}
	rp, code, err := s.loadRouteProfile(r.Context(), rider, g.RouteSlug)
	if err != nil {
		s.fail(w, err)
		return
	}
	if code != "" {
		unavailable(code)
		return
	}
	profile, _, err := s.Training.GetProfile(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	if profile.FTPWatts <= 0 {
		unavailable(reasonNoFTP)
		return
	}

	ph := pacing.DefaultPhysics(profile.WeightKG)
	ifv, derived := g.PacingIF, false
	if ifv == 0 {
		ifv, derived = pacing.DerivedIF(rp.Segs, ph, profile.FTPWatts), true
	}
	cl := demandClimbs(rp, profile.FTPWatts, ph, ifv)

	workouts, err := s.Training.ListWorkouts(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	longest, zone := longestSustained(workouts, g, s.now().Format(dateLayout))
	for i := range cl {
		cl[i].Covered = climbCovered(longest, cl[i].DurationSec)
	}

	s.logger().Debug("route-demands", "rider", rider, "goal", g.ID, "route", g.RouteSlug, "outcome", "available")
	writeJSON(w, http.StatusOK, routeDemandsDTO{
		Available:   true,
		Route:       routeRefDTO{Slug: rp.Route.Slug, Name: rp.Route.Name},
		Assumptions: routeAssumptions(profile.WeightKG, ifv, derived),
		Climbs:      cl,
		Coverage:    coverageFor(cl, longest, zone),
		Bias:        s.biasFor(r.Context(), g, rp, cl),
	})
}
