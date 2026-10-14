package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type seasonResponse struct {
	Period struct {
		Kind  string `json:"kind"`
		Label string `json:"label"`
		From  string `json:"from"`
		To    string `json:"to"`
	} `json:"period"`
	Volume *struct {
		Rides       int     `json:"rides"`
		Seconds     float64 `json:"seconds"`
		DistanceM   float64 `json:"distanceM"`
		AscentM     float64 `json:"ascentM"`
		AscentRides int     `json:"ascentRides"`
		TSS         float64 `json:"tss"`
	} `json:"volume"`
	Fitness *struct {
		StartCTL *float64 `json:"startCtl"`
		PeakCTL  float64  `json:"peakCtl"`
		PeakDate string   `json:"peakDate"`
		EndCTL   *float64 `json:"endCtl"`
	} `json:"fitness"`
	FTP *struct {
		StartWatts    *float64 `json:"startWatts"`
		EndWatts      *float64 `json:"endWatts"`
		HistoryStarts string   `json:"historyStarts"`
		Changes       []struct {
			Date   string  `json:"date"`
			To     float64 `json:"to"`
			Source string  `json:"source"`
		} `json:"changes"`
	} `json:"ftp"`
	Plan *struct {
		Planned       int  `json:"planned"`
		Done          int  `json:"done"`
		CompliancePct *int `json:"compliancePct"`
	} `json:"plan"`
	BestEfforts []struct {
		SessionID string `json:"sessionId"`
		Metric    string `json:"metric"`
		Scope     string `json:"scope"`
	} `json:"bestEfforts"`
	Goals []struct {
		ID   string   `json:"id"`
		CTL  *float64 `json:"ctl"`
		Ride *struct {
			SessionID string `json:"sessionId"`
		} `json:"ride"`
	} `json:"goals"`
	Available struct {
		Years []string `json:"years"`
		Goals []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			From  string `json:"from"`
			To    string `json:"to"`
			Label string `json:"label"`
		} `json:"goals"`
	} `json:"available"`
}

func mustTime(t *testing.T, date string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatal(err)
	}
	return ts.Add(12 * time.Hour)
}

func (h *metricsSyncHarness) season(user, query string) (int, seasonResponse) {
	h.t.Helper()
	resp := h.as(user, "cyclists", http.MethodGet, "/api/training/season"+query, "")
	var out seasonResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			h.t.Fatal(err)
		}
	}
	return resp.StatusCode, out
}

// seedSeason gives wilant two seasons of data and a goal; the clock is
// 2026-09-10. Another rider has data of their own that must never show.
func seedSeason(t *testing.T, h *metricsSyncHarness) (goalID string) {
	t.Helper()
	ctx := context.Background()
	tr := h.training
	for _, s := range []workout.UpsertSessionRequest{
		{Rider: "wilant", Provider: "garmin", ExternalID: "a", Sport: "cycling", Date: "2025-12-31", DurationSeconds: 3600, DistanceM: 30000, AscentM: 300, TrainingLoad: 50},
		{Rider: "wilant", Provider: "garmin", ExternalID: "b", Sport: "cycling", Date: "2026-01-01", DurationSeconds: 7200, DistanceM: 60000, AscentM: 600, TrainingLoad: 90},
		{Rider: "wilant", Provider: "garmin", ExternalID: "c", Sport: "cycling", Date: "2026-06-14", DurationSeconds: 21600, DistanceM: 160000, TrainingLoad: 280},
		{Rider: "other", Provider: "garmin", ExternalID: "x", Sport: "cycling", Date: "2026-06-14", DurationSeconds: 99999, DistanceM: 999999, TrainingLoad: 999},
	} {
		if _, err := tr.UpsertSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	for _, rider := range []string{"wilant", "other"} {
		if err := tr.RecomputeFitnessSnapshots(ctx, rider); err != nil {
			t.Fatal(err)
		}
	}
	// FTP: one typed value last year, a test this year; and another rider's.
	h.setClock(mustTime(t, "2025-11-01"))
	if _, err := tr.SaveProfile(ctx, workout.RiderProfile{Rider: "wilant", FTPWatts: 250}); err != nil {
		t.Fatal(err)
	}
	h.setClock(mustTime(t, "2026-03-10"))
	if _, err := tr.SaveProfileFrom(ctx, workout.RiderProfile{Rider: "wilant", FTPWatts: 262}, workout.FTPSourceTest); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.SaveProfile(ctx, workout.RiderProfile{Rider: "other", FTPWatts: 400}); err != nil {
		t.Fatal(err)
	}
	h.fixClock()

	if _, err := tr.UpsertEfforts(ctx, "wilant", "garmin:c", "cycling", "2026-06-14", map[string]float64{"power_1200": 300}); err != nil {
		t.Fatal(err)
	}
	if err := tr.SetEffortAward(ctx, "garmin:c", "power_1200", "season", 280); err != nil {
		t.Fatal(err)
	}
	g, err := tr.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", Sport: model.SportCycling, EventDate: "2026-06-14", Priority: workout.PriorityA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Someday", Sport: model.SportCycling}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "other", Name: "Theirs", Sport: model.SportCycling, EventDate: "2026-08-01", Priority: workout.PriorityA}); err != nil {
		t.Fatal(err)
	}
	return g.ID
}

