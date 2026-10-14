package api_test

import (
	"net/http"
	"testing"
	"time"
)

// The date of an FTP change is the rider's local day (the training timezone),
// not UTC's: at 23:30 UTC on 31 December it is already 1 January in Brussels,
// and a change filed under the wrong year lands in the wrong season review.
func TestAnFTPChangeIsDatedOnTheRidersLocalDay(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.setClock(time.Date(2026, time.December, 31, 23, 30, 0, 0, time.UTC))
	if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", `{"ftpWatts":250}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("save: status %d", resp.StatusCode)
	}
	got := h.ftpChanges()
	if len(got) != 1 || got[0].Date != "2027-01-01" {
		t.Errorf("changes = %+v, want the change on the rider's 1 January", got)
	}
}
