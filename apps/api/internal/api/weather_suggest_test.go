package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Days of the harness forecast (Wed 2026-03-25 is "today", 08:00 local).
const (
	wWed = "2026-03-25"
	wThu = "2026-03-26"
	wFri = "2026-03-27"
	wSat = "2026-03-28"
)

// forecastByDay is four days from 2026-03-25: wet days are soaked, the others
// calm and mild.
func forecastByDay(wetDays ...string) string {
	wet := map[string]bool{}
	for _, d := range wetDays {
		wet[d] = true
	}
	start, _ := time.Parse("2006-01-02", wWed)
	var times []string
	var temp, prob, rain, wind, gust, code []float64
	for i := 0; i < 24*4; i++ {
		ts := start.Add(time.Duration(i) * time.Hour)
		times = append(times, ts.Format("2006-01-02T15:04"))
		isWet := wet[ts.Format("2006-01-02")]
		temp = append(temp, 14)
		wind = append(wind, 12)
		gust = append(gust, 20)
		if isWet {
			prob, rain, code = append(prob, 90), append(rain, 1.5), append(code, 63)
		} else {
			prob, rain, code = append(prob, 0), append(rain, 0), append(code, 1)
		}
	}
	raw, _ := json.Marshal(map[string]any{
		"utc_offset_seconds": 7200,
		"hourly": map[string]any{
			"time": times, "temperature_2m": temp, "precipitation_probability": prob,
			"precipitation": rain, "wind_speed_10m": wind, "wind_gusts_10m": gust, "weather_code": code,
		},
	})
	return string(raw)
}

type weatherOut struct {
	weatherPrefsOut
	Unavailable bool `json:"unavailable"`
	Days        []struct {
		Date    string `json:"date"`
		Bad     bool   `json:"bad"`
		Worst   string `json:"worst"`
		Summary struct {
			TempMin  float64 `json:"tempMin"`
			TempMax  float64 `json:"tempMax"`
			RainProb float64 `json:"rainProb"`
			GustMax  float64 `json:"gustMax"`
		} `json:"summary"`
		Reasons []string `json:"reasons"`
	} `json:"days"`
	Suggestions []struct {
		WorkoutID string   `json:"workoutId"`
		Date      string   `json:"date"`
		Reasons   []string `json:"reasons"`
		Worst     string   `json:"worst"`
		CanSwitch bool     `json:"canSwitch"`
		AltDate   string   `json:"altDate"`
	} `json:"suggestions"`
}

func (h *weatherHarness) suggestions(user, query string) (weatherOut, string) {
	h.t.Helper()
	resp := h.as(user, "cyclists", http.MethodGet, "/api/training/weather"+query, "")
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("GET weather: %d %s", resp.StatusCode, readAll(h.t, resp))
	}
	raw := readAll(h.t, resp)
	var out weatherOut
	if err := json.Unmarshal(raw, &out); err != nil {
		h.t.Fatalf("decode %s: %v", raw, err)
	}
	return out, string(raw)
}

