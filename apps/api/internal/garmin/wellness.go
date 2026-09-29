package garmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// dailySummaryPath is Connect's own wellness-widget endpoint — confirmed
// against github.com/cyberjunky/python-garminconnect's get_stats (the
// UserSummaryDTO shape its typed.py decodes), the same reverse-engineered
// reference this package already cites for Activities and Profile. Unlike
// those two, this path has not been exercised against a live account as
// part of this change — best-effort in the same grey-area sense the
// package's own doc comment already states for all of Connect's
// unofficial API. A wrong field name here fails closed: RestingHeartRate
// returns 0, the same "no reading" value a real night without the watch
// worn would produce, never a bogus number.
const dailySummaryPath = "/usersummary-service/usersummary/daily"

// rhrStatsPath is Connect's wellness-stats endpoint, which python-
// garminconnect's get_rhr_day queries with fromDate=untilDate and
// metricId=60; the reading is allMetrics.metricsMap.
// WELLNESS_RESTING_HEART_RATE[0].value. It is the source Connect's own
// resting-heart-rate chart reads, so it has a value on days the daily
// summary's restingHeartRate is null.
const rhrStatsPath = "/userstats-service/wellness/daily"

// Plausible resting heart rates, in bpm. A value outside is treated as no
// reading rather than stored: Connect uses 0 and sentinel-like numbers for
// "unknown", and a bogus 0 or 300 would poison the rider's HR zones.
const (
	minPlausibleRestingHR = 25
	maxPlausibleRestingHR = 120
)

// plausibleRestingHR turns a decoded number into a bpm, or 0 if it is
// missing, non-numeric or implausible.
func plausibleRestingHR(n json.Number) int {
	if n == "" {
		return 0
	}
	f, err := n.Float64()
	if err != nil {
		return 0
	}
	bpm := int(math.Round(f))
	if bpm < minPlausibleRestingHR || bpm > maxPlausibleRestingHR {
		return 0
	}
	return bpm
}

// statusError is a non-200 answer, kept typed so Wellness can name the HTTP
// status of a real failure in Partial. A status code is not health data.
type statusError struct {
	what   string
	status int
	body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("garmin: the %s request returned %d: %s", e.what, e.status, e.body)
}

// partialName is the Partial entry for a failed signal: "hrv:500" when the
// endpoint answered with a status, the bare name for anything else
// (transport error, no display name).
func partialName(name string, err error) string {
	var se *statusError
	if errors.As(err, &se) {
		return fmt.Sprintf("%s:%d", name, se.status)
	}
	var ue *unreadableError
	if errors.As(err, &ue) {
		return fmt.Sprintf("%s:%d", name, ue.status)
	}
	return name
}

// unreadableError is a 200 with content that was not the JSON expected.
type unreadableError struct {
	what   string
	status int
	body   string
}

func (e *unreadableError) Error() string {
	return fmt.Sprintf("garmin: unreadable %s response: %s", e.what, e.body)
}

// noReading is true for the answers that mean "Connect holds nothing for
// this date" rather than "the request failed": 204, 404 or an empty body.
// Many devices simply do not record HRV (Edge head units, some watches), and
// a day nothing was processed for answers the same way.
func noReading(status int, raw []byte) bool {
	if status == http.StatusNoContent || status == http.StatusNotFound {
		return true
	}
	return status == http.StatusOK && len(strings.TrimSpace(string(raw))) == 0
}

// RHRDiagnostics collects, across the dates one sync tries, which resting
// heart rate sources came back empty and which top-level key names the daily
// summary carried. Key names and source names only, never values: health
// data does not belong in a log line, but the key names are what shows
// whether Garmin renamed a field.
type RHRDiagnostics struct {
	mu      sync.Mutex
	sources []string
	keys    []string
}

type rhrDiagKey struct{}

// WithRHRDiagnostics returns a context that makes RestingHeartRate record
// what it saw into the returned collector.
func WithRHRDiagnostics(ctx context.Context) (context.Context, *RHRDiagnostics) {
	d := &RHRDiagnostics{}
	return context.WithValue(ctx, rhrDiagKey{}, d), d
}

// Note records an empty source and, for the daily summary, its key names.
// Safe on a nil receiver so callers need no check.
func (d *RHRDiagnostics) Note(source string, keys []string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sources = addUnique(d.sources, source)
	for _, k := range keys {
		d.keys = addUnique(d.keys, k)
	}
	sort.Strings(d.keys)
}

// Sources returns the empty sources noted, in the order first seen.
func (d *RHRDiagnostics) Sources() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.sources...)
}

