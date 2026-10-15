package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func upsert(rider, ext, date string) workout.UpsertSessionRequest {
	return workout.UpsertSessionRequest{Rider: rider, Provider: "garmin", ExternalID: ext, Sport: "cycling", Date: date, DurationSeconds: 3600, TrainingLoad: 50}
}

func saAnalysis(rider, session string) workout.SessionAnalysis {
	return workout.SessionAnalysis{SessionID: session, Rider: rider, Outcome: "unplanned", LoadSource: "fit_power"}
}

type effortCell struct {
	Value     float64 `json:"value"`
	Date      string  `json:"date"`
	SessionID string  `json:"sessionId"`
}

type effortsResponse struct {
	Sport   string `json:"sport"`
	Metrics []struct {
		Metric  string      `json:"metric"`
		Label   string      `json:"label"`
		Unit    string      `json:"unit"`
		AllTime *effortCell `json:"allTime"`
		Season  *effortCell `json:"season"`
		Last42  *effortCell `json:"last42"`
	} `json:"metrics"`
	Curve struct {
		Season     []curvePoint `json:"season"`
		LastSeason []curvePoint `json:"lastSeason"`
		AllTime    []curvePoint `json:"allTime"`
	} `json:"curve"`
	NewBests []struct {
		SessionID string `json:"sessionId"`
		Metric    string `json:"metric"`
		Scope     string `json:"scope"`
	} `json:"newBests"`
}

type curvePoint struct {
	Seconds int     `json:"seconds"`
	Watts   float64 `json:"watts"`
}

func (h *metricsSyncHarness) efforts(user, query string) (int, effortsResponse) {
	h.t.Helper()
	resp := h.as(user, "cyclists", http.MethodGet, "/api/training/best-efforts"+query, "")
	var out effortsResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			h.t.Fatal(err)
		}
	}
	return resp.StatusCode, out
}

func (r effortsResponse) row(metric string) (e struct {
	Metric  string      `json:"metric"`
	Label   string      `json:"label"`
	Unit    string      `json:"unit"`
	AllTime *effortCell `json:"allTime"`
	Season  *effortCell `json:"season"`
	Last42  *effortCell `json:"last42"`
}, ok bool) {
	for _, m := range r.Metrics {
		if m.Metric == metric {
			return m, true
		}
	}
	return e, false
}

// seedEfforts gives wilant a record book spread over three years (the clock is
// 2026-09-10), and another rider a better one that must never show.
func seedEfforts(t *testing.T, h *metricsSyncHarness) {
	t.Helper()
	ctx := context.Background()
	put := func(rider, session, sport, date string, efforts map[string]float64) {
		t.Helper()
		if _, err := h.training.UpsertEfforts(ctx, rider, session, sport, date, efforts); err != nil {
			t.Fatal(err)
		}
	}
	put("wilant", "old", "cycling", "2024-03-01", map[string]float64{"power_1200": 330, "power_300": 380, "max_hr": 186})
	put("wilant", "last", "cycling", "2025-06-01", map[string]float64{"power_1200": 310, "power_300": 360})
	put("wilant", "jan", "cycling", "2026-02-01", map[string]float64{"power_1200": 280})
	put("wilant", "may", "cycling", "2026-05-05", map[string]float64{"power_1200": 300, "max_hr": 181})
	put("wilant", "recent", "cycling", "2026-09-01", map[string]float64{"power_1200": 290})
	put("wilant", "run", "running", "2026-08-01", map[string]float64{"speed_1200": 3.4, "max_hr": 192})
	put("other", "theirs", "cycling", "2026-09-02", map[string]float64{"power_1200": 500, "power_300": 700, "max_hr": 205})
}

