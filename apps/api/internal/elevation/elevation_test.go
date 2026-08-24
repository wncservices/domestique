package elevation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestLookupReturnsElevationsInOrder(t *testing.T) {
	c := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body lookupRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		results := make([]struct {
			Elevation float64 `json:"elevation"`
		}, len(body.Locations))
		for i, loc := range body.Locations {
			// A deterministic, checkable function of the input, not a fixed
			// value — proves order survives the round trip, not just count.
			results[i].Elevation = loc.Latitude + loc.Longitude
		}
		_ = json.NewEncoder(w).Encode(lookupResponse{Results: results})
	})

	points := []Point{{Lat: 1, Lon: 2}, {Lat: 3, Lon: 4}, {Lat: 5, Lon: 6}}
	got, err := c.Lookup(t.Context(), points)
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{3, 7, 11}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("point %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestLookupBatchesLargeRequests(t *testing.T) {
	var requestSizes []int
	c := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body lookupRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requestSizes = append(requestSizes, len(body.Locations))
		results := make([]struct {
			Elevation float64 `json:"elevation"`
		}, len(body.Locations))
		_ = json.NewEncoder(w).Encode(lookupResponse{Results: results})
	})

	points := make([]Point, 250)
	if _, err := c.Lookup(t.Context(), points); err != nil {
		t.Fatal(err)
	}
	want := []int{100, 100, 50}
	if len(requestSizes) != len(want) {
		t.Fatalf("requests = %v, want %v", requestSizes, want)
	}
	for i := range want {
		if requestSizes[i] != want[i] {
			t.Errorf("request %d: size = %d, want %d", i, requestSizes[i], want[i])
		}
	}
}

func TestLookupFailsOnANonOKStatus(t *testing.T) {
	c := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	if _, err := c.Lookup(t.Context(), []Point{{Lat: 1, Lon: 2}}); err == nil {
		t.Fatal("expected an error for a 503 response")
	}
}

func TestLookupFailsOnAMismatchedResultCount(t *testing.T) {
	c := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(lookupResponse{Results: nil})
	})

	if _, err := c.Lookup(t.Context(), []Point{{Lat: 1, Lon: 2}, {Lat: 3, Lon: 4}}); err == nil {
		t.Fatal("expected an error when the service returns fewer results than requested")
	}
}

func TestNewDefaultsAnEmptyURL(t *testing.T) {
	c := New("")
	if c.url != DefaultURL {
		t.Errorf("url = %q, want DefaultURL", c.url)
	}
}
