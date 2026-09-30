package api

import (
	"context"

	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// whyDTO is the "Why?" behind an automatic change, as the popover shows it:
// the rule's name, the sentence, the day it happened and the facts derived
// from the stored inputs. Mirrored by hand in apps/web/src/api/types.ts.
//
// It carries the rider's own health and load values, so it only ever appears
// inside a DTO the owner already gets; no endpoint lists adjustments.
type whyDTO struct {
	Rule  string       `json:"rule"`
	Title string       `json:"title"`
	Text  string       `json:"text"`
	Day   string       `json:"day"`
	Facts []whyFactDTO `json:"facts"`
}

type whyFactDTO struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

func whyDTOFrom(a workout.Adjustment) *whyDTO {
	out := &whyDTO{Rule: string(a.Rule), Title: why.Title(a.Rule), Text: a.Text, Day: a.Day, Facts: []whyFactDTO{}}
	for _, f := range why.Facts(a.Rule, a.Inputs) {
		out.Facts = append(out.Facts, whyFactDTO{Label: f.Label, Value: f.Value})
	}
	return out
}

// attachWhy fills Why on every workout in lists with one batched read for all
// of them, never one per workout. A read failure is a Warn and leaves the
// workouts without a why: the description note is still there for the UI to
// fall back on, and a missing explanation must not fail a page of sessions.
func (s *Server) attachWhy(ctx context.Context, rider string, lists ...[]workoutDTO) {
	var ids []string
	for _, l := range lists {
		for _, d := range l {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	found, err := s.Training.LatestAdjustments(ctx, rider, workout.SubjectWorkout, ids)
	if err != nil {
		s.logger().Warn("could not read why for workouts", "rider", rider, "err", err)
		return
	}
	for _, l := range lists {
		for i := range l {
			if a, ok := found[l[i].ID]; ok {
				l[i].Why = whyDTOFrom(a)
			}
		}
	}
}
