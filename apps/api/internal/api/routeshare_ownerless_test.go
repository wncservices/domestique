package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// A route with no recorded owner is legitimate (library imports). Its share must
// open, not be mistaken for a pre-binding link.
func TestShareOfAnOwnerlessRouteOpens(t *testing.T) {
	h := newShareHarness(t)
	r := h.seedRoute(t, "Orphan Loop", "")
	resp := h.as("root", "domestique-admins,domestique-access", http.MethodPost,
		"/api/routes/"+r.Slug+"/shares", `{"ttlDays":7}`)
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

// A failing store is ours to log, not the client's to read.
func TestShareCreateStoreFailureIsA500(t *testing.T) {
	h := newShareHarness(t)
	r := h.seedRoute(t, "Hill Loop", "alice")
	if _, err := h.src.Conn().Exec(`DROP TABLE route_shares`); err != nil {
		t.Fatal(err)
	}
	resp := h.as("alice", riderGroups, http.MethodPost, "/api/routes/"+r.Slug+"/shares", `{"ttlDays":7}`)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s; want 500 (the no-leak half lives in errorleak_share_test.go once fail is generic)", resp.StatusCode, b)
	}
}
