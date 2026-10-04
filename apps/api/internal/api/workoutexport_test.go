package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// exportRide is a plain power workout: a warmup, two over-unders and a
// cooldown, at FTP 250 so the numbers are easy to read.
func exportRide(name, date string) workout.CreateWorkoutRequest {
	t := func(n, in string, secs, lo, hi float64) workout.WorkoutStep {
		return workout.WorkoutStep{Name: n, Intensity: workout.Intensity(in), Duration: workout.DurationTime, Seconds: secs,
			Target: workout.TargetPower, TargetLow: lo, TargetHigh: hi}
	}
	return workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: name, Date: date, Zone: workout.ZoneThreshold,
		Steps: []workout.WorkoutStep{
			t("Warm up", "warmup", 600, 125, 175),
			{Name: "Over-unders", Repeat: 2, Steps: []workout.WorkoutStep{
				t("Over", "active", 300, 262, 262), t("Under", "active", 300, 212, 212)}},
			t("Cool down", "cooldown", 300, 125, 175),
		},
	}
}

func (h *trainingHarness) seedExport(req workout.CreateWorkoutRequest) string {
	h.t.Helper()
	wk, err := h.store.CreateWorkout(context.Background(), req)
	if err != nil {
		h.t.Fatal(err)
	}
	return wk.ID
}

func (h *trainingHarness) setFTP(rider string, ftp float64) {
	h.t.Helper()
	if _, err := h.store.SaveProfile(context.Background(), workout.RiderProfile{Rider: rider, FTPWatts: ftp}); err != nil {
		h.t.Fatal(err)
	}
}

func errorBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	var out struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Error
}

