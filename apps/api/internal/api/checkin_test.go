package api_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

type checkInBody struct {
	Date         string `json:"date"`
	Legs         int    `json:"legs"`
	Mood         int    `json:"mood"`
	SleepQuality int    `json:"sleepQuality"`
	Note         string `json:"note"`
}

func getCheckIn(t *testing.T, h *metricsSyncHarness, user, groups string) (int, checkInBody) {
	t.Helper()
	resp := h.as(user, groups, http.MethodGet, "/api/training/checkin", "")
	var out checkInBody
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, out
}

func TestCheckInRoundTrip(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.srv.Clock = func() time.Time { return time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC) }
	code, out := getCheckIn(t, h, "wilant", "cyclists")
	if code != http.StatusOK || out.Date != "2026-10-18" || out.Legs != 0 || out.Mood != 0 || out.SleepQuality != 0 || out.Note != "" {
		t.Fatalf("unanswered = %d %+v; want zeros for today", code, out)
	}
	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/checkin", `{"legs":2,"mood":4,"sleepQuality":3,"note":"heavy"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put = %d", resp.StatusCode)
	}
	_, out = getCheckIn(t, h, "wilant", "cyclists")
	if out.Legs != 2 || out.Mood != 4 || out.SleepQuality != 3 || out.Note != "heavy" {
		t.Fatalf("after put %+v", out)
	}
	h.as("wilant", "cyclists", http.MethodPut, "/api/training/checkin", `{"legs":5}`)
	_, out = getCheckIn(t, h, "wilant", "cyclists")
	if out.Legs != 5 || out.Mood != 0 || out.Note != "" {
		t.Fatalf("second put should replace: %+v", out)
	}
}

func TestCheckInIgnoresABodyDateAndRider(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.srv.Clock = func() time.Time { return time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC) }
	h.as("wilant", "cyclists", http.MethodPut, "/api/training/checkin", `{"rider":"someone","date":"2026-01-01","legs":2}`)
	if _, ok, _ := h.srv.Training.GetCheckIn(t.Context(), "someone", "2026-10-18"); ok {
		t.Fatal("body rider was honoured")
	}
	if _, ok, _ := h.srv.Training.GetCheckIn(t.Context(), "wilant", "2026-01-01"); ok {
		t.Fatal("body date was honoured")
	}
	if c, ok, _ := h.srv.Training.GetCheckIn(t.Context(), "wilant", "2026-10-18"); !ok || c.Legs != 2 {
		t.Fatalf("not stored under session rider and today: %+v %v", c, ok)
	}
}

func TestCheckInRejectsOutOfRangeScoresAndLongNotes(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	for name, body := range map[string]string{
		"legs high":  `{"legs":6}`,
		"mood neg":   `{"mood":-1}`,
		"sleep high": `{"sleepQuality":9}`,
		"long note":  `{"note":"` + strings.Repeat("x", 501) + `"}`,
		"bad json":   `{`,
	} {
		if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/checkin", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, resp.StatusCode)
		}
	}
	// 500 multi-byte characters are within the limit.
	if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/checkin", `{"note":"`+strings.Repeat("é", 500)+`"}`); resp.StatusCode != http.StatusOK {
		t.Errorf("500 characters: status %d, want 200", resp.StatusCode)
	}
}

func TestCheckInIsOwnerOnly(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.as("wilant", "cyclists", http.MethodPut, "/api/training/checkin", `{"legs":2}`)
	if _, out := getCheckIn(t, h, "friend", "cyclists"); out.Legs != 0 {
		t.Fatalf("another rider reads %+v", out)
	}
	if code, _ := getCheckIn(t, h, "watcher", "viewers"); code != http.StatusForbidden {
		t.Errorf("viewer GET = %d, want 403", code)
	}
	if resp := h.as("watcher", "viewers", http.MethodPut, "/api/training/checkin", `{"legs":2}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer PUT = %d, want 403", resp.StatusCode)
	}
}

func TestCheckInNoteIsNeverLogged(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	logs := &bytes.Buffer{}
	h.srv.Log = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.as("wilant", "cyclists", http.MethodPut, "/api/training/checkin", `{"legs":2,"note":"secret-knee-pain"}`)
	h.as("wilant", "cyclists", http.MethodPut, "/api/training/checkin", `{"legs":9,"note":"secret-knee-pain"}`)
	getCheckIn(t, h, "wilant", "cyclists")
	if strings.Contains(logs.String(), "secret-knee-pain") {
		t.Fatalf("note in logs:\n%s", logs.String())
	}
}
