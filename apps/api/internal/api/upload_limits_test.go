package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
)

func gpxBody(points string) []byte {
	return []byte(`<?xml version="1.0"?><gpx version="1.1"><trk><trkseg>` + points + `</trkseg></trk></gpx>`)
}

// Audit N6: a 20 MiB file of <trkpt/> was parsed into 700k points and about
// 540 MiB of allocation, per request, with nothing to stop four at once.
func TestUploadOfTooManyPointsIsRefusedCheaply(t *testing.T) {
	h := newHarness(t)
	pt := `<trkpt lat="51.0" lon="4.0"><ele>1</ele></trkpt>`
	body := gpxBody(strings.Repeat(pt, (19<<20)/len(pt)))

	resp := h.upload(map[string]string{"name": "big"}, body, "big.gpx")

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var out map[string]string
	h.decode(resp, &out)
	if !strings.Contains(out["error"], "points") {
		t.Errorf("error = %q, want it to say the route has too many points", out["error"])
	}
}

func TestUploadOfDeeplyNestedXMLIsRefused(t *testing.T) {
	h := newHarness(t)
	body := []byte(`<gpx>` + strings.Repeat("<a>", 4000) + strings.Repeat("</a>", 4000) + `</gpx>`)
	start := time.Now()
	resp := h.upload(map[string]string{"name": "deep"}, body, "deep.gpx")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

// Audit N8: NaN made the upload fail with an opaque 500; 91 degrees and 1e300
// were stored and served back.
func TestUploadRefusesCoordinatesThatAreNotOnEarth(t *testing.T) {
	h := newHarness(t)
	for name, bad := range map[string]string{
		"NaN":            `<trkpt lat="NaN" lon="4"/>`,
		"latitude 91":    `<trkpt lat="91" lon="4"/>`,
		"1e300":          `<trkpt lat="1e300" lon="4"/>`,
		"longitude -200": `<trkpt lat="50" lon="-200"/>`,
		"ele 1e9":        `<trkpt lat="50" lon="4"><ele>1e9</ele></trkpt>`,
	} {
		t.Run(name, func(t *testing.T) {
			body := gpxBody(`<trkpt lat="51.0" lon="4.0"/>` + bad + `<trkpt lat="51.1" lon="4.1"/>`)
			resp := h.upload(map[string]string{"name": "x"}, body, "x.gpx")
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
	var lib libraryDTO
	h.decode(h.get("/api/routes"), &lib)
	if len(lib.Routes) != 0 {
		t.Errorf("%d routes stored from refused uploads", len(lib.Routes))
	}
}

func TestUploadsAreRateLimitedPerRider(t *testing.T) {
	h := newHarness(t)
	h.srv.UploadLimiter = ratelimit.New(2, time.Hour)

	up := func(i int) int {
		pts := fmt.Sprintf(`<trkpt lat="51.%d" lon="4.0"/><trkpt lat="51.%d" lon="4.1"/>`, i, i)
		return h.upload(map[string]string{"name": fmt.Sprintf("r%d", i)}, gpxBody(pts), "r.gpx").StatusCode
	}
	for i := 1; i <= 2; i++ {
		if got := up(i); got != http.StatusCreated {
			t.Fatalf("upload %d = %d, want 201", i, got)
		}
	}
	if got := up(3); got != http.StatusTooManyRequests {
		t.Errorf("third upload = %d, want 429", got)
	}
}

// Four uploads at once used to be four full parses at once. Whatever happens
// to the requests, every one is answered and none takes the process down.
func TestParallelOversizedUploadsAreAllAnswered(t *testing.T) {
	h := newHarness(t)
	pt := `<trkpt lat="51.0" lon="4.0"/>`
	body := gpxBody(strings.Repeat(pt, 300_000))
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = h.upload(map[string]string{"name": "big"}, body, "big.gpx").StatusCode
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusBadRequest {
			t.Errorf("upload %d = %d, want 400", i, c)
		}
	}
}
