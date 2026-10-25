package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The model's replies for the three text jobs. None carries a number, so the
// number rule in internal/llm never refuses them.
const (
	explainReply = `{"text":"Today's ride was eased one level because readiness dipped."}`
	weeklyReply  = `{"text":"The week was mostly done and the legs had time to recover."}`
	checkInReply = `{"legs":2,"sleep":2,"mood":3,"life_event":null}`
	explainText  = "Today's ride was eased one level because readiness dipped."
)

// recordCaution stores one readiness adjustment for the rider on a day.
func recordCaution(t *testing.T, h *ledgerHarness, workoutID, day string) {
	t.Helper()
	rec := why.NewRecord(why.ReadinessCaution, "Eased: slept badly", why.ReadinessInputs{Verdict: "caution"})
	if err := h.store.RecordAdjustment(context.Background(), "wilant", workout.SubjectWorkout, workoutID, rec, day); err != nil {
		t.Fatal(err)
	}
}

func storedNote(t *testing.T, h *ledgerHarness, kind, day string) (string, bool) {
	t.Helper()
	text, ok, err := h.store.GetLLMNote(context.Background(), "wilant", kind, day)
	if err != nil {
		t.Fatal(err)
	}
	return text, ok
}

func firstWorkoutIDs(t *testing.T, h *ledgerHarness, n int) []string {
	t.Helper()
	ws := h.thisWeek(t)
	if len(ws) < n {
		t.Fatalf("the week has %d workouts, want %d", len(ws), n)
	}
	ids := make([]string, 0, n)
	for _, w := range ws[:n] {
		ids = append(ids, w.ID)
	}
	return ids
}

// TestChangesNoteWrittenOnAMorningWithAdjustments: the first sync slot of a
// day with an adjustment stores the model's explanation under that day.
func TestChangesNoteWrittenOnAMorningWithAdjustments(t *testing.T) {
	h := newLedgerHarness(t, 250)
	recordCaution(t, h, firstWorkoutIDs(t, h, 1)[0], "2026-10-06")
	f := withLLM(t, h.srv, fixed(explainReply))

	h.at(6, 6, 45)
	h.tick()

	got, ok := storedNote(t, h, workout.LLMNoteChanges, "2026-10-06")
	if !ok || got != explainText {
		t.Fatalf("changes note = %q (ok %v), want %q", got, ok, explainText)
	}
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("model calls = %d, want 1", n)
	}
}

// TestChangesFallbackWhenTheJobIsOff: with explain_changes off the note is the
// de-duplicated rule titles, each ending in a full stop.
func TestChangesFallbackWhenTheJobIsOff(t *testing.T) {
	h := newLedgerHarness(t, 250)
	ids := firstWorkoutIDs(t, h, 2)
	recordCaution(t, h, ids[0], "2026-10-06")
	recordCaution(t, h, ids[1], "2026-10-06")
	f := withLLMJobs(t, h.srv, map[string]bool{"explain_changes": false}, fixed(explainReply))

	h.at(6, 6, 45)
	h.tick()

	// The morning pass makes up Monday's missed session too, so the note is
	// longer than this one rule; the rule must still appear once, not twice.
	got, ok := storedNote(t, h, workout.LLMNoteChanges, "2026-10-06")
	want := why.Title(why.ReadinessCaution) + "."
	if !ok || strings.Count(got, want) != 1 {
		t.Fatalf("fallback note = %q (ok %v), want %q once", got, ok, want)
	}
	if !strings.HasSuffix(got, ".") {
		t.Fatalf("fallback note does not end in a full stop: %q", got)
	}
	if n := f.calls.Load(); n != 0 {
		t.Fatalf("a switched-off job was called %d times", n)
	}
}

// TestNoChangesNoteWithoutAdjustments: a morning with nothing to explain makes
// no model call and writes no note. The goal is saved on the Monday, so that
// morning has no missed session for the pass to make up.
func TestNoChangesNoteWithoutAdjustments(t *testing.T) {
	h := newLedgerHarness(t, 250)
	var explains atomic.Int32
	withLLM(t, h.srv, func(job string, _ []byte) string {
		if job == "explain_changes" {
			explains.Add(1)
		}
		return explainReply
	})

	h.at(5, 6, 45)
	h.tick()

	adjustments, err := h.store.AdjustmentsOnDay(context.Background(), "wilant", "2026-10-05")
	if err != nil || len(adjustments) != 0 {
		t.Fatalf("precondition: the morning has %d adjustments (%v)", len(adjustments), err)
	}
	if _, ok := storedNote(t, h, workout.LLMNoteChanges, "2026-10-05"); ok {
		t.Fatal("a changes note was written with no adjustments")
	}
	if n := explains.Load(); n != 0 {
		t.Fatalf("explain_changes calls = %d, want 0", n)
	}
}

