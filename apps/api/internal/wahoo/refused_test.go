package wahoo

import (
	"errors"
	"net/http"
	"testing"
)

// A revoked or expired token must be recognisable as such, not just a string.
func TestA401Or403FromRouteCallsIsASessionRefusal(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })

		if err := c.DeleteRoute(t.Context(), "at", "5"); !errors.Is(err, ErrSessionRefused) {
			t.Errorf("%d delete: %v, want ErrSessionRefused", status, err)
		}
		if _, err := c.ListRoutes(t.Context(), "at"); !errors.Is(err, ErrSessionRefused) {
			t.Errorf("%d list: %v, want ErrSessionRefused", status, err)
		}
		if _, err := c.CreateRoute(t.Context(), "at", aRouteRequest()); !errors.Is(err, ErrSessionRefused) {
			t.Errorf("%d create: %v, want ErrSessionRefused", status, err)
		}
	}
	// An ordinary failure is not one.
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	if err := c.DeleteRoute(t.Context(), "at", "5"); errors.Is(err, ErrSessionRefused) || err == nil {
		t.Errorf("a 500: %v", err)
	}
}