func TestBestEffortsReturnsAllTimeSeasonAndLast42WithDateAndRide(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	seedEfforts(t, h)

	code, out := h.efforts("wilant", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out.Sport != "cycling" {
		t.Errorf("sport = %q, want cycling by default", out.Sport)
	}
	row, ok := out.row("power_1200")
	if !ok || row.Label != "20-min power" || row.Unit != "W" {
		t.Fatalf("row = %+v %v", row, ok)
	}
	if row.AllTime == nil || *row.AllTime != (effortCell{330, "2024-03-01", "old"}) {
		t.Errorf("all time = %+v", row.AllTime)
	}
	if row.Season == nil || *row.Season != (effortCell{300, "2026-05-05", "may"}) {
		t.Errorf("season = %+v, want the best of 2026", row.Season)
	}
	if row.Last42 == nil || *row.Last42 != (effortCell{290, "2026-09-01", "recent"}) {
		t.Errorf("last 42 = %+v", row.Last42)
	}
	// A metric the rider has no value for is a row with empty cells, not a zero.
	if empty, ok := out.row("power_3600"); !ok || empty.AllTime != nil || empty.Season != nil || empty.Last42 != nil {
		t.Errorf("power_3600 = %+v %v, want a row with no cells", empty, ok)
	}
	// Running metrics belong to the other switch; max HR is on both.
	if _, ok := out.row("speed_1200"); ok {
		t.Error("a running metric on the cycling card")
	}
	if hr, ok := out.row("max_hr"); !ok || hr.AllTime == nil || hr.AllTime.Value != 192 {
		t.Errorf("max_hr = %+v %v, want any sport's best (the run's 192)", hr, ok)
	}
	// Nothing of the other rider's.
	for _, m := range out.Metrics {
		for _, c := range []*effortCell{m.AllTime, m.Season, m.Last42} {
			if c != nil && c.SessionID == "theirs" {
				t.Errorf("another rider's ride appeared in %s", m.Metric)
			}
		}
	}
}

func TestBestEffortsCurveHasThisSeasonLastSeasonAndAllTime(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	seedEfforts(t, h)

	_, out := h.efforts("wilant", "?sport=cycling")
	find := func(points []curvePoint, seconds int) (float64, bool) {
		for _, p := range points {
			if p.Seconds == seconds {
				return p.Watts, true
			}
		}
		return 0, false
	}
	if w, ok := find(out.Curve.Season, 1200); !ok || w != 300 {
		t.Errorf("season 20 min = %v %v", w, ok)
	}
	if _, ok := find(out.Curve.Season, 300); ok {
		t.Error("the season has no 5-minute value but the curve has a point for it")
	}
	if w, ok := find(out.Curve.LastSeason, 300); !ok || w != 360 {
		t.Errorf("last season 5 min = %v %v", w, ok)
	}
	if w, ok := find(out.Curve.AllTime, 300); !ok || w != 380 {
		t.Errorf("all-time 5 min = %v %v", w, ok)
	}
	if w, _ := find(out.Curve.AllTime, 1200); w != 330 {
		t.Errorf("all-time 20 min = %v, want the envelope 330", w)
	}
	for i := 1; i < len(out.Curve.AllTime); i++ {
		if out.Curve.AllTime[i-1].Seconds >= out.Curve.AllTime[i].Seconds {
			t.Errorf("curve not in duration order: %+v", out.Curve.AllTime)
		}
	}
}

func TestBestEffortsRunningSwitchAndBadSport(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	seedEfforts(t, h)

	code, out := h.efforts("wilant", "?sport=running")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if row, ok := out.row("speed_1200"); !ok || row.AllTime == nil || row.AllTime.Value != 3.4 {
		t.Errorf("speed_1200 = %+v %v", row, ok)
	}
	if _, ok := out.row("power_1200"); ok {
		t.Error("a cycling metric on the running card")
	}
	if len(out.Curve.AllTime) != 0 || len(out.Curve.Season) != 0 {
		t.Errorf("a run has no power curve: %+v", out.Curve)
	}
	if code, _ := h.efforts("wilant", "?sport=swimming"); code != http.StatusBadRequest {
		t.Errorf("sport=swimming status = %d, want 400", code)
	}
}

func TestBestEffortsForARiderWithNoRidesIsAnEmptyCardNotAnError(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	code, out := h.efforts("newcomer", "")
	if code != http.StatusOK || len(out.Metrics) == 0 {
		t.Fatalf("status %d, %d metrics", code, len(out.Metrics))
	}
	for _, m := range out.Metrics {
		if m.AllTime != nil || m.Season != nil || m.Last42 != nil {
			t.Errorf("%s has data for a rider with none", m.Metric)
		}
	}
	resp := h.as("newcomer", "cyclists", http.MethodGet, "/api/training/best-efforts", "")
	var raw map[string]json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	if !strings.HasPrefix(string(raw["curve"]), "{") || !strings.Contains(string(raw["curve"]), `"season":[]`) {
		t.Errorf("curve = %s, want empty arrays", raw["curve"])
	}
}

func TestBestEffortsRefusesAnAccountWithNoTrainingRole(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	resp := h.as("stranger", "nobody", http.MethodGet, "/api/training/best-efforts", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestNewBestsAreListedUntilTheRiderAnnouncesThem(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	seedEfforts(t, h)
	ctx := context.Background()
	if err := h.training.SetEffortAward(ctx, "recent", "power_1200", "season", 280); err != nil {
		t.Fatal(err)
	}
	if err := h.training.SetEffortAward(ctx, "theirs", "power_1200", "all_time", 400); err != nil {
		t.Fatal(err)
	}

	_, out := h.efforts("wilant", "")
	if len(out.NewBests) != 1 || out.NewBests[0].SessionID != "recent" || out.NewBests[0].Scope != "season" {
		t.Fatalf("newBests = %+v, want only wilant's own", out.NewBests)
	}

	// Another rider naming wilant's award announces nothing.
	h.as("other", "cyclists", http.MethodPost, "/api/training/best-efforts/announce", `{"awards":[{"sessionId":"recent","metric":"power_1200"}]}`)
	if _, again := h.efforts("wilant", ""); len(again.NewBests) != 1 {
		t.Fatalf("another rider announced wilant's award: %+v", again.NewBests)
	}

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/best-efforts/announce", `{"awards":[{"sessionId":"recent","metric":"power_1200"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("announce status = %d", resp.StatusCode)
	}
	if _, after := h.efforts("wilant", ""); len(after.NewBests) != 0 {
		t.Errorf("an announced award is still listed: %+v", after.NewBests)
	}
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/best-efforts/announce", `{"awards":`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a broken body = %d, want 400", resp.StatusCode)
	}
}

