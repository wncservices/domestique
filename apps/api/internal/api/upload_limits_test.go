package api_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
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
// to the requests, none takes the process down and every one that is answered
// is refused with 400.
//
// The server stops reading as soon as the point cap is passed and answers while
// the client may still be writing the rest. A client can then see a write error
// (a reset, a broken pipe, ENOBUFS on macOS under load) instead of the answer:
// that is the transport losing the race, not a missing answer. So a request
// that ends in a write-side error is allowed, a request that is answered must
// say 400, and the server must still answer afterwards. The body is only just
// over the cap so the unread remainder, and the room for that race, is small.
func TestParallelOversizedUploadsAreAllAnswered(t *testing.T) {
	h := newHarness(t)
	pt := `<trkpt lat="51.0" lon="4.0"/>`
	body := gpxBody(strings.Repeat(pt, gpx.MaxPoints+500))

	send := func() (int, error) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		_ = w.WriteField("name", "big")
		part, err := w.CreateFormFile("file", "big.gpx")
		if err != nil {
			t.Error(err)
			return 0, err
		}
		_, _ = part.Write(body)
		_ = w.Close()
		req, err := http.NewRequest(http.MethodPost, h.base+"/api/routes", &buf)
		if err != nil {
			t.Error(err)
			return 0, err
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
		resp, err := h.client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		return resp.StatusCode, nil
	}

	var wg sync.WaitGroup
	codes := make([]int, 6)
	errs := make([]error, len(codes))
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], errs[i] = send()
		}()
	}
	wg.Wait()
	for i := range codes {
		switch {
		case errs[i] != nil:
			var op *net.OpError
			if !errors.As(errs[i], &op) && !errors.Is(errs[i], io.ErrUnexpectedEOF) && !errors.Is(errs[i], io.EOF) {
				t.Errorf("upload %d failed with something other than a lost write race: %v", i, errs[i])
			}
		case codes[i] != http.StatusBadRequest:
			t.Errorf("upload %d = %d, want 400", i, codes[i])
		}
	}

	// Still serving: the refusals left no parse slot held. Retried because
	// this request can lose the same race on a loaded machine.
	var last error
	for range 5 {
		code, err := send()
		if err == nil {
			if code != http.StatusBadRequest {
				t.Fatalf("follow-up upload = %d, want 400", code)
			}
			return
		}
		last = err
	}
	t.Fatalf("the server stopped answering uploads: %v", last)
}
