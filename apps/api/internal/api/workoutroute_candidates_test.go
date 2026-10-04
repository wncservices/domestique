package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
	"github.com/wncservices/domestique/apps/api/internal/routefit"
	"github.com/wncservices/domestique/apps/api/internal/routing"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const (
	wrToday  = "2026-10-03"
	wrFuture = "2026-10-04"
	wrPast   = "2026-10-02"
)

// A distinctive FTP and weight, so a scan of a log for them cannot match by
// chance.
const (
	wrFTP    = 251
	wrWeight = 73.5
)

type candidateOut struct {
	ID               string       `json:"id"`
	Points           [][2]float64 `json:"points"`
	DistanceM        float64      `json:"distanceM"`
	AscentM          float64      `json:"ascentM"`
	Surface          []any        `json:"surface"`
	Elevation        []any        `json:"elevationProfile"`
	EstimatedSeconds float64      `json:"estimatedSeconds"`
	Family           string       `json:"family"`
	Score            float64      `json:"score"`
	TerrainFit       float64      `json:"terrainFit"`
	Note             string       `json:"note"`
}

type candidatesOut struct {
	Candidates     []candidateOut `json:"candidates"`
	PlannedSeconds float64        `json:"plannedSeconds"`
	SpeedKph       float64        `json:"speedKph"`
	SpeedAssumed   bool           `json:"speedAssumed"`
}

// candidatesFor asks for loops for a workout as a rider.
func (h *wrHarness) candidatesFor(rider, workoutID, query, body string) *http.Response {
	h.t.Helper()
	return h.as(rider, http.MethodPost, "/api/training/workouts/"+workoutID+"/route-candidates"+query, body)
}

func (h *wrHarness) decodeCandidates(resp *http.Response) candidatesOut {
	h.t.Helper()
	raw := h.body(resp)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	var out candidatesOut
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		h.t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

// ready is a harness with a rider who has an FTP, a weight, a start point and
// a two-hour endurance ride tomorrow.
func readyHarness(t *testing.T, mutate ...func(*wrHarness, *api.Server)) (*wrHarness, workout.Workout) {
	t.Helper()
	h := newWRHarness(t, mutate...)
	h.setProfile("wilant", wrFTP, wrWeight)
	h.setStart("wilant")
	return h, h.enduranceWorkout("wilant", wrFuture)
}

func TestCandidatesBuildTwoRoundsAtTheRightLengthsFromTheStoredStart(t *testing.T) {
	h, wk := readyHarness(t)
	out := h.decodeCandidates(h.candidatesFor("wilant", wk.ID, "", ""))

	calls := h.engine.Calls()
	if len(calls) != 10 {
		t.Fatalf("%d engine calls, want 10: three calibration seeds and seven refinement seeds", len(calls))
	}
	v, _ := routefit.FlatSpeed(routefit.Rider{FTP: wrFTP, WeightKg: wrWeight}, 0.65)
	first := v * 7200
	var atFirst, refined []float64
	for _, c := range calls {
		if c.Start != wrStartStored {
			t.Errorf("a call started at %+v, want the stored three-decimal start %+v", c.Start, wrStartStored)
		}
		if math.Abs(c.LengthM-first) < 1 {
			atFirst = append(atFirst, c.LengthM)
		} else {
			refined = append(refined, c.LengthM)
		}
	}
	if len(atFirst) != 3 || len(refined) != 7 {
		t.Fatalf("%d calls at flat speed x planned seconds and %d refined, want 3 and 7", len(atFirst), len(refined))
	}
	// The fake overshoots by a tenth, so the refined length is about a
	// tenth under the first guess (less a little for the climbing).
	for _, l := range refined {
		if l >= first || l < first*0.7 {
			t.Errorf("refined length %v, want a bit under the first guess %v", l, first)
		}
	}
	if out.PlannedSeconds != 7200 {
		t.Errorf("plannedSeconds = %v, want 7200", out.PlannedSeconds)
	}
	if math.Abs(out.SpeedKph-v*3.6) > 0.1 || out.SpeedAssumed {
		t.Errorf("speed = %v kph assumed=%v, want %v and not assumed", out.SpeedKph, out.SpeedAssumed, v*3.6)
	}
}

func TestCandidatesAskTheEngineForTheFamilysProfileAndHilliness(t *testing.T) {
	cases := []struct {
		name      string
		zone      workout.Zone
		steps     []workout.WorkoutStep
		profile   string
		hilliness int
	}{
		{"recovery", workout.ZoneEndurance, []workout.WorkoutStep{timedStep("Ride", workout.IntensityActive, 50*60, 0)}, "cycling-regular", 0},
		{"endurance", workout.ZoneEndurance, []workout.WorkoutStep{timedStep("Ride", workout.IntensityActive, 120*60, 0)}, "cycling-road", 1},
		{"long", workout.ZoneEndurance, []workout.WorkoutStep{timedStep("Ride", workout.IntensityActive, 200*60, 0)}, "cycling-road", 3},
		{"steady", workout.ZoneThreshold, []workout.WorkoutStep{timedStep("On", workout.IntensityActive, 90*60, 250)}, "cycling-road", 1},
		{"climb", workout.ZoneVO2Max, []workout.WorkoutStep{timedStep("On", workout.IntensityActive, 90*60, 300)}, "cycling-road", 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newWRHarness(t)
			h.setProfile("wilant", wrFTP, wrWeight)
			h.setStart("wilant")
			wk := h.seedWorkout("wilant", "Session", c.zone, wrFuture, c.steps...)
			h.candidatesFor("wilant", wk.ID, "", "")
			calls := h.engine.Calls()
			if len(calls) != 10 {
				t.Fatalf("%d calls, want 10", len(calls))
			}
			for _, call := range calls {
				if call.Profile != c.profile || call.Hilliness != c.hilliness {
					t.Fatalf("a call asked for (%s, %d), want (%s, %d)", call.Profile, call.Hilliness, c.profile, c.hilliness)
				}
			}
		})
	}
}

