package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// buildRideFIT encodes a minimal, synthetic FIT activity file — one record
// per second, all at the same power — with the repo's own encoder. No real
// ride ever produced these bytes, and nothing here is written to disk: the
// same "never persist raw ride data" rule the global constraints state for
// fixtures, satisfied by only ever holding this in memory for one test.
func buildRideFIT(t *testing.T, start time.Time, seconds, watts int) []byte {
	t.Helper()

	act := filedef.NewActivity()
	act.FileId = *mesgdef.NewFileId(nil).
		SetType(typedef.FileActivity).
		SetManufacturer(typedef.ManufacturerDevelopment).
		SetProduct(0).
		SetTimeCreated(start).
		SetSerialNumber(0)

	for i := 0; i < seconds; i++ {
		r := mesgdef.NewRecord(nil).SetTimestamp(start.Add(time.Duration(i) * time.Second))
		if watts > 0 {
			r.SetPower(uint16(watts))
		}
		act.Records = append(act.Records, r)
	}

	fitFile := act.ToFIT(nil)
	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&fitFile); err != nil {
		t.Fatalf("encode fixture fit: %v", err)
	}
	return buf.Bytes()
}

// TestSyncAnalysesARideAgainstItsPlannedWorkout drives Task 5 end to end: a
// synced ride with a matching planned workout gets an analysis saved,
// training_load is updated to the analysed figure, and fitness snapshots are
// recomputed off the back of it.
func TestSyncAnalysesARideAgainstItsPlannedWorkout(t *testing.T) {
	start := time.Now().AddDate(0, 0, -1)
	date := start.Format("2006-01-02")
	fake := &fakeGarmin{
		activities: []garmin.Activity{
			{ID: "5100", Sport: "cycling", StartTime: start, DurationSeconds: 1200, AvgPowerWatts: 180},
		},
		fitByID: map[string][]byte{"5100": buildRideFIT(t, start, 1200, 180)},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")

	// FTP on file so Analyze can score real power data, not just fall back
	// to the duration-only estimate.
	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", `{"ftpWatts":200}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save profile: status = %d", resp.StatusCode)
	}

	if _, err := h.srv.Training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "20 min steady", Date: date,
		Steps: []workout.WorkoutStep{
			{Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime,
				Seconds: 1200, Target: workout.TargetPower, TargetLow: 150, TargetHigh: 200},
		},
	}); err != nil {
		t.Fatalf("create planned workout: %v", err)
	}

	resp = h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Synced int `json:"synced"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Synced != 1 {
		t.Fatalf("synced = %d, want 1", out.Synced)
	}

	analysis, ok, err := h.srv.Training.GetAnalysis(context.Background(), "garmin:5100")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected a stored analysis for garmin:5100")
	}
	if analysis.Outcome == "" {
		t.Error("expected a non-empty outcome")
	}
	if analysis.LoadSource != "fit_power" {
		t.Errorf("loadSource = %q, want fit_power (a real FIT file with power was downloaded)", analysis.LoadSource)
	}
	if analysis.TSS <= 0 {
		t.Errorf("tss = %v, want > 0", analysis.TSS)
	}
	// The round-1 fix: rideanalysis.StepResult.Hard must survive the DTO
	// conversion (analysisStepsDTO) and the JSON round trip through storage —
	// this workout's only step is Active/power-targeted, so isHardStep says
	// it is hard.
	if len(analysis.Steps) != 1 || !analysis.Steps[0].Hard {
		t.Errorf("steps = %+v, want a single Hard step (Active intensity, power target)", analysis.Steps)
	}

	sessions, err := h.srv.Training.ListSessions(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].TrainingLoad != analysis.TSS {
		t.Errorf("sessions = %+v, want training_load == the analysed TSS %v", sessions, analysis.TSS)
	}

	snaps, err := h.srv.Training.ListFitnessSnapshots(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) == 0 {
		t.Error("expected fitness snapshots to have been recomputed")
	}
}

// TestSyncDoesNotAnalyseARideOlderThan42Days is the spec's own window: a
// ride from 50 days ago is still recorded (existing sync behaviour,
// untouched by this task) but must not get an analysis.
func TestSyncDoesNotAnalyseARideOlderThan42Days(t *testing.T) {
	old := time.Now().AddDate(0, 0, -50)
	fake := &fakeGarmin{
		activities: []garmin.Activity{
			{ID: "5200", Sport: "cycling", StartTime: old, DurationSeconds: 1200, AvgPowerWatts: 180},
		},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	_, ok, err := h.srv.Training.GetAnalysis(context.Background(), "garmin:5200")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("a ride older than 42 days must not be analysed")
	}
}

// TestSync25NewRidesCapsAt20FITDownloadsPerProvider is the spec's own
// worked example: a first sync for a rider with months of history downloads
// at most 20 FIT files for one provider, newest first, and the remaining 5
// catch up on the next sync.
func TestSync25NewRidesCapsAt20FITDownloadsPerProvider(t *testing.T) {
	const total = 25
	var activities []garmin.Activity
	for i := 0; i < total; i++ {
		activities = append(activities, garmin.Activity{
			ID:              fmt.Sprintf("r%d", i),
			Sport:           "cycling",
			StartTime:       time.Now().AddDate(0, 0, -i),
			DurationSeconds: 600,
			AvgPowerWatts:   150,
		})
	}
	fake := &fakeGarmin{activities: activities}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(fake.fitCalls) != 20 {
		t.Fatalf("fit downloads in first sync = %d, want exactly 20 (got %v)", len(fake.fitCalls), fake.fitCalls)
	}
	// Newest first: i=0..19 are the most recent 20 rides.
	for i := 0; i < 20; i++ {
		want := fmt.Sprintf("r%d", i)
		found := false
		for _, id := range fake.fitCalls {
			if id == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected a FIT download for %s (one of the newest 20), calls = %v", want, fake.fitCalls)
		}
	}

	fake.fitCalls = nil
	resp = h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second sync: status = %d, want 200", resp.StatusCode)
	}
	if len(fake.fitCalls) != 5 {
		t.Fatalf("fit downloads in second sync = %d, want exactly 5 (the remainder), got %v", len(fake.fitCalls), fake.fitCalls)
	}
}

// TestSyncFallsBackToSummaryWhenFITDownloadFails is the spec's failure path:
// a 500 (or any download error) still leaves the sync succeeding, with the
// ride analysed from the provider's own summary numbers instead, and a Warn
// logged.
func TestSyncFallsBackToSummaryWhenFITDownloadFails(t *testing.T) {
	start := time.Now().AddDate(0, 0, -1)
	fake := &fakeGarmin{
		activities: []garmin.Activity{
			{ID: "5300", Sport: "cycling", StartTime: start, DurationSeconds: 1200, AvgPowerWatts: 180,
				NormalizedPower: 185, TrainingStressScore: 60},
		},
		fitErr: fmt.Errorf("garmin: activity FIT download returned 500"),
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")

	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, nil))

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 even though the FIT download failed", resp.StatusCode)
	}

	analysis, ok, err := h.srv.Training.GetAnalysis(context.Background(), "garmin:5300")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected an analysis from the summary fallback")
	}
	if analysis.LoadSource != "provider_tss" && analysis.LoadSource != "estimate" {
		t.Errorf("loadSource = %q, want provider_tss or estimate (no FIT file was ever downloaded)", analysis.LoadSource)
	}

	if !strings.Contains(logs.String(), "fit download failed") {
		t.Errorf("logs = %q, want a warning about the failed fit download", logs.String())
	}
}

// TestSyncDoesNotReDownloadAnAlreadyAnalysedSession checks the idempotency
// half of the cap: a session analysed on one sync is not downloaded again on
// the next, even though the provider keeps listing it every time.
func TestSyncDoesNotReDownloadAnAlreadyAnalysedSession(t *testing.T) {
	start := time.Now().AddDate(0, 0, -1)
	fake := &fakeGarmin{
		activities: []garmin.Activity{
			{ID: "5400", Sport: "cycling", StartTime: start, DurationSeconds: 600, AvgPowerWatts: 150},
		},
		fitByID: map[string][]byte{"5400": buildRideFIT(t, start, 600, 150)},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")

	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if len(fake.fitCalls) != 1 {
		t.Fatalf("first sync: fit calls = %v, want exactly one", fake.fitCalls)
	}

	fake.fitCalls = nil
	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if len(fake.fitCalls) != 0 {
		t.Errorf("second sync: fit calls = %v, want none — the session is already analysed", fake.fitCalls)
	}
}

// TestSyncNeverMatchesAnotherRidersPlannedWorkout is the cross-rider safety
// property: a workout scheduled for a different rider on the same date and
// sport must never be picked up as the match for this rider's own ride.
func TestSyncNeverMatchesAnotherRidersPlannedWorkout(t *testing.T) {
	start := time.Now().AddDate(0, 0, -1)
	date := start.Format("2006-01-02")
	fake := &fakeGarmin{
		activities: []garmin.Activity{
			{ID: "5500", Sport: "cycling", StartTime: start, DurationSeconds: 1200, AvgPowerWatts: 180},
		},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")

	if _, err := h.srv.Training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "other", Sport: model.SportCycling, Name: "Someone else's plan", Date: date,
		Steps: []workout.WorkoutStep{
			{Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime,
				Seconds: 1200, Target: workout.TargetPower, TargetLow: 150, TargetHigh: 200},
		},
	}); err != nil {
		t.Fatalf("create the other rider's workout: %v", err)
	}

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	analysis, ok, err := h.srv.Training.GetAnalysis(context.Background(), "garmin:5500")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected a stored analysis")
	}
	if analysis.WorkoutID != "" {
		t.Errorf("workoutID = %q, want empty — another rider's workout must never match", analysis.WorkoutID)
	}
	if analysis.Outcome != "unplanned" {
		t.Errorf("outcome = %q, want unplanned", analysis.Outcome)
	}
}

// syncNailedThresholdRide is TestSyncAnalysesARideAgainstItsPlannedWorkout's
// own fixture shape (a ride that exactly matches a power-targeted planned
// workout, in seconds and in wattage), except the planned workout also
// carries a structured zone and level — the ingredient
// applyProgressionForAnalysis needs to have anything to move. Returns the
// harness and the session id the sync will produce.
func syncNailedThresholdRide(t *testing.T) (*metricsSyncHarness, string) {
	t.Helper()
	start := time.Now().AddDate(0, 0, -1)
	date := start.Format("2006-01-02")
	fake := &fakeGarmin{
		activities: []garmin.Activity{
			{ID: "6100", Sport: "cycling", StartTime: start, DurationSeconds: 1200, AvgPowerWatts: 180},
		},
		fitByID: map[string][]byte{"6100": buildRideFIT(t, start, 1200, 180)},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")

	if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", `{"ftpWatts":200}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("save profile: status = %d", resp.StatusCode)
	}

	if _, err := h.srv.Training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "Threshold 3x12", Date: date,
		Zone: workout.ZoneThreshold, Level: 5.0,
		Steps: []workout.WorkoutStep{
			{Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime,
				Seconds: 1200, Target: workout.TargetPower, TargetLow: 150, TargetHigh: 200},
		},
	}); err != nil {
		t.Fatalf("create planned workout: %v", err)
	}

	return h, "garmin:6100"
}

