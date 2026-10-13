package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const riderGroups = "cyclists,domestique-access"

// getShared fetches a shared route's GPX as a signed-in caller.
func (h *shareHarness) getShared(user, token string) (int, string) {
	h.t.Helper()
	resp := h.as(user, riderGroups, http.MethodGet, "/api/shares/"+token+"/gpx", "")
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// A deleted route's slug is free to be taken again, so a share that outlived
// the route would hand a stranger's route to whoever still holds the link.
// This is the converted proof of concept from the audit (finding M1).
func TestShareDoesNotFollowReusedSlug(t *testing.T) {
	h := newShareHarness(t)
	a := h.seedRoute(t, "Morning Loop", "alice")
	share := h.mustCreateShare(t, "alice", a.Slug)

	if resp := h.as("alice", riderGroups, http.MethodDelete, "/api/routes/"+a.Slug, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204", resp.StatusCode)
	}
	b := h.seedRoute(t, "Morning Loop", "bob")
	if b.Slug != a.Slug {
		t.Fatalf("slug %q was not reused (%q): the scenario this test guards no longer arises", a.Slug, b.Slug)
	}

	status, body := h.getShared("carol", share.Token)
	if status != http.StatusNotFound {
		t.Fatalf("alice's old link: status = %d, want 404; body mentions a GPX: %v",
			status, strings.Contains(body, "<gpx"))
	}
}

// Revoking on delete is the first line of defence, but a route can leave the
// library by other paths. The share must therefore also refuse on its own when
// the route behind the slug is not the one it was made for.
func TestShareRefusesADifferentRouteUnderTheSameSlug(t *testing.T) {
	h := newShareHarness(t)
	a := h.seedRoute(t, "Morning Loop", "alice")
	share := h.mustCreateShare(t, "alice", a.Slug)

	// Straight through the library, bypassing the HTTP delete handler.
	if err := h.src.Delete(t.Context(), a.Slug); err != nil {
		t.Fatal(err)
	}
	h.seedRoute(t, "Morning Loop", "bob")

	if status, _ := h.getShared("carol", share.Token); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for another rider's route behind the old slug", status)
	}
}

// Same owner, but a new route: a different generation of the slug.
func TestShareRefusesARecreatedRouteOfTheSameOwner(t *testing.T) {
	h := newShareHarness(t)
	a := h.seedRoute(t, "Morning Loop", "alice")
	share := h.mustCreateShare(t, "alice", a.Slug)

	if status, _ := h.getShared("carol", share.Token); status != http.StatusOK {
		t.Fatalf("before: status = %d, want 200", status)
	}
	// Simulate "a later route took this slug": created_at is the generation marker.
	if _, err := h.src.Conn().Exec(`UPDATE routes SET created_at = '2001-01-01T00:00:00Z' WHERE slug = 'morning-loop'`); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.getShared("carol", share.Token); status != http.StatusNotFound {
		t.Fatalf("after: status = %d, want 404 once the route is a different generation", status)
	}
}

// An admin may share any rider's route; that link must keep working for as
// long as the route does, even though the admin is not its owner.
func TestAdminShareOfAnotherRidersRouteStillResolves(t *testing.T) {
	h := newShareHarness(t)
	b := h.seedRoute(t, "Bob Loop", "bob")
	resp := h.as("root", "domestique-admins,domestique-access", http.MethodPost,
		"/api/routes/"+b.Slug+"/shares", `{"ttlDays":7}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin create: status = %d, want 201", resp.StatusCode)
	}
	var out createShareOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	if status, _ := h.getShared("carol", out.Token); status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
}

// Deleting a route takes its rendered card with it. The caches are keyed by
// slug, and a slug is reusable, so a leftover would be shown on the next route
// to take the name (finding M4).
func TestDeletingARouteClearsItsPreviewCaches(t *testing.T) {
	h := newShareHarness(t)
	a := h.seedRoute(t, "Morning Loop", "alice")
	if err := h.previews.Put(a.Slug, "u1", `{"layers":[]}`); err != nil {
		t.Fatal(err)
	}
	if err := h.images.Put(a.Slug, "light", "u1", []byte("png")); err != nil {
		t.Fatal(err)
	}

	if resp := h.as("alice", riderGroups, http.MethodDelete, "/api/routes/"+a.Slug, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204", resp.StatusCode)
	}
	if _, found, _ := h.previews.Get(a.Slug, "u1"); found {
		t.Error("track_preview_cache row survived the route")
	}
	if _, found, _ := h.images.Get(a.Slug, "light", "u1"); found {
		t.Error("track_preview_images row survived the route")
	}
}
