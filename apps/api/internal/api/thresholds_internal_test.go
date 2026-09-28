package api

import (
	"errors"
	"net/http/httptest"
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

// TestUpsertThresholdSuggestionOppositeDirectionDismissalDoesNotGate is
// review round 1's fix #1: a dismissed *up* suggestion must never hold back
// a genuine *down* finding (or vice versa) — those are unrelated claims
// ("FTP rose" vs "FTP dropped"), not the same estimate moving further in
// one direction. Before the fix, LatestDismissedSuggestion ignored
// direction entirely, so this down finding would have been compared against
// the up dismissal's value and very likely swallowed.
func TestUpsertThresholdSuggestionOppositeDirectionDismissalDoesNotGate(t *testing.T) {
	s := newThresholdTestServer(t)
	ctx := t.Context()

	dismissedUp, err := s.Training.CreateSuggestion(ctx, workout.ThresholdSuggestion{
		Rider: "wilant", Field: "ftp", Value: 268, Previous: 250, Direction: "up",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Training.MarkSuggestionDismissed(ctx, dismissedUp.ID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	// 260 is deliberately chosen so a direction-blind comparison against the
	// *up* dismissal's value (268) would say "not moved far enough" (260 is
	// not <= 268*0.97 = 259.96) and wrongly suppress this — while a
	// correctly direction-scoped lookup finds no down dismissal at all and
	// creates it unconditionally.
	if err := s.upsertThresholdSuggestion(ctx, "wilant", thresholds.Finding{
		Field: "ftp", Direction: "down", Value: 260, Previous: 250, Reason: "no effort near 250 W in the last 90 days",
	}); err != nil {
		t.Fatalf("upsert down: %v", err)
	}
	pending, err := s.Training.ListPendingSuggestions(ctx, "wilant")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pending) != 1 || pending[0].Direction != "down" || pending[0].Value != 260 {
		t.Fatalf("pending = %+v, err=%v, want one down suggestion at 260", pending, err)
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

// TestDetectThresholdsDeletesAStalePendingSuggestionForAFieldWithNoFinding is
// review round 1's fix #2: a field that produces no finding at all this
// pass — the evidence aged out of the window, or the rider typed a value
// close enough that nothing qualifies any more — must not leave an earlier
// pending suggestion sitting around showing an outdated number.
func TestDetectThresholdsDeletesAStalePendingSuggestionForAFieldWithNoFinding(t *testing.T) {
	s := newThresholdTestServer(t)
	ctx := t.Context()

	if _, err := s.Training.CreateSuggestion(ctx, workout.ThresholdSuggestion{
		Rider: "wilant", Field: workout.FieldMaxHR, Value: 191, Previous: 188, Direction: "up",
	}); err != nil {
		t.Fatalf("seed stale suggestion: %v", err)
	}

	// No analysed rides at all this pass, so none of the three fields
	// produce a finding — max_hr's stale suggestion above must be cleared.
	if _, err := s.detectThresholds(ctx, "wilant", workout.RiderProfile{Rider: "wilant"}, nil, fixedNow); err != nil {
		t.Fatalf("detectThresholds: %v", err)
	}

	pending, err := s.Training.ListPendingSuggestions(ctx, "wilant")
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending = %+v, err=%v, want none — the stale max_hr suggestion should be cleared", pending, err)
	}
}

// TestDetectThresholdsLeavesAFieldsPendingSuggestionAloneWhenItStillFinds
// confirms the cleanup is per-field, not all-or-nothing: a field that DOES
// still produce a finding this pass keeps its own suggestion handling
// (upsertThresholdSuggestion's own replace-on-create), untouched by the
// stale-cleanup pass running for its sibling fields.
func TestDetectThresholdsLeavesAFieldsPendingSuggestionAloneWhenItStillFinds(t *testing.T) {
	s := newThresholdTestServer(t)
	ctx := t.Context()

	// A stale max_hr suggestion with nothing backing it this pass...
	if _, err := s.Training.CreateSuggestion(ctx, workout.ThresholdSuggestion{
		Rider: "wilant", Field: workout.FieldMaxHR, Value: 191, Previous: 188, Direction: "up",
	}); err != nil {
		t.Fatalf("seed stale max_hr suggestion: %v", err)
	}

	// ...alongside a real, currently-analysed cycling ride that still
	// produces an FTP finding this pass, for a rider-typed (never
	// overwritten) FTP.
	session, err := s.Training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "d3", Sport: "cycling",
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

	if _, err := s.detectThresholds(ctx, "wilant", workout.RiderProfile{Rider: "wilant", FTPWatts: 250}, sessions, fixedNow); err != nil {
		t.Fatalf("detectThresholds: %v", err)
	}

	pending, err := s.Training.ListPendingSuggestions(ctx, "wilant")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	fields := map[string]bool{}
	for _, p := range pending {
		fields[p.Field] = true
	}
	if fields[workout.FieldMaxHR] {
		t.Errorf("pending = %+v, want the stale max_hr suggestion gone", pending)
	}
	if !fields["ftp"] {
		t.Errorf("pending = %+v, want an ftp suggestion — that field still produced a finding", pending)
	}
}

// TestDetectThresholdsLoadsEnoughHistoryForTheDownDirectionRule is the fix
// for the bug where detectThresholds queried ListAnalyses back only
// thresholds.HistoryWindowDays (90 days), while
// thresholds.hasFTPHistoryBefore/hasPaceHistoryBefore need a ride strictly
// *before* that same 90-day window to prove enough history exists to trust
// a down-direction finding. A ride "strictly before the window" is, by
// construction, older than the query's own cutoff — so with the old
// sinceDate that ride could never be loaded at all, and the down rule could
// almost never fire. This seeds exactly that shape (an old power ride ~120
// days back — clear of the 90-day window's own start of ~89 days back — and
// recent low-power rides inside the 42-day detection window) through the
// real sync-time entry point, detectThresholds, and checks a pending "down"
// ftp suggestion actually gets created. Confirmed to fail against the old
// `now.AddDate(0, 0, -thresholds.HistoryWindowDays)` sinceDate: with that
// query, the ~120-day-old ride is excluded from ListAnalyses entirely,
// hasFTPHistoryBefore sees no history at all, and no suggestion is ever
// stored.
func TestDetectThresholdsLoadsEnoughHistoryForTheDownDirectionRule(t *testing.T) {
	s := newThresholdTestServer(t)
	ctx := t.Context()

	// Old ride: ~120 days before fixedNow, well clear of the 90-day
	// history window's own start (~89 days back) — this is what proves
	// "the rider's history reaches back far enough to trust the recent
	// silence means something," not the recent rides below.
	oldDate := fixedNow.AddDate(0, 0, -120).Format("2006-01-02")
	oldSession, err := s.Training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "old-ftp", Sport: "cycling",
		Date: oldDate, DurationSeconds: 1200,
	})
	if err != nil {
		t.Fatalf("upsert old session: %v", err)
	}
	if err := s.Training.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: oldSession.ID, Rider: "wilant", Outcome: "unplanned",
		PowerCurve: map[string]float64{"1200": 300},
	}); err != nil {
		t.Fatalf("save old analysis: %v", err)
	}

	// Recent ride: inside the 42-day detection window, with a best eFTP
	// well below 285 (300 * downFactor 0.95) — the recent evidence a down
	// finding needs to point to.
	recentDate := fixedNow.AddDate(0, 0, -10).Format("2006-01-02")
	recentSession, err := s.Training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "recent-ftp", Sport: "cycling",
		Date: recentDate, DurationSeconds: 1200,
	})
	if err != nil {
		t.Fatalf("upsert recent session: %v", err)
	}
	if err := s.Training.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: recentSession.ID, Rider: "wilant", Outcome: "unplanned",
		PowerCurve: map[string]float64{"1200": 270}, // eFTP 256.5, well under 285
	}); err != nil {
		t.Fatalf("save recent analysis: %v", err)
	}

	sessions, err := s.Training.ListSessions(ctx, "wilant")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}

	profile := workout.RiderProfile{Rider: "wilant", FTPWatts: 300} // rider-typed
	if _, err := s.detectThresholds(ctx, "wilant", profile, sessions, fixedNow); err != nil {
		t.Fatalf("detectThresholds: %v", err)
	}

	pending, err := s.Training.ListPendingSuggestions(ctx, "wilant")
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	var found *workout.ThresholdSuggestion
	for i := range pending {
		if pending[i].Field == "ftp" {
			found = &pending[i]
		}
	}
	if found == nil {
		t.Fatalf("pending = %+v, want a down ftp suggestion", pending)
	}
	if found.Direction != "down" {
		t.Errorf("suggestion direction = %q, want %q", found.Direction, "down")
	}
	if found.Previous != 300 {
		t.Errorf("suggestion previous = %v, want 300", found.Previous)
	}
}

