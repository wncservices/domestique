package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// TestTheForecastReadsTodaysAssessmentWithoutTheSurvey: heavy legs two days
// running is today's caution, shown on the readiness chip and acted on today,
// but the tomorrow forecast is load-based by design and must be handed the
// assessment without it.
func TestTheForecastReadsTodaysAssessmentWithoutTheSurvey(t *testing.T) {
	h := newRecalHarness(t)
	ctx := context.Background()
	for _, day := range []string{"2026-03-18", "2026-03-19"} {
		sess, err := h.db.UpsertSession(ctx, workout.UpsertSessionRequest{
			Rider: "wilant", Provider: "garmin", ExternalID: day, Sport: "cycling", Date: day, DurationSeconds: 3600,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.db.SaveAnalysis(ctx, workout.SessionAnalysis{SessionID: sess.ID, Rider: "wilant", Outcome: "unplanned"}); err != nil {
			t.Fatal(err)
		}
		if err := h.db.SetAnalysisSurvey(ctx, sess.ID, 3, "heavy", "", 0); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := h.db.ListSessions(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC)

	today := h.s.assessReadinessAt(ctx, "wilant", sessions, nil, now)
	if len(today.Reasons) != 1 || !strings.Contains(today.Reasons[0], "heavy legs") {
		t.Fatalf("today's assessment = %+v, want the heavy-legs caution", today)
	}
	forecast := h.s.assessReadinessForForecast(ctx, "wilant", sessions, nil, now)
	if len(forecast.Reasons) != 0 {
		t.Errorf("the forecast was handed %+v, want no survey reasons", forecast)
	}
}
