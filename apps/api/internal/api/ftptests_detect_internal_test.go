package api

import (
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/thresholds"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// seedTestRide records a completed ride analysed against a planned workout
// with the given protocol ("" = an ordinary planned workout) and best-minute
// power, dated 2026-03-10 (five days before fixedNow).
func seedTestRide(t *testing.T, s *Server, protocol string, curve map[string]float64) []workout.CompletedSession {
	t.Helper()
	ctx := t.Context()
	planned, err := s.Training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Name: "Planned", Date: "2026-03-10", TestProtocol: protocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.Training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ride-" + protocol, Sport: "cycling",
		Date: "2026-03-10", DurationSeconds: 2100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Training.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: session.ID, Rider: "wilant", WorkoutID: planned.ID, Outcome: "completed", PowerCurve: curve,
	}); err != nil {
		t.Fatal(err)
	}
	sessions, err := s.Training.ListSessions(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	return sessions
}

func TestDetectThresholdsReadsATestRideFromItsPlannedWorkout(t *testing.T) {
	s := newThresholdTestServer(t)
	sessions := seedTestRide(t, s, "ramp", map[string]float64{"60": 416, "1200": 400})

	profile := workout.RiderProfile{Rider: "wilant", FTPWatts: 250} // rider-typed
	result, err := s.detectThresholds(t.Context(), "wilant", profile, sessions, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile.FTPWatts != 250 || len(result.Detected) != 0 {
		t.Fatalf("a rider-typed FTP was applied over: %+v %+v", result.Profile, result.Detected)
	}
	pending, err := s.Training.ListPendingSuggestions(t.Context(), "wilant")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v err=%v, want one", pending, err)
	}
	if pending[0].Value != 312 || !strings.Contains(pending[0].Reason, "ramp test (416 W best minute)") {
		t.Errorf("suggestion = %+v, want the ramp's 312, not the 380 eFTP", pending[0])
	}
	if !result.HasFTPPowerCurve {
		t.Error("a test result must stop the crude EstimateFTP fallback from running")
	}
}

func TestDetectThresholdsAutoAppliesATestToAnEstimatedFTP(t *testing.T) {
	s := newThresholdTestServer(t)
	sessions := seedTestRide(t, s, "ramp", map[string]float64{"60": 416})

	profile := workout.RiderProfile{Rider: "wilant", FTPWatts: 250, FTPEstimated: true}
	result, err := s.detectThresholds(t.Context(), "wilant", profile, sessions, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile.FTPWatts != 312 || len(result.Detected) != 1 {
		t.Fatalf("profile %+v detected %+v, want auto 312", result.Profile, result.Detected)
	}
}

func TestDetectThresholdsTreatsAnOrdinaryPlannedRideAsBefore(t *testing.T) {
	s := newThresholdTestServer(t)
	sessions := seedTestRide(t, s, "", map[string]float64{"60": 416, "1200": 400})

	result, err := s.detectThresholds(t.Context(), "wilant", workout.RiderProfile{Rider: "wilant", FTPWatts: 250, FTPEstimated: true}, sessions, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile.FTPWatts != 380 {
		t.Errorf("ftp = %v, want the ordinary eFTP 380", result.Profile.FTPWatts)
	}
}

func TestATestSuggestionIgnoresAnEarlierDismissalMargin(t *testing.T) {
	s := newThresholdTestServer(t)
	ctx := t.Context()
	dismissed, err := s.Training.CreateSuggestion(ctx, workout.ThresholdSuggestion{
		Rider: "wilant", Field: "ftp", Value: 310, Previous: 250, Direction: "up",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Training.MarkSuggestionDismissed(ctx, dismissed.ID); err != nil {
		t.Fatal(err)
	}
	// 312 is not 3% beyond 310, so an eFTP finding would stay quiet; a test
	// the rider just rode must not.
	f := thresholds.Finding{Field: "ftp", Direction: "up", Value: 312, Previous: 250, Reason: "from a test", FromTest: true}
	if err := s.upsertThresholdSuggestion(ctx, "wilant", f); err != nil {
		t.Fatal(err)
	}
	pending, _ := s.Training.ListPendingSuggestions(ctx, "wilant")
	if len(pending) != 1 || pending[0].Value != 312 {
		t.Fatalf("pending = %+v, want the test's 312", pending)
	}
}
