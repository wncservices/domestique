package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/weather"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// This file is the rider's side of weather: opting in with a town, choosing the
// hours they ride, and opting out. What weather then *suggests* lives with the
// suggestions endpoint.
//
// Privacy shapes every line. A coordinate is personal data, so:
//   - nothing is fetched for a rider with no row in weather_locations;
//   - the row holds a town rounded to two decimals, and the API never returns
//     the coordinates (only the place name the rider picked);
//   - nothing here logs a coordinate, a place name, an API key, or a rider's
//     name beside a weather value. Counts and error class only.

// weatherAttribution is what Open-Meteo's CC-BY licence asks to be shown.
const weatherAttribution = "Weather data by Open-Meteo.com"

type weatherWindowDTO struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// weatherPrefsDTO deliberately has no coordinate fields.
type weatherPrefsDTO struct {
	Configured  bool             `json:"configured"`
	Place       string           `json:"place,omitempty"`
	Window      weatherWindowDTO `json:"window"`
	Attribution string           `json:"attribution"`
}

func weatherPrefsDTOFrom(p weather.Preference, ok bool) weatherPrefsDTO {
	dto := weatherPrefsDTO{
		Window:      weatherWindowDTO{Start: weather.DefaultWindowStart, End: weather.DefaultWindowEnd},
		Attribution: weatherAttribution,
	}
	if ok {
		dto.Configured = true
		dto.Place = p.Place
		dto.Window = weatherWindowDTO{Start: p.WindowStart, End: p.WindowEnd}
	}
	return dto
}

// weatherAvailable answers 412 when this deployment has no weather: switched
// off with weather.enabled: false, or built without the client and store. It
// logs at Warn with the reason, since a UI that shows a disabled feature's
// error with nothing server-side to match it is the failure the observability
// checklist exists for. No place, coordinate or rider in the line.
func (s *Server) weatherAvailable(w http.ResponseWriter) bool {
	switch {
	case s.Config != nil && !s.Config.Weather.On():
		s.logger().Warn("weather request refused: weather is disabled", "reason", "weather.enabled is false")
	case s.Weather == nil || s.WeatherPrefs == nil:
		s.logger().Warn("weather request refused: weather is not set up on this server", "reason", "no forecast client or preference store")
	default:
		return true
	}
	writeJSON(w, http.StatusPreconditionFailed, map[string]string{
		"error": "weather is not available on this deployment",
	})
	return false
}

// weatherRider is the caller after the checks every weather endpoint shares.
func (s *Server) weatherRider(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.require(w, r, auth.PermManageTraining) || !s.weatherAvailable(w) {
		return "", false
	}
	// The rider comes from the session, never the body: a body that names
	// someone else is simply not read.
	return auth.FromContext(r.Context()).User, true
}

type weatherSummaryDTO struct {
	TempMin  float64 `json:"tempMin"`
	TempMax  float64 `json:"tempMax"`
	RainProb float64 `json:"rainProb"`
	GustMax  float64 `json:"gustMax"`
}

type weatherDayDTO struct {
	Date    string            `json:"date"`
	Bad     bool              `json:"bad"`
	Summary weatherSummaryDTO `json:"summary"`
	Reasons []string          `json:"reasons"`
	// Worst is the most severe reason's code (thunder, wintry, rain, wind,
	// cold, heat), which is what picks the chip's icon. Empty for a good day.
	Worst string `json:"worst,omitempty"`
}

type weatherSuggestionDTO struct {
	WorkoutID string   `json:"workoutId"`
	Date      string   `json:"date"`
	Reasons   []string `json:"reasons"`
	Worst     string   `json:"worst,omitempty"`
	CanSwitch bool     `json:"canSwitch"`
	AltDate   string   `json:"altDate,omitempty"`
}

// weatherDTO is the rider's opt-in plus, when they have one, what the forecast
// says. Days and Suggestions are always arrays (never null), and the response
// carries no coordinates.
type weatherDTO struct {
	weatherPrefsDTO
	// Unavailable means the forecast could not be fetched: the lists are empty
	// and the Plan page simply renders without weather.
	Unavailable bool                   `json:"unavailable,omitempty"`
	Days        []weatherDayDTO        `json:"days"`
	Suggestions []weatherSuggestionDTO `json:"suggestions"`
}