func TestSeasonByYearSummarisesTheRidersOwnPeriod(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	seedSeason(t, h)

	code, out := h.season("wilant", "?year=2026")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out.Period.Kind != "year" || out.Period.From != "2026-01-01" || out.Period.To != "2026-12-31" || out.Period.Label != "2026" {
		t.Errorf("period = %+v", out.Period)
	}
	v := out.Volume
	if v == nil || v.Rides != 2 || v.Seconds != 28800 || v.DistanceM != 220000 || v.TSS != 370 || v.AscentM != 600 || v.AscentRides != 1 {
		t.Errorf("volume = %+v, want the rider's two 2026 rides only (the 31 December ride and the other rider's are not in it)", v)
	}
	if out.FTP == nil || out.FTP.StartWatts == nil || *out.FTP.StartWatts != 250 || out.FTP.EndWatts == nil || *out.FTP.EndWatts != 262 ||
		len(out.FTP.Changes) != 1 || out.FTP.Changes[0].Source != "test" || out.FTP.Changes[0].Date != "2026-03-10" {
		t.Errorf("ftp = %+v", out.FTP)
	}
	if len(out.BestEfforts) != 1 || out.BestEfforts[0].Metric != "power_1200" || out.BestEfforts[0].Scope != "season" {
		t.Errorf("best efforts = %+v", out.BestEfforts)
	}
	if out.Fitness == nil || out.Fitness.PeakCTL <= 0 {
		t.Errorf("fitness = %+v", out.Fitness)
	}
	if len(out.Goals) != 1 || out.Goals[0].Ride == nil || out.Goals[0].Ride.SessionID != "garmin:c" || out.Goals[0].CTL == nil {
		t.Errorf("goals = %+v, want only wilant's Gran Fondo with its event-day ride", out.Goals)
	}
	if out.Plan != nil {
		t.Errorf("a rider with nothing planned has a plan section: %+v", out.Plan)
	}
}

func TestSeasonByGoalRunsFromTheGoalsCreationToItsEvent(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	goalID := seedSeason(t, h)
	// seedSeason's goal was created on the fixed clock, 2026-09-10, after its
	// event: that has no season.
	if code, _ := h.season("wilant", "?goal="+goalID); code != http.StatusBadRequest {
		t.Errorf("a goal created after its event: status %d, want 400", code)
	}

	// A goal created earlier does.
	h.setClock(mustTime(t, "2026-02-01"))
	g, err := h.training.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: "wilant", Name: "Spring", Sport: model.SportCycling, EventDate: "2026-06-14", Priority: workout.PriorityB})
	if err != nil {
		t.Fatal(err)
	}
	h.fixClock()
	code, out := h.season("wilant", "?goal="+g.ID)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out.Period.Kind != "goal" || out.Period.From != "2026-02-01" || out.Period.To != "2026-06-14" || out.Period.Label != "Spring" {
		t.Errorf("period = %+v", out.Period)
	}
	if out.Volume == nil || out.Volume.Rides != 1 {
		t.Errorf("volume = %+v, want the one ride between 1 February and the event day", out.Volume)
	}
}

