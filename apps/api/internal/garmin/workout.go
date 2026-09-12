package garmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/wncservices/domestique/apps/api/internal/fitworkout"
)

// workoutPath is Connect's structured-workout service.
//
// Unlike coursePath, this does NOT take a file — see
// docs/training-plan.md's own "Structured workouts and the providers":
// Garmin's push here is Connect's own JSON workout schema, confirmed from
// two independent open-source clients that talk to it
// (github.com/cyberjunky/python-garminconnect's garminconnect/workout.py,
// cross-checked against github.com/mkuthan/garmin-workouts), not this
// app's own internal/fitworkout — Connect renders a FIT course-equivalent
// from this JSON server-side only when asked to sync a workout to a
// device, it never accepts a client-supplied FIT workout the way course
// push accepts a client-supplied FIT course.
//
// Undocumented and liable to move, the same caveat coursePath's own doc
// comment carries. One field in particular is flagged below
// (encodeTarget) as inferred rather than confirmed — worth a real capture
// from Connect's own web workout builder before trusting it on more than
// power targets.
const workoutPath = "/workout-service/workout"

// sportType maps a plain sport string to Connect's own id/key pair —
// confirmed constants from python-garminconnect's own typed enum.
func sportType(sport string) sportTypeDTO {
	if sport == "running" {
		return sportTypeDTO{ID: 1, Key: "running"}
	}
	return sportTypeDTO{ID: 2, Key: "cycling"}
}

type sportTypeDTO struct {
	ID  int    `json:"sportTypeId"`
	Key string `json:"sportTypeKey"`
}

type stepTypeDTO struct {
	ID  int    `json:"stepTypeId"`
	Key string `json:"stepTypeKey"`
}

// stepTypes maps a fitworkout.Step's Intensity string to Connect's own
// step-type id/key — confirmed constants, the same source as sportType.
// "active" and anything unrecognised falls back to Connect's own generic
// "main" step (id 8): every listed step type here has a specific meaning
// Connect's UI treats differently (a warmup/cooldown/rest are excluded
// from some summary stats, for instance), and guessing wrong among those
// would be worse than the deliberately generic fallback.
var stepTypes = map[string]stepTypeDTO{
	"warmup":   {1, "warmup"},
	"cooldown": {2, "cooldown"},
	"interval": {3, "interval"},
	"recovery": {4, "recovery"},
	"rest":     {5, "rest"},
	"other":    {7, "other"},
}

func stepType(intensity string) stepTypeDTO {
	if st, ok := stepTypes[intensity]; ok {
		return st
	}
	return stepTypeDTO{ID: 8, Key: "main"}
}

type endConditionDTO struct {
	ID  int    `json:"conditionTypeId"`
	Key string `json:"conditionTypeKey"`
}

// End-condition constants — confirmed. lap.button (id 1) is what an open,
// manually-ended step uses; Connect's own UI calls this "Open."
var (
	endConditionLapButton  = endConditionDTO{1, "lap.button"}
	endConditionTime       = endConditionDTO{2, "time"}
	endConditionDistance   = endConditionDTO{3, "distance"}
	endConditionIterations = endConditionDTO{7, "iterations"}
)

type targetTypeDTO struct {
	ID  int    `json:"workoutTargetTypeId"`
	Key string `json:"workoutTargetTypeKey"`
}

// Target-type constants. no.target and power.zone are confirmed from
// source (python-garminconnect's own step-builder helpers and
// mkuthan/garmin-workouts' Power model, which assigns raw watts straight
// to targetValueOne/Two). heart_rate.zone, pace.zone and cadence's own ids
// are confirmed as *labels* the same enum defines, but this research pass
// found no primary-source example of what unit a heart-rate or pace range
// actually wants in targetValueOne/Two — see encodeTarget below for what
// this package assumes in that gap, and why it is a reasonable bet rather
// than a shot in the dark.
var targetTypes = map[fitworkout.Target]targetTypeDTO{
	fitworkout.TargetOpen:      {1, "no.target"},
	fitworkout.TargetPower:     {2, "power.zone"},
	fitworkout.TargetCadence:   {3, "cadence"},
	fitworkout.TargetHeartRate: {4, "heart_rate.zone"},
	fitworkout.TargetPace:      {6, "pace.zone"},
}

