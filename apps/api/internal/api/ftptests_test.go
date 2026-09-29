package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// ftpClock is far enough after any FTP verified "today" (the store dates those
// with the real clock) that it reads as stale, and a Tuesday so the expected
// day is easy to state: with Tue/Thu available the next one from tomorrow is
// Thursday 2031-03-20.
var ftpClock = time.Date(2031, 3, 18, 9, 0, 0, 0, time.UTC)

type ftpTestsOut struct {
	Protocols []struct {
		ID          string `json:"id"`
		TrainerMode string `json:"trainerMode"`
	} `json:"protocols"`
	Suggestion *struct {
		Reason            string `json:"reason"`
		Message           string `json:"message"`
		Date              string `json:"date"`
		Recommended       string `json:"recommended"`
		ReplacesWorkoutID string `json:"replacesWorkoutId"`
	} `json:"suggestion"`
	Scheduled *struct {
		WorkoutID string `json:"workoutId"`
		Protocol  string `json:"protocol"`
		Date      string `json:"date"`
	} `json:"scheduled"`
	LastTest *struct {
		Protocol    string  `json:"protocol"`
		Date        string  `json:"date"`
		ResultWatts float64 `json:"resultWatts"`
	} `json:"lastTest"`
	FTPVerifiedAt string `json:"ftpVerifiedAt"`
}

