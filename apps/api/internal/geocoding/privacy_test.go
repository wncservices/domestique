package geocoding

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The search text is in the request URL, and net/http's error text repeats the
// URL. The handler logs the error, so a Nominatim timeout used to write a
// rider's typed address to the log (audit N3).
func TestSearchErrorsDoNotEchoTheQuery(t *testing.T) {
	const typed = "12 Rue Privee, Home Town"
	leaks := func(err error) bool {
		return err != nil && (strings.Contains(err.Error(), "Privee") || strings.Contains(err.Error(), "Rue+"))
	}

	t.Run("connection refused", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		dead.Close()
		_, err := New(dead.URL).Search(t.Context(), typed)
		if err == nil {
			t.Fatal("want an error")
		}
		if leaks(err) {
			t.Errorf("error %q carries the query", err)
		}
	})

	t.Run("upstream error body quoting the query", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "bad query: "+r.URL.Query().Get("q"), http.StatusBadRequest)
		}))
		defer srv.Close()
		_, err := New(srv.URL).Search(t.Context(), typed)
		if err == nil {
			t.Fatal("want an error")
		}
		if leaks(err) {
			t.Errorf("error %q carries the query", err)
		}
	})
}
