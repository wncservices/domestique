package wahoo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// workoutsPath is the completed-activity resource — confirmed (see
// docs/training-plan.md's own account of the research behind this) to be
// a *read-back* endpoint, the metrics-pull counterpart to
// ListRoutes/DownloadRoute, not a place to push a planned session: pushing
// a structured workout is a separate, further-gated "Plans" feature this
// app does not have access to.
const workoutsPath = "/v1/workouts"

// Workout is one completed session pulled back from a rider's Wahoo
// account.
type Workout struct {
	ID              string
	Name            string
	Starts          time.Time
	DurationSeconds float64
	DistanceM       float64
	AvgHR           int
	AvgPowerWatts   float64
}

// workoutSummaryDTO is Wahoo's own nested summary object. Every numeric
// field here is a JSON *string*, not a number — confirmed against two
// independent secondary sources (a derived OpenAPI spec and a real
// deployed webhook receiver), which is also where the one real
// discrepancy in this shape lives: the two sources disagree on the power
// field's name (power_bike_avg vs. power_avg). Both are decoded and the
// first non-empty one wins, the same defensive multi-spelling lookup
// internal/garmin's courseID/workoutID use for exactly the same
// reason — an unconfirmed field name is safer to check twice than to
// pick one and silently read 0 forever if it turns out to be the other.
type workoutSummaryDTO struct {
	DurationTotalAccum string `json:"duration_total_accum"`
	DistanceAccum      string `json:"distance_accum"`
	HeartRateAvg       string `json:"heart_rate_avg"`
	PowerBikeAvg       string `json:"power_bike_avg"`
	PowerAvg           string `json:"power_avg"`
}

func (s *workoutSummaryDTO) avgPower() float64 {
	for _, raw := range []string{s.PowerBikeAvg, s.PowerAvg} {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			return v
		}
	}
	return 0
}

func parseFloatOr0(raw string) float64 {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return v
}

type workoutListItem struct {
	ID             int64              `json:"id"`
	Name           string             `json:"name"`
	Starts         string             `json:"starts"`
	Minutes        float64            `json:"minutes"`
	WorkoutSummary *workoutSummaryDTO `json:"workout_summary"`
}

type workoutListResponse struct {
	Workouts []workoutListItem `json:"workouts"`
	Total    int               `json:"total"`
	Page     int               `json:"page"`
	PerPage  int               `json:"per_page"`
}

// ListWorkouts lists a rider's completed activities, most recent first —
// Wahoo's own default ordering. page is 1-based; pass 1 for the first,
// most-recent page. perPage is capped at 100 by Wahoo's own API regardless
// of what is asked for.
func (c *Client) ListWorkouts(ctx context.Context, accessToken string, page, perPage int) ([]Workout, error) {
	if page < 1 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 30
	}

	url := fmt.Sprintf("%s%s?page=%d&per_page=%d", c.APIBase, workoutsPath, page, perPage)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wahoo: list workouts: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("wahoo: reading workout list: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wahoo: list workouts returned %d: %s", resp.StatusCode, snippet(body))
	}

	var parsed workoutListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("wahoo: unreadable workout list: %s", snippet(body))
	}

	out := make([]Workout, 0, len(parsed.Workouts))
	for _, it := range parsed.Workouts {
		w := Workout{
			ID:              strconv.FormatInt(it.ID, 10),
			Name:            it.Name,
			DurationSeconds: it.Minutes * 60,
		}
		if starts, err := time.Parse(time.RFC3339, it.Starts); err == nil {
			w.Starts = starts
		}
		if it.WorkoutSummary != nil {
			// duration_total_accum is the summary's own, more precise
			// figure when present; the list item's own `minutes` above is
			// the fallback for a workout with no summary yet (one still
			// processing on Wahoo's side, per the API's own semantics).
			if d := parseFloatOr0(it.WorkoutSummary.DurationTotalAccum); d > 0 {
				w.DurationSeconds = d
			}
			w.DistanceM = parseFloatOr0(it.WorkoutSummary.DistanceAccum)
			w.AvgHR = int(parseFloatOr0(it.WorkoutSummary.HeartRateAvg))
			w.AvgPowerWatts = it.WorkoutSummary.avgPower()
		}
		out = append(out, w)
	}
	return out, nil
}