// SummaryKeys returns the daily summary's top-level key names, sorted.
func (d *RHRDiagnostics) SummaryKeys() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.keys...)
}

func addUnique(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

// sleepRHRSource yields the sleep response's top-level restingHeartRate.
// RestingHeartRate fetches it on demand; Wellness hands over the response it
// already fetched, so the fallback costs no second sleep request.
type sleepRHRSource func(ctx context.Context) (int, error)

// RestingHeartRate returns the rider's resting heart rate for one calendar
// date, as Connect's own wellness pipeline recorded it from a worn device
// overnight — not a workout's average heart rate, which this same account
// may also report and which is a much higher, different number entirely.
//
// Sources are tried in order, stopping at the first plausible value: the
// daily summary's restingHeartRate (null or 0 on some accounts, which is why
// production never filled it), the dedicated wellness-stats endpoint, then
// the top-level restingHeartRate of the sleep response. Shapes are from
// python-garminconnect's get_user_summary, get_rhr_day and get_sleep_data.
//
// 0 with a nil error means no source had a reading for that date (the watch
// was not worn, or Connect has not processed it yet), which is a normal
// day and not an error to warn about — see the call site in
// garminBiometrics, which tries a small run of recent dates and logs the
// diagnostics from WithRHRDiagnostics once if all of them are empty. Only the
// daily summary's own failure is returned as an error; a failing fallback is
// noted in the diagnostics and the next source is tried.
func (c *Client) RestingHeartRate(ctx context.Context, date time.Time) (int, error) {
	if c.session.DisplayName == "" {
		return 0, errors.New("garmin: no display name on the stored session to ask the wellness endpoint for — reconnect to fetch one")
	}

	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return 0, err
	}

	return c.restingHeartRate(ctx, bearer, date, func(ctx context.Context) (int, error) {
		var w Wellness
		if err := c.fetchSleep(ctx, bearer, date, &w); err != nil {
			return 0, err
		}
		return w.sleepRestingHR, nil
	})
}

func (c *Client) restingHeartRate(ctx context.Context, bearer string, date time.Time, fromSleep sleepRHRSource) (int, error) {
	diag, _ := ctx.Value(rhrDiagKey{}).(*RHRDiagnostics)
	day := date.Format("2006-01-02")

	// 1. The daily summary.
	url := fmt.Sprintf("%s%s/%s?calendarDate=%s", c.APIBase, dailySummaryPath, c.session.DisplayName, day)
	raw, status, err := c.do(ctx, http.MethodGet, url, nil, "", c.wellnessHeaders(bearer)...)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, &statusError{"daily summary", status, snippet(raw)}
	}
	var summary map[string]json.RawMessage
	if err := json.Unmarshal(raw, &summary); err != nil {
		return 0, &unreadableError{"daily summary", status, snippet(raw)}
	}
	var n json.Number
	if v, ok := summary["restingHeartRate"]; ok && string(v) != "null" {
		// A non-numeric value is no reading, not a failure.
		_ = json.Unmarshal(v, &n)
	}
	if bpm := plausibleRestingHR(n); bpm > 0 {
		return bpm, nil
	}
	keys := make([]string, 0, len(summary))
	for k := range summary {
		keys = append(keys, k)
	}
	diag.Note("daily_summary", keys)

	// 2. The dedicated resting heart rate endpoint.
	if bpm, err := c.restingFromStats(ctx, bearer, day); bpm > 0 {
		return bpm, nil
	} else if err != nil {
		diag.Note(partialName("rhr_endpoint", err), nil)
	} else {
		diag.Note("rhr_endpoint", nil)
	}

	// 3. The sleep response's top-level value.
	if bpm, err := fromSleep(ctx); bpm > 0 {
		return bpm, nil
	} else if err != nil {
		diag.Note(partialName("sleep", err), nil)
	} else {
		diag.Note("sleep", nil)
	}
	return 0, nil
}

