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
			ID:              d.ActivityID.String(),
			Name:            d.ActivityName,
			Sport:           mapSport(d.ActivityType.TypeKey),
			StartTime:       start,
			DurationSeconds: d.Duration,
			DistanceM:       d.Distance,
			AvgHR:           int(d.AverageHR),
			AvgPowerWatts:   d.AvgPower,
		})
	}
	return out, nil
}
