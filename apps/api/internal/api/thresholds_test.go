package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// TestThresholdsSyncAutoAppliesToAnEmptyFTPAndReportsDetected drives Task 3
// end to end: a rider with no FTP on file syncs a real 20-minute power
// effort, internal/thresholds.Detect finds an eFTP well above the (zero)
// current value, and — because FTP is unset, so Auto — the sync applies it
// straight to the profile (still marked estimated) and reports it in the
// sync result's own "detected" list, distinct from the cruder
// fitnesstest.EstimateFTP path (TestSyncEstimatesFTPFromAQualifyingSession
// in metricssync_test.go already covers that one; this test only passes
// because a real FIT file with a power curve is downloaded, giving
// detectThresholds something to find before the fallback would ever run).
func TestThresholdsSyncAutoAppliesToAnEmptyFTPAndReportsDetected(t *testing.T) {
	start := time.Now().AddDate(0, 0, -1)
	fake := &fakeGarmin{
		activities: []garmin.Activity{
			{ID: "8001", Sport: "cycling", StartTime: start, DurationSeconds: 1200, AvgPowerWatts: 280},
		},
		fitByID: map[string][]byte{"8001": buildRideFIT(t, start, 1200, 280)},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Synced   int `json:"synced"`
		Detected []struct {
			Field  string  `json:"field"`
			Value  float64 `json:"value"`
			Reason string  `json:"reason"`
		} `json:"detected"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Synced != 1 {
		t.Fatalf("synced = %d, want 1", out.Synced)
	}

	wantFTP := 280 * 0.95
	found := false
	for _, d := range out.Detected {
		if d.Field == "ftp" {
			found = true
			if d.Value != wantFTP {
				t.Errorf("detected ftp value = %v, want %v", d.Value, wantFTP)
			}
			if d.Reason == "" {
				t.Error("expected a non-empty reason")
			}
		}
	}
	if !found {
		t.Fatalf("detected = %+v, want an ftp entry", out.Detected)
	}

	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/profile", "")
	var profile struct {
		FTPWatts     float64 `json:"ftpWatts"`
		FTPEstimated bool    `json:"ftpEstimated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		t.Fatal(err)
	}
	if profile.FTPWatts != wantFTP || !profile.FTPEstimated {
		t.Errorf("profile = %+v, want ftpWatts=%v ftpEstimated=true", profile, wantFTP)
	}

	// Nothing pending: an Auto finding is applied, never stored as a
	// suggestion too.
	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/thresholds", "")
	var thresholds struct {
		Suggestions []any `json:"suggestions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&thresholds); err != nil {
		t.Fatal(err)
	}
	if len(thresholds.Suggestions) != 0 {
		t.Errorf("suggestions = %+v, want none for an auto-applied field", thresholds.Suggestions)
	}
}

// TestThresholdsSyncSuggestsInsteadOfOverwritingARiderTypedFTP is review
// focus #1: a rider-typed FTP must never change from a sync, only ever be
// suggested — the same safety property TestSyncNeverOverwritesARiderConfirmedFTP
// already proves for the older fitnesstest path, now proven for
// internal/thresholds too.
func TestThresholdsSyncSuggestsInsteadOfOverwritingARiderTypedFTP(t *testing.T) {
	start := time.Now().AddDate(0, 0, -1)
	fake := &fakeGarmin{
		activities: []garmin.Activity{
			{ID: "8002", Sport: "cycling", StartTime: start, DurationSeconds: 1200, AvgPowerWatts: 300},
		},
		fitByID: map[string][]byte{"8002": buildRideFIT(t, start, 1200, 300)},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")

	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", `{"ftpWatts":250}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save profile: status = %d", resp.StatusCode)
	}

	resp = h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	var out struct {
		Detected []any `json:"detected"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Detected) != 0 {
		t.Errorf("detected = %+v, want none — a rider-typed field is only ever suggested", out.Detected)
	}

	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/profile", "")
	var profile struct {
		FTPWatts     float64 `json:"ftpWatts"`
		FTPEstimated bool    `json:"ftpEstimated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		t.Fatal(err)
	}
	if profile.FTPWatts != 250 || profile.FTPEstimated {
		t.Fatalf("profile = %+v, want the rider's own 250 W untouched", profile)
	}

	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/thresholds", "")
	var thresholdsOut struct {
		Suggestions []struct {
			ID       string  `json:"id"`
			Field    string  `json:"field"`
			Value    float64 `json:"value"`
			Previous float64 `json:"previous"`
		} `json:"suggestions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&thresholdsOut); err != nil {
		t.Fatal(err)
	}
	if len(thresholdsOut.Suggestions) != 1 || thresholdsOut.Suggestions[0].Field != "ftp" {
		t.Fatalf("suggestions = %+v, want exactly one ftp suggestion", thresholdsOut.Suggestions)
	}
	wantFTP := 300 * 0.95
	if thresholdsOut.Suggestions[0].Value != wantFTP {
		t.Errorf("suggested ftp = %v, want %v", thresholdsOut.Suggestions[0].Value, wantFTP)
	}
	if thresholdsOut.Suggestions[0].Previous != 250 {
		t.Errorf("suggested previous = %v, want 250", thresholdsOut.Suggestions[0].Previous)
	}

	// A different rider's suggestions are never visible.
	resp = h.as("other", "cyclists", http.MethodGet, "/api/training/thresholds", "")
	var otherOut struct {
		Suggestions []any `json:"suggestions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&otherOut); err != nil {
		t.Fatal(err)
	}
	if len(otherOut.Suggestions) != 0 {
		t.Errorf("other rider's suggestions = %+v, want none", otherOut.Suggestions)
	}
}

