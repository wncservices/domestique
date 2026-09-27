package garmin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// activityListPath is what Connect's own activity feed asks for — the
// summary list, not the per-second detail stream (activity-service's own
// /details endpoint): fitness accounting (see internal/workout.
// CompletedSession and ComputeFitness) only needs one row per session —
// duration, distance, average HR/power — not a full metric time series,
// so this package does not implement the positional metricDescriptors
// format the details endpoint uses at all. A future feature that wants a
// lap-by-lap or second-by-second view is what that endpoint is for, not
// this one.
const activityListPath = "/activitylist-service/activities/search/activities"

// Activity is one completed session pulled back from Connect.
type Activity struct {
	ID              string
	Name            string
	Sport           string
	StartTime       time.Time
	DurationSeconds float64
	DistanceM       float64
	AvgHR           int
	AvgPowerWatts   float64

	// NormalizedPower, TrainingStressScore and IntensityFactor are Connect's
	// own summary numbers — the fallback used when a rider's FIT file could
	// not be downloaded or decoded (internal/rideanalysis's own analysis
	// from the full file is preferred when available). Absent from the JSON
	// leaves these at zero, the same as an activity with no power meter.
	NormalizedPower     float64
	TrainingStressScore float64
	IntensityFactor     float64
	// BestPower is Connect's own best-average-power figures, keyed by the
	// window length in seconds (5, 60, 300, 1200, 3600). A window Connect
	// did not report — no power meter, or too short a ride — is simply
	// absent from the map rather than present as zero, so a caller can tell
	// "no power for 20 minutes" apart from "did not ride 20 minutes."
	BestPower map[int]float64
}

// activityDTO is Connect's own flat shape — confirmed against
// github.com/cyberjunky/python-garminconnect's typed.py, whose Pydantic
// aliases mirror Garmin's raw JSON field names exactly. Only the fields
// this app uses are decoded.
type activityDTO struct {
	ActivityID     json.Number `json:"activityId"`
	ActivityName   string      `json:"activityName"`
	StartTimeLocal string      `json:"startTimeLocal"`
	ActivityType   struct {
		TypeKey string `json:"typeKey"`
	} `json:"activityType"`
	Duration  float64 `json:"duration"`
	Distance  float64 `json:"distance"`
	AverageHR float64 `json:"averageHR"`
	AvgPower  float64 `json:"avgPower"`

	NormPower           float64 `json:"normPower"`
	TrainingStressScore float64 `json:"trainingStressScore"`
	IntensityFactor     float64 `json:"intensityFactor"`
	// The maxAvgPower_* fields are pointers so a window Connect did not
	// report is distinguishable from one it reported as exactly zero — see
	// Activity.BestPower.
	MaxAvgPower5    *float64 `json:"maxAvgPower_5"`
	MaxAvgPower60   *float64 `json:"maxAvgPower_60"`
	MaxAvgPower300  *float64 `json:"maxAvgPower_300"`
	MaxAvgPower1200 *float64 `json:"maxAvgPower_1200"`
	MaxAvgPower3600 *float64 `json:"maxAvgPower_3600"`
}

// bestPowerWindows pairs each maxAvgPower_* field with the window length (in
// seconds) it represents, in Connect's own ascending order.
func (d activityDTO) bestPowerWindows() map[int]float64 {
	windows := map[int]*float64{
		5:    d.MaxAvgPower5,
		60:   d.MaxAvgPower60,
		300:  d.MaxAvgPower300,
		1200: d.MaxAvgPower1200,
		3600: d.MaxAvgPower3600,
	}
	var out map[int]float64
	for seconds, v := range windows {
		if v == nil {
			continue
		}
		if out == nil {
			out = make(map[int]float64, len(windows))
		}
		out[seconds] = *v
	}
	return out
}

// mapSport turns one of Connect's many activity-type keys
// ("road_biking", "trail_running", "indoor_cycling", "treadmill_running",
// and others) into this app's own two-value model.Sport — plain substring
// matching rather than an exhaustive table, since Connect's own list of
// type keys is long, not fully enumerated anywhere this package found,
// and growing that table is not worth it when "does the key mention
// running" already answers the only question this app's data model asks.
func mapSport(typeKey string) string {
	if strings.Contains(typeKey, "run") {
		return "running"
	}
	return "cycling"
}

// Activities lists a rider's most recent completed activities, most
// recent first — Connect's own default ordering for this endpoint.
func (c *Client) Activities(ctx context.Context, limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = 50
	}

	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s%s?limit=%d&start=0", c.APIBase, activityListPath, limit)
	raw, status, err := c.do(ctx, http.MethodGet, url, nil, "",
		header{"Authorization", "Bearer " + bearer},
		header{"Accept", "application/json"},
		header{"X-Requested-With", "XMLHttpRequest"},
	)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("garmin: the activity list returned %d: %s", status, snippet(raw))
	}

	var dtos []activityDTO
	if err := json.Unmarshal(raw, &dtos); err != nil {
		return nil, fmt.Errorf("garmin: unreadable activity list: %s", snippet(raw))
	}

	out := make([]Activity, 0, len(dtos))
	for _, d := range dtos {
		// Connect's own local-time format, confirmed by
		// python-garminconnect's own date handling: "2006-01-02 15:04:05".
		start, _ := time.Parse("2006-01-02 15:04:05", d.StartTimeLocal)
		out = append(out, Activity{
			ID:                  d.ActivityID.String(),
			Name:                d.ActivityName,
			Sport:               mapSport(d.ActivityType.TypeKey),
			StartTime:           start,
			DurationSeconds:     d.Duration,
			DistanceM:           d.Distance,
			AvgHR:               int(d.AverageHR),
			AvgPowerWatts:       d.AvgPower,
			NormalizedPower:     d.NormPower,
			TrainingStressScore: d.TrainingStressScore,
			IntensityFactor:     d.IntensityFactor,
			BestPower:           d.bestPowerWindows(),
		})
	}
	return out, nil
}