func TestCandidatesReturnAtMostThreeWithinFifteenPercentBestFirst(t *testing.T) {
	h, wk := readyHarness(t)
	out := h.decodeCandidates(h.candidatesFor("wilant", wk.ID, "", ""))
	if n := len(out.Candidates); n == 0 || n > 3 {
		t.Fatalf("%d candidates, want 1 to 3", n)
	}
	for i, c := range out.Candidates {
		if math.Abs(c.EstimatedSeconds-7200) > 0.15*7200 {
			t.Errorf("candidate %d takes %v s, more than 15 percent off the planned 7200", i, c.EstimatedSeconds)
		}
		if c.Family != "endurance" {
			t.Errorf("candidate %d family = %q, want endurance", i, c.Family)
		}
		if len(c.ID) < 16 || len(c.Points) < 4 || len(c.Elevation) == 0 || c.DistanceM <= 0 {
			t.Errorf("candidate %d is missing parts: %+v", i, c)
		}
		if i > 0 && c.Score > out.Candidates[i-1].Score+1e-9 {
			t.Errorf("candidate %d scores better (%v) than the one before it (%v): not best first", i, c.Score, out.Candidates[i-1].Score)
		}
	}
}

func TestCandidatesDropLoopsOutsideFifteenPercentOfThePlannedTime(t *testing.T) {
	h, wk := readyHarness(t)
	// Every loop is far too long, whatever is asked.
	h.engine.behave = func(c wrCall, _ int) (routing.Path, error) { return wrLoop(c.Start, 150000, 40), nil }
	resp := h.candidatesFor("wilant", wk.ID, "", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 when nothing is within 15 percent: %s", resp.StatusCode, h.body(resp))
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "level=ERROR") {
		t.Errorf("no Error log for a request that failed:\n%s", logs)
	}
}

func TestCandidatesTheMessageForNothingCloseEnough(t *testing.T) {
	h, wk := readyHarness(t)
	h.engine.behave = func(c wrCall, _ int) (routing.Path, error) { return wrLoop(c.Start, 150000, 40), nil }
	resp := h.candidatesFor("wilant", wk.ID, "", "")
	if raw := h.body(resp); !strings.Contains(raw, "close enough to the length of that ride") {
		t.Errorf("body %s, want the plain 'could not find a loop close enough' message", raw)
	}
}

func TestCandidatesDropLoopsThatMostlyBacktrack(t *testing.T) {
	h, wk := readyHarness(t)
	h.engine.behave = func(c wrCall, n int) (routing.Path, error) {
		if c.Seed%2 == 0 {
			return wrSpur(c.Start, c.LengthM*1.0), nil
		}
		return wrLoop(c.Start, c.LengthM*1.1, 10*c.LengthM*1.1/1000), nil
	}
	out := h.decodeCandidates(h.candidatesFor("wilant", wk.ID, "", ""))
	for i, c := range out.Candidates {
		first, last := c.Points[0], c.Points[len(c.Points)-1]
		if first != last {
			t.Errorf("candidate %d is an out-and-back, not a loop: it ends somewhere else", i)
		}
	}
	if len(out.Candidates) == 0 {
		t.Fatal("no candidates")
	}
}

