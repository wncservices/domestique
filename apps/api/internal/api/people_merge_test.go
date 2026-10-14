package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const mergeOrphan = "wilant.nackaerts"

func mergeHarness(t *testing.T) *bindingHarness {
	t.Helper()
	h := newBindingHarness(t)
	h.srv.Source = h.db
	h.dataFor(t, mergeOrphan, dataTime)
	return h
}

func (h *bindingHarness) goalsOf(rider string) int {
	h.t.Helper()
	var n int
	if err := h.db.Conn().QueryRow(`SELECT COUNT(*) FROM goals WHERE rider = ?`, rider).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

type mergeOut struct {
	From          string `json:"from"`
	Into          string `json:"into"`
	DryRun        bool   `json:"dryRun"`
	Moved         int    `json:"moved"`
	KeptTarget    int    `json:"keptTarget"`
	DroppedSource int    `json:"droppedSource"`
	Tables        []struct {
		Table string `json:"table"`
		Moved int    `json:"moved"`
	} `json:"tables"`
}

func decodeMerge(t *testing.T, resp *http.Response) mergeOut {
	t.Helper()
	var out mergeOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAnAdminPreviewsAndThenMergesAnOrphanRiderIntoAnother(t *testing.T) {
	h := mergeHarness(t)
	path := "/api/people/riders/" + mergeOrphan + "/merge"

	resp := h.admin.do(http.MethodPost, path, `{"into":"Wilant","dryRun":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dry run = %d", resp.StatusCode)
	}
	preview := decodeMerge(t, resp)
	if !preview.DryRun || preview.From != mergeOrphan || preview.Into != "wilant" || preview.Moved < 1 {
		t.Errorf("preview = %+v, want a dry run of wilant.nackaerts into wilant moving at least the goal", preview)
	}
	if h.goalsOf(mergeOrphan) != 1 || h.goalsOf("wilant") != 0 {
		t.Fatal("the dry run wrote")
	}

	resp = h.admin.do(http.MethodPost, path, `{"into":"wilant"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("merge = %d", resp.StatusCode)
	}
	done := decodeMerge(t, resp)
	if done.DryRun || done.Moved != preview.Moved {
		t.Errorf("merge = %+v, want what the preview said (%d moved)", done, preview.Moved)
	}
	if h.goalsOf(mergeOrphan) != 0 || h.goalsOf("wilant") != 1 {
		t.Errorf("goals: %d under the old rider, %d under wilant, want 0 and 1", h.goalsOf(mergeOrphan), h.goalsOf("wilant"))
	}

	out := h.logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "riders merged") ||
		!strings.Contains(out, "from=wilant.nackaerts") || !strings.Contains(out, "into=wilant") || !strings.Contains(out, "moved=") {
		t.Errorf("no Warn naming both riders and the counts:\n%s", out)
	}
	if strings.Contains(out, "@example.com") || strings.Contains(out, "auth0|") {
		t.Errorf("the log carries an email or a sub:\n%s", out)
	}
}

func TestARiderCannotMergeRiders(t *testing.T) {
	h := mergeHarness(t)
	rider := h.newActor()
	if resp := rider.signIn(inGroups(google("google-oauth2|pat", "Pat", "pat@example.com", true), "cyclists")); resp.StatusCode != http.StatusFound {
		t.Fatalf("sign-in = %d", resp.StatusCode)
	}
	for _, body := range []string{`{"into":"pat","dryRun":true}`, `{"into":"pat"}`} {
		if resp := rider.do(http.MethodPost, "/api/people/riders/"+mergeOrphan+"/merge", body); resp.StatusCode != http.StatusForbidden {
			t.Errorf("a rider's merge %s = %d, want 403", body, resp.StatusCode)
		}
	}
	if h.goalsOf(mergeOrphan) != 1 || h.goalsOf("pat") != 0 {
		t.Error("a refused merge moved data")
	}
}

func TestMergeEndsTheSessionsOfAnAccountOfTheMergedRider(t *testing.T) {
	h := mergeHarness(t)
	moved := h.newActor()
	moved.signIn(inGroups(google("google-oauth2|moved", "Moved", "moved@example.com", true), "cyclists"))
	if got := moved.rider(); got != "moved" {
		t.Fatalf("first sign-in is %q", got)
	}
	resp := h.admin.do(http.MethodPost, "/api/people/riders/moved/merge", `{"into":"wilant"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("merge = %d", resp.StatusCode)
	}
	if got := moved.rider(); got != "" {
		t.Errorf("after the merge the session still signs in as %q, want it ended", got)
	}
	if rider, ok := h.binding("google-oauth2|moved"); !ok || rider != "wilant" {
		t.Errorf("binding = %q (%v), want it moved to wilant", rider, ok)
	}
}

func TestMergeRefusesWhatCannotBeMerged(t *testing.T) {
	h := mergeHarness(t)
	for name, c := range map[string]struct{ path, body string }{
		"into itself": {"/api/people/riders/wilant/merge", `{"into":"Wilant"}`},
		"no target":   {"/api/people/riders/wilant/merge", `{}`},
		"bad name":    {"/api/people/riders/wilant/merge", `{"into":"a b"}`},
	} {
		if resp := h.admin.do(http.MethodPost, c.path, c.body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, resp.StatusCode)
		}
	}
}