// TestDetectThresholdsLoadsEnoughHistoryForTheDownDirectionRulePace is the
// threshold-pace equivalent of the FTP test above — same bug, same fix,
// different field, cheap to cover since the shape is identical.
func TestDetectThresholdsLoadsEnoughHistoryForTheDownDirectionRulePace(t *testing.T) {
	s := newThresholdTestServer(t)
	ctx := t.Context()

	// Old run: ~120 days back, well clear of the 90-day history window.
	oldDate := fixedNow.AddDate(0, 0, -120).Format("2006-01-02")
	oldSession, err := s.Training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "old-pace", Sport: "running",
		Date: oldDate, DurationSeconds: 1800,
	})
	if err != nil {
		t.Fatalf("upsert old session: %v", err)
	}
	if err := s.Training.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: oldSession.ID, Rider: "wilant", Outcome: "unplanned",
		BestSpeed1800: 4.0, // ~4:10/km — just needs to exist, value doesn't matter
	}); err != nil {
		t.Fatalf("save old analysis: %v", err)
	}

	// Recent run: inside the 42-day window, clearly slower than the
	// rider's current threshold pace — the recent evidence a down finding
	// needs to point to. Current pace 240 sec/km == speed 1000/240 ≈
	// 4.1667 m/s; downFactor 0.95 cutoff ≈ 3.958 m/s.
	recentDate := fixedNow.AddDate(0, 0, -10).Format("2006-01-02")
	recentSession, err := s.Training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "recent-pace", Sport: "running",
		Date: recentDate, DurationSeconds: 1800,
	})
	if err != nil {
		t.Fatalf("upsert recent session: %v", err)
	}
	if err := s.Training.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: recentSession.ID, Rider: "wilant", Outcome: "unplanned",
		BestSpeed1800: 3.5, // well below the 3.958 m/s down cutoff
	}); err != nil {
		t.Fatalf("save recent analysis: %v", err)
	}

	sessions, err := s.Training.ListSessions(ctx, "wilant")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}

	profile := workout.RiderProfile{Rider: "wilant", ThresholdPaceSecPerKM: 240} // rider-typed
	if _, err := s.detectThresholds(ctx, "wilant", profile, sessions, fixedNow); err != nil {
		t.Fatalf("detectThresholds: %v", err)
	}

	pending, err := s.Training.ListPendingSuggestions(ctx, "wilant")
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	var found *workout.ThresholdSuggestion
	for i := range pending {
		if pending[i].Field == workout.FieldThresholdPace {
			found = &pending[i]
		}
	}
	if found == nil {
		t.Fatalf("pending = %+v, want a down threshold_pace suggestion", pending)
	}
	if found.Direction != "down" {
		t.Errorf("suggestion direction = %q, want %q", found.Direction, "down")
	}
}