func TestCandidatesNeverShowTheSameShapeTwice(t *testing.T) {
	h, wk := readyHarness(t)
	h.engine.behave = func(c wrCall, _ int) (routing.Path, error) {
		return wrLoop(c.Start, 56000, 400), nil // the same loop for every seed
	}
	// 56 km flat-ish at a 120 minute plan is within 15 percent.
	out := h.decodeCandidates(h.candidatesFor("wilant", wk.ID, "", ""))
	if len(out.Candidates) != 1 {
		t.Errorf("%d candidates from ten copies of one loop, want 1", len(out.Candidates))
	}
}

func TestCandidatesSomeSeedsFailingIsAWarningAndTheRestAreUsed(t *testing.T) {
	h, wk := readyHarness(t)
	h.engine.behave = func(c wrCall, n int) (routing.Path, error) {
		if c.Seed%2 == 0 {
			return routing.Path{}, errors.New("routing service returned 404: Unable to find a route for point (47.377, 8.542)")
		}
		return wrLoop(c.Start, c.LengthM*1.1, float64(c.Seed%5)*c.LengthM*1.1/1000*4), nil
	}
	out := h.decodeCandidates(h.candidatesFor("wilant", wk.ID, "", ""))
	if len(out.Candidates) == 0 {
		t.Fatal("no candidates although half the seeds worked")
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "workout route") {
		t.Errorf("no Warn for failed seeds:\n%s", logs)
	}
	if strings.Contains(logs, "level=ERROR") {
		t.Errorf("an Error for a request that succeeded:\n%s", logs)
	}
}

func TestCandidatesEveryEngineCallFailingIs502AndAnError(t *testing.T) {
	h, wk := readyHarness(t)
	h.engine.behave = func(c wrCall, _ int) (routing.Path, error) {
		return routing.Path{}, errors.New("routing service returned 404: Unable to find a route for point (47.377, 8.542)")
	}
	resp := h.candidatesFor("wilant", wk.ID, "", "")
	raw := h.body(resp)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "level=ERROR") {
		t.Errorf("no Error log when every seed failed:\n%s", logs)
	}
	// The engine's own message can echo a coordinate; neither the log nor the
	// answer repeats it.
	for _, leak := range []string{"47.37", "8.54", "Unable to find"} {
		if strings.Contains(logs, leak) || strings.Contains(raw, leak) {
			t.Errorf("the engine's message leaked %q:\nlogs: %s\nbody: %s", leak, logs, raw)
		}
	}
}

func TestCandidatesWithoutAnEngineAre412WithAWarningAndNoCall(t *testing.T) {
	h, wk := readyHarness(t, func(_ *wrHarness, srv *api.Server) { srv.Routing = nil })
	resp := h.candidatesFor("wilant", wk.ID, "", "")
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", resp.StatusCode)
	}
	if logs := h.logs.String(); !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "routing engine") {
		t.Errorf("no Warn for the missing engine:\n%s", logs)
	}
	if n := len(h.engine.Calls()); n != 0 {
		t.Errorf("%d engine calls without an engine", n)
	}
}

func TestCandidatesWithoutAStartPointAre409AndAnInfo(t *testing.T) {
	h := newWRHarness(t)
	h.setProfile("wilant", wrFTP, wrWeight)
	wk := h.enduranceWorkout("wilant", wrFuture)
	resp := h.candidatesFor("wilant", wk.ID, "", "")
	raw := h.body(resp)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, raw)
	}
	var out struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal([]byte(raw), &out)
	if out.Code != "no_start_point" {
		t.Errorf("code = %q, want no_start_point: the UI keys off it", out.Code)
	}
	if logs := h.logs.String(); !strings.Contains(logs, "level=INFO") || !strings.Contains(logs, "start point") {
		t.Errorf("no Info for the missing start point:\n%s", logs)
	}
	if n := len(h.engine.Calls()); n != 0 {
		t.Errorf("%d engine calls without a start point", n)
	}
}

