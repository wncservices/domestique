package api

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/thresholds"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// newThresholdTestServer builds a bare *Server with only a real Training
// store wired — enough for detectThresholds/upsertThresholdSuggestion,
// which never touch anything else. A fresh SQLite database, the same
// openStore-style helper internal/workout's own tests use, just inlined
// here since this package cannot import that unexported test helper.
func newThresholdTestServer(t *testing.T) *Server {
	t.Helper()
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { src.Close() })

	db, err := workout.UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatalf("use db: %v", err)
	}
	return &Server{Training: db}
}

// fixedNow is a fixed clock with an explicit zone, per the global
// constraints — every test in this file must pass under both TZ=UTC and
// TZ=Europe/Brussels, so nothing here reads time.Now().
var fixedNow = time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC)

// TestThresholdMovedFurtherGatesByDirectionAndMargin is thresholdMovedFurther's
// own unit coverage — the design spec's "a dismissed suggestion reappears
// only if a later estimate moves at least a further 3% beyond the dismissed
// value (1 bpm for max HR)", exercised directly rather than through a full
// sync (see TestUpsertThresholdSuggestionSkipsWithinMarginOfADismissedValue
// below for the storage-backed version of the same rule).
func TestThresholdMovedFurtherGatesByDirectionAndMargin(t *testing.T) {
	cases := []struct {
		name      string
		f         thresholds.Finding
		dismissed workout.ThresholdSuggestion
		want      bool
	}{
		{"ftp up exactly 3% further passes", thresholds.Finding{Field: "ftp", Direction: "up", Value: 268*1.03 - 0.0001}, workout.ThresholdSuggestion{Value: 268}, false},
		{"ftp up exactly at the 3% boundary passes", thresholds.Finding{Field: "ftp", Direction: "up", Value: 268 * 1.03}, workout.ThresholdSuggestion{Value: 268}, true},
		{"ftp up short of 3% fails", thresholds.Finding{Field: "ftp", Direction: "up", Value: 270}, workout.ThresholdSuggestion{Value: 268}, false},
		{"ftp down 3% further passes", thresholds.Finding{Field: "ftp", Direction: "down", Value: 268 * 0.97}, workout.ThresholdSuggestion{Value: 268}, true},
		{"ftp down short of 3% fails", thresholds.Finding{Field: "ftp", Direction: "down", Value: 265}, workout.ThresholdSuggestion{Value: 268}, false},
		{"max_hr up needs only +1 bpm", thresholds.Finding{Field: workout.FieldMaxHR, Direction: "up", Value: 192}, workout.ThresholdSuggestion{Value: 191}, true},
		{"max_hr up short of 1 bpm fails", thresholds.Finding{Field: workout.FieldMaxHR, Direction: "up", Value: 191}, workout.ThresholdSuggestion{Value: 191}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := thresholdMovedFurther(c.f, c.dismissed); got != c.want {
				t.Errorf("thresholdMovedFurther(%+v, %+v) = %v, want %v", c.f, c.dismissed, got, c.want)
			}
		})
	}
}

