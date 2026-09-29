package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/fitnesstest"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type syncFTPTestsOut struct {
	Synced   int `json:"synced"`
	FTPTests []struct {
		WorkoutID string  `json:"workoutId"`
		Protocol  string  `json:"protocol"`
		FTPWatts  float64 `json:"ftpWatts"`
		Date      string  `json:"date"`
		Outcome   string  `json:"outcome"`
		Source    string  `json:"source"`
		TestWatts float64 `json:"testWatts"`
	} `json:"ftpTests"`
	Detected []struct {
		Field string  `json:"field"`
		Value float64 `json:"value"`
	} `json:"detected"`
}

// ftpResultHarness is a rider who rode a test yesterday: the test workout is
// planned on that day and a real FIT file with constant power is downloaded.
type ftpResultHarness struct {
	*metricsSyncHarness
	testID string
	date   string
	fake   *fakeGarmin
}

// newFTPResultHarness plans protocol as a test yesterday and has the rider ride
// it at a constant watts (so every best-effort window equals watts). watts 0
// makes a ride with no power data at all.
func newFTPResultHarness(t *testing.T, protocol string, watts int, profile workout.RiderProfile) *ftpResultHarness {
	t.Helper()
	return newFTPResultHarnessFor(t, protocol, watts, 1200, profile)
}

// newFTPResultHarnessFor is newFTPResultHarness with the ride's length chosen.
func newFTPResultHarnessFor(t *testing.T, protocol string, watts, seconds int, profile workout.RiderProfile) *ftpResultHarness {
	t.Helper()
	start := time.Now().AddDate(0, 0, -1)
	fake := &fakeGarmin{
		activities: []garmin.Activity{{ID: "9100", Sport: "cycling", StartTime: start, DurationSeconds: float64(seconds), AvgPowerWatts: float64(watts)}},
		fitByID:    map[string][]byte{"9100": buildRideFIT(t, start, seconds, watts)},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	ctx := context.Background()

	profile.Rider = "wilant"
	if _, err := h.srv.Training.SaveProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	req, ok := fitnesstest.BuildTestWorkout(protocol, 250)
	if !ok {
		t.Fatalf("unknown protocol %q", protocol)
	}
	req.Rider, req.Date, req.TestProtocol = "wilant", start.Format("2006-01-02"), protocol
	test, err := h.srv.Training.CreateWorkout(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return &ftpResultHarness{metricsSyncHarness: h, testID: test.ID, date: req.Date, fake: fake}
}

func (h *ftpResultHarness) sync() syncFTPTestsOut {
	h.t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("sync status = %d", resp.StatusCode)
	}
	var out syncFTPTestsOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *ftpResultHarness) pending() []struct {
	ID     string  `json:"id"`
	Field  string  `json:"field"`
	Value  float64 `json:"value"`
	Reason string  `json:"reason"`
} {
	h.t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/thresholds", "")
	var out struct {
		Suggestions []struct {
			ID     string  `json:"id"`
			Field  string  `json:"field"`
			Value  float64 `json:"value"`
			Reason string  `json:"reason"`
		} `json:"suggestions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	return out.Suggestions
}

func (h *ftpResultHarness) ftp() (float64, bool) {
	h.t.Helper()
	p, _, err := h.srv.Training.GetProfile(context.Background(), "wilant")
	if err != nil {
		h.t.Fatal(err)
	}
	return p.FTPWatts, p.FTPEstimated
}

func TestSyncCapturesATestOnARiderTypedFTPAsASuggestionOnce(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 400, workout.RiderProfile{FTPWatts: 250})

	out := h.sync()
	if len(out.FTPTests) != 1 {
		t.Fatalf("ftpTests = %+v, want one", out.FTPTests)
	}
	got := out.FTPTests[0]
	if got.WorkoutID != h.testID || got.Protocol != "ramp" || got.FTPWatts != 300 || got.Outcome != "suggested" || got.Date != h.date {
		t.Errorf("ftpTest = %+v, want ramp 300 W suggested on %s", got, h.date)
	}
	if ftp, _ := h.ftp(); ftp != 250 {
		t.Errorf("a rider-typed FTP was overwritten: %v", ftp)
	}
	pending := h.pending()
	if len(pending) != 1 || pending[0].Value != 300 {
		t.Fatalf("pending = %+v, want one suggestion at 300", pending)
	}

	stored, _ := h.srv.Training.GetWorkout(context.Background(), h.testID)
	if stored.TestResultWatts != 300 {
		t.Errorf("stored result = %v, want 300", stored.TestResultWatts)
	}

	// A second sync neither re-toasts nor changes anything.
	again := h.sync()
	if len(again.FTPTests) != 0 {
		t.Errorf("second sync ftpTests = %+v, want none", again.FTPTests)
	}
	if pending := h.pending(); len(pending) != 1 || pending[0].Value != 300 {
		t.Errorf("second sync pending = %+v", pending)
	}
}

func TestADismissedTestSuggestionIsNotOfferedAgainBySecondSync(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 400, workout.RiderProfile{FTPWatts: 250})
	h.sync()
	sug := h.pending()
	if len(sug) != 1 {
		t.Fatalf("pending = %+v", sug)
	}
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/thresholds/"+sug[0].ID, `{"action":"dismiss"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("dismiss status = %d", resp.StatusCode)
	}
	h.sync()
	if pending := h.pending(); len(pending) != 0 {
		t.Errorf("a dismissed test suggestion came back: %+v", pending)
	}
}

func TestSyncAutoAppliesATestToAnEstimatedFTP(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 400, workout.RiderProfile{FTPWatts: 260, FTPEstimated: true})
	out := h.sync()
	if len(out.FTPTests) != 1 || out.FTPTests[0].Outcome != "applied" || out.FTPTests[0].FTPWatts != 300 {
		t.Fatalf("ftpTests = %+v, want an applied 300 W", out.FTPTests)
	}
	if ftp, _ := h.ftp(); ftp != 300 {
		t.Errorf("ftp = %v, want 300 applied", ftp)
	}
	if len(h.pending()) != 0 {
		t.Error("an auto-applied result must not also leave a suggestion")
	}
}

func TestSyncReportsAResultWithinOnePercentAsConfirmed(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 400, workout.RiderProfile{FTPWatts: 300})
	out := h.sync()
	if len(out.FTPTests) != 1 || out.FTPTests[0].Outcome != "confirmed" || out.FTPTests[0].FTPWatts != 300 {
		t.Fatalf("ftpTests = %+v, want confirmed 300", out.FTPTests)
	}
	if len(h.pending()) != 0 {
		t.Errorf("a confirming test must not suggest anything: %+v", h.pending())
	}
	p, _, _ := h.srv.Training.GetProfile(context.Background(), "wilant")
	if p.FTPVerifiedAt == "" {
		t.Error("a test result verifies FTP whether or not it changed")
	}
}

