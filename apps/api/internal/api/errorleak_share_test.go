package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// Create's store failure is ours to log, not the client's to read. (The status
// half of this is TestShareCreateStoreFailureIsA500, from before fail was generic.)
func TestShareCreateStoreFailureDoesNotEchoTheSQLError(t *testing.T) {
	h := newShareHarness(t)
	r := h.seedRoute(t, "Hill Loop", "alice")
	if _, err := h.src.Conn().Exec(`DROP TABLE route_shares`); err != nil {
		t.Fatal(err)
	}
	resp := h.as("alice", riderGroups, http.MethodPost, "/api/routes/"+r.Slug+"/shares", `{"ttlDays":7}`)
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), "route_shares") || strings.Contains(string(b), "SQL") {
		t.Fatalf("the store's error reached the client: %s", b)
	}
}
