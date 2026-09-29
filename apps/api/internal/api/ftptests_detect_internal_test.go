package api

import (
	"strings"
	"testing"

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

// A test the rider just rode is shown even when it lands within the 3% margin
// of an earlier dismissal (they asked for this measurement), but only on the
// sync that first reads it: the finding recurs on every later sync while the
// ride is in the 42-day window, and there the dismissal must hold.
func TestAFreshTestSuggestionIgnoresAnEarlierDismissalMarginButALaterSyncDoesNot(t *testing.T) {
	seed := func(t *testing.T) (*Server, []workout.CompletedSession) {
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
		return s, seedTestRide(t, s, "ramp", map[string]float64{"60": 416}) // 312: within 3% of 310
	}
	profile := workout.RiderProfile{Rider: "wilant", FTPWatts: 250}

	s, sessions := seed(t)
	fresh := map[string]bool{"garmin:ride-ramp": true}
	if _, err := s.detectThresholdsFresh(t.Context(), "wilant", profile, sessions, fixedNow, fresh); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.Training.ListPendingSuggestions(t.Context(), "wilant"); len(pending) != 1 || pending[0].Value != 312 {
		t.Fatalf("fresh: pending = %+v, want the test's 312", pending)
	}

	s, sessions = seed(t)
	if _, err := s.detectThresholds(t.Context(), "wilant", profile, sessions, fixedNow); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.Training.ListPendingSuggestions(t.Context(), "wilant"); len(pending) != 0 {
		t.Errorf("later sync: pending = %+v, want the dismissal to hold", pending)
	}
}

func TestDetectThresholdsReportsWhatBecameOfAFreshTestsFinding(t *testing.T) {
	for name, c := range map[string]struct {
		profile workout.RiderProfile
		want    string
	}{
		"rider-typed FTP":      {workout.RiderProfile{Rider: "wilant", FTPWatts: 250}, "suggested"},
		"estimated FTP":        {workout.RiderProfile{Rider: "wilant", FTPWatts: 260, FTPEstimated: true}, "applied"},
		"within 1% of the FTP": {workout.RiderProfile{Rider: "wilant", FTPWatts: 312}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			s := newThresholdTestServer(t)
			sessions := seedTestRide(t, s, "ramp", map[string]float64{"60": 416})
			res, err := s.detectThresholdsFresh(t.Context(), "wilant", c.profile, sessions, fixedNow, map[string]bool{"garmin:ride-ramp": true})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.TestOutcomes["garmin:ride-ramp"]; got != c.want {
				t.Errorf("outcome = %q, want %q", got, c.want)
			}
		})
	}
}