// weatherToday is the caller's local date: ?today= when sent (bounded by
// parseTodayParam, the browser knows its own day), otherwise the date in the
// configured training zone, since the server's process zone is no guarantee.
func (s *Server) weatherToday(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.URL.Query().Get("today") != "" {
		t, ok := parseTodayParam(w, r, s.now())
		return t.Format(dateFormat), ok
	}
	zone := config.DefaultSyncTimezone
	if s.Config != nil && s.Config.Training.Timezone != "" {
		zone = s.Config.Training.Timezone
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		// config.Validate rejects a bad zone at startup; a Server built in
		// code with one falls back to UTC rather than failing the page.
		loc = time.UTC
	}
	return s.now().In(loc).Format(dateFormat), true
}

// handleGetWeather reports the rider's weather opt-in and, when they have one,
// the next four days and a suggestion for each planned session the forecast
// makes a bad idea. It never returns coordinates and makes no request to
// Open-Meteo for a rider with no saved town.
//
// It only suggests. A failed forecast is a 200 with unavailable: true and empty
// lists, never an error, and never a stale forecast.
func (s *Server) handleGetWeather(w http.ResponseWriter, r *http.Request) {
	rider, ok := s.weatherRider(w, r)
	if !ok {
		return
	}
	today, ok := s.weatherToday(w, r)
	if !ok {
		return
	}
	pref, found, err := s.WeatherPrefs.Get(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := weatherDTO{
		weatherPrefsDTO: weatherPrefsDTOFrom(pref, found),
		Days:            []weatherDayDTO{},
		Suggestions:     []weatherSuggestionDTO{},
	}
	if !found {
		// The normal state of a rider who has not opted in, not a fault, so
		// not a Warn: this fires on every plan-page load for them.
		s.logger().Debug("weather not requested: this rider has no saved town")
		writeJSON(w, http.StatusOK, out)
		return
	}

	forecast, err := s.Weather.Forecast(r.Context(), pref.Location())
	if err != nil {
		// The error is built without the request URL, so neither coordinates
		// nor the API key are in it; and no rider beside the failure.
		s.logger().Warn("weather forecast unavailable", "err", err)
		out.Unavailable = true
		writeJSON(w, http.StatusOK, out)
		return
	}

	opts := weather.Options{
		StartHour: pref.WindowStart, EndHour: pref.WindowEnd,
		Today: today, Now: s.now(),
	}
	var workouts []workout.Workout
	var ridden map[string]bool
	if s.Training != nil {
		profile, _, err := s.Training.GetProfile(r.Context(), rider)
		if err != nil {
			s.fail(w, err)
			return
		}
		opts.SmartTrainer, opts.AvailableDays = profile.SmartTrainer, profile.AvailableDays
		if workouts, err = s.Training.ListWorkouts(r.Context(), rider); err != nil {
			s.fail(w, err)
			return
		}
		if ridden, err = s.riddenToday(r.Context(), rider, today, workouts); err != nil {
			s.fail(w, err)
			return
		}
		if opts.Blackout, err = s.blackoutFor(r.Context(), rider); err != nil {
			s.fail(w, err)
			return
		}
	}

	for _, d := range weather.Days(forecast, opts) {
		v := d.Verdict
		out.Days = append(out.Days, weatherDayDTO{
			Date: d.Date, Bad: v.Bad, Reasons: nonNilStrings(v.Reasons), Worst: weather.WorstCode(v.Codes),
			Summary: weatherSummaryDTO{TempMin: v.TempMin, TempMax: v.TempMax, RainProb: v.RainProbMax, GustMax: v.GustMax},
		})
	}
	for _, sg := range weather.Suggest(forecast, workouts, ridden, opts) {
		out.Suggestions = append(out.Suggestions, weatherSuggestionDTO{
			WorkoutID: sg.WorkoutID, Date: sg.Date, Reasons: nonNilStrings(sg.Reasons),
			Worst: weather.WorstCode(sg.Codes), CanSwitch: sg.CanSwitch, AltDate: sg.AltDate,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func (s *Server) weatherStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, weather.ErrNoLocation):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, weather.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		s.fail(w, err)
	}
}

// handleSetWeatherLocation opts the rider in, or moves them. The town comes
// from the existing geocoder search in the browser; the server rounds it.
func (s *Server) handleSetWeatherLocation(w http.ResponseWriter, r *http.Request) {
	rider, ok := s.weatherRider(w, r)
	if !ok {
		return
	}
	var body struct {
		Place string   `json:"place"`
		Lat   *float64 `json:"lat"`
		Lon   *float64 `json:"lon"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.Lat == nil || body.Lon == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "lat and lon are required"})
		return
	}
	if err := s.WeatherPrefs.SetLocation(r.Context(), rider, body.Place, *body.Lat, *body.Lon); err != nil {
		s.weatherStoreError(w, err)
		return
	}
	// No rider, place or coordinate: this line says only that it happened.
	s.logger().Info("weather location saved")
	pref, found, err := s.WeatherPrefs.Get(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, weatherPrefsDTOFrom(pref, found))
}

// handleSetWeatherWindow sets the hours the rider usually rides.
func (s *Server) handleSetWeatherWindow(w http.ResponseWriter, r *http.Request) {
	rider, ok := s.weatherRider(w, r)
	if !ok {
		return
	}
	var body struct {
		Start *int `json:"start"`
		End   *int `json:"end"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.Start == nil || body.End == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "start and end are required"})
		return
	}
	if err := s.WeatherPrefs.SetWindow(r.Context(), rider, *body.Start, *body.End); err != nil {
		s.weatherStoreError(w, err)
		return
	}
	pref, found, err := s.WeatherPrefs.Get(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, weatherPrefsDTOFrom(pref, found))
}