func TestCandidatesAreRefusedForARideThatCannotTakeARoute(t *testing.T) {
	h := newWRHarness(t)
	h.setProfile("wilant", wrFTP, wrWeight)
	h.setStart("wilant")
	ctx := context.Background()

	past := h.enduranceWorkout("wilant", wrPast)
	undated := h.enduranceWorkout("wilant", "")

	indoor := h.enduranceWorkout("wilant", wrFuture)
	yes := true
	if _, err := h.training.UpdateWorkout(ctx, indoor.ID, workout.UpdateWorkoutRequest{Indoor: &yes}); err != nil {
		t.Fatal(err)
	}

	test, err := h.training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Name: "FTP test", Date: wrFuture, TestProtocol: "ramp",
		Steps: []workout.WorkoutStep{timedStep("Ramp", workout.IntensityActive, 1800, 0)},
	})
	if err != nil {
		t.Fatal(err)
	}

	running, err := h.training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: "running", Name: "Easy run", Date: wrFuture,
		Steps: []workout.WorkoutStep{timedStep("Run", workout.IntensityActive, 1800, 0)},
	})
	if err != nil {
		t.Fatal(err)
	}

	ridden := h.enduranceWorkout("wilant", wrToday)
	if _, err := h.training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "x1", Sport: "cycling", Date: wrToday, DurationSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}

	for name, id := range map[string]string{
		"past": past.ID, "undated": undated.ID, "indoor": indoor.ID, "test": test.ID,
		"running": running.ID, "ridden today": ridden.ID,
	} {
		resp := h.candidatesFor("wilant", id, "", "")
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("%s: status = %d, want 409: %s", name, resp.StatusCode, h.body(resp))
		}
	}
	if n := len(h.engine.Calls()); n != 0 {
		t.Errorf("%d engine calls for rides that cannot take a route", n)
	}
}

func TestCandidatesForTodayAreAllowedWhenNotYetRidden(t *testing.T) {
	h := newWRHarness(t)
	h.setProfile("wilant", wrFTP, wrWeight)
	h.setStart("wilant")
	wk := h.enduranceWorkout("wilant", wrToday)
	if out := h.decodeCandidates(h.candidatesFor("wilant", wk.ID, "?today="+wrToday, "")); len(out.Candidates) == 0 {
		t.Error("no candidates for today's unridden ride")
	}
}

func TestCandidatesNeedAPlannedLength(t *testing.T) {
	h := newWRHarness(t)
	h.setStart("wilant")
	open := workout.WorkoutStep{Name: "Ride", Intensity: workout.IntensityActive, Duration: workout.DurationOpen, Target: workout.TargetOpen}
	wk := h.seedWorkout("wilant", "Free ride", workout.ZoneEndurance, wrFuture, open)
	if resp := h.candidatesFor("wilant", wk.ID, "", ""); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 for a ride with no planned time", resp.StatusCode)
	}
	if n := len(h.engine.Calls()); n != 0 {
		t.Errorf("%d engine calls for a ride with no length", n)
	}
}

func TestCandidatesWithoutAnFTPFallBackToTwentyFiveKph(t *testing.T) {
	h := newWRHarness(t)
	h.setStart("wilant") // no profile at all
	wk := h.enduranceWorkout("wilant", wrFuture)
	out := h.decodeCandidates(h.candidatesFor("wilant", wk.ID, "", ""))
	if out.SpeedKph != 25 || !out.SpeedAssumed {
		t.Errorf("speed = %v kph assumed=%v, want 25 and assumed", out.SpeedKph, out.SpeedAssumed)
	}
	var first float64
	for _, c := range h.engine.Calls() {
		first = math.Max(first, c.LengthM)
	}
	if math.Abs(first-25/3.6*7200) > 1 {
		t.Errorf("first guess = %v m, want 25 km/h x 2 h = 50000", first)
	}
}

func TestCandidatesAnotherRidersWorkoutIsNotFound(t *testing.T) {
	h, wk := readyHarness(t)
	h.setStart("marie")
	if resp := h.candidatesFor("marie", wk.ID, "", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for someone else's workout", resp.StatusCode)
	}
	if resp := h.candidatesFor("marie", "no-such-workout", "", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a workout that does not exist", resp.StatusCode)
	}
	if n := len(h.engine.Calls()); n != 0 {
		t.Errorf("%d engine calls for a workout that is not the caller's", n)
	}
}

func TestCandidatesAreForRidersNotViewers(t *testing.T) {
	h, wk := readyHarness(t)
	resp := h.asGroup("wilant", "guests", http.MethodPost, "/api/training/workouts/"+wk.ID+"/route-candidates", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer status = %d, want 403", resp.StatusCode)
	}
}

func TestCandidatesTheStartComesFromTheStoreNeverTheBody(t *testing.T) {
	h, wk := readyHarness(t)
	h.candidatesFor("wilant", wk.ID, "", `{"start":{"lat":1.5,"lon":2.5},"lat":3.5,"lon":4.5}`)
	for _, c := range h.engine.Calls() {
		if c.Start != wrStartStored {
			t.Fatalf("a call started at %+v: the body's start was used", c.Start)
		}
	}
}

