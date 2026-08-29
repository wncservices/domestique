package geocoding

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func nominatimResponse(entries ...[3]string) string {
	var b strings.Builder
	b.WriteString("[")
	for i, e := range entries {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"display_name":%q,"lat":%q,"lon":%q}`, e[0], e[1], e[2])
	}
	b.WriteString("]")
	return b.String()
}

func TestSearchReturnsParsedResults(t *testing.T) {
	var gotQuery, gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(nominatimResponse([3]string{"Brussels, Belgium", "50.8503", "4.3517"})))
	}))
	defer server.Close()

	c := New(server.URL)
	results, err := c.Search(context.Background(), "Brussels")
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "Brussels" {
		t.Errorf("query = %q, want %q", gotQuery, "Brussels")
	}
	if gotUserAgent == "" {
		t.Error("expected a User-Agent header identifying this app, per Nominatim's usage policy")
	}
	if len(results) != 1 || results[0].Name != "Brussels, Belgium" || results[0].Lat != 50.8503 || results[0].Lon != 4.3517 {
		t.Errorf("results = %+v, want one parsed Brussels result", results)
	}
}

func TestEmptyQueryIsRejectedWithoutCallingOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("geocoding service was called despite an empty query")
	}))
	defer server.Close()

	c := New(server.URL)
	for _, q := range []string{"", "   "} {
		if _, err := c.Search(context.Background(), q); err == nil {
			t.Errorf("query %q: expected an error, got none", q)
		}
	}
}

func TestNonOKStatusIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	c := New(server.URL)
	if _, err := c.Search(context.Background(), "anywhere"); err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

// A repeated identical search — a rider re-running the same query, or two
// riders searching the same place — must not spend Nominatim's shared
// request budget twice for an answer already in hand.
func TestIdenticalSearchesAreServedFromCache(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(nominatimResponse([3]string{"Ghent, Belgium", "51.0543", "3.7174"})))
	}))
	defer server.Close()

	c := New(server.URL)
	if _, err := c.Search(context.Background(), "Ghent"); err != nil {
		t.Fatal(err)
	}
	// Case-insensitive: the same place searched differently is still the
	// same question.
	if _, err := c.Search(context.Background(), "  ghent  "); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("geocoding service was called %d times for two identical searches, want 1", calls)
	}

	if _, err := c.Search(context.Background(), "Antwerp"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("geocoding service was called %d times after a genuinely different search, want 2", calls)
	}
}

func TestMalformedLatLonIsSkippedNotFatal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"display_name":"bad","lat":"not-a-number","lon":"4.35"},{"display_name":"good","lat":"50.85","lon":"4.35"}]`))
	}))
	defer server.Close()

	c := New(server.URL)
	results, err := c.Search(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "good" {
		t.Errorf("results = %+v, want only the well-formed entry", results)
	}
}