func TestSeasonRefusesAMalformedPeriod(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	for _, q := range []string{"?year=abc", "?year=26", "?year=1800", "?year=2026&goal=x", "?goal="} {
		if code, _ := h.season("wilant", q); code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, code)
		}
	}
}

func TestSeasonDefaultsToTheCurrentYear(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	seedSeason(t, h)
	code, out := h.season("wilant", "")
	if code != http.StatusOK || out.Period.Label != "2026" {
		t.Errorf("default period = %d %+v, want 2026", code, out.Period)
	}
}

func TestSeasonGoalOfAnotherRiderOrUnknownIs404(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	seedSeason(t, h)
	theirs, _ := h.training.ListGoals(context.Background(), "other")
	if len(theirs) != 1 {
		t.Fatalf("other's goals = %+v", theirs)
	}
	if code, _ := h.season("wilant", "?goal="+theirs[0].ID); code != http.StatusNotFound {
		t.Errorf("another rider's goal: status %d, want 404", code)
	}
	if code, _ := h.season("wilant", "?goal=no-such-goal"); code != http.StatusNotFound {
		t.Errorf("unknown goal: status %d, want 404", code)
	}
	// An admin does not get to read it either: the endpoints are the rider's own.
	resp := h.as("boss", "admins", http.MethodGet, "/api/training/season?goal="+theirs[0].ID, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("admin on another rider's goal: status %d, want 404", resp.StatusCode)
	}
}

func TestSeasonListsTheYearsWithRidesAndTheGoalsWithAnEventDate(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	seedSeason(t, h)
	_, out := h.season("wilant", "")
	if len(out.Available.Years) != 2 || out.Available.Years[0] != "2026" || out.Available.Years[1] != "2025" {
		t.Errorf("years = %v, want 2026 and 2025, newest first", out.Available.Years)
	}
	// The Gran Fondo was created on the fixed clock, after its event, so it has no
	// season; "Someday" has no event date. Neither is offered.
	if len(out.Available.Goals) != 0 {
		t.Errorf("goals = %+v, want none: neither has a usable season", out.Available.Goals)
	}
	h.setClock(mustTime(t, "2026-02-01"))
	if _, err := h.training.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: "wilant", Name: "Spring", Sport: model.SportCycling, EventDate: "2026-06-14", Priority: workout.PriorityB}); err != nil {
		t.Fatal(err)
	}
	h.fixClock()
	_, out = h.season("wilant", "")
	if len(out.Available.Goals) != 1 || out.Available.Goals[0].Name != "Spring" || out.Available.Goals[0].From != "2026-02-01" || out.Available.Goals[0].To != "2026-06-14" {
		t.Errorf("goals = %+v", out.Available.Goals)
	}
}

func TestSeasonOfAnEmptyYearHasNoSectionsAndOfANewRiderStillAnswers(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	seedSeason(t, h)
	code, out := h.season("wilant", "?year=2019")
	if code != http.StatusOK || out.Volume != nil || out.Fitness != nil || out.FTP != nil || out.Plan != nil {
		t.Errorf("2019: %d %+v %+v %+v %+v", code, out.Volume, out.Fitness, out.FTP, out.Plan)
	}
	code, out = h.season("newcomer", "")
	if code != http.StatusOK || out.Volume != nil || len(out.Available.Years) != 1 {
		t.Errorf("newcomer: %d volume %+v years %v, want an answer with only the current year on offer", code, out.Volume, out.Available.Years)
	}
}

func TestSeasonRefusesAnAccountWithNoTrainingRole(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	resp := h.as("stranger", "nobody", http.MethodGet, "/api/training/season", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}
