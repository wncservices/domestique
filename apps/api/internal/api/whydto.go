package api

import (
	"context"
	"time"

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

// whyIsCurrent is whether an adjustment still describes its subject: it was
// recorded at or after the subject's last write. An automatic change writes
// the subject first and records afterwards, so a genuine row always passes;
// anything that rewrote the subject since (a manual edit, an indoor convert, an
// alternate swap or its revert, a Train Now replace, a later level move) leaves
// the row older than the subject, and an explanation of a session that no
// longer exists is worse than none. The two stamps come in different widths
// (seconds on the subject, nanoseconds on the row), so both are parsed; one
// that does not parse counts as not current.
func whyIsCurrent(adjustmentCreated, subjectUpdated string) bool {
	created, err := time.Parse(time.RFC3339Nano, adjustmentCreated)
	if err != nil {
		return false
	}
	updated, err := time.Parse(time.RFC3339Nano, subjectUpdated)
	if err != nil {
		return false
	}
	return !created.Before(updated)
}

// workoutDTOWithWhy is workoutDTOFrom for a response that carries one workout:
// every endpoint that answers with a workout the owner can read goes through
// here (or attachWhy, for lists), so none can forget the why or send a stale
// one. Responses to a create deliberately do not: a new workout has no
// adjustment.
func (s *Server) workoutDTOWithWhy(ctx context.Context, w workout.Workout) workoutDTO {
	one := []workoutDTO{workoutDTOFrom(w)}
	s.attachWhy(ctx, w.Rider, one)
	s.attachRoutes(ctx, w.Rider, one)
	return one[0]
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
			if a, ok := found[l[i].ID]; ok && whyIsCurrent(a.CreatedAt, l[i].UpdatedAt) {
				l[i].Why = whyDTOFrom(a)
			}
		}
	}
}