func TestExportWorkoutFormats(t *testing.T) {
	h := newTrainingHarness(t)
	h.setFTP("wilant", 250)
	id := h.seedExport(exportRide("Over unders", "2026-03-04"))

	cases := []struct {
		format, mime, ext, want string
	}{
		{"zwo", "application/xml", ".zwo", `<IntervalsT Repeat="2" OnDuration="300" OffDuration="300" OnPower="1.048" OffPower="0.848"/>`},
		{"mrc", "text/plain", ".mrc", "MINUTES PERCENT"},
		{"erg", "text/plain", ".erg", "MINUTES WATTS"},
	}
	for _, c := range cases {
		t.Run(c.format, func(t *testing.T) {
			resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+id+"/export?format="+c.format, "")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d: %s", resp.StatusCode, readAll(t, resp))
			}
			if ct := resp.Header.Get("Content-Type"); ct != c.mime {
				t.Errorf("content-type = %q, want %q", ct, c.mime)
			}
			if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="over-unders`+c.ext+`"` {
				t.Errorf("content-disposition = %q", cd)
			}
			if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
				t.Error("missing nosniff")
			}
			if body := string(readAll(t, resp)); !strings.Contains(body, c.want) {
				t.Errorf("body missing %q:\n%s", c.want, body)
			}
		})
	}
}

func TestExportDescriptionCarriesTheIndoorNote(t *testing.T) {
	h := newTrainingHarness(t)
	h.setFTP("wilant", 250)
	req := exportRide("Long ride", "2026-03-04")
	req.Zone = workout.ZoneEndurance
	req.Steps = []workout.WorkoutStep{{Name: "Ride", Intensity: workout.IntensityActive, Duration: workout.DurationTime,
		Seconds: 13200, Target: workout.TargetPower, TargetLow: 140, TargetHigh: 160}}
	id := h.seedExport(req)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+id+"/export?format=zwo", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body := string(readAll(t, resp))
	if !strings.Contains(body, "Indoor version of Long ride (3h40), 2h00 on the trainer.") {
		t.Errorf("the converter's note must reach the description:\n%s", body)
	}
	if !strings.Contains(body, `<SteadyState Duration="7200"`) {
		t.Errorf("the export must be the shortened indoor form:\n%s", body)
	}
}

func TestExportWorkoutGuards(t *testing.T) {
	h := newTrainingHarness(t)
	h.setFTP("wilant", 250)
	id := h.seedExport(exportRide("Over unders", "2026-03-04"))
	url := "/api/training/workouts/" + id + "/export"

	for _, q := range []string{"", "?format=", "?format=docx", "?format=ZWO"} {
		if resp := h.as("wilant", "cyclists", http.MethodGet, url+q, ""); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400", q, resp.StatusCode)
		}
	}
	if resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/nope/export?format=zwo", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id: status = %d, want 404", resp.StatusCode)
	}
	if resp := h.as("other", "cyclists", http.MethodGet, url+"?format=zwo", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("other rider: status = %d, want 403", resp.StatusCode)
	}
	if resp := h.as("guest", "guests", http.MethodGet, url+"?format=zwo", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer: status = %d, want 403", resp.StatusCode)
	}
}

func TestExportRefusals(t *testing.T) {
	h := newTrainingHarness(t)
	h.setFTP("wilant", 250)

	run := exportRide("Easy run", "2026-03-04")
	run.Sport = model.SportRunning
	runID := h.seedExport(run)

	hr := exportRide("Heart rate ride", "2026-03-04")
	hr.Zone = ""
	hr.Steps = []workout.WorkoutStep{{Name: "Z2", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 1800,
		Target: workout.TargetHeartRate, TargetLow: 120, TargetHigh: 140}}
	hrID := h.seedExport(hr)

	test := exportRide("FTP Test (twenty minute)", "2026-03-04")
	test.Zone = ""
	test.TestProtocol = "twenty_minute"
	test.Steps = []workout.WorkoutStep{
		{Name: "Warmup", Intensity: workout.IntensityWarmup, Duration: workout.DurationTime, Seconds: 600, Target: workout.TargetOpen},
		{Name: "Effort", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 1200, Target: workout.TargetOpen},
	}
	testID := h.seedExport(test)

	get := func(id, format string) (int, string) {
		resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+id+"/export?format="+format, "")
		if resp.StatusCode == http.StatusOK {
			return resp.StatusCode, string(readAll(t, resp))
		}
		return resp.StatusCode, errorBody(t, resp)
	}

	if code, msg := get(runID, "zwo"); code != http.StatusUnprocessableEntity || msg != "Only cycling workouts export to trainer files." {
		t.Errorf("running: %d %q", code, msg)
	}
	for _, f := range []string{"zwo", "mrc", "erg"} {
		if code, msg := get(hrID, f); code != http.StatusUnprocessableEntity ||
			msg != "This workout is paced by heart rate and can't be turned into power. Set your FTP first." {
			t.Errorf("hr %s: %d %q", f, code, msg)
		}
	}
	// .zwo carries the open step as a FreeRide; the other two refuse, naming .zwo.
	if code, body := get(testID, "zwo"); code != http.StatusOK || !strings.Contains(body, `<FreeRide Duration="1200"`) {
		t.Errorf("zwo test: %d %s", code, body)
	}
	for _, f := range []string{"mrc", "erg"} {
		code, msg := get(testID, f)
		if code != http.StatusUnprocessableEntity || !strings.Contains(msg, "can't hold") || !strings.Contains(msg, ".zwo") {
			t.Errorf("%s test: %d %q", f, code, msg)
		}
	}
}

func TestExportNeedsFTPForZwoAndMrcOnly(t *testing.T) {
	h := newTrainingHarness(t)
	// No profile at all: no FTP. The workout's own steps are power already.
	id := h.seedExport(exportRide("Over unders", "2026-03-04"))
	for _, f := range []string{"zwo", "mrc"} {
		resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+id+"/export?format="+f, "")
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s: status = %d, want 409", f, resp.StatusCode)
		}
		if msg := errorBody(t, resp); msg != "Set your FTP to export this as .zwo or .mrc." {
			t.Errorf("%s message = %q", f, msg)
		}
	}
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+id+"/export?format=erg", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("erg status = %d, want 200 without an FTP", resp.StatusCode)
	}
	if body := string(readAll(t, resp)); strings.Contains(body, "FTP =") {
		t.Errorf("erg without FTP must omit the FTP line:\n%s", body)
	}
}

func TestExportLogsRiderIDAndOutcomeButNoWatts(t *testing.T) {
	h := newTrainingHarness(t)
	h.setFTP("wilant", 250)
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, nil))
	id := h.seedExport(exportRide("Over unders", "2026-03-04"))

	h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+id+"/export?format=mrc", "")
	out := logs.String()
	for _, want := range []string{"workout exported", "rider=wilant", "id=" + id, "format=mrc"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"250", "262", "FTP"} {
		if strings.Contains(out, bad) {
			t.Errorf("log leaks %q:\n%s", bad, out)
		}
	}
}

func zipEntries(t *testing.T, body []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestExportWeekZip(t *testing.T) {
	h := newTrainingHarness(t)
	h.setFTP("wilant", 250)
	h.seedExport(exportRide("Over unders", "2026-03-02")) // Monday
	h.seedExport(exportRide("Over unders", "2026-03-02")) // same day and name: must not collide
	h.seedExport(exportRide("Sweet spot", "2026-03-06"))
	hr := exportRide("Heart rate ride", "2026-03-05")
	hr.Zone = ""
	hr.Steps = []workout.WorkoutStep{{Name: "Z2", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 1800,
		Target: workout.TargetHeartRate, TargetLow: 120, TargetHigh: 140}}
	h.seedExport(hr)
	h.seedExport(exportRide("Next week", "2026-03-09"))
	other := exportRide("Someone elses", "2026-03-03")
	other.Rider = "other"
	h.seedExport(other)

	// Any day of the week snaps to its Monday, like the week read does.
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/weeks/2026-03-04/export?format=mrc", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, readAll(t, resp))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("content-type = %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="week-2026-03-02-mrc.zip"` {
		t.Errorf("content-disposition = %q", cd)
	}
	got := zipEntries(t, readAll(t, resp))

	for _, name := range []string{"2026-03-02-over-unders.mrc", "2026-03-02-over-unders-2.mrc", "2026-03-06-sweet-spot.mrc"} {
		if !strings.Contains(got[name], "[COURSE DATA]") {
			t.Errorf("entry %s missing or not a course; entries: %v", name, keys(got))
		}
	}
	skipped, ok := got["SKIPPED.txt"]
	if !ok || !strings.Contains(skipped, "2026-03-05-heart-rate-ride") || !strings.Contains(skipped, "heart rate") {
		t.Errorf("SKIPPED.txt = %q", skipped)
	}
	if len(got) != 4 {
		t.Errorf("entries = %v, want 3 courses and SKIPPED.txt (no other rider's, no next week's)", keys(got))
	}
}

func TestExportWeekZipHasNoSkippedFileWhenNothingSkipped(t *testing.T) {
	h := newTrainingHarness(t)
	h.setFTP("wilant", 250)
	h.seedExport(exportRide("Over unders", "2026-03-02"))
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/weeks/2026-03-02/export?format=zwo", "")
	got := zipEntries(t, readAll(t, resp))
	if _, ok := got["SKIPPED.txt"]; ok || len(got) != 1 {
		t.Errorf("entries = %v, want just the one workout", keys(got))
	}
}

func TestExportWeekWithNothingExportableSaysSoInAHeader(t *testing.T) {
	h := newTrainingHarness(t)
	h.setFTP("wilant", 250)
	hr := exportRide("Heart rate ride", "2026-03-05")
	hr.Zone = ""
	hr.Steps = []workout.WorkoutStep{{Name: "Z2", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 1800,
		Target: workout.TargetHeartRate, TargetLow: 120, TargetHigh: 140}}
	h.seedExport(hr)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/weeks/2026-03-02/export?format=mrc", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Domestique-Skipped"); got != "all" {
		t.Errorf("X-Domestique-Skipped = %q, want all so the page can say so instead of saving an empty zip", got)
	}
	// A week where something exported carries no such header.
	h.seedExport(exportRide("Over unders", "2026-03-02"))
	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/weeks/2026-03-02/export?format=mrc", "")
	if got := resp.Header.Get("X-Domestique-Skipped"); got != "" {
		t.Errorf("X-Domestique-Skipped = %q on a week that exported", got)
	}
}

func TestExportWeekZipErrors(t *testing.T) {
	h := newTrainingHarness(t)
	h.setFTP("wilant", 250)
	h.seedExport(exportRide("Over unders", "2026-03-02"))

	if resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/weeks/2026-03-02/export?format=docx", ""); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown format: %d, want 400", resp.StatusCode)
	}
	if resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/weeks/yesterday/export?format=zwo", ""); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad date: %d, want 400", resp.StatusCode)
	}
	if resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/weeks/2026-04-06/export?format=zwo", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("empty week: %d, want 404", resp.StatusCode)
	}
	// Someone else's week is empty for the caller, not forbidden: nothing leaks.
	if resp := h.as("other", "cyclists", http.MethodGet, "/api/training/weeks/2026-03-02/export?format=zwo", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("other rider: %d, want 404", resp.StatusCode)
	}
	if resp := h.as("guest", "guests", http.MethodGet, "/api/training/weeks/2026-03-02/export?format=zwo", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer: %d, want 403", resp.StatusCode)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