type workoutStepDTO struct {
	Type               string           `json:"type"`
	StepOrder          int              `json:"stepOrder"`
	StepType           *stepTypeDTO     `json:"stepType,omitempty"`
	EndCondition       *endConditionDTO `json:"endCondition,omitempty"`
	EndConditionValue  float64          `json:"endConditionValue,omitempty"`
	TargetType         *targetTypeDTO   `json:"targetType,omitempty"`
	TargetValueOne     *float64         `json:"targetValueOne"`
	TargetValueTwo     *float64         `json:"targetValueTwo"`
	NumberOfIterations int              `json:"numberOfIterations,omitempty"`
	WorkoutSteps       []workoutStepDTO `json:"workoutSteps,omitempty"`
	SmartRepeat        *bool            `json:"smartRepeat,omitempty"`
}

type workoutSegmentDTO struct {
	SegmentOrder int              `json:"segmentOrder"`
	SportType    sportTypeDTO     `json:"sportType"`
	WorkoutSteps []workoutStepDTO `json:"workoutSteps"`
}

type workoutDTO struct {
	WorkoutID       string              `json:"workoutId,omitempty"`
	WorkoutName     string              `json:"workoutName"`
	SportType       sportTypeDTO        `json:"sportType"`
	WorkoutSegments []workoutSegmentDTO `json:"workoutSegments"`
}

// buildWorkout encodes a structured workout into Connect's own JSON
// schema — see workoutPath's own doc comment for the shape and its
// sourcing.
func buildWorkout(name, sport string, steps []fitworkout.Step) (workoutDTO, error) {
	if len(steps) == 0 {
		return workoutDTO{}, errors.New("garmin: need at least one step")
	}
	if name == "" {
		name = "Workout"
	}

	order := 0
	encoded, err := encodeSteps(steps, &order)
	if err != nil {
		return workoutDTO{}, err
	}

	st := sportType(sport)
	return workoutDTO{
		WorkoutName: name,
		SportType:   st,
		WorkoutSegments: []workoutSegmentDTO{
			{SegmentOrder: 1, SportType: st, WorkoutSteps: encoded},
		},
	}, nil
}

func encodeSteps(steps []fitworkout.Step, order *int) ([]workoutStepDTO, error) {
	out := make([]workoutStepDTO, 0, len(steps))
	for _, s := range steps {
		if s.Repeat > 1 {
			if len(s.Steps) == 0 {
				return nil, fmt.Errorf("garmin: repeat block %q has no steps", s.Name)
			}
			children, err := encodeSteps(s.Steps, order)
			if err != nil {
				return nil, err
			}
			*order++
			ec := endConditionIterations
			noSmartRepeat := false
			out = append(out, workoutStepDTO{
				Type:               "RepeatGroupDTO",
				StepOrder:          *order,
				EndCondition:       &ec,
				EndConditionValue:  float64(s.Repeat),
				NumberOfIterations: s.Repeat,
				WorkoutSteps:       children,
				SmartRepeat:        &noSmartRepeat,
			})
			continue
		}

		*order++
		step, err := encodeStep(s, *order)
		if err != nil {
			return nil, err
		}
		out = append(out, step)
	}
	return out, nil
}

func encodeStep(s fitworkout.Step, order int) (workoutStepDTO, error) {
	step := workoutStepDTO{Type: "ExecutableStepDTO", StepOrder: order}

	st := stepType(s.Intensity)
	step.StepType = &st

	switch s.Duration {
	case fitworkout.DurationTime:
		if s.Seconds <= 0 {
			return workoutStepDTO{}, fmt.Errorf("garmin: step %q has a time duration but no seconds", s.Name)
		}
		step.EndCondition = &endConditionTime
		step.EndConditionValue = s.Seconds
	case fitworkout.DurationDistance:
		if s.Meters <= 0 {
			return workoutStepDTO{}, fmt.Errorf("garmin: step %q has a distance duration but no meters", s.Name)
		}
		step.EndCondition = &endConditionDistance
		step.EndConditionValue = s.Meters
	case fitworkout.DurationOpen, "":
		step.EndCondition = &endConditionLapButton
	default:
		return workoutStepDTO{}, fmt.Errorf("garmin: step %q has an unknown duration type %q", s.Name, s.Duration)
	}

	if err := encodeTarget(&step, s); err != nil {
		return workoutStepDTO{}, err
	}
	return step, nil
}

