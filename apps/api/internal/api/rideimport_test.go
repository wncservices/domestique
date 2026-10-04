package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/muktihari/fit/profile/typedef"

	"github.com/wncservices/domestique/apps/api/internal/rideimport"
	"github.com/wncservices/domestique/apps/api/internal/rideimport/rideimporttest"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// importClock is the fixed "now" of every import test, with an explicit zone.
var importClock = time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)

type importHarness struct {
	*trainingHarness
	now  time.Time
	logs *bytes.Buffer
	tmp  string
}

func newImportHarness(t *testing.T) *importHarness {
	t.Helper()
	// The spool goes to the temp dir; pointing it at a dir of our own is how a
	// test proves nothing is left behind.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	h := &importHarness{trainingHarness: newTrainingHarness(t), now: importClock, tmp: tmp, logs: &bytes.Buffer{}}
	h.srv.Clock = func() time.Time { return h.now }
	h.srv.Log = slog.New(slog.NewTextHandler(h.logs, nil))
	h.setFTP("wilant", 250)
	return h
}

func (h *importHarness) leftovers() []string {
	entries, _ := os.ReadDir(h.tmp)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// upload posts the given file parts (and extra form fields) as multipart.
func (h *importHarness) upload(user, groups string, files map[string][]byte, fields map[string]string) *http.Response {
	h.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	// A stable order, so a test's parts are the same on every run.
	for _, name := range sortedKeys(files) {
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			h.t.Fatal(err)
		}
		_, _ = fw.Write(files[name])
	}
	_ = mw.Close()
	req, err := http.NewRequest(http.MethodPost, h.base+"/api/training/import", &body)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Remote-User", user)
	req.Header.Set("Remote-Groups", groups)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

type importStatus struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	Phase         string `json:"phase"`
	Added         int    `json:"added"`
	Duplicate     int    `json:"duplicate"`
	SkippedSport  int    `json:"skippedSport"`
	Unsupported   int    `json:"unsupported"`
	Unreadable    int    `json:"unreadable"`
	Error         string `json:"error"`
	EarliestDate  string `json:"earliestDate"`
	CooldownUntil string `json:"cooldownUntil"`
}

func (h *importHarness) status(user string) (int, importStatus) {
	h.t.Helper()
	resp := h.as(user, "cyclists", http.MethodGet, "/api/training/import/status", "")
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, importStatus{}
	}
	var st importStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		h.t.Fatal(err)
	}
	return resp.StatusCode, st
}

// runImport uploads and waits for the background job to finish.
func (h *importHarness) runImport(user string, files map[string][]byte) importStatus {
	h.t.Helper()
	resp := h.upload(user, "cyclists", files, nil)
	if resp.StatusCode != http.StatusAccepted {
		h.t.Fatalf("upload status = %d: %s", resp.StatusCode, readAll(h.t, resp))
	}
	h.srv.WaitForBackground()
	_, st := h.status(user)
	return st
}

// ride makes a synthetic ride on a fixed day, with a distinct duration so
// rides on one day are not mistaken for one another.
func ride(day int, seconds int, watts int) rideimporttest.Spec {
	return rideimporttest.Spec{Start: time.Date(2026, 3, day, 9, 0, 0, 0, time.UTC), Seconds: seconds, Watts: watts, HR: 140}
}

func stravaZip(t *testing.T, specs ...rideimporttest.Spec) []byte {
	t.Helper()
	var entries []rideimporttest.Entry
	for i, s := range specs {
		entries = append(entries, rideimporttest.Entry{Name: fmt.Sprintf("activities/%d.fit.gz", 1000+i), Data: rideimporttest.Gzip(t, s.Build(t))})
	}
	entries = append(entries,
		rideimporttest.Entry{Name: "activities/9001.gpx", Data: []byte("<gpx/>")},
		rideimporttest.Entry{Name: "activities.csv", Data: []byte("id\n")})
	return rideimporttest.Zip(t, entries...)
}