func (c *Client) restingFromStats(ctx context.Context, bearer, day string) (int, error) {
	url := fmt.Sprintf("%s%s/%s?fromDate=%s&untilDate=%s&metricId=60", c.APIBase, rhrStatsPath, c.session.DisplayName, day, day)
	raw, status, err := c.do(ctx, http.MethodGet, url, nil, "", c.wellnessHeaders(bearer)...)
	if err != nil {
		return 0, err
	}
	if noReading(status, raw) {
		return 0, nil
	}
	if status != http.StatusOK {
		return 0, &statusError{"resting heart rate", status, snippet(raw)}
	}
	var dto struct {
		AllMetrics struct {
			MetricsMap map[string][]struct {
				Value json.Number `json:"value"`
			} `json:"metricsMap"`
		} `json:"allMetrics"`
	}
	if err := json.Unmarshal(raw, &dto); err != nil {
		return 0, &unreadableError{"resting heart rate", status, snippet(raw)}
	}
	rows := dto.AllMetrics.MetricsMap["WELLNESS_RESTING_HEART_RATE"]
	if len(rows) == 0 {
		return 0, nil
	}
	return plausibleRestingHR(rows[0].Value), nil
}

func (c *Client) wellnessHeaders(bearer string) []header {
	return []header{
		{"Authorization", "Bearer " + bearer},
		{"Accept", "application/json"},
		{"X-Requested-With", "XMLHttpRequest"},
	}
}

// hrvPath is Connect's HRV summary endpoint. Response shape confirmed
// against python-garminconnect's get_hrv_data (unverified against a live
// account, the same caveat dailySummaryPath's own comment states) — an
// hrvSummary object carrying lastNightAvg, weeklyAvg and a status string
// (BALANCED, UNBALANCED, LOW, POOR).
const hrvPath = "/hrv-service/hrv"

type hrvResponseDTO struct {
	HRVSummary *struct {
		LastNightAvg float64 `json:"lastNightAvg"`
		WeeklyAvg    float64 `json:"weeklyAvg"`
		Status       string  `json:"status"`
	} `json:"hrvSummary"`
}

// sleepPath is Connect's daily sleep endpoint, keyed by display name like
// dailySummaryPath rather than by date alone. Response shape confirmed
// against python-garminconnect's get_sleep_data (same live-account caveat):
// a dailySleepDTO carrying sleepTimeSeconds and a nested sleepScores.overall
// value out of 100.
const sleepPath = "/wellness-service/wellness/dailySleepData"

type dailySleepResponseDTO struct {
	// RestingHeartRate is the sleep response's own top-level resting heart
	// rate, the third source RestingHeartRate falls back to.
	RestingHeartRate json.Number `json:"restingHeartRate"`
	DailySleepDTO    struct {
		SleepTimeSeconds int `json:"sleepTimeSeconds"`
		SleepScores      struct {
			Overall struct {
				Value int `json:"value"`
			} `json:"overall"`
		} `json:"sleepScores"`
	} `json:"dailySleepDTO"`
}

// trainingReadinessPath is Connect's Training Readiness endpoint. Response
// shape confirmed against python-garminconnect's get_training_readiness
// (same live-account caveat): a JSON array, most recent reading first, whose
// first element carries score and level (POOR, LOW, MODERATE, HIGH). An
// empty array means Connect has no reading for that date — the same "no
// reading" case dailySummaryPath documents for resting heart rate — not an
// error.
const trainingReadinessPath = "/metrics-service/metrics/trainingreadiness"

type trainingReadinessEntryDTO struct {
	Score int    `json:"score"`
	Level string `json:"level"`
}

// Wellness is one day's recovery signals from Garmin Connect: HRV, sleep,
// Garmin's own Training Readiness verdict and resting heart rate. Each is
// fetched independently, so one endpoint failing (a 500, an unreadable
// body, or simply no reading for that day) leaves only that field at its
// zero value — the same "0 means no reading" contract RestingHeartRate's
// own doc comment already states, extended to every signal here.
type Wellness struct {
	Date string

	HRVLastNight float64
	HRVWeeklyAvg float64
	HRVStatus    string

	SleepSeconds int
	SleepScore   int

	ReadinessScore int
	ReadinessLevel string

	RestingHR int

	// sleepRestingHR is the sleep response's own resting heart rate, kept
	// so the fallback in Wellness reuses that response.
	sleepRestingHR int

	// Partial names which signals this call could not read — the endpoint's
	// own failure (non-200, unreadable JSON), never a URL or a value: health
	// data does not belong in a log line, so a caller logs len(Partial) and
	// the names here, nothing more identifying. An empty Partial with every
	// field still zero is a normal day with no readings yet, not a failure.
	Partial []string
}

