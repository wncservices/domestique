package routing

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ORS error bodies name the point they could not snap ("Could not find
// routable point ... coordinate 0: 4.5 51.2"): a rider's start or waypoint.
// The error is logged, so only the status and class may be in it (audit
// review: the engine's words are never quoted).
func TestRouteErrorDoesNotQuoteTheEnginesBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":2010,"message":"Could not find routable point within a radius of 350.0 meters of specified coordinate 0: 4.5123456 51.2345678."}}`, http.StatusNotFound)
	}))
	defer server.Close()

	_, err := New(server.URL, "").Route(t.Context(), []LatLng{{Lat: 51.2345678, Lon: 4.5123456}, {Lat: 51.3, Lon: 4.6}}, "cycling-road")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, leak := range []string{"4.5123456", "51.2345678", "routable point", "coordinate"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q quotes %q", err, leak)
		}
	}
	if !strings.Contains(err.Error(), "routing service returned 404") {
		t.Errorf("error %q lost its status, which FailureClass reads", err)
	}
}