func TestImportUploadRunsInTheBackgroundAndReportsCounts(t *testing.T) {
	h := newImportHarness(t)
	zipBytes := stravaZip(t, ride(1, 1800, 200), ride(2, 2400, 210), ride(3, 3000, 190))

	resp := h.upload("wilant", "cyclists", map[string][]byte{"export.zip": zipBytes}, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d: %s", resp.StatusCode, readAll(t, resp))
	}
	var accepted struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil || accepted.ID == "" {
		t.Fatalf("no job id in the 202: %v", err)
	}
	h.srv.WaitForBackground()

	code, st := h.status("wilant")
	if code != http.StatusOK || st.ID != accepted.ID || st.State != "done" {
		t.Fatalf("status = %d %+v", code, st)
	}
	if st.Added != 3 || st.Duplicate != 0 || st.Unsupported != 1 || st.SkippedSport != 0 || st.Unreadable != 0 {
		t.Errorf("counts = %+v, want 3 added and the gpx counted unsupported", st)
	}
	if st.EarliestDate != "2026-03-01" {
		t.Errorf("earliest = %q, want the oldest ride's day", st.EarliestDate)
	}

	sessions, _ := h.store.ListSessions(context.Background(), "wilant")
	if len(sessions) != 3 {
		t.Fatalf("%d sessions, want 3", len(sessions))
	}
	for _, s := range sessions {
		if s.Provider != "import" {
			t.Errorf("session %+v is not marked as an import", s)
		}
	}
	if analyses, _ := h.store.ListAnalyses(context.Background(), "wilant", "2000-01-01"); len(analyses) != 3 {
		t.Errorf("%d analyses, want one per ride, however old", len(analyses))
	}
	if snaps, _ := h.store.ListFitnessSnapshots(context.Background(), "wilant"); len(snaps) == 0 {
		t.Error("the fitness history was not recomputed")
	}
	if left := h.leftovers(); len(left) != 0 {
		t.Errorf("the spool survived the job: %v", left)
	}
}

func TestImportReuploadAddsNothing(t *testing.T) {
	h := newImportHarness(t)
	zipBytes := stravaZip(t, ride(1, 1800, 200), ride(2, 2400, 210))
	if st := h.runImport("wilant", map[string][]byte{"a.zip": zipBytes}); st.Added != 2 {
		t.Fatalf("first import = %+v", st)
	}
	h.now = h.now.Add(11 * time.Minute) // past the cooldown
	st := h.runImport("wilant", map[string][]byte{"a.zip": zipBytes})
	if st.State != "done" || st.Added != 0 || st.Duplicate != 2 {
		t.Errorf("re-upload = %+v, want added 0 and 2 already here", st)
	}
	if sessions, _ := h.store.ListSessions(context.Background(), "wilant"); len(sessions) != 2 {
		t.Errorf("%d sessions after a re-upload, want 2", len(sessions))
	}
}

func TestImportSkipsRidesAlreadySyncedFromAProvider(t *testing.T) {
	h := newImportHarness(t)
	if _, err := h.store.UpsertSession(context.Background(), workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "123", Sport: "cycling", Date: "2026-03-01",
		DurationSeconds: 1805, AvgPowerWatts: 201, TrainingLoad: 40,
	}); err != nil {
		t.Fatal(err)
	}
	st := h.runImport("wilant", map[string][]byte{"a.zip": stravaZip(t, ride(1, 1800, 200), ride(2, 2400, 210))})
	if st.Added != 1 || st.Duplicate != 1 {
		t.Errorf("counts = %+v, want the Garmin ride recognised and one new ride", st)
	}
}

func TestImportCountsOtherSportsAndBrokenFiles(t *testing.T) {
	h := newImportHarness(t)
	swim := rideimporttest.Spec{Start: time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC), Seconds: 600, Watts: 100, Sport: typedef.SportSwimming}
	zipBytes := rideimporttest.Zip(t,
		rideimporttest.Entry{Name: "a.fit", Data: ride(1, 1800, 200).Build(t)},
		rideimporttest.Entry{Name: "b.fit", Data: swim.Build(t)},
		rideimporttest.Entry{Name: "c.fit", Data: append(ride(2, 600, 200).Build(t)[:20], make([]byte, 40)...)}, // a FIT header, then rubbish
		rideimporttest.Entry{Name: "d.tcx", Data: []byte("<tcx/>")},
	)
	st := h.runImport("wilant", map[string][]byte{"a.zip": zipBytes})
	if st.State != "done" || st.Added != 1 || st.SkippedSport != 1 || st.Unreadable != 1 || st.Unsupported != 1 {
		t.Errorf("counts = %+v, want one each of added, other sport, unreadable and unsupported", st)
	}
}

func TestImportLooseFilesAndSeveralParts(t *testing.T) {
	h := newImportHarness(t)
	st := h.runImport("wilant", map[string][]byte{
		"one.fit":    ride(1, 1800, 200).Build(t),
		"two.fit.gz": rideimporttest.Gzip(t, ride(2, 2400, 210).Build(t)),
		"three.zip":  stravaZip(t, ride(3, 3000, 190)),
	})
	if st.State != "done" || st.Added != 3 || st.Unsupported != 1 {
		t.Errorf("counts = %+v, want 3 rides from three kinds of upload", st)
	}
}