func TestSyncUsesEachProtocolsOwnFormula(t *testing.T) {
	for protocol, want := range map[string]float64{"twenty_minute": 285, "two_by_eight": 270} {
		h := newFTPResultHarness(t, protocol, 300, workout.RiderProfile{FTPWatts: 200})
		out := h.sync()
		if len(out.FTPTests) != 1 || out.FTPTests[0].FTPWatts != want {
			t.Errorf("%s: ftpTests = %+v, want %v W", protocol, out.FTPTests, want)
		}
	}
}

func TestSyncReportsARideWithNoPowerAsUnreadableOnceAndOnlyOnce(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 0, workout.RiderProfile{FTPWatts: 250})
	out := h.sync()
	if len(out.FTPTests) != 1 || out.FTPTests[0].Outcome != "unreadable" || out.FTPTests[0].FTPWatts != 0 {
		t.Fatalf("ftpTests = %+v, want one unreadable", out.FTPTests)
	}
	if ftp, _ := h.ftp(); ftp != 250 {
		t.Errorf("ftp = %v, an unreadable ride must change nothing", ftp)
	}
	if again := h.sync(); len(again.FTPTests) != 0 {
		t.Errorf("second sync re-reported the unreadable test: %+v", again.FTPTests)
	}
	stored, _ := h.srv.Training.GetWorkout(context.Background(), h.testID)
	if stored.TestResultWatts != workout.TestResultUnreadable {
		t.Errorf("stored marker = %v, want the unreadable marker", stored.TestResultWatts)
	}
}

