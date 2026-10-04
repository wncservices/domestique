package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type crewAdviceOut struct {
	Date      string `json:"date"`
	RouteName string `json:"routeName"`
	Severity  string `json:"severity"`
	Advice    string `json:"advice"`
}

type readinessCrewOut struct {
	Today struct {
		Verdict string `json:"verdict"`
	} `json:"today"`
	Tomorrow *struct {
		WorkoutID string `json:"workoutId"`
	} `json:"tomorrow"`
	CrewRide *crewAdviceOut `json:"crewRide"`
}

func (h *crewPlanHarness) readiness(rider string) readinessCrewOut {
	h.t.Helper()
	resp := h.as(rider, "cyclists", http.MethodGet, "/api/training/readiness?today="+cpToday, "")
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("readiness: status = %d", resp.StatusCode)
	}
	var out readinessCrewOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *crewPlanHarness) wellness(rider, level string, score int) {
	h.t.Helper()
	if err := h.training.SaveWellness(context.Background(), workout.DailyWellness{
		Rider: rider, Date: cpToday, ReadinessLevel: level, ReadinessScore: score, SleepScore: 70,
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *crewPlanHarness) ftp(rider string, watts float64) {
	h.t.Helper()
	if _, err := h.training.SaveProfile(context.Background(), workout.RiderProfile{Rider: rider, FTPWatts: watts}); err != nil {
		h.t.Fatal(err)
	}
}

// joinToday makes wilant go to a ride dated date and returns its id.
func (h *crewPlanHarness) joinOn(date string) string {
	h.t.Helper()
	s := h.setup()
	ride := h.seedRide(s.crewID, s.route, date, "wilant")
	h.mustGo("wilant", ride.ID)
	return ride.ID
}

func TestARestVerdictOnACrewRideDayAdvisesAndChangesNothing(t *testing.T) {
	h := newCrewPlanHarness(t)
	h.ftp("wilant", 260)
	rideID := h.joinOn(cpToday)
	h.wellness("wilant", "POOR", 10)
	row, _ := h.fixedRow("wilant", rideID)

	out := h.readiness("wilant")
	if out.Today.Verdict != "rest" {
		t.Fatalf("verdict = %q, want rest", out.Today.Verdict)
	}
	c := out.CrewRide
	if c == nil || c.Date != cpToday || c.RouteName != "Hill Loop" || c.Severity != "rest" {
		t.Fatalf("crewRide = %+v, want rest advice for today's ride", c)
	}
	if !strings.Contains(c.Advice, "Ride at conversational pace, or skip it; your call") {
		t.Errorf("advice = %q", c.Advice)
	}

	// Advice only: nothing is moved, eased, swapped or skipped, by the page or
	// by the adaptation that follows.
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	h.srv.AdaptWorkouts(context.Background())
	after, ok := h.fixedRow("wilant", rideID)
	if !ok || after.UpdatedAt != row.UpdatedAt || after.Date != row.Date || after.Description != row.Description ||
		workout.PlannedSeconds(after.Steps) != workout.PlannedSeconds(row.Steps) {
		t.Error("readiness changed the crew ride")
	}
}

func TestACautionVerdictAdvisesSitInWithAWattCapOnlyWhenFTPIsKnown(t *testing.T) {
	h := newCrewPlanHarness(t)
	h.ftp("wilant", 260)
	h.joinOn(cpToday)
	h.wellness("wilant", "LOW", 40)

	c := h.readiness("wilant").CrewRide
	if c == nil || c.Severity != "caution" {
		t.Fatalf("crewRide = %+v, want caution", c)
	}
	for _, want := range []string{"Group ride today", "sit in", "skip the pulls", "stay under about 195 W"} {
		if !strings.Contains(c.Advice, want) {
			t.Errorf("advice = %q, missing %q", c.Advice, want)
		}
	}

	h.ftp("wilant", 0)
	c = h.readiness("wilant").CrewRide
	if c == nil || strings.Contains(c.Advice, " W") {
		t.Errorf("advice without an FTP = %+v, want no wattage", c)
	}
}

func TestAReadyVerdictGivesNoCrewAdviceAndNeitherDoesANonRideDay(t *testing.T) {
	h := newCrewPlanHarness(t)
	h.joinOn(cpToday)
	if c := h.readiness("wilant").CrewRide; c != nil {
		t.Errorf("a ready rider got advice: %+v", c)
	}
	h2 := newCrewPlanHarness(t)
	h2.setup()
	h2.wellness("wilant", "POOR", 10)
	if c := h2.readiness("wilant").CrewRide; c != nil {
		t.Errorf("a rider with no ride today got advice: %+v", c)
	}
}

func TestTomorrowsCrewRideIsAdvisedFromTheForecastAndNeverEased(t *testing.T) {
	h := newCrewPlanHarness(t)
	h.ftp("wilant", 260)
	rideID := h.joinOn(cpThursday)
	h.wellness("wilant", "POOR", 10) // resting today makes tomorrow a caution
	row, _ := h.fixedRow("wilant", rideID)

	out := h.readiness("wilant")
	c := out.CrewRide
	if c == nil || c.Date != cpThursday || c.Severity != "caution" || !strings.Contains(c.Advice, "Group ride tomorrow") {
		t.Fatalf("crewRide = %+v, want caution advice for tomorrow's ride", c)
	}
	if out.Tomorrow != nil {
		t.Errorf("tomorrow = %+v: a crew ride is never offered as a session to ease", out.Tomorrow)
	}

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/readiness/tomorrow/ease?today="+cpToday, "")
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("ease tomorrow status = %d, want 409", resp.StatusCode)
	}
	after, _ := h.fixedRow("wilant", rideID)
	if after.UpdatedAt != row.UpdatedAt || after.Description != row.Description {
		t.Error("ease tomorrow touched the crew ride")
	}
}

func TestAFTPTestCannotBeScheduledOnACrewRideDay(t *testing.T) {
	h := newCrewPlanHarness(t)
	h.ftp("wilant", 250)
	rideID := h.joinOn(cpSaturday)
	before := len(h.stored("wilant"))

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/tests/ftp", `{"protocol":"ramp","date":"`+cpSaturday+`"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body["error"], "crew ride") {
		t.Errorf("error = %q, want it to name the crew ride", body["error"])
	}
	if got := len(h.stored("wilant")); got != before {
		t.Errorf("workouts = %d, want %d: nothing is written", got, before)
	}
	if _, ok := h.fixedRow("wilant", rideID); !ok {
		t.Error("the crew ride was replaced by a test")
	}
}

func TestACrewRideIsOutdoorAndHasNoAlternates(t *testing.T) {
	h := newCrewPlanHarness(t)
	h.ftp("wilant", 250)
	rideID := h.joinOn(cpSaturday)
	row, _ := h.fixedRow("wilant", rideID)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+row.ID+"/alternates?today="+cpToday, "")
	var menu struct {
		Options []json.RawMessage `json:"options"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&menu)
	if len(menu.Options) != 0 {
		t.Errorf("a crew ride is offered %d alternates, want none", len(menu.Options))
	}
	resp = h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+row.ID+"/alternates?today="+cpToday, `{"kind":"shorter"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("swap status = %d, want 409", resp.StatusCode)
	}
	resp = h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+row.ID+"/indoor?today="+cpToday, "")
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("indoor conversion status = %d, want 409", resp.StatusCode)
	}
	after, _ := h.fixedRow("wilant", rideID)
	if after.Indoor || after.Description != row.Description || workout.PlannedSeconds(after.Steps) != workout.PlannedSeconds(row.Steps) {
		t.Error("a crew ride was converted or swapped")
	}
}