func TestImportIgnoresTheRiderInTheForm(t *testing.T) {
	h := newImportHarness(t)
	resp := h.upload("wilant", "cyclists", map[string][]byte{"a.fit": ride(1, 1800, 200).Build(t)},
		map[string]string{"rider": "other", "uploadedBy": "other"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()
	if sessions, _ := h.store.ListSessions(context.Background(), "wilant"); len(sessions) != 1 {
		t.Errorf("the session did not land on the rider in the session: %+v", sessions)
	}
	if sessions, _ := h.store.ListSessions(context.Background(), "other"); len(sessions) != 0 {
		t.Errorf("a rider in the form received a session: %+v", sessions)
	}
	if code, _ := h.status("other"); code != http.StatusNoContent {
		t.Errorf("the other rider's status = %d, want 204: it is not theirs", code)
	}
}

func TestImportRefusals(t *testing.T) {
	h := newImportHarness(t)
	fit := map[string][]byte{"a.fit": ride(1, 600, 200).Build(t)}

	if resp := h.upload("wilant", "cyclists", nil, map[string]string{"note": "no file here"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no file part: %d, want 400", resp.StatusCode)
	}
	if resp := h.upload("guest", "guests", fit, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer: %d, want 403", resp.StatusCode)
	}

	// Someone mid-import gets 409, without the upload being read.
	if err := h.store.StartRideImport(context.Background(), "wilant", "busy", h.now); err != nil {
		t.Fatal(err)
	}
	if resp := h.upload("wilant", "cyclists", fit, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("while running: %d, want 409", resp.StatusCode)
	}
	if resp := h.upload("other", "cyclists", fit, nil); resp.StatusCode != http.StatusAccepted {
		t.Errorf("another rider while this one runs: %d, want 202", resp.StatusCode)
	}
	h.srv.WaitForBackground()
	if left := h.leftovers(); len(left) != 0 {
		t.Errorf("a refused upload left a spool behind: %v", left)
	}
}

func TestImportCooldownIsTenMinutesAfterAFinishedJob(t *testing.T) {
	h := newImportHarness(t)
	fit := map[string][]byte{"a.fit": ride(1, 600, 200).Build(t)}
	if st := h.runImport("wilant", fit); st.State != "done" {
		t.Fatalf("first = %+v", st)
	}

	h.now = h.now.Add(9*time.Minute + 59*time.Second)
	resp := h.upload("wilant", "cyclists", fit, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("inside the cooldown: %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") != "1" {
		t.Errorf("Retry-After = %q, want the one second left", resp.Header.Get("Retry-After"))
	}
	if _, st := h.status("wilant"); st.CooldownUntil == "" {
		t.Error("the status says nothing about the cooldown, so the page cannot tell the rider")
	}

	h.now = h.now.Add(time.Second)
	resp = h.upload("wilant", "cyclists", fit, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("at ten minutes: %d, want 202", resp.StatusCode)
	}
	h.srv.WaitForBackground()
	if _, st := h.status("wilant"); st.CooldownUntil != "" && st.State != "done" {
		t.Errorf("status = %+v", st)
	}
}

func TestImportAnInterruptedJobDoesNotImposeACooldown(t *testing.T) {
	h := newImportHarness(t)
	if err := h.store.StartRideImport(context.Background(), "wilant", "killed", h.now); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(3 * time.Minute) // a restart killed it; nothing has touched it since
	_, st := h.status("wilant")
	if st.State != "interrupted" || st.CooldownUntil != "" {
		t.Errorf("status = %+v, want interrupted with no cooldown", st)
	}
	if resp := h.upload("wilant", "cyclists", map[string][]byte{"a.fit": ride(1, 600, 200).Build(t)}, nil); resp.StatusCode != http.StatusAccepted {
		t.Errorf("re-upload after an interruption: %d, want 202", resp.StatusCode)
	}
	h.srv.WaitForBackground()
}

func TestImportBodyOverTheCapIs413AndLeavesNothing(t *testing.T) {
	h := newImportHarness(t)
	h.srv.ImportMaxUploadBytes = 4 << 10
	big := map[string][]byte{"a.fit": bytes.Repeat([]byte("x"), 64<<10)}
	resp := h.upload("wilant", "cyclists", big, nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	h.srv.WaitForBackground()
	if left := h.leftovers(); len(left) != 0 {
		t.Errorf("the spool of a refused upload survived: %v", left)
	}
	if code, _ := h.status("wilant"); code != http.StatusNoContent {
		t.Errorf("a refused upload created a job: status %d", code)
	}
}

func TestImportCapBreachFailsTheJobAndKeepsTheRidesAlreadyAdded(t *testing.T) {
	h := newImportHarness(t)
	var entries []rideimporttest.Entry
	for i := 1; i <= 6; i++ {
		// Rides on neighbouring days differ by far more than the duplicate
		// tolerance, or they would be taken for one another.
		entries = append(entries, rideimporttest.Entry{Name: fmt.Sprintf("%d.fit", i), Data: ride(i, 600*i, 200).Build(t)})
	}
	h.srv.ImportLimits = rideimport.DefaultLimits
	h.srv.ImportLimits.MaxTotalBytes = int64(len(entries[0].Data)+len(entries[1].Data)+len(entries[2].Data)) + 10 // room for exactly three
	st := h.runImport("wilant", map[string][]byte{"a.zip": rideimporttest.Zip(t, entries...)})
	if st.State != "failed" || st.Error != "too large" {
		t.Fatalf("status = %+v, want failed: too large", st)
	}
	if st.Added != 3 {
		t.Errorf("added = %d, want the 3 that fit under the cap: rides before the breach stay, none after it", st.Added)
	}
	if sessions, _ := h.store.ListSessions(context.Background(), "wilant"); len(sessions) != st.Added {
		t.Errorf("%d sessions but the job says %d added", len(sessions), st.Added)
	}
	if left := h.leftovers(); len(left) != 0 {
		t.Errorf("spool left behind: %v", left)
	}
	// The kept rides still reach the fitness history.
	if snaps, _ := h.store.ListFitnessSnapshots(context.Background(), "wilant"); len(snaps) == 0 {
		t.Error("a failed job must still recompute what it did add")
	}

	// And the rider can try again once the cooldown is over; idempotency finishes it.
	h.now = h.now.Add(11 * time.Minute)
	h.srv.ImportLimits = rideimport.DefaultLimits
	st2 := h.runImport("wilant", map[string][]byte{"a.zip": rideimporttest.Zip(t, entries...)})
	if st2.State != "done" || st2.Added != 3 || st2.Duplicate != 3 {
		t.Errorf("retry = %+v after %d, want the remaining rides added", st2, st.Added)
	}
}

func TestImportAZipBombFailsTheJobWithoutAddingAnything(t *testing.T) {
	h := newImportHarness(t)
	h.srv.ImportLimits = rideimport.DefaultLimits
	h.srv.ImportLimits.MaxRatio = 20
	zeros := bytes.Repeat([]byte{0}, 4<<20)
	zeros[8], zeros[9], zeros[10], zeros[11] = '.', 'F', 'I', 'T'
	st := h.runImport("wilant", map[string][]byte{"bomb.zip": rideimporttest.Zip(t, rideimporttest.Entry{Name: "a.fit", Data: zeros})})
	if st.State != "failed" || st.Error != "unsafe archive" || st.Added != 0 {
		t.Errorf("status = %+v, want failed: unsafe archive", st)
	}
}

func TestImportAPanicInTheJobFailsItAndCleansUp(t *testing.T) {
	h := newImportHarness(t)
	h.srv.BeforeImportJob = func() { panic("boom") }
	st := h.runImport("wilant", map[string][]byte{"a.fit": ride(1, 600, 200).Build(t)})
	if st.State != "failed" || st.Error != "internal" {
		t.Errorf("status = %+v, want failed: internal", st)
	}
	if left := h.leftovers(); len(left) != 0 {
		t.Errorf("a panicking job left its spool: %v", left)
	}
	// The server is still up and the rider can go again later.
	h.srv.BeforeImportJob = nil
	h.now = h.now.Add(11 * time.Minute)
	if st := h.runImport("wilant", map[string][]byte{"a.fit": ride(1, 600, 200).Build(t)}); st.State != "done" || st.Added != 1 {
		t.Errorf("after the panic: %+v", st)
	}
}

func TestImportNeverMatchesAPlanOrMovesALevel(t *testing.T) {
	h := newImportHarness(t)
	ctx := context.Background()
	wk := exportRide("Threshold", "2026-03-01")
	wk.Rider = "wilant"
	wk.Level = 4
	planned := h.seedExport(wk)

	st := h.runImport("wilant", map[string][]byte{"a.fit": ride(1, 1800, 250).Build(t)})
	if st.Added != 1 {
		t.Fatalf("status = %+v", st)
	}
	analyses, _ := h.store.ListAnalyses(ctx, "wilant", "2000-01-01")
	if len(analyses) != 1 || analyses[0].WorkoutID != "" || analyses[0].Outcome != "unplanned" {
		t.Errorf("analysis = %+v, want no plan match", analyses)
	}
	if levels, _ := h.store.ListLevels(ctx, "wilant"); len(levels) != 0 {
		t.Errorf("levels = %+v, an import must not move one", levels)
	}
	if got, _ := h.store.GetWorkout(ctx, planned); got.Level != 4 {
		t.Errorf("the planned workout changed: %+v", got)
	}
}

func TestImportFeedsThresholdDetection(t *testing.T) {
	h := newImportHarness(t)
	h.setFTP("wilant", 0) // an empty FTP may be filled in by detection
	// Inside the 90-day window of the fixed clock: 25 minutes at 260 W.
	r := rideimporttest.Spec{Start: time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC), Seconds: 1500, Watts: 260, HR: 160}
	st := h.runImport("wilant", map[string][]byte{"a.fit": r.Build(t)})
	if st.State != "done" || st.Added != 1 {
		t.Fatalf("status = %+v", st)
	}
	p, _, err := h.store.GetProfile(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if p.FTPWatts < 240 || p.FTPWatts > 260 || !p.FTPEstimated {
		t.Errorf("FTP = %v (estimated %v), want detection to have filled the empty field from the imported ride", p.FTPWatts, p.FTPEstimated)
	}
}

func TestImportStatus(t *testing.T) {
	h := newImportHarness(t)
	if code, _ := h.status("wilant"); code != http.StatusNoContent {
		t.Errorf("no job: %d, want 204", code)
	}
	if err := h.store.StartRideImport(context.Background(), "wilant", "j1", h.now); err != nil {
		t.Fatal(err)
	}
	if code, st := h.status("wilant"); code != http.StatusOK || st.State != "running" || st.Phase != "reading" {
		t.Errorf("fresh = %d %+v", code, st)
	}
	h.now = h.now.Add(2*time.Minute + time.Second)
	if _, st := h.status("wilant"); st.State != "interrupted" {
		t.Errorf("a running job two minutes stale = %+v, want interrupted", st)
	}
	if resp := h.as("guest", "guests", http.MethodGet, "/api/training/import/status", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer: %d, want 403", resp.StatusCode)
	}
}

// tablesText reads every cell of every table and returns it as text.
func tablesText(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	var all strings.Builder
	for _, table := range tables {
		r, err := db.Query(`SELECT * FROM "` + table + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = r.Scan(ptrs...)
			for _, v := range vals {
				switch x := v.(type) {
				case []byte:
					all.Write(x)
				default:
					fmt.Fprint(&all, x)
				}
				all.WriteByte(' ')
			}
		}
		r.Close()
	}
	return all.String()
}

func TestImportNeverStoresOrLogsWhatIsInsideTheUpload(t *testing.T) {
	h := newImportHarness(t)
	const marker = "SECRET-INSIDE-THE-FILE-7f3a"
	const name = "SECRET-FILE-NAME-home-street-9b1c"
	r := ride(1, 1800, 237)
	r.Marker = marker
	raw := r.Build(t)
	if !bytes.Contains(raw, []byte(marker)) {
		t.Fatal("the fixture does not carry its marker, so this test would prove nothing")
	}
	zipBytes := rideimporttest.Zip(t,
		rideimporttest.Entry{Name: "activities/" + name + ".fit", Data: raw},
		rideimporttest.Entry{Name: "activities/" + name + ".gpx", Data: []byte("<gpx/>")})
	st := h.runImport("wilant", map[string][]byte{name + ".zip": zipBytes})
	if st.State != "done" || st.Added != 1 {
		t.Fatalf("status = %+v", st)
	}

	stored := tablesText(t, h.conn)
	for _, secret := range []string{marker, "SECRET-FILE-NAME", "home-street"} {
		if strings.Contains(stored, secret) {
			t.Errorf("%q reached the database", secret)
		}
	}
	logged := h.logs.String()
	for _, secret := range []string{marker, "SECRET-FILE-NAME", "home-street", "watts", "power", "heart"} {
		if strings.Contains(logged, secret) {
			t.Errorf("%q reached a log line:\n%s", secret, logged)
		}
	}
	for _, want := range []string{"ride import started", "ride import finished", "rider=wilant", "added=1"} {
		if !strings.Contains(logged, want) {
			t.Errorf("log lacks %q:\n%s", want, logged)
		}
	}
	// And the upload itself is gone from disk.
	if left := h.leftovers(); len(left) != 0 {
		t.Errorf("files left on disk: %v", left)
	}
	_ = io.Discard
}
