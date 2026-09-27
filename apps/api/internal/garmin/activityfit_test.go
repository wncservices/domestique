package garmin

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/fitcourse"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
)

// syntheticFIT builds a tiny, real, valid FIT course file — synthetic
// coordinates, never real ride data (AGENTS.md: GPX/FIT fixtures must stay
// synthetic).
func syntheticFIT(t *testing.T) []byte {
	t.Helper()
	points := []gpx.Point{
		{Lat: 51.05, Lon: 3.72},
		{Lat: 51.06, Lon: 3.73},
		{Lat: 51.07, Lon: 3.74},
	}
	data, err := fitcourse.Encode(points, fitcourse.Options{Name: "Test"})
	if err != nil {
		t.Fatalf("building a synthetic FIT fixture: %v", err)
	}
	return data
}

func zipOf(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	return zipOfEntries(t, map[string][]byte{name: data})
}

// zipOfEntries builds a zip with one entry per map key. Go's map iteration
// order is randomized, which is exactly what TestActivityFITRejectsAZipWithMultipleFitEntries
// wants: the "ambiguous" error names both entries regardless of which one
// zip.Writer happened to visit first.
func zipOfEntries(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func activityFITFake(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-service/oauth/exchange/user/2.0", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"bearer-1","expires_in":3600}`)
	})
	mux.HandleFunc(activityFITPath, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	c := New()
	c.APIBase = server.URL
	c.SetConsumer(testKey, testSecret)
	c.Resume(Session{OAuth1Token: "tok-1", OAuth1Secret: "sec-1"})
	return c
}

func TestActivityFITUnzipsTheSingleEntry(t *testing.T) {
	fit := syntheticFIT(t)
	zipped := zipOf(t, "123_ACTIVITY.fit", fit)

	var gotAuth, gotAccept, gotXRW string
	c := activityFITFake(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotXRW = r.Header.Get("X-Requested-With")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(zipped)
	})

	got, err := c.ActivityFIT(t.Context(), "123")
	if err != nil {
		t.Fatalf("ActivityFIT: %v", err)
	}
	if !bytes.Equal(got, fit) {
		t.Fatalf("got %d bytes, want the fixture's %d bytes to match exactly", len(got), len(fit))
	}
	if gotAuth != "Bearer bearer-1" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want the same header Activities sends", gotAccept)
	}
	if gotXRW != "XMLHttpRequest" {
		t.Errorf("X-Requested-With = %q, want the same header Activities sends", gotXRW)
	}
}

func TestActivityFITAcceptsAPlainNonZipBody(t *testing.T) {
	fit := syntheticFIT(t)
	c := activityFITFake(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fit)
	})

	got, err := c.ActivityFIT(t.Context(), "123")
	if err != nil {
		t.Fatalf("ActivityFIT: %v", err)
	}
	if !bytes.Equal(got, fit) {
		t.Fatalf("got %d bytes, want the fixture's %d bytes to match exactly", len(got), len(fit))
	}
}

func TestActivityFITRejectsA404(t *testing.T) {
	c := activityFITFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	})
	if _, err := c.ActivityFIT(t.Context(), "123"); err == nil {
		t.Fatal("expected an error for a 404")
	}
}

func TestActivityFITRejectsAnHTMLBody(t *testing.T) {
	c := activityFITFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>Sign in again</body></html>"))
	})
	if _, err := c.ActivityFIT(t.Context(), "123"); err == nil {
		t.Fatal("expected an error for an HTML body")
	}
}

// TestActivityFITRejectsAnOversizedZipEntryWithoutReadingItAll builds a zip
// whose single entry, once decompressed, is larger than MaxFITBytes — a
// classic zip-bomb shape: the entry compresses many repeats of the same byte
// down to almost nothing, so the response itself stays small while the
// decompressed stream would not. If ActivityFIT read the whole thing before
// checking the size, this test would allocate ~40MiB and take a while; it
// should instead fail fast because entry.Open() is capped with
// io.LimitReader while still being decompressed.
func TestActivityFITRejectsAnOversizedZipEntryWithoutReadingItAll(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "big.fit", Method: zip.Deflate})
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	// One repeated byte compresses extremely well; MaxFITBytes+1MiB of it
	// decompressed is still a tiny compressed payload.
	chunk := bytes.Repeat([]byte{'A'}, 1<<20)
	total := 0
	for total <= MaxFITBytes {
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("zip write: %v", err)
		}
		total += len(chunk)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	c := activityFITFake(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buf.Bytes())
	})
	if _, err := c.ActivityFIT(t.Context(), "123"); err == nil {
		t.Fatal("expected an error for an oversized zip entry")
	}
}

func TestActivityFITRefusesAnEmptyID(t *testing.T) {
	c := activityFITFake(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should have been sent for an empty activity id")
	})
	if _, err := c.ActivityFIT(t.Context(), ""); err == nil {
		t.Fatal("expected an error for an empty activity id")
	}
}

func TestExtractFITRejectsAZipWithNoFitEntry(t *testing.T) {
	zipped := zipOf(t, "readme.txt", []byte("not a fit file"))
	if _, err := extractFIT(zipped); err == nil {
		t.Fatal("expected an error for a zip with no .fit entry")
	}
}

// A zip with more than one .fit entry is ambiguous — Connect's own download
// has only ever been observed to hold one, so a second is treated as an
// error rather than silently picking whichever entry came first, which
// could return the wrong ride's data.
func TestActivityFITRejectsAZipWithMultipleFitEntries(t *testing.T) {
	fitA := syntheticFIT(t)
	fitB := syntheticFIT(t)
	zipped := zipOfEntries(t, map[string][]byte{
		"111_ACTIVITY.fit": fitA,
		"222_ACTIVITY.fit": fitB,
	})

	if _, err := extractFIT(zipped); err == nil {
		t.Fatal("expected an error for a zip with more than one .fit entry")
	} else if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error = %q, want it to call out the ambiguity", err.Error())
	}
}

func TestValidateFITRejectsShortOrUnsignedData(t *testing.T) {
	if _, err := validateFIT([]byte("short")); err == nil {
		t.Fatal("expected an error for data shorter than the header")
	}
	notFIT := strings.Repeat("x", 20)
	if _, err := validateFIT([]byte(notFIT)); err == nil {
		t.Fatal("expected an error for data missing the .FIT signature")
	}
}
