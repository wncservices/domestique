package garmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// workoutSchedulePath is Connect's calendar for workouts: a workout that
// exists on the account is placed on a date by POSTing that date here, which
// is what makes it show up on the watch on the day instead of sitting in a
// library nobody opens. Path from github.com/cyberjunky/python-garminconnect
// (its `garmin_workouts_schedule_url`); the request and response shapes below
// are from the same reference's behaviour, not a documented API, and have not
// been exercised against a live account — see ScheduleWorkout.
const workoutSchedulePath = "/workout-service/schedule"

// ScheduleWorkout places an existing workout on a calendar date
// ("YYYY-MM-DD") and returns Connect's id for that calendar entry, which is
// what UnscheduleWorkout needs to move it later. The id is best-effort: if
// the response names none the schedule still happened, and the caller simply
// cannot remove that entry itself.
func (c *Client) ScheduleWorkout(ctx context.Context, workoutID, date string) (string, error) {
	if workoutID == "" || date == "" {
		return "", errors.New("garmin: a workout id and a date are both needed to schedule")
	}
	body, err := json.Marshal(map[string]string{"date": date})
	if err != nil {
		return "", err
	}

	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return "", err
	}
	raw, status, err := c.do(ctx, http.MethodPost, c.APIBase+workoutSchedulePath+"/"+workoutID, bytes.NewReader(body),
		"application/json",
		header{"Authorization", "Bearer " + bearer},
		header{"Accept", "application/json"},
		header{"X-Requested-With", "XMLHttpRequest"},
	)
	if err != nil {
		return "", err
	}
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return "", errors.New("garmin: the session was refused — sign in again")
	case status == http.StatusNotFound:
		return "", fmt.Errorf("%w (scheduling returned 404)", ErrWorkoutGone)
	case status >= 300:
		return "", fmt.Errorf("garmin: scheduling the workout returned %d: %s", status, snippet(raw))
	}
	return scheduleID(raw), nil
}

// UnscheduleWorkout removes one calendar entry. A 404 is success: the entry
// is already gone, which is all the caller wanted.
func (c *Client) UnscheduleWorkout(ctx context.Context, scheduleID string) error {
	if scheduleID == "" {
		return errors.New("garmin: no schedule id to remove")
	}
	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return err
	}
	raw, status, err := c.do(ctx, http.MethodDelete, c.APIBase+workoutSchedulePath+"/"+scheduleID, nil, "",
		header{"Authorization", "Bearer " + bearer},
		header{"Accept", "application/json"},
		header{"X-Requested-With", "XMLHttpRequest"},
	)
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return errors.New("garmin: the session was refused — sign in again")
	case status == http.StatusNotFound:
		return nil
	case status >= 300:
		return fmt.Errorf("garmin: removing the calendar entry returned %d: %s", status, snippet(raw))
	}
	return nil
}

// scheduleID reads the calendar entry's id out of the response, trying the
// spellings the reference client has been seen to get back — an empty result
// is a normal outcome, not an error (see ScheduleWorkout).
func scheduleID(raw []byte) string {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return ""
	}
	for _, key := range []string{"workoutScheduleId", "scheduleId", "id"} {
		switch v := body[key].(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			return fmt.Sprintf("%.0f", v)
		}
	}
	return ""
}
