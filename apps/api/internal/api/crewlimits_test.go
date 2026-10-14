package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/crew"
)

// Audit N9: one rider created 300 crews in a loop, and every rider's crew list
// then cost two queries per crew.
func TestCrewCreationIsCappedPerCreator(t *testing.T) {
	h := newCrewHarness(t)
	create := func(user, groups, name string) int {
		return h.as(user, groups, http.MethodPost, "/api/crews", fmt.Sprintf(`{"name":%q}`, name)).StatusCode
	}
	for i := 0; i < api.MaxCrewsPerCreator; i++ {
		if got := create("mallory", "cyclists", fmt.Sprintf("spam %d", i)); got != http.StatusCreated {
			t.Fatalf("crew %d = %d, want 201", i+1, got)
		}
	}
	if got := create("mallory", "cyclists", "one too many"); got != http.StatusConflict {
		t.Errorf("crew %d = %d, want 409", api.MaxCrewsPerCreator+1, got)
	}
	// The cap is per creator, and an admin is not held to it.
	if got := create("sam", "cyclists", "sam's crew"); got != http.StatusCreated {
		t.Errorf("another rider's first crew = %d, want 201", got)
	}
	if got := create("boss", "admins", "admin crew"); got != http.StatusCreated {
		t.Errorf("admin crew = %d, want 201", got)
	}
}

func TestCrewCreationCapSurvivesDeletingAndReusingTheName(t *testing.T) {
	h := newCrewHarness(t)
	// Deleting a crew frees a slot: the cap counts crews, not creations.
	var ids []string
	for i := 0; i < api.MaxCrewsPerCreator; i++ {
		c := decodeCrew(t, h.as("mallory", "cyclists", http.MethodPost, "/api/crews", fmt.Sprintf(`{"name":"c%d"}`, i)))
		ids = append(ids, c.ID)
	}
	h.as("mallory", "cyclists", http.MethodDelete, "/api/crews/"+ids[0], "")
	if got := h.as("mallory", "cyclists", http.MethodPost, "/api/crews", `{"name":"fresh"}`).StatusCode; got != http.StatusCreated {
		t.Errorf("after deleting one, a new crew = %d, want 201", got)
	}
}

func TestCrewNameLengthIsBounded(t *testing.T) {
	h := newCrewHarness(t)
	got := h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"`+strings.Repeat("n", 200)+`"}`).StatusCode
	if got != http.StatusBadRequest {
		t.Errorf("a 200-character crew name = %d, want 400", got)
	}
}

func TestCrewListIsPaginated(t *testing.T) {
	h := newCrewHarness(t)
	for _, owner := range []string{"a", "b", "c"} {
		h.as(owner, "cyclists", http.MethodPost, "/api/crews", fmt.Sprintf(`{"name":"crew %s"}`, owner))
	}

	list := func(query string) (int, []map[string]any, string) {
		resp := h.as("wilant", "cyclists", http.MethodGet, "/api/crews"+query, "")
		var out []map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out, resp.Header.Get("X-Total-Count")
	}
	if code, out, total := list(""); code != 200 || len(out) != 3 || total != "3" {
		t.Errorf("default = %d, %d crews, total %q; want all 3 and X-Total-Count 3", code, len(out), total)
	}
	if code, out, total := list("?limit=2"); code != 200 || len(out) != 2 || total != "3" {
		t.Errorf("limit=2 = %d, %d crews, total %q", code, len(out), total)
	}
	if code, out, _ := list("?limit=2&offset=2"); code != 200 || len(out) != 1 {
		t.Errorf("limit=2&offset=2 = %d, %d crews, want the last one", code, len(out))
	}
	if code, _, _ := list("?limit=-1"); code != http.StatusBadRequest {
		t.Errorf("limit=-1 = %d, want 400", code)
	}
}

// A poll of ride-together refreshes the proposals of every crew the caller is
// in; it refreshes at most a bounded number, in a stable order.
func TestRideTogetherPollRefreshesAtMostAFewCrews(t *testing.T) {
	var all []crew.Crew
	for i := 0; i < 25; i++ {
		all = append(all, crew.Crew{ID: fmt.Sprintf("crew:c%02d", i)})
	}
	snap := crew.Snapshot{Crews: all, ApprovedRiders: crew.MemberSet{}}
	for _, c := range all {
		snap.ApprovedRiders[c.ID] = []string{"wilant"}
	}
	got := api.CrewsToRefreshForTest(snap, "wilant")
	if len(got) != api.MaxTogetherCrews {
		t.Fatalf("refreshes %d crews, want %d", len(got), api.MaxTogetherCrews)
	}
	if got[0].ID != "crew:c00" {
		t.Errorf("first = %s, want a stable order", got[0].ID)
	}
	if n := len(api.CrewsToRefreshForTest(snap, "nobody")); n != 0 {
		t.Errorf("a non-member refreshes %d crews", n)
	}
}