// TestWeeklyNoteOnlyOnMonday: the weekly summary is written on the Monday
// morning, keyed by the Monday of the week it summarises, and no other day.
func TestWeeklyNoteOnlyOnMonday(t *testing.T) {
	h := newLedgerHarness(t, 250)
	withLLM(t, h.srv, fixed(weeklyReply))

	h.at(6, 6, 45)
	h.tick()
	for _, day := range []string{"2026-09-28", "2026-10-05"} {
		if _, ok := storedNote(t, h, workout.LLMNoteWeekly, day); ok {
			t.Fatalf("a weekly note for %s was written on a Tuesday", day)
		}
	}

	h.at(12, 6, 45)
	h.tick()
	if got, ok := storedNote(t, h, workout.LLMNoteWeekly, "2026-10-05"); !ok || got == "" {
		t.Fatalf("no weekly note for the week of 2026-10-05 on the Monday")
	}
}

// TestWeeklySummaryOmitsUnknownFigures: the figures the week has no record of
// are left out of the request, never sent as zeros.
func TestWeeklySummaryOmitsUnknownFigures(t *testing.T) {
	h := newLedgerHarness(t, 250)
	f := withLLM(t, h.srv, fixed(weeklyReply))

	// Monday 12 October: the week of 5 October was never opened by the ledger,
	// so there is no target, no carried debt and no forgiveness to report.
	h.at(12, 6, 45)
	h.tick()

	body := string(f.last)
	if !strings.Contains(body, "Sessions:") {
		t.Fatalf("the planned week's compliance is missing from the request:\n%s", body)
	}
	for _, unknown := range []string{"Target:", "Debt carried in:", "Forgiven:"} {
		if strings.Contains(body, unknown) {
			t.Fatalf("request names %q, which the app has no figure for:\n%s", unknown, body)
		}
	}
}

// TestNothingOutsideTheMorningSlot: the same adjustments made at 13:00 and at
// 21:00 write no changes note and call no model.
func TestNothingOutsideTheMorningSlot(t *testing.T) {
	h := newLedgerHarness(t, 250)
	recordCaution(t, h, firstWorkoutIDs(t, h, 1)[0], "2026-10-06")
	f := withLLM(t, h.srv, fixed(explainReply))

	for _, at := range [][2]int{{13, 0}, {21, 0}} {
		h.at(6, at[0], at[1])
		h.tick()
	}

	if _, ok := storedNote(t, h, workout.LLMNoteChanges, "2026-10-06"); ok {
		t.Fatal("a changes note was written outside the morning slot")
	}
	if n := f.calls.Load(); n != 0 {
		t.Fatalf("model calls = %d outside the morning slot, want 0", n)
	}
}

// TestLLMNotesRunOnce: two morning passes make one model call per kind.
func TestLLMNotesRunOnce(t *testing.T) {
	h := newLedgerHarness(t, 250)
	recordCaution(t, h, firstWorkoutIDs(t, h, 1)[0], "2026-10-12")

	var changes, weekly atomic.Int32
	withLLM(t, h.srv, func(job string, _ []byte) string {
		switch job {
		case "explain_changes":
			changes.Add(1)
			return explainReply
		case "weekly_summary":
			weekly.Add(1)
			return weeklyReply
		}
		return ""
	})

	h.at(12, 6, 45)
	h.tick()
	h.at(12, 7, 10)
	h.tick()

	if n := changes.Load(); n != 1 {
		t.Fatalf("explain_changes calls = %d, want 1", n)
	}
	if n := weekly.Load(); n != 1 {
		t.Fatalf("weekly_summary calls = %d, want 1", n)
	}
}

type notesView struct {
	Changes *struct {
		Day  string `json:"day"`
		Text string `json:"text"`
	} `json:"changes"`
	Weekly *struct {
		WeekStart string `json:"weekStart"`
		Text      string `json:"text"`
	} `json:"weekly"`
}

func getNotes(t *testing.T, h *ledgerHarness, user, groups string) notesView {
	t.Helper()
	resp := h.as(user, groups, http.MethodGet, "/api/training/notes", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/training/notes as %s: status %d", user, resp.StatusCode)
	}
	var out notesView
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestNotesAreOwnerOnly: the notes come from the session's rider. Another
// rider sees none of them.
func TestNotesAreOwnerOnly(t *testing.T) {
	h := newLedgerHarness(t, 250)
	recordCaution(t, h, firstWorkoutIDs(t, h, 1)[0], "2026-10-12")
	withLLM(t, h.srv, func(job string, _ []byte) string {
		if job == "weekly_summary" {
			return weeklyReply
		}
		return explainReply
	})
	h.at(12, 6, 45)
	h.tick()

	own := getNotes(t, h, "wilant", "cyclists")
	if own.Changes == nil || own.Changes.Day != "2026-10-12" || own.Changes.Text != explainText {
		t.Fatalf("own changes note = %+v", own.Changes)
	}
	if own.Weekly == nil || own.Weekly.WeekStart != "2026-10-05" {
		t.Fatalf("own weekly note = %+v", own.Weekly)
	}

	other := getNotes(t, h, "alice", "cyclists")
	if other.Changes != nil || other.Weekly != nil {
		t.Fatalf("another rider can read notes: %+v", other)
	}
}

// TestNotesNeverLogTheNoteOrText: neither the model's text nor the rider's
// check-in note reaches a log line.
func TestNotesNeverLogTheNoteOrText(t *testing.T) {
	h := newLedgerHarness(t, 250)
	logs := captureLogs(h)
	recordCaution(t, h, firstWorkoutIDs(t, h, 1)[0], "2026-10-06")
	withLLM(t, h.srv, func(job string, _ []byte) string {
		if job == "understand_checkin" {
			return `{"legs":2,"sleep":2,"mood":3,"life_event":null}`
		}
		return explainReply
	})
	const secretNote = "BANANA legs note"
	h.at(6, 6, 45)
	if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/checkin",
		`{"legs":3,"mood":3,"sleepQuality":3,"note":"`+secretNote+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("put check-in: status %d", resp.StatusCode)
	}

	h.tick()
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/checkin/understand", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("understand: status %d", resp.StatusCode)
	}

	for _, leak := range []string{secretNote, "readiness dipped"} {
		if strings.Contains(logs.String(), leak) {
			t.Fatalf("a log line carries %q:\n%s", leak, logs.String())
		}
	}
}

