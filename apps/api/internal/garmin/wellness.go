package garmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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

// dailySummaryDTO is Connect's own flat per-day wellness shape. Only the
// field this app uses is decoded.
type dailySummaryDTO struct {
	RestingHeartRate int `json:"restingHeartRate"`
}

// RestingHeartRate returns the rider's resting heart rate for one calendar
// date, as Connect's own wellness pipeline recorded it from a worn device
// overnight — not a workout's average heart rate, which this same account
// may also report and which is a much higher, different number entirely.
//
// 0 with a nil error means Connect had no reading for that date (the watch
// was not worn, or Connect has not processed it yet), which is a normal
// day and not an error to warn about — see the call site in
// handleSyncTrainingMetrics, which tries a small run of recent dates for
// exactly this reason.
func (c *Client) RestingHeartRate(ctx context.Context, date time.Time) (int, error) {
	if c.session.DisplayName == "" {
		return 0, errors.New("garmin: no display name on the stored session to ask the wellness endpoint for — reconnect to fetch one")
	}

	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return 0, err
	}

	url := fmt.Sprintf("%s%s/%s?calendarDate=%s", c.APIBase, dailySummaryPath, c.session.DisplayName, date.Format("2006-01-02"))
	raw, status, err := c.do(ctx, http.MethodGet, url, nil, "",
		header{"Authorization", "Bearer " + bearer},
		header{"Accept", "application/json"},
		header{"X-Requested-With", "XMLHttpRequest"},
	)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, fmt.Errorf("garmin: the daily summary request returned %d: %s", status, snippet(raw))
	}

	var dto dailySummaryDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		return 0, fmt.Errorf("garmin: unreadable daily summary response: %s", snippet(raw))
	}
	return dto.RestingHeartRate, nil
}

// hrvPath is Connect's HRV summary endpoint. Response shape confirmed
// against python-garminconnect's get_hrv_data (unverified against a live
// account, the same caveat dailySummaryPath's own comment states) — an
// hrvSummary object carrying lastNightAvg, weeklyAvg and a status string
// (BALANCED, UNBALANCED, LOW, POOR).
const hrvPath = "/hrv-service/hrv"

type hrvResponseDTO struct {
	HRVSummary struct {
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
	DailySleepDTO struct {
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
		w.Partial = append(w.Partial, "hrv")
	}
	if err := c.fetchSleep(ctx, bearer, date, &w); err != nil {
		w.Partial = append(w.Partial, "sleep")
	}
	if err := c.fetchTrainingReadiness(ctx, bearer, date, &w); err != nil {
		w.Partial = append(w.Partial, "readiness")
	}
	if hr, err := c.RestingHeartRate(ctx, date); err != nil {
		w.Partial = append(w.Partial, "resting_hr")
	} else {
		w.RestingHR = hr
	}

	return w, nil
}

func (c *Client) fetchHRV(ctx context.Context, bearer string, date time.Time, w *Wellness) error {
	url := fmt.Sprintf("%s%s/%s", c.APIBase, hrvPath, date.Format("2006-01-02"))
	raw, status, err := c.do(ctx, http.MethodGet, url, nil, "",
		header{"Authorization", "Bearer " + bearer},
		header{"Accept", "application/json"},
		header{"X-Requested-With", "XMLHttpRequest"},
	)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("garmin: the HRV request returned %d: %s", status, snippet(raw))
	}

	var dto hrvResponseDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		return fmt.Errorf("garmin: unreadable HRV response: %s", snippet(raw))
	}
	w.HRVLastNight = dto.HRVSummary.LastNightAvg
	w.HRVWeeklyAvg = dto.HRVSummary.WeeklyAvg
	w.HRVStatus = strings.ToUpper(dto.HRVSummary.Status)
	return nil
}

func (c *Client) fetchSleep(ctx context.Context, bearer string, date time.Time, w *Wellness) error {
	url := fmt.Sprintf("%s%s/%s?date=%s&nonSleepBufferMinutes=60",
		c.APIBase, sleepPath, c.session.DisplayName, date.Format("2006-01-02"))
	raw, status, err := c.do(ctx, http.MethodGet, url, nil, "",
		header{"Authorization", "Bearer " + bearer},
		header{"Accept", "application/json"},
		header{"X-Requested-With", "XMLHttpRequest"},
	)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("garmin: the sleep request returned %d: %s", status, snippet(raw))
	}

	var dto dailySleepResponseDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		return fmt.Errorf("garmin: unreadable sleep response: %s", snippet(raw))
	}
	w.SleepSeconds = dto.DailySleepDTO.SleepTimeSeconds
	w.SleepScore = dto.DailySleepDTO.SleepScores.Overall.Value
	return nil
}

func (c *Client) fetchTrainingReadiness(ctx context.Context, bearer string, date time.Time, w *Wellness) error {
	url := fmt.Sprintf("%s%s/%s", c.APIBase, trainingReadinessPath, date.Format("2006-01-02"))
	raw, status, err := c.do(ctx, http.MethodGet, url, nil, "",
		header{"Authorization", "Bearer " + bearer},
		header{"Accept", "application/json"},
		header{"X-Requested-With", "XMLHttpRequest"},
	)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("garmin: the training readiness request returned %d: %s", status, snippet(raw))
	}

	var entries []trainingReadinessEntryDTO
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("garmin: unreadable training readiness response: %s", snippet(raw))
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