func (h *weatherHarness) profile(rider string, smart bool, available ...string) {
	h.t.Helper()
	if _, err := h.training.SaveProfile(context.Background(), workout.RiderProfile{
		Rider: rider, FTPWatts: 250, SmartTrainer: smart, AvailableDays: available,
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *weatherHarness) planned(rider, name, date string, mutate ...func(*workout.CreateWorkoutRequest)) workout.Workout {
	h.t.Helper()
	req := workout.CreateWorkoutRequest{
		Rider: rider, Sport: model.SportCycling, Name: name, Date: date,
		Steps: []workout.WorkoutStep{{Name: "Ride", Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetOpen}},
	}
	for _, m := range mutate {
		m(&req)
	}
	w, err := h.training.CreateWorkout(context.Background(), req)
	if err != nil {
		h.t.Fatal(err)
	}
	return w
}

func TestSuggestionsForASmartTrainerRider(t *testing.T) {
	h := newWeatherHarness(t)
	h.seed("wilant", weatherPlace, 51.054321, 3.719876)
	h.profile("wilant", true)
	h.setForecast(http.StatusOK, forecastByDay(wThu))
	dry := h.planned("wilant", "Friday ride", wFri)
	wet := h.planned("wilant", "Thursday ride", wThu)

	out, raw := h.suggestions("wilant", "")
	if !out.Configured || out.Unavailable || out.Place != weatherPlace {
		t.Fatalf("response = %s", raw)
	}
	if len(out.Suggestions) != 1 {
		t.Fatalf("suggestions = %s", raw)
	}
	s := out.Suggestions[0]
	if s.WorkoutID != wet.ID || s.Date != wThu || !s.CanSwitch || s.AltDate != "" || s.Worst != "rain" || len(s.Reasons) == 0 {
		t.Errorf("suggestion = %+v", s)
	}
	if s.WorkoutID == dry.ID {
		t.Error("the dry day got one")
	}
	if !strings.Contains(out.Attribution, "Open-Meteo") || out.Window.Start != 9 || out.Window.End != 12 {
		t.Errorf("attribution/window: %s", raw)
	}

	// Four days from today, the wet one flagged with its summary.
	if len(out.Days) != 4 {
		t.Fatalf("days = %s", raw)
	}
	for _, d := range out.Days {
		if d.Bad != (d.Date == wThu) {
			t.Errorf("%s bad = %v", d.Date, d.Bad)
		}
	}
	if d := out.Days[1]; d.Worst != "rain" || d.Summary.RainProb != 90 || d.Summary.TempMax != 14 || len(d.Reasons) == 0 {
		t.Errorf("thursday = %+v", d)
	}

	// One request, exactly the documented query, coordinates rounded.
	if n := h.requests.Load(); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	q := h.queries[0]
	if q.Get("latitude") != "51.05" || q.Get("longitude") != "3.72" || q.Get("forecast_days") != "4" || q.Get("apikey") != weatherAPIKey {
		t.Errorf("query = %v", q)
	}
	if len(q) != 7 {
		t.Errorf("query has %d params, want exactly the 7 documented: %v", len(q), q)
	}

	// The cache serves the next page load.
	h.suggestions("wilant", "")
	if n := h.requests.Load(); n != 1 {
		t.Errorf("second GET made %d more request(s)", n-1)
	}
}

func TestSuggestionsForARiderWithoutASmartTrainerCanMoveOrJustKnow(t *testing.T) {
	h := newWeatherHarness(t)
	h.seed("wilant", weatherPlace, 51.05, 3.72)
	h.profile("wilant", false, "thu", "sat")
	h.setForecast(http.StatusOK, forecastByDay(wThu))
	h.planned("wilant", "Thursday ride", wThu)

	out, raw := h.suggestions("wilant", "")
	if len(out.Suggestions) != 1 {
		t.Fatalf("suggestions = %s", raw)
	}
	s := out.Suggestions[0]
	if s.CanSwitch {
		t.Error("canSwitch without the smart-trainer preference")
	}
	// Friday is dry but is not one of the rider's days; Saturday is.
	if s.AltDate != wSat {
		t.Errorf("altDate = %q, want %q", s.AltDate, wSat)
	}
}

func TestSuggestionsAreOnlyForPlannedUnriddenNonIndoorCyclingSessions(t *testing.T) {
	h := newWeatherHarness(t)
	h.seed("wilant", weatherPlace, 51.05, 3.72)
	h.profile("wilant", true)
	h.setForecast(http.StatusOK, forecastByDay(wWed, wThu, wFri, wSat))

	keep := h.planned("wilant", "Outdoor", wThu)
	converted := h.planned("wilant", "Indoor", wThu)
	yes := true
	if _, err := h.training.UpdateWorkout(context.Background(), converted.ID, workout.UpdateWorkoutRequest{Indoor: &yes}); err != nil {
		t.Fatal(err)
	}
	h.planned("wilant", "Run", wThu, func(r *workout.CreateWorkoutRequest) { r.Sport = model.SportRunning })
	h.planned("wilant", "FTP test", wThu, func(r *workout.CreateWorkoutRequest) { r.TestProtocol = "ramp" })
	h.planned("wilant", "Past", "2026-03-24")
	h.planned("wilant", "Beyond the horizon", "2026-03-29")
	ridden := h.planned("wilant", "Ridden today", wWed)
	if _, err := h.training.UpsertSession(context.Background(), workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ride-1", Sport: "cycling", Date: wWed, DurationSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}

	out, raw := h.suggestions("wilant", "")
	if len(out.Suggestions) != 1 || out.Suggestions[0].WorkoutID != keep.ID {
		t.Fatalf("suggestions = %s (ridden workout %s)", raw, ridden.ID)
	}
}

func TestSuggestionsNeverShowAnotherRidersSessions(t *testing.T) {
	h := newWeatherHarness(t)
	h.seed("wilant", weatherPlace, 51.05, 3.72)
	h.seed("sam", weatherPlace, 51.05, 3.72)
	h.profile("wilant", true)
	h.profile("sam", true)
	h.setForecast(http.StatusOK, forecastByDay(wThu))
	mine := h.planned("wilant", "Mine", wThu)
	theirs := h.planned("sam", "Theirs", wThu)

	out, raw := h.suggestions("wilant", "")
	if len(out.Suggestions) != 1 || out.Suggestions[0].WorkoutID != mine.ID {
		t.Fatalf("wilant sees %s", raw)
	}
	out, raw = h.suggestions("sam", "")
	if len(out.Suggestions) != 1 || out.Suggestions[0].WorkoutID != theirs.ID {
		t.Fatalf("sam sees %s", raw)
	}
	// Two riders in one town: the shared cache made one request.
	if n := h.requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestSuggestionsWithNoLocationMakeNoRequestAndReturnEmptyLists(t *testing.T) {
	h := newWeatherHarness(t)
	h.profile("wilant", true)
	h.planned("wilant", "Ride", wThu)

	out, raw := h.suggestions("wilant", "")
	if out.Configured || out.Unavailable {
		t.Errorf("response = %s", raw)
	}
	// Arrays, not null: the frontend iterates them.
	if !strings.Contains(raw, `"days":[]`) || !strings.Contains(raw, `"suggestions":[]`) {
		t.Errorf("want empty arrays, got %s", raw)
	}
	if !strings.Contains(raw, "Open-Meteo") {
		t.Errorf("attribution missing: %s", raw)
	}
	if n := h.requests.Load(); n != 0 {
		t.Fatalf("%d request(s) for a rider with no location", n)
	}
}

func TestOpenMeteoDownDegradesToAnEmptyButSuccessfulAnswer(t *testing.T) {
	h := newWeatherHarness(t)
	h.seed("wilant", weatherPlace, 51.054321, 3.719876)
	h.profile("wilant", true)
	h.planned("wilant", "Ride", wThu)

	// Warm the cache with a good forecast, then let it expire while the
	// service is down: no stale suggestion may be served.
	h.setForecast(http.StatusOK, forecastByDay(wThu))
	if out, _ := h.suggestions("wilant", ""); len(out.Suggestions) != 1 {
		t.Fatal("setup: no suggestion from the good forecast")
	}
	h.setNow(weatherNow.Add(61 * time.Minute))
	h.setForecast(http.StatusInternalServerError, `{"error":true}`)

	out, raw := h.suggestions("wilant", "")
	if !out.Configured || !out.Unavailable || len(out.Days) != 0 || len(out.Suggestions) != 0 {
		t.Fatalf("response = %s", raw)
	}
	if !strings.Contains(raw, `"days":[]`) || !strings.Contains(raw, `"suggestions":[]`) {
		t.Errorf("want empty arrays: %s", raw)
	}
	if out.Place != weatherPlace || !strings.Contains(out.Attribution, "Open-Meteo") {
		t.Errorf("prefs must still be reported: %s", raw)
	}

	logs := h.logs.String()
	if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "forecast unavailable") {
		t.Errorf("want a Warn about the failed fetch; logs:\n%s", logs)
	}
	for _, leak := range []string{"Ghent", "51.05", "3.72", "latitude", weatherAPIKey} {
		if strings.Contains(logs, leak) {
			t.Errorf("logs leak %q:\n%s", leak, logs)
		}
	}
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "forecast unavailable") && strings.Contains(line, "wilant") {
			t.Errorf("the failure line names the rider: %s", line)
		}
	}

	// It recovers by itself when the service does.
	h.setForecast(http.StatusOK, forecastByDay(wThu))
	if out, _ := h.suggestions("wilant", ""); out.Unavailable || len(out.Suggestions) != 1 {
		t.Errorf("did not recover: %+v", out)
	}
}

func TestSuggestionsFollowTheSmartTrainerPreference(t *testing.T) {
	h := newWeatherHarness(t)
	h.seed("wilant", weatherPlace, 51.05, 3.72)
	h.setForecast(http.StatusOK, forecastByDay(wThu))
	h.planned("wilant", "Ride", wThu)

	h.profile("wilant", false)
	if out, _ := h.suggestions("wilant", ""); out.Suggestions[0].CanSwitch {
		t.Error("canSwitch without the preference")
	}
	h.profile("wilant", true)
	if out, _ := h.suggestions("wilant", ""); !out.Suggestions[0].CanSwitch {
		t.Error("canSwitch not set after opting in to the trainer")
	}
}

func TestSuggestionsHonourTheTodayParamAndItsBounds(t *testing.T) {
	h := newWeatherHarness(t)
	h.seed("wilant", weatherPlace, 51.05, 3.72)
	h.profile("wilant", true)
	h.setForecast(http.StatusOK, forecastByDay(wWed, wThu, wFri, wSat))
	h.planned("wilant", "Wednesday", wWed)
	h.planned("wilant", "Thursday", wThu)

	// A browser already on Thursday: Wednesday is past and not a candidate.
	out, raw := h.suggestions("wilant", "?today="+wThu)
	if len(out.Suggestions) != 1 || out.Suggestions[0].Date != wThu {
		t.Fatalf("suggestions = %s", raw)
	}
	if len(out.Days) == 0 || out.Days[0].Date != wThu {
		t.Errorf("days should start at the caller's today: %s", raw)
	}

	for _, bad := range []string{"?today=2026-03-28", "?today=nonsense"} {
		resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/weather"+bad, "")
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", bad, resp.StatusCode)
		}
	}
}

func TestSuggestionsUseTheConfiguredZoneForTodayWhenNoParamIsSent(t *testing.T) {
	// 23:30 UTC on the 25th is already 00:30 on the 26th in Brussels.
	h := newWeatherHarness(t, func(s *api.Server) {
		s.Config.Training.Timezone = "Europe/Brussels"
	})
	h.setNow(time.Date(2026, 3, 25, 23, 30, 0, 0, time.UTC))
	h.seed("wilant", weatherPlace, 51.05, 3.72)
	h.profile("wilant", true)
	h.setForecast(http.StatusOK, forecastByDay(wWed, wThu, wFri, wSat))

	out, raw := h.suggestions("wilant", "")
	if len(out.Days) == 0 || out.Days[0].Date != wThu {
		t.Fatalf("today should be the 26th in Brussels: %s", raw)
	}
}
