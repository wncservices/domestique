package api

import (
	"context"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const (
	wellnessSourceGarmin = "garmin"
	wellnessSourceApple  = "apple"
)

// wellnessSource says where a rider's recovery signals come from. A rider has
// exactly one source: Garmin and Apple measure HRV differently, so rows from
// both in one band would make every night look unusual. Linking Apple Health
// switches the rider over; revoking switches back. A read error answers
// garmin, the long-standing default, and is logged by stage only.
func (s *Server) wellnessSource(ctx context.Context, rider string) string {
	if s.AppleHealth == nil {
		return wellnessSourceGarmin
	}
	linked, err := s.AppleHealth.Linked(ctx, rider)
	if err != nil {
		s.logger().Warn("wellness source: reading the Apple Health link failed", "rider", rider, "stage", "linked", "err", err)
		return wellnessSourceGarmin
	}
	if linked {
		return wellnessSourceApple
	}
	return wellnessSourceGarmin
}

// readinessWellness is ListWellness limited to the rider's current source, so
// readiness never blends the two devices. Rows from the other source stay
// stored, which makes revoking the link a clean switch back.
func (s *Server) readinessWellness(ctx context.Context, rider, sinceDate string) ([]workout.DailyWellness, error) {
	rows, err := s.Training.ListWellness(ctx, rider, sinceDate)
	if err != nil {
		return nil, err
	}
	want := s.wellnessSource(ctx, rider)
	out := make([]workout.DailyWellness, 0, len(rows))
	for _, row := range rows {
		src := row.Source
		if src == "" {
			src = wellnessSourceGarmin
		}
		if src == want {
			out = append(out, row)
		}
	}
	return out, nil
}