func TestAnalysedRidesCarryTheirAwardsOnTheFitnessResponse(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	ctx := context.Background()
	s, err := h.training.UpsertSession(ctx, upsert("wilant", "g1", "2026-09-08"))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := h.training.UpsertSession(ctx, upsert("wilant", "g2", "2026-09-07"))
	for _, id := range []string{s.ID, plain.ID} {
		if err := h.training.SaveAnalysis(ctx, saAnalysis("wilant", id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.training.UpsertEfforts(ctx, "wilant", s.ID, "cycling", "2026-09-08", map[string]float64{"power_1200": 300, "max_hr": 180}); err != nil {
		t.Fatal(err)
	}
	_ = h.training.SetEffortAward(ctx, s.ID, "power_1200", "all_time", 290)
	_ = h.training.SetEffortAward(ctx, s.ID, "max_hr", "season", 178)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/fitness", "")
	var out struct {
		Sessions []struct {
			ID       string `json:"id"`
			Analysis *struct {
				Awards []struct {
					Metric string `json:"metric"`
					Label  string `json:"label"`
					Scope  string `json:"scope"`
				} `json:"awards"`
			} `json:"analysis"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	for _, sess := range out.Sessions {
		switch sess.ID {
		case s.ID:
			if sess.Analysis == nil || len(sess.Analysis.Awards) != 2 {
				t.Fatalf("awards = %+v, want two", sess.Analysis)
			}
			a := sess.Analysis.Awards[0]
			if a.Metric != "power_1200" || a.Label != "20-min power" || a.Scope != "all_time" {
				t.Errorf("first award = %+v, want the all-time 20 minutes first (display order)", a)
			}
		case plain.ID:
			if sess.Analysis == nil || len(sess.Analysis.Awards) != 0 {
				t.Errorf("a ride with no award has %+v", sess.Analysis)
			}
		}
	}
}

func TestBestEffortReadsLogNoValueAndNoRiderNameBesideOne(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	logs := &bytes.Buffer{}
	h.srv.Log = slog.New(slog.NewTextHandler(logs, nil))
	seedEfforts(t, h)
	h.efforts("wilant", "")
	h.as("wilant", "cyclists", http.MethodGet, "/api/training/season?year=2026", "")
	for _, leak := range []string{"330", "380", "186", "300"} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("the log carries %s:\n%s", leak, logs.String())
		}
	}
}