func TestSyncIgnoresATestThatWasNotRidden(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 400, workout.RiderProfile{FTPWatts: 250})
	// Move the test to a day with no ride: it is never linked, so nothing is read.
	other := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	if _, err := h.srv.Training.UpdateWorkout(context.Background(), h.testID, workout.UpdateWorkoutRequest{Date: &other}); err != nil {
		t.Fatal(err)
	}
	if out := h.sync(); len(out.FTPTests) != 0 {
		t.Errorf("ftpTests = %+v for a test with no ride", out.FTPTests)
	}
}

// A FIT that cannot be fetched or decoded says nothing about the ride: the
// test is not spent, and the next sync that gets the file reads it.
func TestATransientFITFailureDoesNotBurnTheTest(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 400, workout.RiderProfile{FTPWatts: 250})
	fit := h.fake.fitByID["9100"]

	h.fake.fitErr = errors.New("connect is having a moment")
	if out := h.sync(); len(out.FTPTests) != 0 {
		t.Fatalf("a failed download reported %+v", out.FTPTests)
	}
	stored, _ := h.srv.Training.GetWorkout(context.Background(), h.testID)
	if stored.TestResultWatts != 0 {
		t.Fatalf("result = %v after a failed download, want the test left open", stored.TestResultWatts)
	}

	h.fake.fitErr = nil
	h.fake.fitByID["9100"] = []byte("not a fit file") // downloads, cannot be decoded
	if out := h.sync(); len(out.FTPTests) != 0 {
		t.Fatalf("a failed decode reported %+v", out.FTPTests)
	}

	h.fake.fitByID["9100"] = fit
	out := h.sync()
	if len(out.FTPTests) != 1 || out.FTPTests[0].Outcome != "suggested" || out.FTPTests[0].FTPWatts != 300 {
		t.Fatalf("after the file arrived: %+v, want a suggested 300 W", out.FTPTests)
	}
}

// A ride that decoded fine but is too short for the protocol's window is a
// genuine "no result", and only that gets the unreadable marker.
func TestADecodedRideTooShortForTheWindowIsUnreadable(t *testing.T) {
	h := newFTPResultHarnessFor(t, "ramp", 400, 30, workout.RiderProfile{FTPWatts: 250})
	out := h.sync()
	if len(out.FTPTests) != 1 || out.FTPTests[0].Outcome != "unreadable" {
		t.Fatalf("ftpTests = %+v, want one unreadable", out.FTPTests)
	}
	stored, _ := h.srv.Training.GetWorkout(context.Background(), h.testID)
	if stored.TestResultWatts != workout.TestResultUnreadable {
		t.Errorf("marker = %v, want the unreadable marker", stored.TestResultWatts)
	}
}

// When a real breakthrough in the rider's ordinary rides outranks the test, the
// report says so and carries the number that actually moved (or was offered),
// not the test's.
func TestSyncReportsTheRidesFindingWhenAnEFTPBreakthroughOutranksTheTest(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 400, workout.RiderProfile{FTPWatts: 250}) // test reads 300
	other := time.Now().AddDate(0, 0, -3)
	h.fake.activities = append(h.fake.activities, garmin.Activity{
		ID: "9200", Sport: "cycling", StartTime: other, DurationSeconds: 1200, AvgPowerWatts: 360,
	})
	h.fake.fitByID["9200"] = buildRideFIT(t, other, 1200, 360) // 0.95 x 360 = 342: over 300 x 1.03

	out := h.sync()
	if len(out.FTPTests) != 1 {
		t.Fatalf("ftpTests = %+v, want one", out.FTPTests)
	}
	got := out.FTPTests[0]
	if got.Outcome != "suggested" || got.Source != "rides" || got.FTPWatts != 342 || got.TestWatts != 300 {
		t.Errorf("ftpTest = %+v, want a suggested 342 W from the rides, test 300 W", got)
	}
	if pending := h.pending(); len(pending) != 1 || pending[0].Value != 342 {
		t.Errorf("pending = %+v, want the rides' 342", pending)
	}
}

func TestSyncReportsATestFindingAsFromTheTest(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 400, workout.RiderProfile{FTPWatts: 250})
	if got := h.sync().FTPTests[0]; got.Source != "test" || got.TestWatts != 0 {
		t.Errorf("ftpTest = %+v, want source test and no separate test watts", got)
	}
}