// Wellness fetches HRV, sleep, Training Readiness and resting heart rate for
// one calendar date in a single call. It returns an error only when nothing
// at all could be requested — no display name on the session, or the bearer
// token exchange itself failing — the same up-front check
// RestingHeartRate already makes. Once a bearer is in hand, each of the four
// signals is fetched and decoded independently: a single one failing is
// recorded in Partial and does not stop the others, so a rider missing only
// last night's HRV still gets sleep, readiness and resting HR back.
func (c *Client) Wellness(ctx context.Context, date time.Time) (Wellness, error) {
	if c.session.DisplayName == "" {
		return Wellness{}, errors.New("garmin: no display name on the stored session to ask the wellness endpoints for — reconnect to fetch one")
	}

	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return Wellness{}, err
	}

	w := Wellness{Date: date.Format("2006-01-02")}

	if err := c.fetchHRV(ctx, bearer, date, &w); err != nil {
		w.Partial = append(w.Partial, partialName("hrv", err))
	}
	sleepErr := c.fetchSleep(ctx, bearer, date, &w)
	if sleepErr != nil {
		w.Partial = append(w.Partial, partialName("sleep", sleepErr))
	}
	if err := c.fetchTrainingReadiness(ctx, bearer, date, &w); err != nil {
		w.Partial = append(w.Partial, partialName("readiness", err))
	}
	// The sleep response is already in hand (or already failed and named in
	// Partial), so the resting-HR fallback reads it rather than asking again.
	hr, err := c.restingHeartRate(ctx, bearer, date, func(context.Context) (int, error) {
		return w.sleepRestingHR, sleepErr
	})
	if err != nil {
		w.Partial = append(w.Partial, partialName("resting_hr", err))
	} else {
		w.RestingHR = hr
	}

	return w, nil
}

func (c *Client) fetchHRV(ctx context.Context, bearer string, date time.Time, w *Wellness) error {
	url := fmt.Sprintf("%s%s/%s", c.APIBase, hrvPath, date.Format("2006-01-02"))
	raw, status, err := c.do(ctx, http.MethodGet, url, nil, "", c.wellnessHeaders(bearer)...)
	if err != nil {
		return err
	}
	// Many devices never record HRV, and Connect answers those riders with
	// a 204, a 404 or a body without hrvSummary: no reading, not a failure.
	if noReading(status, raw) {
		return nil
	}
	if status != http.StatusOK {
		return &statusError{"HRV", status, snippet(raw)}
	}

	var dto hrvResponseDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		return &unreadableError{"HRV", status, snippet(raw)}
	}
	if dto.HRVSummary == nil {
		return nil
	}
	w.HRVLastNight = dto.HRVSummary.LastNightAvg
	w.HRVWeeklyAvg = dto.HRVSummary.WeeklyAvg
	w.HRVStatus = strings.ToUpper(dto.HRVSummary.Status)
	return nil
}

func (c *Client) fetchSleep(ctx context.Context, bearer string, date time.Time, w *Wellness) error {
	url := fmt.Sprintf("%s%s/%s?date=%s&nonSleepBufferMinutes=60",
		c.APIBase, sleepPath, c.session.DisplayName, date.Format("2006-01-02"))
	raw, status, err := c.do(ctx, http.MethodGet, url, nil, "", c.wellnessHeaders(bearer)...)
	if err != nil {
		return err
	}
	if noReading(status, raw) {
		return nil
	}
	if status != http.StatusOK {
		return &statusError{"sleep", status, snippet(raw)}
	}

	var dto dailySleepResponseDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		return &unreadableError{"sleep", status, snippet(raw)}
	}
	w.SleepSeconds = dto.DailySleepDTO.SleepTimeSeconds
	w.SleepScore = dto.DailySleepDTO.SleepScores.Overall.Value
	w.sleepRestingHR = plausibleRestingHR(dto.RestingHeartRate)
	return nil
}

func (c *Client) fetchTrainingReadiness(ctx context.Context, bearer string, date time.Time, w *Wellness) error {
	url := fmt.Sprintf("%s%s/%s", c.APIBase, trainingReadinessPath, date.Format("2006-01-02"))
	raw, status, err := c.do(ctx, http.MethodGet, url, nil, "", c.wellnessHeaders(bearer)...)
	if err != nil {
		return err
	}
	if noReading(status, raw) {
		return nil
	}
	if status != http.StatusOK {
		return &statusError{"training readiness", status, snippet(raw)}
	}

	var entries []trainingReadinessEntryDTO
	if err := json.Unmarshal(raw, &entries); err != nil {
		return &unreadableError{"training readiness", status, snippet(raw)}
	}
	if len(entries) == 0 {
		// No reading for this date — not an error, the same as an empty
		// dailySummaryDTO for resting heart rate.
		return nil
	}
	w.ReadinessScore = entries[0].Score
	w.ReadinessLevel = strings.ToUpper(entries[0].Level)
	return nil
}