// encodeTarget fills in a step's target. Power is confirmed: Connect
// wants plain watts in targetValueOne/Two, the same absolute units
// fitworkout.Step already carries — no conversion needed. Heart rate,
// pace and cadence are passed through the same way (raw bpm, raw m/s, raw
// rpm) on the same reasoning — a consistent absolute-value convention
// across every target type Connect's own enum defines — but that
// consistency is this package's own inference, not something a captured
// request confirmed for those three specifically. If a push with one of
// those three targets is rejected or silently mis-scaled, this is the
// first place to look, and the fix is a real captured request from
// Connect's own web workout builder, not another guess.
func encodeTarget(step *workoutStepDTO, s fitworkout.Step) error {
	tt, ok := targetTypes[s.Target]
	if !ok {
		return fmt.Errorf("garmin: step %q has an unknown target type %q", s.Name, s.Target)
	}
	step.TargetType = &tt

	if s.Target == fitworkout.TargetOpen {
		return nil
	}
	if s.TargetLow <= 0 || s.TargetHigh <= 0 {
		return fmt.Errorf("garmin: step %q has target %q but no low/high value", s.Name, s.Target)
	}
	low, high := s.TargetLow, s.TargetHigh
	if low > high {
		low, high = high, low
	}
	step.TargetValueOne = &low
	step.TargetValueTwo = &high
	return nil
}

// CreateWorkout pushes a new structured workout to Connect and returns its
// id.
func (c *Client) CreateWorkout(ctx context.Context, name, sport string, steps []fitworkout.Step) (string, error) {
	dto, err := buildWorkout(name, sport, steps)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(dto)
	if err != nil {
		return "", fmt.Errorf("garmin: encode workout: %w", err)
	}

	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return "", err
	}

	raw, status, err := c.do(ctx, http.MethodPost, c.APIBase+workoutPath, bytes.NewReader(body),
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
	case status >= 300:
		return "", fmt.Errorf("garmin: creating the workout returned %d: %s", status, longSnippet(raw))
	}

	id, err := workoutID(raw)
	if err != nil {
		return "", fmt.Errorf("garmin: the workout was created but no id came back: %w", err)
	}
	return id, nil
}

// UpdateWorkout replaces an existing workout in place — a full replace,
// the same "send the same shape as create, with the id forced to match"
// contract python-garminconnect's own update_workout uses.
func (c *Client) UpdateWorkout(ctx context.Context, id, name, sport string, steps []fitworkout.Step) error {
	if id == "" {
		return errors.New("garmin: no workout id to update")
	}
	dto, err := buildWorkout(name, sport, steps)
	if err != nil {
		return err
	}
	dto.WorkoutID = id

	body, err := json.Marshal(dto)
	if err != nil {
		return fmt.Errorf("garmin: encode workout: %w", err)
	}

	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return err
	}

	raw, status, err := c.do(ctx, http.MethodPut, c.APIBase+workoutPath+"/"+id, bytes.NewReader(body),
		"application/json",
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
	case status >= 300:
		return fmt.Errorf("garmin: updating the workout returned %d: %s", status, longSnippet(raw))
	}
	return nil
}

// DeleteWorkout removes a workout from the account.
func (c *Client) DeleteWorkout(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("garmin: no workout id to delete")
	}

	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return err
	}

	raw, status, err := c.do(ctx, http.MethodDelete, c.APIBase+workoutPath+"/"+id, nil, "",
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
	case status >= 300:
		return fmt.Errorf("garmin: deleting the workout returned %d: %s", status, snippet(raw))
	}
	return nil
}

// workoutID reads the created/updated workout's id out of Connect's
// response, the same defensive multi-spelling lookup courseID uses for
// exactly the same reason: the shape is not documented.
func workoutID(raw []byte) (string, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", fmt.Errorf("garmin: unreadable workout response: %s", snippet(raw))
	}
	for _, key := range []string{"workoutId", "id"} {
		switch value := body[key].(type) {
		case string:
			if value != "" {
				return value, nil
			}
		case float64:
			return fmt.Sprintf("%.0f", value), nil
		}
	}
	return "", fmt.Errorf("garmin: the workout was accepted but the response named no id: %s", snippet(raw))
}
