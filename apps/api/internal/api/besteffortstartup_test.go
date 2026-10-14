package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
)

// Startup must not wait for the record book: the rebuild runs in the
// background, listening begins at once, and the loops that write to the book
// (and read what it holds) start only when it is done.
func TestStartupDoesNotBlockOnTheRebuildAndTheLoopsWaitForIt(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	release := make(chan struct{})
	h.srv.BeforeBestEffortsRebuild = func() { <-release }

	started := make(chan string, 3)
	loop := func(name string) func(context.Context) {
		return func(context.Context) { started <- name }
	}

	returned := make(chan struct{})
	go func() {
		h.srv.StartAfterBestEffortsRebuild(context.Background(), loop("import"), loop("schedule"), loop("metrics"))
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("StartAfterBestEffortsRebuild blocked on a slow rebuild: listening would be delayed")
	}
	select {
	case name := <-started:
		t.Fatalf("the %s loop started before the rebuild finished", name)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	for range 3 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("the loops did not start after the rebuild finished")
		}
	}
}

func TestAFailedRebuildStillStartsTheLoopsAndSaysSo(t *testing.T) {
	act, fit := rideAt(t, "7020", time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC), 317)
	h, logs := newEffortHarness(t, []garmin.Activity{act}, map[string][]byte{"7020": fit})
	if _, err := h.db.Conn().Exec(`DROP TABLE best_efforts`); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	h.srv.StartAfterBestEffortsRebuild(context.Background(), func(context.Context) { started <- struct{}{} })
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("a failed rebuild kept the loops from starting")
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "best efforts") {
		t.Errorf("a failed rebuild is not logged at Error:\n%s", logs.String())
	}
}

func TestTheRebuildLogsItsStartProgressAndEndWithCountsOnly(t *testing.T) {
	act, fit := rideAt(t, "7021", time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC), 317)
	h, logs := newEffortHarness(t, []garmin.Activity{act}, map[string][]byte{"7021": fit})
	h.syncEfforts()
	if _, err := h.db.Conn().Exec(`DELETE FROM best_efforts`); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	if err := h.srv.RebuildBestEfforts(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	for _, want := range []string{"best efforts rebuild starting", "best efforts rebuild progress", "best efforts rebuilt"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q line:\n%s", want, out)
		}
	}
	if strings.Contains(out, "317") || strings.Contains(out, "wilant") {
		t.Errorf("the rebuild log names a rider or a value:\n%s", out)
	}
}
