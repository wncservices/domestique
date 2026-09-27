package wahoo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	// NormalizedPower and TSS are the summary fallback used when a rider's
	// FIT file could not be downloaded or decoded — the same role Garmin's
	// own normPower/trainingStressScore summary fields play there.
	NormalizedPower float64
	TSS             float64
	// FileURL is workout_summary.file.url — the FIT file for this workout,
	// fetched with WorkoutFIT. Empty when Wahoo has not attached a summary
	// yet (see ListWorkouts' own fallback for a workout still processing).
	FileURL string
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
	// PowerBikeNPLast and PowerBikeTSSLast were observed to vary between a
	// JSON string and a bare JSON number — unlike every other field above,
	// confirmed to always be strings — so they decode through
	// flexibleNumber rather than assuming one form.
	PowerBikeNPLast  flexibleNumber `json:"power_bike_np_last"`
	PowerBikeTSSLast flexibleNumber `json:"power_bike_tss_last"`
	// File is where the FIT file for this workout lives — the same shape
	// GET /v1/routes' file.url already carries for a route (wahoo.go's
	// routeListItem).
	File struct {
		URL string `json:"url"`
	} `json:"file"`
}

// flexibleNumber decodes a JSON field that may arrive as either a quoted
// string ("85.5") or a bare number (85.5). Wahoo's own workout_summary
// object is documented nowhere; power_bike_np_last and power_bike_tss_last
// are the two fields observed to vary, so this is not applied to the rest of
// workoutSummaryDTO's plain-string fields above, which have not shown the
// same inconsistency.
type flexibleNumber float64

func (n *flexibleNumber) UnmarshalJSON(data []byte) error {
	s := string(data)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("wahoo: unreadable number %q: %w", s, err)
	}
	*n = flexibleNumber(v)
	return nil
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
			w.NormalizedPower = float64(it.WorkoutSummary.PowerBikeNPLast)
			w.TSS = float64(it.WorkoutSummary.PowerBikeTSSLast)
			w.FileURL = it.WorkoutSummary.File.URL
		}
		out = append(out, w)
	}
	return out, nil
}

// MaxFITBytes caps a downloaded FIT file — the same limit
// garmin.MaxFITBytes enforces, kept as its own constant here since this
// package has no dependency on internal/garmin for one shared number.
const MaxFITBytes = 32 << 20

// fitSignature is a FIT file header's own magic bytes, at offset 8 of the
// header, per the Global FIT SDK.
var fitSignature = []byte(".FIT")

// WorkoutFIT downloads one completed workout's FIT file from fileURL —
// workout_summary.file.url, which in every observed case lands on Wahoo's
// CDN (cdn.wahooligan.com) rather than the Cloud API host itself, the same
// split DownloadRoute already handles for a route's own file.url.
//
// Unlike DownloadRoute, this takes no access token: fileURL is a pre-signed
// link that works unauthenticated, and there is nothing to attach even in
// the case where it happens to resolve to the API host — allowedFileHost is
// still checked purely so a compromised or malicious upstream response
// cannot point this pod at an arbitrary internal address (the same
// SSRF-prevention reasoning as DownloadRoute's own doc comment), not to
// decide whether to send a credential this function was never given.
func (c *Client) WorkoutFIT(ctx context.Context, fileURL string) ([]byte, error) {
	if fileURL == "" {
		return nil, errors.New("wahoo: no file URL to download")
	}
	if _, err := c.allowedFileHost(fileURL); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, wrapURLErr("building workout FIT request", err)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, wrapURLErr("downloading workout FIT", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read up to MaxFITBytes+1 so a body over the cap is reported as an
	// error rather than silently truncated — the same distinction
	// garmin.ActivityFIT's readLimited draws.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxFITBytes+1))
	if err != nil {
		return nil, fmt.Errorf("wahoo: reading workout FIT: %w", err)
	}
	if len(raw) > MaxFITBytes {
		return nil, fmt.Errorf("wahoo: workout FIT exceeded the %d byte limit", MaxFITBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wahoo: workout FIT download returned %d: %s", resp.StatusCode, snippet(raw))
	}
	if len(raw) < 12 || !bytes.Equal(raw[8:12], fitSignature) {
		return nil, errors.New("wahoo: workout FIT response was not a FIT file (missing the .FIT signature)")
	}
	return raw, nil
}