// handleDeleteWeatherLocation is "Stop using weather": the row goes, and with
// it every reason to contact Open-Meteo for this rider. Idempotent.
func (s *Server) handleDeleteWeatherLocation(w http.ResponseWriter, r *http.Request) {
	rider, ok := s.weatherRider(w, r)
	if !ok {
		return
	}
	if err := s.WeatherPrefs.Delete(r.Context(), rider); err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("weather location removed")
	writeJSON(w, http.StatusOK, weatherPrefsDTOFrom(weather.Preference{}, false))
}

// warmWeather fetches the forecast once per distinct rounded location among
// opted-in riders, so the first page load after a sync is a cache hit. Two
// riders in one town cost one request; a rider with no location costs none.
//
// Best effort by design: a failure logs a Warn and the sync carries on, since
// weather is an optional convenience and the wellness pass must not be undone
// by a forecast outage. ctx is the sync's own, so a shutdown cuts it short.
func (s *Server) warmWeather(ctx context.Context) {
	if s.WeatherPrefs == nil {
		return
	}
	prefs, err := s.WeatherPrefs.List(ctx)
	if err != nil {
		s.logger().Warn("weather warm-up: listing opted-in riders failed", "err", err)
		return
	}
	if len(prefs) == 0 {
		return
	}
	if (s.Config != nil && !s.Config.Weather.On()) || s.Weather == nil {
		s.logger().Warn("weather warm-up skipped: weather is disabled", "riders", len(prefs))
		return
	}

	seen := map[weather.Location]bool{}
	fetched, failed := 0, 0
	for _, p := range prefs {
		if ctx.Err() != nil {
			return
		}
		loc := weather.RoundLocation(p.Location())
		if seen[loc] {
			continue
		}
		seen[loc] = true
		if _, err := s.Weather.Forecast(ctx, loc); err != nil {
			// err is built without the request URL (see weather.Client), so
			// neither the coordinates nor the API key reach the log.
			s.logger().Warn("weather warm-up: forecast fetch failed", "err", err)
			failed++
			continue
		}
		fetched++
	}
	s.logger().Info("weather warm-up finished", "locations", len(seen), "fetched", fetched, "failed", failed)
}
