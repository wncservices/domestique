package api_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/rideimport/rideimporttest"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// stalledUpload opens a raw connection and sends the headers and the start of
// a multipart body that never finishes: a client holding a slot, or a
// slow-loris. It returns once the server has started spooling it.
func (h *importHarness) stalledUpload(user string) net.Conn {
	h.t.Helper()
	host := strings.TrimPrefix(h.base, "http://")
	conn, err := net.Dial("tcp", host)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { conn.Close() })
	const boundary = "xxBOUNDARYxx"
	head := fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.fit\"\r\nContent-Type: application/octet-stream\r\n\r\n", boundary)
	body := head + "partial-bytes"
	req := fmt.Sprintf("POST /api/training/import HTTP/1.1\r\nHost: %s\r\nContent-Type: multipart/form-data; boundary=%s\r\nContent-Length: %d\r\nRemote-User: %s\r\nRemote-Groups: cyclists\r\n\r\n%s",
		host, boundary, len(body)+100000, user, body)
	if _, err := conn.Write([]byte(req)); err != nil {
		h.t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.leftovers()) == 0 {
		if time.Now().After(deadline) {
			h.t.Fatal("the server never started spooling the stalled upload")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return conn
}

func TestImportSecondUploadFromTheSameRiderIsRefusedBeforeAnythingIsSpooled(t *testing.T) {
	h := newImportHarness(t)
	conn := h.stalledUpload("wilant")
	held := len(h.leftovers())

	resp := h.upload("wilant", "cyclists", map[string][]byte{"a.fit": ride(1, 600, 200).Build(t)}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("parallel upload: %d, want 409", resp.StatusCode)
	}
	if got := len(h.leftovers()); got != held {
		t.Errorf("the refused upload spooled %d more files, want none", got-held)
	}

	// The slot frees when the first upload ends.
	conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp = h.upload("wilant", "cyclists", map[string][]byte{"a.fit": ride(1, 600, 200).Build(t)}, nil)
		if resp.StatusCode == http.StatusAccepted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the slot never freed: last status %d", resp.StatusCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.srv.WaitForBackground()
}

func TestImportGlobalConcurrencyIsCapped(t *testing.T) {
	h := newImportHarness(t)
	h.srv.ImportMaxConcurrent = 1
	h.stalledUpload("wilant")
	held := len(h.leftovers())

	resp := h.upload("other", "cyclists", map[string][]byte{"a.fit": ride(1, 600, 200).Build(t)}, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("a second rider over the global cap: %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
	if got := len(h.leftovers()); got != held {
		t.Errorf("the refused upload spooled %d more files", got-held)
	}
}

func TestImportABodyThatStallsHitsTheReadDeadlineAndFreesEverything(t *testing.T) {
	h := newImportHarness(t)
	h.srv.ImportReadTimeout = 300 * time.Millisecond
	conn := h.stalledUpload("wilant")

	// The server gives up on its own: its answer arrives, and the spool and the
	// slot go with it.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.Contains(line, "400") {
		t.Fatalf("after the deadline the server answered %q, %v; want 400", line, err)
	}
	if left := h.leftovers(); len(left) != 0 {
		t.Errorf("the stalled upload's spool survived: %v", left)
	}
	h.srv.ImportReadTimeout = 0
	if resp := h.upload("wilant", "cyclists", map[string][]byte{"a.fit": ride(1, 600, 200).Build(t)}, nil); resp.StatusCode != http.StatusAccepted {
		t.Errorf("after the timeout the rider is refused: %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()
}

func TestImportOneBadFileDoesNotAbortTheJob(t *testing.T) {
	h := newImportHarness(t)
	calls := 0
	h.srv.BeforeImportFile = func() {
		calls++
		if calls == 2 {
			panic("a file that breaks the analysis")
		}
	}
	zipBytes := rideimporttest.Zip(t,
		rideimporttest.Entry{Name: "a.fit", Data: ride(1, 1200, 200).Build(t)},
		rideimporttest.Entry{Name: "b.fit", Data: ride(3, 1800, 210).Build(t)},
		rideimporttest.Entry{Name: "c.fit", Data: ride(5, 2400, 190).Build(t)})
	st := h.runImport("wilant", map[string][]byte{"a.zip": zipBytes})
	if st.State != "done" || st.Added != 2 || st.Unreadable != 1 {
		t.Fatalf("status = %+v, want done with 2 added and the panicking file counted unreadable", st)
	}
	if snaps, _ := h.store.ListFitnessSnapshots(context.Background(), "wilant"); len(snaps) == 0 {
		t.Error("recompute did not run after a file panicked")
	}
	logged := h.logs.String()
	if !strings.Contains(logged, "level=WARN") || !strings.Contains(logged, "a file could not be filed") || !strings.Contains(logged, "job="+st.ID) {
		t.Errorf("no Warn naming the job:\n%s", logged)
	}
	if strings.Contains(logged, "breaks the analysis") {
		t.Errorf("the panic value reached the log:\n%s", logged)
	}
}

func TestImportDetectionDoesNotRevertAProfileEditedMidJob(t *testing.T) {
	h := newImportHarness(t)
	h.setFTP("wilant", 0)
	h.srv.BeforeImportRecompute = func() {
		// The rider types an FTP while the job runs.
		if _, err := h.store.SaveProfile(context.Background(), workout.RiderProfile{Rider: "wilant", FTPWatts: 300}); err != nil {
			t.Error(err)
		}
	}
	r := rideimporttest.Spec{Start: time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC), Seconds: 1500, Watts: 260, HR: 160}
	st := h.runImport("wilant", map[string][]byte{"a.fit": r.Build(t)})
	if st.State != "done" {
		t.Fatalf("status = %+v", st)
	}
	p, _, _ := h.store.GetProfile(context.Background(), "wilant")
	if p.FTPWatts != 300 || p.FTPEstimated {
		t.Errorf("FTP = %v (estimated %v): detection overwrote what the rider typed during the import", p.FTPWatts, p.FTPEstimated)
	}
}

func TestImportStopsWritingOnceTheRiderIsPurged(t *testing.T) {
	h := newImportHarness(t)
	calls := 0
	h.srv.BeforeImportFile = func() {
		calls++
		if calls == 2 {
			if _, err := h.store.DeleteRider(context.Background(), "wilant"); err != nil {
				t.Error(err)
			}
		}
	}
	zipBytes := rideimporttest.Zip(t,
		rideimporttest.Entry{Name: "a.fit", Data: ride(1, 1200, 200).Build(t)},
		rideimporttest.Entry{Name: "b.fit", Data: ride(3, 1800, 210).Build(t)},
		rideimporttest.Entry{Name: "c.fit", Data: ride(5, 2400, 190).Build(t)})
	resp := h.upload("wilant", "cyclists", map[string][]byte{"a.zip": zipBytes}, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()
	ctx := context.Background()
	if sessions, _ := h.store.ListSessions(ctx, "wilant"); len(sessions) != 0 {
		t.Errorf("%d sessions exist for a purged rider: the job kept writing", len(sessions))
	}
	if analyses, _ := h.store.ListAnalyses(ctx, "wilant", "2000-01-01"); len(analyses) != 0 {
		t.Errorf("%d analyses exist for a purged rider", len(analyses))
	}
	if left := h.leftovers(); len(left) != 0 {
		t.Errorf("spool left behind: %v", left)
	}
}

func TestSweepImportSpoolsRemovesOnlyOldOnes(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	mk := func(name string, age time.Duration) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	mk("domestique-upload-old", 11*time.Minute)
	mk("domestique-import-old", time.Hour)
	mk("domestique-upload-fresh", 9*time.Minute)
	mk("domestique-import-fresh", time.Minute)
	mk("someone-elses-file", 48*time.Hour)

	if n := api.SweepImportSpools(dir, 10*time.Minute, now, slog.New(slog.NewTextHandler(io.Discard, nil))); n != 2 {
		t.Errorf("removed %d, want the 2 old spools", n)
	}
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := "domestique-import-fresh domestique-upload-fresh someone-elses-file"
	if got := strings.Join(left, " "); got != want {
		t.Errorf("left %q, want %q", got, want)
	}
}

func TestImportHeartbeatRunsWellInsideTheStaleWindow(t *testing.T) {
	if api.ImportHeartbeat*3 > workout.ImportStaleAfter {
		t.Errorf("heartbeat %v against stale window %v: one missed beat would read as dead", api.ImportHeartbeat, workout.ImportStaleAfter)
	}
}
