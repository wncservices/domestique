package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/wahoo"
)

func TestPushSendsARouteToWahoo(t *testing.T) {
	h := newWahooHarness(t, true)
	h.connect("wilant", "cyclists")

	if _, err := h.db.Create(t.Context(), source.CreateRequest{
		Filename:   "ride.gpx",
		Name:       "Kluisbergen",
		Targets:    &[]string{"wahoo:wilant"},
		UploadedBy: "wilant",
		GPX:        []byte(aTestGPX),
	}); err != nil {
		t.Fatal(err)
	}

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/push")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var out struct {
		Applied  int      `json:"applied"`
		Failures []string `json:"failures"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Failures) != 0 {
		t.Fatalf("push failed: %v", out.Failures)
	}
	if len(h.upstream.createdRoutes) != 1 {
		t.Fatalf("created %d routes on wahoo, want 1", len(h.upstream.createdRoutes))
	}

	created := h.upstream.createdRoutes[0]
	if created.Get("route[external_id]") != "kluisbergen" {
		t.Errorf("external_id = %q, want the route's slug", created.Get("route[external_id]"))
	}
	if created.Get("route[name]") != "Kluisbergen" {
		t.Errorf("name = %q", created.Get("route[name]"))
	}
	if created.Get("route[file]") == "" {
		t.Error("no course file reached wahoo")
	}

	// Pushed with the session that was actually stored, not something ad hoc.
	if h.upstream.routeAuth[0] != "Bearer at-1" {
		t.Errorf("Authorization = %q, want the stored access token", h.upstream.routeAuth[0])
	}
}

// A rider who has not connected must fail their own push and nobody else's,
// with a sentence that says what to do — the same contract Garmin's push
// keeps.
func TestPushWithoutAWahooConnectionFailsThatAccountOnly(t *testing.T) {
	h := newWahooHarness(t, true)

	// The account has to exist to be a push target at all — linking it
	// directly, the way an admin-added or CLI-imported account would be,
	// without ever going through /wahoo/connect.
	if _, err := h.accounts.Link(t.Context(), "wahoo", "wilant", "wilant's Wahoo"); err != nil {
		t.Fatal(err)
	}

	if _, err := h.db.Create(t.Context(), source.CreateRequest{
		Filename:   "ride.gpx",
		Name:       "Kluisbergen",
		Targets:    &[]string{"wahoo:wilant"},
		UploadedBy: "wilant",
		GPX:        []byte(aTestGPX),
	}); err != nil {
		t.Fatal(err)
	}

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/push")
	var out struct {
		Failures []string `json:"failures"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Failures) != 1 {
		t.Fatalf("failures = %v, want exactly the unconnected account", out.Failures)
	}
	if !strings.Contains(out.Failures[0], "has not connected Wahoo") {
		t.Errorf("failure = %q, want it to say the account is not connected", out.Failures[0])
	}
	if len(h.upstream.createdRoutes) != 0 {
		t.Error("a route reached wahoo despite no connection")
	}
}

// An expired access token is refreshed transparently, unlike Garmin's
// expire-and-reconnect story — Wahoo issued a refresh token for exactly
// this. The refreshed session is what the push uses, and it is the one left
// stored afterwards, without clobbering the connection's email/display name.
func TestPushRefreshesAnExpiredWahooSession(t *testing.T) {
	h := newWahooHarness(t, true)

	sealed, err := json.Marshal(wahoo.Session{
		AccessToken: "stale-token", RefreshToken: "rt-1",
		ExpiresAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.links.Save("wahoo", "wilant", providerlink.Connection{
		Email: "rider@example.test", DisplayName: "Rider One", Secret: string(sealed),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.accounts.Link(t.Context(), "wahoo", "wilant", "wilant's Wahoo"); err != nil {
		t.Fatal(err)
	}

	if _, err := h.db.Create(t.Context(), source.CreateRequest{
		Filename:   "ride.gpx",
		Name:       "Kluisbergen",
		Targets:    &[]string{"wahoo:wilant"},
		UploadedBy: "wilant",
		GPX:        []byte(aTestGPX),
	}); err != nil {
		t.Fatal(err)
	}

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/push")
	var out struct {
		Failures []string `json:"failures"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Failures) != 0 {
		t.Fatalf("push failed: %v", out.Failures)
	}
	if len(h.upstream.createdRoutes) != 1 {
		t.Fatalf("created %d routes on wahoo, want 1", len(h.upstream.createdRoutes))
	}
	if h.upstream.routeAuth[0] != "Bearer at-1" {
		t.Errorf("pushed with %q, want the refreshed token", h.upstream.routeAuth[0])
	}

	link, err := h.links.Get("wahoo", "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if link.Email != "rider@example.test" || link.DisplayName != "Rider One" {
		t.Errorf("refresh clobbered the connection's identity: %+v", link)
	}

	_, secret, err := h.links.Secret("wahoo", "wilant")
	if err != nil {
		t.Fatal(err)
	}
	var stored wahoo.Session
	if err := json.Unmarshal([]byte(secret), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken == "stale-token" {
		t.Error("the stale token is still what's stored — refresh did not persist")
	}
	if stored.Expired() {
		t.Error("the refreshed session is stored as already expired")
	}
}
