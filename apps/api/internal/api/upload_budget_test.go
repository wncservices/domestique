package api_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
)

// The production budget, not a test one: importing a folder of thirty routes
// by hand is a normal afternoon and must not meet a 429, while a loop still does.
func TestTheProductionUploadBudgetAllowsAFolderOfThirtyRoutes(t *testing.T) {
	h := newHarness(t)
	h.srv.UploadLimiter = api.NewUploadLimiter()

	up := func(i int) int {
		pts := fmt.Sprintf(`<trkpt lat="51.%03d" lon="4.0"/><trkpt lat="51.%03d" lon="4.1"/>`, i, i)
		return h.upload(map[string]string{"name": fmt.Sprintf("r%d", i)}, gpxBody(pts), "r.gpx").StatusCode
	}
	for i := 1; i <= 30; i++ {
		if got := up(i); got != http.StatusCreated {
			t.Fatalf("upload %d of 30 = %d, want 201", i, got)
		}
	}
	refused := 0
	for i := 31; i <= 80; i++ {
		if up(i) == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused == 0 {
		t.Error("a loop of 80 uploads was never refused")
	}
}