func TestCandidatesHaveTheirOwnTighterLimiter(t *testing.T) {
	h, wk := readyHarness(t, func(_ *wrHarness, srv *api.Server) {
		srv.WorkoutRouteLimiter = ratelimit.New(1, time.Hour)
	})
	h.decodeCandidates(h.candidatesFor("wilant", wk.ID, "", ""))
	before := len(h.engine.Calls())
	if resp := h.candidatesFor("wilant", wk.ID, "", ""); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", resp.StatusCode)
	}
	if after := len(h.engine.Calls()); after != before {
		t.Errorf("a limited request still made %d engine calls", after-before)
	}
	if logs := h.logs.String(); !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "rate limited") {
		t.Errorf("no Warn when the limiter fired:\n%s", logs)
	}
}

// The builder's generous budget is not the ride route's: exhausting one must
// not spend or block the other.
func TestTheRouteBuilderLimiterDoesNotGovernCandidates(t *testing.T) {
	h, wk := readyHarness(t, func(_ *wrHarness, srv *api.Server) {
		srv.RouteBuilderLimiter = ratelimit.New(1, time.Hour)
		srv.WorkoutRouteLimiter = ratelimit.New(6, time.Hour)
	})
	for i := 0; i < 3; i++ {
		if resp := h.candidatesFor("wilant", wk.ID, "", ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200: the builder limiter must not apply", i+1, resp.StatusCode)
		}
	}
}

func TestAnEngineThatFailsEveryCalibrationCallCostsOnlyThree(t *testing.T) {
	h, wk := readyHarness(t)
	h.engine.behave = func(c wrCall, _ int) (routing.Path, error) {
		return routing.Path{}, errors.New("routing service returned 429 Too Many Requests: Unable to find a route for point (47.377, 8.542)")
	}
	resp := h.candidatesFor("wilant", wk.ID, "", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", resp.StatusCode)
	}
	if n := len(h.engine.Calls()); n != 3 {
		t.Errorf("%d engine calls, want only the 3 calibration calls", n)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "cause=quota") {
		t.Errorf("the log does not classify the failure as quota:\n%s", logs)
	}
	if strings.Contains(logs, "47.37") || strings.Contains(logs, "Unable to find") {
		t.Errorf("the engine's words reached the log:\n%s", logs)
	}
}

func TestCandidatesStopSpendingEngineCallsWhenTheRequestIsCancelled(t *testing.T) {
	h, wk := readyHarness(t)
	h.engine.blockUntilCancel = true

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.base+"/api/training/workouts/"+wk.ID+"/route-candidates", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Remote-User", "wilant")
	req.Header.Set("Remote-Groups", "cyclists")
	if resp, err := h.client.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("the request was answered although every engine call blocks")
	}
	time.Sleep(300 * time.Millisecond) // let the handler notice
	if n := len(h.engine.Calls()); n != 3 {
		t.Errorf("%d engine calls after cancelling, want only the 3 of the first round", n)
	}
	if logs := h.logs.String(); strings.Contains(logs, "level=ERROR") {
		t.Errorf("a rider closing the page was logged as an error:\n%s", logs)
	}
}

func TestCandidatesLogsCarryNoCoordinatePlaceFTPWattsOrWeight(t *testing.T) {
	h, wk := readyHarness(t)
	h.decodeCandidates(h.candidatesFor("wilant", wk.ID, "", ""))

	h.engine.behave = func(c wrCall, _ int) (routing.Path, error) {
		return routing.Path{}, errors.New("engine down at (47.377, 8.542)")
	}
	h.candidatesFor("wilant", wk.ID, "", "")

	logs := h.logs.String()
	if logs == "" {
		t.Fatal("nothing was logged at all")
	}
	for _, leak := range []string{"47.37", "8.54", "Ghent", "Belgium", "251", "73.5", "ftp", "FTP", "watts", "weight"} {
		if strings.Contains(logs, leak) {
			t.Errorf("logs contain %q:\n%s", leak, logs)
		}
	}
}

func TestCandidatesResponseOnlyCarriesCoordinatesInTheCandidates(t *testing.T) {
	h, wk := readyHarness(t)
	raw := h.body(h.candidatesFor("wilant", wk.ID, "", ""))
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		t.Fatal(err)
	}
	for k := range top {
		switch k {
		case "candidates", "plannedSeconds", "speedKph", "speedAssumed":
		default:
			t.Errorf("unexpected top-level field %q", k)
		}
	}
	// The place the rider chose is never part of an answer.
	if strings.Contains(raw, "Ghent") {
		t.Error("the start's place is in the candidates response")
	}
}