// TestUpsertThresholdSuggestionSkipsWithinMarginOfADismissedValue is review
// focus #2, exercised against a real store: a dismissed FTP suggestion
// blocks a barely-higher re-estimate, but not one that clears the further-3%
// bar — and once it does, the new value is what gets stored.
func TestUpsertThresholdSuggestionSkipsWithinMarginOfADismissedValue(t *testing.T) {
	s := newThresholdTestServer(t)
	ctx := t.Context()

	dismissed, err := s.Training.CreateSuggestion(ctx, workout.ThresholdSuggestion{
		Rider: "wilant", Field: "ftp", Value: 268, Previous: 250,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Training.MarkSuggestionDismissed(ctx, dismissed.ID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	// Barely above 268 (< 3% further) — must stay quiet.
	if err := s.upsertThresholdSuggestion(ctx, "wilant", thresholds.Finding{
		Field: "ftp", Direction: "up", Value: 270, Previous: 250, Reason: "too close",
	}); err != nil {
		t.Fatalf("upsert too-close: %v", err)
	}
	pending, err := s.Training.ListPendingSuggestions(ctx, "wilant")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after a too-close estimate = %+v, want none", pending)
	}

	// At least 3% beyond 268 (>= 276.04) — should now create one.
	if err := s.upsertThresholdSuggestion(ctx, "wilant", thresholds.Finding{
		Field: "ftp", Direction: "up", Value: 280, Previous: 250, Reason: "far enough",
	}); err != nil {
		t.Fatalf("upsert far-enough: %v", err)
	}
	pending, err = s.Training.ListPendingSuggestions(ctx, "wilant")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pending) != 1 || pending[0].Value != 280 {
		t.Fatalf("pending after a far-enough estimate = %+v, want one at 280", pending)
	}
}

// TestUpsertThresholdSuggestionWithNoDismissalAlwaysCreates confirms the
// gate only applies once a dismissal exists — an entirely new field/rider
// combination is never held back.
func TestUpsertThresholdSuggestionWithNoDismissalAlwaysCreates(t *testing.T) {
	s := newThresholdTestServer(t)
	ctx := t.Context()

	if err := s.upsertThresholdSuggestion(ctx, "wilant", thresholds.Finding{
		Field: "ftp", Direction: "up", Value: 260, Previous: 250, Reason: "first ever",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	pending, err := s.Training.ListPendingSuggestions(ctx, "wilant")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, err=%v, want exactly one", pending, err)
	}
}

// TestDetectThresholdsAppliesAutoFindingToAnEmptyField drives
// detectThresholds directly: a rider with no FTP, one analysed cycling ride
// with a 20-minute power-curve entry well above zero produces an Auto
// finding, which is folded straight into the returned profile and reported
// in Detected — never stored as a pending suggestion.
func TestDetectThresholdsAppliesAutoFindingToAnEmptyField(t *testing.T) {
	s := newThresholdTestServer(t)
	ctx := t.Context()

	session, err := s.Training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "d1", Sport: "cycling",
		Date: "2026-03-10", DurationSeconds: 1200,
	})
	if err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	if err := s.Training.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: session.ID, Rider: "wilant", Outcome: "unplanned",
		PowerCurve: map[string]float64{"1200": 280},
	}); err != nil {
		t.Fatalf("save analysis: %v", err)
	}

	sessions, err := s.Training.ListSessions(ctx, "wilant")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}

	result, err := s.detectThresholds(ctx, "wilant", workout.RiderProfile{Rider: "wilant"}, sessions, fixedNow)
	if err != nil {
		t.Fatalf("detectThresholds: %v", err)
	}

	wantFTP := 280 * 0.95
	if result.Profile.FTPWatts != wantFTP || !result.Profile.FTPEstimated {
		t.Fatalf("profile = %+v, want ftp=%v estimated=true", result.Profile, wantFTP)
	}
	if len(result.Detected) != 1 || result.Detected[0].Field != "ftp" || result.Detected[0].Value != wantFTP {
		t.Fatalf("detected = %+v", result.Detected)
	}
	if !result.HasFTPPowerCurve {
		t.Error("expected HasFTPPowerCurve = true — a 1200s power curve entry is in the 42-day window")
	}

	pending, err := s.Training.ListPendingSuggestions(ctx, "wilant")
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending = %+v, err=%v, want none for an Auto finding", pending, err)
	}
}

// TestDetectThresholdsSuggestsForARiderTypedField is the same scenario
// against an FTP the rider has already confirmed: the profile is returned
// unchanged and a suggestion is stored instead.
func TestDetectThresholdsSuggestsForARiderTypedField(t *testing.T) {
	s := newThresholdTestServer(t)
	ctx := t.Context()

	session, err := s.Training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "d2", Sport: "cycling",
		Date: "2026-03-10", DurationSeconds: 1200,
	})
	if err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	if err := s.Training.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: session.ID, Rider: "wilant", Outcome: "unplanned",
		PowerCurve: map[string]float64{"1200": 300},
	}); err != nil {
		t.Fatalf("save analysis: %v", err)
	}

	sessions, err := s.Training.ListSessions(ctx, "wilant")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}

	profile := workout.RiderProfile{Rider: "wilant", FTPWatts: 250} // rider-typed, not estimated
	result, err := s.detectThresholds(ctx, "wilant", profile, sessions, fixedNow)
	if err != nil {
		t.Fatalf("detectThresholds: %v", err)
	}

	if result.Profile.FTPWatts != 250 {
		t.Fatalf("profile ftp = %v, want unchanged 250", result.Profile.FTPWatts)
	}
	if len(result.Detected) != 0 {
		t.Fatalf("detected = %+v, want none — a rider-typed field is only ever suggested", result.Detected)
	}

	pending, err := s.Training.ListPendingSuggestions(ctx, "wilant")
	if err != nil || len(pending) != 1 || pending[0].Field != "ftp" {
		t.Fatalf("pending = %+v, err=%v, want one ftp suggestion", pending, err)
	}
	wantFTP := 300 * 0.95
	if pending[0].Value != wantFTP || pending[0].Previous != 250 {
		t.Errorf("suggestion = %+v, want value=%v previous=250", pending[0], wantFTP)
	}
}

// TestDetectThresholdsHasFTPPowerCurveFalseWithoutOne confirms the fallback
// gate: no analysed cycling ride with a 1200/3600 power-curve entry in the
// window means the old fitnesstest.EstimateFTP path must still be allowed
// to run — see its call site in syncRiderMetrics.
func TestDetectThresholdsHasFTPPowerCurveFalseWithoutOne(t *testing.T) {
	s := newThresholdTestServer(t)
	ctx := t.Context()

	result, err := s.detectThresholds(ctx, "wilant", workout.RiderProfile{Rider: "wilant"}, nil, fixedNow)
	if err != nil {
		t.Fatalf("detectThresholds: %v", err)
	}
	if result.HasFTPPowerCurve {
		t.Error("HasFTPPowerCurve = true with no analysed rides at all")
	}
	if len(result.Detected) != 0 {
		t.Errorf("detected = %+v, want none", result.Detected)
	}
}