// TestFailThresholdResolveRaceMapsNotFoundTo409 covers review round 2's fix
// #3: handleResolveThreshold already fetched the suggestion and found it
// pending before calling MarkSuggestionAccepted/Dismissed, so if that call
// still comes back with workout.ErrThresholdSuggestionNotFound, it can only
// mean a second resolve of the same id won the race in between — the row is
// real, this request just saw it a moment too late. That is a 409 ("no
// longer pending"), not failTrainingLookup's ordinary 404 for an id that
// never existed at all; a 404 here would tell a client the resource
// vanished, when actually the client's own read of it a moment ago was
// correct and something else changed it out from under this request.
func TestFailThresholdResolveRaceMapsNotFoundTo409(t *testing.T) {
	s := newThresholdTestServer(t)

	rec := httptest.NewRecorder()
	s.failThresholdResolveRace(rec, workout.ErrThresholdSuggestionNotFound)
	if rec.Code != 409 {
		t.Errorf("status = %d, want 409 for a race against ErrThresholdSuggestionNotFound", rec.Code)
	}

	// Any other error is not this race — it falls through to the ordinary
	// 500 path, same as every other unexpected store error.
	rec = httptest.NewRecorder()
	s.failThresholdResolveRace(rec, errors.New("boom"))
	if rec.Code != 500 {
		t.Errorf("status = %d, want 500 for an unrelated error", rec.Code)
	}
}
