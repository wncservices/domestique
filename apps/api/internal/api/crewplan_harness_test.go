package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/routefixture"
	"github.com/wncservices/domestique/apps/api/internal/schedule"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The crew-planning tests run on a fixed Wednesday with an explicit, non-UTC
// zone, so nothing depends on the TZ of the machine running them. The week is
// Monday 2026-10-05 to Sunday 2026-10-11.
const (
	cpMonday    = "2026-10-05"
	cpTuesday   = "2026-10-06"
	cpToday     = "2026-10-07" // Wednesday
	cpThursday  = "2026-10-08"
	cpFriday    = "2026-10-09"
	cpSaturday  = "2026-10-10"
	cpSunday    = "2026-10-11"
	cpNextSat   = "2026-10-17"
	cpNextMonth = "2026-11-14"
)

func cpClock() time.Time {
	return time.Date(2026, 10, 7, 9, 0, 0, 0, time.FixedZone("CEST", 2*3600))
}

// crewPlanHarness is the push harness (a rider with a connected Garmin, a
// training store and a clock) with crews and crew rides wired in.
type crewPlanHarness struct {
	*pushHarness
	crews *crew.Store
	sched *schedule.Store
}

func newCrewPlanHarness(t *testing.T) *crewPlanHarness {
	t.Helper()
	h := &crewPlanHarness{pushHarness: newPushHarness(t)}
	h.srv.Clock = cpClock
	var err error
	if h.crews, err = crew.UseDB(h.db.Conn(), h.db.DSN()); err != nil {
		t.Fatal(err)
	}
	if h.sched, err = schedule.UseDB(h.db.Conn(), h.db.DSN()); err != nil {
		t.Fatal(err)
	}
	h.srv.Crew, h.srv.Schedule = h.crews, h.sched
	return h
}

// seedCrew creates a crew owned by owner with every member approved.
func (h *crewPlanHarness) seedCrew(name, owner string, members ...string) string {
	h.t.Helper()
	ctx := context.Background()
	c, err := h.crews.Create(ctx, name, owner)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, m := range members {
		if _, err := h.crews.RequestJoin(ctx, c.ID, m); err != nil {
			h.t.Fatal(err)
		}
		if err := h.crews.Approve(ctx, c.ID, m, owner); err != nil {
			h.t.Fatal(err)
		}
	}
	return c.ID
}

// seedRoute stores a route shared with crewIDs. km and ascentM are what the
// fixture is built to; the stored stats are what a test should assert against.
func (h *crewPlanHarness) seedRoute(name, owner string, km, ascentM float64, crewIDs ...string) model.Route {
	h.t.Helper()
	grade := ascentM / (km * 1000) * 100
	gpx := routefixture.GPX(name, true, 500, 0, routefixture.Piece{LengthM: km * 1000, Grade: grade})
	targets := append([]string(nil), crewIDs...)
	route, err := h.db.Create(context.Background(), source.CreateRequest{Name: name, GPX: gpx, UploadedBy: owner, Targets: &targets})
	if err != nil {
		h.t.Fatal(err)
	}
	return route
}

func (h *crewPlanHarness) seedRide(crewID, slug, date, by string) schedule.Ride {
	h.t.Helper()
	ride, err := h.sched.Create(context.Background(), crewID, slug, date, "", by)
	if err != nil {
		h.t.Fatal(err)
	}
	return ride
}

// crewWorkoutOut is the workout DTO fields the crew tests read.
type crewWorkoutOut struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	GoalID         string  `json:"goalId"`
	Date           string  `json:"date"`
	Description    string  `json:"description"`
	Zone           string  `json:"zone"`
	PlannedSeconds float64 `json:"plannedSeconds"`
	Steps          []struct {
		Intensity string  `json:"intensity"`
		Duration  string  `json:"duration"`
		Target    string  `json:"target"`
		Seconds   float64 `json:"seconds"`
	} `json:"steps"`
	CrewRide *struct {
		RideID       string   `json:"rideId"`
		CrewID       string   `json:"crewId"`
		CrewName     string   `json:"crewName"`
		RouteName    string   `json:"routeName"`
		Going        bool     `json:"going"`
		EstimatedTSS float64  `json:"estimatedTss"`
		Kind         string   `json:"kind"`
		Orphaned     string   `json:"orphaned"`
		GoingNames   []string `json:"goingNames"`
	} `json:"crewRide"`
}

// goingOut is the PUT .../going response.
type goingOut struct {
	Going   bool            `json:"going"`
	Workout *crewWorkoutOut `json:"workout"`
	Diff    struct {
		Changes []struct {
			ID        string `json:"id"`
			Op        string `json:"op"`
			WorkoutID string `json:"workoutId"`
			Date      string `json:"date"`
			Name      string `json:"name"`
			Reason    string `json:"reason"`
		} `json:"changes"`
		LeftAlone []struct {
			WorkoutID string `json:"workoutId"`
			Reason    string `json:"reason"`
		} `json:"leftAlone"`
		Warnings []string `json:"warnings"`
	} `json:"diff"`
	Applied *struct {
		Removed   int `json:"removed"`
		Eased     int `json:"eased"`
		Shortened int `json:"shortened"`
		Added     int `json:"added"`
	} `json:"applied"`
}

// setGoing PUTs the going state as rider and decodes the answer when it is 200.
func (h *crewPlanHarness) setGoing(rider, rideID, body string) (*http.Response, goingOut) {
	h.t.Helper()
	resp := h.as(rider, "cyclists", http.MethodPut, "/api/training/crew-rides/"+rideID+"/going", body)
	var out goingOut
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			h.t.Fatal(err)
		}
	}
	return resp, out
}

func (h *crewPlanHarness) mustGo(rider, rideID string) goingOut {
	h.t.Helper()
	resp, out := h.setGoing(rider, rideID, `{"going":true}`)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("%s going to %s: status = %d, want 200", rider, rideID, resp.StatusCode)
	}
	return out
}

func (h *crewPlanHarness) mustLeave(rider, rideID string) goingOut {
	h.t.Helper()
	resp, out := h.setGoing(rider, rideID, `{"going":false}`)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("%s leaving %s: status = %d, want 200", rider, rideID, resp.StatusCode)
	}
	return out
}

// workoutsOf is the rider's own workouts through the API, as the Plan page
// reads them.
func (h *crewPlanHarness) workoutsOf(rider string) []crewWorkoutOut {
	h.t.Helper()
	resp := h.as(rider, "cyclists", http.MethodGet, "/api/training/workouts", "")
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("list workouts: status = %d", resp.StatusCode)
	}
	var out []crewWorkoutOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

// stored is the rider's workouts straight from the store.
func (h *crewPlanHarness) stored(rider string) []workout.Workout {
	h.t.Helper()
	list, err := h.training.ListWorkouts(context.Background(), rider)
	if err != nil {
		h.t.Fatal(err)
	}
	return list
}

func (h *crewPlanHarness) fixedRow(rider, rideID string) (workout.Workout, bool) {
	h.t.Helper()
	for _, w := range h.stored(rider) {
		if w.CrewRideID == rideID {
			return w, true
		}
	}
	return workout.Workout{}, false
}

// bodyOf is a response body as text, for assertions that something never
// appears in it.
func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	return strings.TrimSpace(string(readAll(t, resp)))
}
