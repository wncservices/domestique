package garmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