// TestSyncMovesTheProgressionLevelForANailedStructuredWorkout is Task 5's
// central RED/GREEN case: a synced ride that matched a structured, leveled
// planned workout and nailed it moves the rider's level for that zone,
// following the spec's own table — starting at the unset-profile default of
// 3.0, nailing a level-5.0 workout (diff = +2.0 >= -0.5) bumps to
// max(3.0, 5.0) + 0.3 = 5.3.
func TestSyncMovesTheProgressionLevelForANailedStructuredWorkout(t *testing.T) {
	h, sessionID := syncNailedThresholdRide(t)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	analysis, ok, err := h.srv.Training.GetAnalysis(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || analysis.Outcome != "nailed" {
		t.Fatalf("analysis = %+v, ok=%v, want a nailed outcome", analysis, ok)
	}

	levels, err := h.srv.Training.ListLevels(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	var threshold *workout.ProgressionLevel
	for i := range levels {
		if levels[i].Zone == workout.ZoneThreshold {
			threshold = &levels[i]
		}
	}
	if threshold == nil {
		t.Fatal("expected a threshold level to have been saved")
	}
	if threshold.Level != 5.3 {
		t.Errorf("threshold level = %v, want 5.3", threshold.Level)
	}
	if threshold.Reason == "" {
		t.Error("expected a non-empty reason for the level change")
	}
	if analysis.LevelDelta != 2.3 {
		t.Errorf("analysis.LevelDelta = %v, want 2.3 (5.3 - 3.0), stored for a later re-rate", analysis.LevelDelta)
	}
}

// TestSyncingTheSameAnalysedRideAgainNeverMovesTheLevelTwice is the global
// constraints' own review focus #5, and the resolution's explicit ask: a
// second sync of a ride that has already been analysed must not move its
// level a second time — analyseNewSessions' own candidates filter never
// re-analyses a session with a saved analysis, so applyProgressionForAnalysis
// never runs twice for it.
func TestSyncingTheSameAnalysedRideAgainNeverMovesTheLevelTwice(t *testing.T) {
	h, sessionID := syncNailedThresholdRide(t)

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("first sync: status = %d, want 200", resp.StatusCode)
	}
	levelsAfterFirst, err := h.srv.Training.ListLevels(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("second sync: status = %d, want 200", resp.StatusCode)
	}
	levelsAfterSecond, err := h.srv.Training.ListLevels(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}

	if len(levelsAfterFirst) != len(levelsAfterSecond) {
		t.Fatalf("levels after first sync = %+v, after second = %+v", levelsAfterFirst, levelsAfterSecond)
	}
	for i := range levelsAfterFirst {
		if levelsAfterFirst[i] != levelsAfterSecond[i] {
			t.Errorf("level %+v changed to %+v after a second sync of the same ride", levelsAfterFirst[i], levelsAfterSecond[i])
		}
	}

	analysis, ok, err := h.srv.Training.GetAnalysis(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || analysis.LevelDelta != 2.3 {
		t.Errorf("analysis.LevelDelta = %v (ok=%v), want it unchanged at 2.3 after a second sync", analysis.LevelDelta, ok)
	}
}

// TestSyncOfAnUnplannedRideMovesNoLevel proves the guard in
// applyProgressionForAnalysis: a ride with nothing planned to match against
// has no workout level to compare its outcome to, so it must not seed or
// move any progression level at all.
func TestSyncOfAnUnplannedRideMovesNoLevel(t *testing.T) {
	start := time.Now().AddDate(0, 0, -1)
	fake := &fakeGarmin{
		activities: []garmin.Activity{
			{ID: "6200", Sport: "cycling", StartTime: start, DurationSeconds: 1200, AvgPowerWatts: 180},
		},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	levels, err := h.srv.Training.ListLevels(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if len(levels) != 0 {
		t.Errorf("levels = %+v, want none — nothing was planned for this ride to match", levels)
	}
}

// TestSyncOfAnEndurancePlannedRideMovesNoLevel proves the other half of the
// same guard: matching an endurance (zone, but not structured) workout,
// which never carries a real level, must not move anything either.
func TestSyncOfAnEndurancePlannedRideMovesNoLevel(t *testing.T) {
	start := time.Now().AddDate(0, 0, -1)
	date := start.Format("2006-01-02")
	fake := &fakeGarmin{
		activities: []garmin.Activity{
			{ID: "6300", Sport: "cycling", StartTime: start, DurationSeconds: 1200, AvgPowerWatts: 180},
		},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")

	if _, err := h.srv.Training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "Endurance ride", Date: date,
		Zone: workout.ZoneEndurance,
		Steps: []workout.WorkoutStep{
			{Name: "Main", Duration: workout.DurationTime, Seconds: 1200, Target: workout.TargetOpen},
		},
	}); err != nil {
		t.Fatalf("create planned workout: %v", err)
	}

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	levels, err := h.srv.Training.ListLevels(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if len(levels) != 0 {
		t.Errorf("levels = %+v, want none — an endurance workout carries no level to move", levels)
	}
}