// TestThresholdsAcceptWritesProfileAndClearsEstimated exercises accept:
// the profile is saved with the suggested value, the field's estimated
// marker is cleared (it becomes rider-typed from then on), and the
// suggestion itself moves out of the pending list.
func TestThresholdsAcceptWritesProfileAndClearsEstimated(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	ctx := context.Background()

	if _, err := h.srv.Training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", MaxHR: 188,
	}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	// Mark max_hr as estimated the same way autoprofile.Apply would, so
	// accept's own "clear the estimated flag" behaviour has something real
	// to clear.
	p, _, err := h.srv.Training.GetProfile(ctx, "wilant")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	p.MarkEstimated(workout.FieldMaxHR)
	if _, err := h.srv.Training.SaveProfile(ctx, p); err != nil {
		t.Fatalf("mark estimated: %v", err)
	}

	sug, err := h.srv.Training.CreateSuggestion(ctx, workout.ThresholdSuggestion{
		Rider: "wilant", Field: workout.FieldMaxHR, Value: 191, Previous: 188,
		SourceSessionID: "garmin:9001", SourceDate: "2026-01-12",
		Reason: "peak heart rate 191 on Tuesday's ride",
	})
	if err != nil {
		t.Fatalf("create suggestion: %v", err)
	}

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/thresholds/"+sug.ID, `{"action":"accept"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept status = %d", resp.StatusCode)
	}

	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/profile", "")
	var profile struct {
		MaxHR     int      `json:"maxHr"`
		Estimated []string `json:"estimated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		t.Fatal(err)
	}
	if profile.MaxHR != 191 {
		t.Errorf("maxHr = %d, want 191", profile.MaxHR)
	}
	for _, f := range profile.Estimated {
		if f == workout.FieldMaxHR {
			t.Errorf("estimated = %v, want max_hr cleared after accept", profile.Estimated)
		}
	}

	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/thresholds", "")
	var out struct {
		Suggestions []any `json:"suggestions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Suggestions) != 0 {
		t.Errorf("suggestions after accept = %+v, want none pending", out.Suggestions)
	}
}

// TestThresholdsDismissIsOwnerOnlyAnd409sWhenNotPending covers the
// remaining API contract: dismiss updates status, a second resolve on the
// same id 409s, another rider's (or an unknown) id 404s, and a bad action
// 400s.
func TestThresholdsDismissIsOwnerOnlyAnd409sWhenNotPending(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	ctx := context.Background()

	sug, err := h.srv.Training.CreateSuggestion(ctx, workout.ThresholdSuggestion{
		Rider: "wilant", Field: "ftp", Value: 268, Previous: 255,
	})
	if err != nil {
		t.Fatalf("create suggestion: %v", err)
	}

	// Another rider may not resolve it — indistinguishable from unknown.
	resp := h.as("other", "cyclists", http.MethodPost, "/api/training/thresholds/"+sug.ID, `{"action":"dismiss"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other rider dismiss status = %d, want 404", resp.StatusCode)
	}

	// An unknown id is also 404.
	resp = h.as("wilant", "cyclists", http.MethodPost, "/api/training/thresholds/does-not-exist", `{"action":"dismiss"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown id status = %d, want 404", resp.StatusCode)
	}

	// A bad action is 400.
	resp = h.as("wilant", "cyclists", http.MethodPost, "/api/training/thresholds/"+sug.ID, `{"action":"snooze"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad action status = %d, want 400", resp.StatusCode)
	}

	resp = h.as("wilant", "cyclists", http.MethodPost, "/api/training/thresholds/"+sug.ID, `{"action":"dismiss"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dismiss status = %d, want 200", resp.StatusCode)
	}

	stored, err := h.srv.Training.GetSuggestion(ctx, sug.ID)
	if err != nil || stored.Status != workout.ThresholdDismissed {
		t.Fatalf("stored = %+v, err = %v, want status dismissed", stored, err)
	}

	// Resolving it again — accept or dismiss — is a 409, it is no longer pending.
	resp = h.as("wilant", "cyclists", http.MethodPost, "/api/training/thresholds/"+sug.ID, `{"action":"accept"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second resolve status = %d, want 409", resp.StatusCode)
	}
}
