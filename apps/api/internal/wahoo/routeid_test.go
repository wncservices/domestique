package wahoo

import (
	"encoding/json"
	"net/http"
	"testing"
)

// A route id comes from the request, and goes into the path of a call made with
// the rider's own Wahoo token. "../" in it must stay inside the one path
// segment, not walk to another endpoint of the same API.
func TestRouteIDsAreEscapedIntoOnePathSegment(t *testing.T) {
	ids := []string{"5/../../user", "5?x=1", "5#frag", "a b", "../workouts", "..", "."}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			var gotRaw string
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotRaw = r.URL.EscapedPath()
				if r.URL.RawQuery != "" || r.URL.Fragment != "" {
					t.Errorf("id leaked into the query or fragment: %q", r.URL.RequestURI())
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": 5})
			})

			if _, err := c.UpdateRoute(t.Context(), "at", id, aRouteRequest()); err != nil {
				t.Fatalf("update: %v", err)
			}
			if !oneSegmentUnderRoutes(gotRaw) {
				t.Errorf("PUT reached %q, want a single segment under /v1/routes/", gotRaw)
			}

			gotRaw = ""
			if err := c.DeleteRoute(t.Context(), "at", id); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if !oneSegmentUnderRoutes(gotRaw) {
				t.Errorf("DELETE reached %q, want a single segment under /v1/routes/", gotRaw)
			}
		})
	}
}

func oneSegmentUnderRoutes(escapedPath string) bool {
	const prefix = "/v1/routes/"
	if len(escapedPath) <= len(prefix) || escapedPath[:len(prefix)] != prefix {
		return false
	}
	for _, c := range escapedPath[len(prefix):] {
		if c == '/' {
			return false
		}
	}
	return true
}