func getFTPTests(t *testing.T, h *trainingHarness, rider string) ftpTestsOut {
	t.Helper()
	resp := h.as(rider, "cyclists", http.MethodGet, "/api/training/tests", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/training/tests = %d, want 200", resp.StatusCode)
	}
	var out ftpTestsOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func seedRiderWithStaleFTP(t *testing.T, h *trainingHarness, rider string) {
	t.Helper()
	h.srv.Clock = func() time.Time { return ftpClock }
	if _, err := h.store.SaveProfile(context.Background(), workout.RiderProfile{
		Rider: rider, FTPWatts: 250, AvailableDays: []string{"tue", "thu"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGetFTPTestsListsProtocolsAndSuggestsForAStaleFTP(t *testing.T) {
	h := newTrainingHarness(t)
	seedRiderWithStaleFTP(t, h, "wilant")

	out := getFTPTests(t, h, "wilant")
	if len(out.Protocols) != 3 || out.Protocols[0].ID != "ramp" || out.Protocols[0].TrainerMode != "erg" {
		t.Errorf("protocols = %+v", out.Protocols)
	}
	if out.Suggestion == nil {
		t.Fatal("want a suggestion for an FTP verified years ago")
	}
	if out.Suggestion.Reason != "stale" || out.Suggestion.Date != "2031-03-20" || out.Suggestion.Recommended != "ramp" {
		t.Errorf("suggestion = %+v", out.Suggestion)
	}
	if out.FTPVerifiedAt == "" {
		t.Error("ftpVerifiedAt should carry the profile's date")
	}
	if out.Scheduled != nil || out.LastTest != nil {
		t.Errorf("scheduled/lastTest = %+v / %+v, want none", out.Scheduled, out.LastTest)
	}
}

func TestGetFTPTestsNoSuggestionWithoutPowerEvidence(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = func() time.Time { return ftpClock }
	out := getFTPTests(t, h, "wilant") // no profile, no sessions
	if out.Suggestion != nil {
		t.Errorf("suggestion = %+v for a rider with no FTP and no power sessions", out.Suggestion)
	}
	if len(out.Protocols) != 3 {
		t.Errorf("protocols = %+v", out.Protocols)
	}
}

func TestGetFTPTestsSuggestsSettingFTPWhenPowerSessionsExistButNoFTP(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = func() time.Time { return ftpClock }
	if _, err := h.store.UpsertSession(context.Background(), workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "p1", Sport: "cycling",
		Date: "2031-03-10", DurationSeconds: 3600, AvgPowerWatts: 200,
	}); err != nil {
		t.Fatal(err)
	}
	out := getFTPTests(t, h, "wilant")
	if out.Suggestion == nil || out.Suggestion.Reason != "no_ftp" || out.Suggestion.Recommended != "twenty_minute" {
		t.Errorf("suggestion = %+v, want no_ftp recommending the 20-minute test", out.Suggestion)
	}
}

func TestSnoozeSilencesForTwentyEightDaysAndIsPerRider(t *testing.T) {
	h := newTrainingHarness(t)
	seedRiderWithStaleFTP(t, h, "wilant")
	seedRiderWithStaleFTP(t, h, "sam")

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/tests/ftp/snooze", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("snooze = %d, want 204", resp.StatusCode)
	}
	if getFTPTests(t, h, "wilant").Suggestion != nil {
		t.Error("snoozed rider still sees the suggestion")
	}
	if getFTPTests(t, h, "sam").Suggestion == nil {
		t.Error("another rider's suggestion was silenced by wilant's snooze")
	}
	p, _, _ := h.store.GetProfile(context.Background(), "wilant")
	if p.FTPTestSnoozedUntil != "2031-04-15" { // 2031-03-18 + 28 days
		t.Errorf("snoozed until %q, want 2031-04-15", p.FTPTestSnoozedUntil)
	}

	// After the snooze the banner returns because the rules still fire.
	h.srv.Clock = func() time.Time { return ftpClock.AddDate(0, 0, 29) }
	if getFTPTests(t, h, "wilant").Suggestion == nil {
		t.Error("suggestion did not return after the snooze")
	}
}

func TestSnoozeWorksForARiderWithNoProfileRow(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = func() time.Time { return ftpClock }
	if _, err := h.store.UpsertSession(context.Background(), workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "p1", Sport: "cycling",
		Date: "2031-03-10", DurationSeconds: 3600, AvgPowerWatts: 200,
	}); err != nil {
		t.Fatal(err)
	}
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/tests/ftp/snooze", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("snooze = %d", resp.StatusCode)
	}
	if getFTPTests(t, h, "wilant").Suggestion != nil {
		t.Error("the snooze was not stored for a rider with no profile row")
	}
}

func TestFTPTestsEndpointsNeedTheTrainingPermission(t *testing.T) {
	h := newTrainingHarness(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/training/tests"},
		{http.MethodPost, "/api/training/tests/ftp/snooze"},
	} {
		if resp := h.as("guest", "guests", c.method, c.path, ""); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as a viewer = %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestScheduledTestSilencesTheSuggestionAndShowsInTheResponse(t *testing.T) {
	h := newTrainingHarness(t)
	seedRiderWithStaleFTP(t, h, "wilant")
	if _, err := h.store.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", Name: "FTP Test (ramp)", Date: "2031-03-25", TestProtocol: "ramp",
	}); err != nil {
		t.Fatal(err)
	}
	out := getFTPTests(t, h, "wilant")
	if out.Suggestion != nil {
		t.Errorf("suggestion = %+v with a test already scheduled", out.Suggestion)
	}
	if out.Scheduled == nil || out.Scheduled.Date != "2031-03-25" || out.Scheduled.Protocol != "ramp" {
		t.Errorf("scheduled = %+v", out.Scheduled)
	}
}

func TestLastTestIsTheLatestCompletedOneAndSetsTheRecommendation(t *testing.T) {
	h := newTrainingHarness(t)
	seedRiderWithStaleFTP(t, h, "wilant")
	ctx := context.Background()
	for _, tc := range []struct {
		protocol, date string
		result         float64
	}{
		{"ramp", "2031-01-05", 240},
		{"two_by_eight", "2031-02-10", 262},
		{"twenty_minute", "2031-03-01", 0}, // never ridden: no result, not a "last test"
	} {
		w, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
			Rider: "wilant", Name: "Test " + tc.protocol, Date: tc.date, TestProtocol: tc.protocol,
		})
		if err != nil {
			t.Fatal(err)
		}
		if tc.result > 0 {
			if _, err := h.store.SetTestResult(ctx, w.ID, tc.result); err != nil {
				t.Fatal(err)
			}
		}
	}
	out := getFTPTests(t, h, "wilant")
	if out.LastTest == nil || out.LastTest.Protocol != "two_by_eight" || out.LastTest.ResultWatts != 262 {
		t.Errorf("lastTest = %+v, want the 2 x 8 with 262 W", out.LastTest)
	}
	if out.Suggestion == nil || out.Suggestion.Recommended != "two_by_eight" {
		t.Errorf("suggestion = %+v, want like-for-like two_by_eight", out.Suggestion)
	}
}