// TestUnderstandReadsTheStoredNoteNotTheBody: the reading is of the saved
// check-in. A note sent in the request body is ignored.
func TestUnderstandReadsTheStoredNoteNotTheBody(t *testing.T) {
	h := newLedgerHarness(t, 250)
	if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/checkin",
		`{"legs":3,"mood":3,"sleepQuality":3,"note":"Legs heavy, slept badly"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("put check-in: status %d", resp.StatusCode)
	}
	f := withLLM(t, h.srv, fixed(checkInReply))

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/checkin/understand", `{"note":"ignored body text"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("understand: status %d", resp.StatusCode)
	}
	if !strings.Contains(string(f.last), "Legs heavy, slept badly") || strings.Contains(string(f.last), "ignored body text") {
		t.Fatalf("the model was sent the wrong note:\n%s", f.last)
	}
	var out struct {
		Legs      int  `json:"legs"`
		Sleep     int  `json:"sleep"`
		Mood      int  `json:"mood"`
		LifeEvent *any `json:"lifeEvent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Legs != 2 || out.Sleep != 2 || out.Mood != 3 || out.LifeEvent != nil {
		t.Fatalf("reading = %+v", out)
	}
}

// TestUnderstandWithoutANoteIs404.
func TestUnderstandWithoutANoteIs404(t *testing.T) {
	h := newLedgerHarness(t, 250)
	withLLM(t, h.srv, fixed(checkInReply))
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/checkin/understand", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestUnderstandJobOffIs412.
func TestUnderstandJobOffIs412(t *testing.T) {
	h := newLedgerHarness(t, 250)
	if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/checkin",
		`{"legs":3,"mood":3,"sleepQuality":3,"note":"Legs heavy"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("put check-in: status %d", resp.StatusCode)
	}
	withLLMJobs(t, h.srv, map[string]bool{"understand_checkin": false}, fixed(checkInReply))
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/checkin/understand", "")
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", resp.StatusCode)
	}
}

// TestUnderstandFailureLeavesCheckInAlone: a failed reading is a 502 with one
// quiet line, and the saved check-in is untouched.
func TestUnderstandFailureLeavesCheckInAlone(t *testing.T) {
	h := newLedgerHarness(t, 250)
	if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/checkin",
		`{"legs":3,"mood":4,"sleepQuality":2,"note":"Legs heavy"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("put check-in: status %d", resp.StatusCode)
	}
	f := withLLM(t, h.srv, fixed(checkInReply))
	f.code = http.StatusInternalServerError

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/checkin/understand", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	var msg map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil || msg["error"] != "Couldn't read the note this time." {
		t.Fatalf("error = %v (%v)", msg, err)
	}

	kept := h.as("wilant", "cyclists", http.MethodGet, "/api/training/checkin", "")
	var c struct {
		Legs int    `json:"legs"`
		Mood int    `json:"mood"`
		Note string `json:"note"`
	}
	if err := json.NewDecoder(kept.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if c.Legs != 3 || c.Mood != 4 || c.Note != "Legs heavy" {
		t.Fatalf("the check-in changed: %+v", c)
	}
}

// TestMeListsTheNewJobs: the three new jobs appear in /api/me, and a switched
// off job reads false.
func TestMeListsTheNewJobs(t *testing.T) {
	h := newLedgerHarness(t, 250)
	withLLMJobs(t, h.srv, map[string]bool{"weekly_summary": false}, fixed(explainReply))

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/me", "")
	var me struct {
		LLMJobs struct {
			ExplainChanges    bool `json:"explainChanges"`
			UnderstandCheckin bool `json:"understandCheckin"`
			WeeklySummary     bool `json:"weeklySummary"`
		} `json:"llmJobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	if !me.LLMJobs.ExplainChanges || !me.LLMJobs.UnderstandCheckin || me.LLMJobs.WeeklySummary {
		t.Fatalf("llmJobs = %+v", me.LLMJobs)
	}
}
