package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/settings"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The clock is Wednesday 2026-10-07 and the rider trains Tue/Thu/Sat/Sun, so
// this week holds sessions on Thu 8, Sat 10 and Sun 11 once the tick has run.

type lifeChange struct {
	ID        string `json:"id"`
	Op        string `json:"op"`
	WorkoutID string `json:"workoutId"`
	Date      string `json:"date"`
	ToDate    string `json:"toDate"`
	Reason    string `json:"reason"`
	Default   bool   `json:"default"`
}

type lifeResult struct {
	Event *struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		StartDate string `json:"startDate"`
		EndDate   string `json:"endDate"`
		Option    string `json:"option"`
		Note      string `json:"note"`
	} `json:"event"`
	Diff struct {
		Changes   []lifeChange `json:"changes"`
		LeftAlone []struct {
			WorkoutID string `json:"workoutId"`
			Reason    string `json:"reason"`
		} `json:"leftAlone"`
		Advice []string `json:"advice"`
	} `json:"diff"`
	Applied *struct {
		Removed   int `json:"removed"`
		Moved     int `json:"moved"`
		Eased     int `json:"eased"`
		Shortened int `json:"shortened"`
		Indoor    int `json:"indoor"`
		Added     int `json:"added"`
	} `json:"applied"`
	Error string `json:"error"`
}

func (r lifeResult) change(id string) (lifeChange, bool) {
	for _, c := range r.Diff.Changes {
		if c.ID == id {
			return c, true
		}
	}
	return lifeChange{}, false
}

func (h *onceHarness) life(t *testing.T, user, method, path, body string) (int, lifeResult) {
	t.Helper()
	resp := h.as(user, "cyclists", method, path, body)
	raw, _ := io.ReadAll(resp.Body)
	var out lifeResult
	_ = json.Unmarshal(raw, &out)
	if out.Error == "" && resp.StatusCode >= 400 {
		out.Error = string(raw)
	}
	return resp.StatusCode, out
}

// ticked is a planned week with Saturday's session removed, so a Thursday
// session has somewhere to go.
func (h *onceHarness) tickedWithFreeSaturday(t *testing.T) (thu workout.Workout) {
	t.Helper()
	ctx := context.Background()
	h.srv.AutoScheduleTick(ctx)
	for _, w := range h.all(t) {
		switch w.Date {
		case "2026-10-08":
			thu = w
		case "2026-10-10":
			if err := h.store.DeleteWorkout(ctx, w.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if thu.ID == "" {
		t.Fatal("no Thursday session")
	}
	return thu
}

const tripBody = `{"kind":"travel","startDate":"2026-10-08","endDate":"2026-10-08","option":"no_bike","note":"SECRETNOTE"%s}`

func TestADryRunWritesNothingAndReturnsTheDiff(t *testing.T) {
	h := newOnceHarness(t)
	thu := h.tickedWithFreeSaturday(t)
	before := h.all(t)

	status, res := h.life(t, "wilant", http.MethodPost, "/api/training/life-events", strings.Replace(tripBody, "%s", `,"dryRun":true`, 1))
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, res.Error)
	}
	mv, ok := res.change("move:" + thu.ID)
	if !ok || mv.ToDate != "2026-10-10" || !mv.Default || mv.Reason == "" {
		t.Fatalf("diff = %+v, want Thursday's session moved to Saturday with a reason", res.Diff.Changes)
	}
	if res.Event != nil || res.Applied != nil {
		t.Errorf("a dry run returned an event or applied counts: %+v %+v", res.Event, res.Applied)
	}
	events, _ := h.store.ListLifeEvents(context.Background(), "wilant", "")
	if len(events) != 0 {
		t.Errorf("a dry run stored %d events", len(events))
	}
	after := h.all(t)
	if len(after) != len(before) {
		t.Fatalf("a dry run changed the plan: %d -> %d workouts", len(before), len(after))
	}
	for i := range before {
		if before[i].Date != after[i].Date || before[i].Description != after[i].Description {
			t.Fatalf("a dry run changed %s", before[i].ID)
		}
	}
}

func TestApplyingCreatesTheEventAndMovesTheSessionWithMarkers(t *testing.T) {
	h := newOnceHarness(t)
	thu := h.tickedWithFreeSaturday(t)

	status, res := h.life(t, "wilant", http.MethodPost, "/api/training/life-events", strings.Replace(tripBody, "%s", "", 1))
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("status %d: %s", status, res.Error)
	}
	if res.Event == nil || res.Event.ID == "" || res.Event.Note != "SECRETNOTE" || res.Event.Option != "no_bike" {
		t.Fatalf("event = %+v", res.Event)
	}
	if res.Applied == nil || res.Applied.Moved != 1 {
		t.Fatalf("applied = %+v, want one move", res.Applied)
	}

	got, err := h.store.GetWorkout(context.Background(), thu.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Date != "2026-10-10" {
		t.Fatalf("session is on %s, want 2026-10-10", got.Date)
	}
	from, ok := scheduler.MovedFrom(got.Description)
	if !ok || from != "2026-10-08" {
		t.Errorf("MovedFrom = %q, %v", from, ok)
	}
	if !strings.Contains(got.Description, "Rescheduled by a life event: moved from 2026-10-08.") ||
		!strings.Contains(got.Description, scheduler.AdjustedMarker) {
		t.Errorf("description %q lacks the life-event note or the adjusted marker", got.Description)
	}
	if scheduler.IsGenerated(got) {
		t.Error("a moved session must not be adaptable again")
	}

	// And the tick, replan and fill put nothing back on Thursday.
	h.srv.AutoScheduleTick(context.Background())
	for _, w := range h.all(t) {
		if w.Date == "2026-10-08" {
			t.Errorf("%q came back on the vacated day", w.Name)
		}
	}
}

func TestSkippingAChangeLeavesThatSessionAlone(t *testing.T) {
	h := newOnceHarness(t)
	thu := h.tickedWithFreeSaturday(t)
	status, res := h.life(t, "wilant", http.MethodPost, "/api/training/life-events",
		strings.Replace(tripBody, "%s", `,"skip":["move:`+thu.ID+`"]`, 1))
	if status >= 300 {
		t.Fatalf("status %d: %s", status, res.Error)
	}
	if res.Event == nil {
		t.Fatal("the event was not created")
	}
	got, _ := h.store.GetWorkout(context.Background(), thu.ID)
	if got.Date != "2026-10-08" || got.Description != thu.Description {
		t.Errorf("a skipped session was changed: %+v", got)
	}
}

func TestAnOptInRemovalIsOnlyAppliedWhenIncluded(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	h.srv.AutoScheduleTick(ctx)
	mine, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: "cycling", Name: "My own ride", Date: "2026-10-09", Description: "mine",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"kind":"busy","startDate":"2026-10-09","endDate":"2026-10-09"%s}`

	status, res := h.life(t, "wilant", http.MethodPost, "/api/training/life-events", strings.Replace(body, "%s", "", 1))
	if status >= 300 {
		t.Fatalf("status %d: %s", status, res.Error)
	}
	if c, ok := res.change("remove:" + mine.ID); !ok || c.Default {
		t.Fatalf("the rider's own session should be offered, unticked: %+v", res.Diff.Changes)
	}
	if _, err := h.store.GetWorkout(ctx, mine.ID); err != nil {
		t.Fatalf("a session the rider built was removed without being included: %v", err)
	}

	// Another day, this time ticked.
	mine2, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: "cycling", Name: "My other ride", Date: "2026-10-14", Description: "mine",
	})
	if err != nil {
		t.Fatal(err)
	}
	body = `{"kind":"other","startDate":"2026-10-14","endDate":"2026-10-14"%s}`
	status, _ = h.life(t, "wilant", http.MethodPost, "/api/training/life-events", strings.Replace(body, "%s", `,"include":["remove:`+mine2.ID+`"]`, 1))
	if status >= 300 {
		t.Fatalf("status %d", status)
	}
	if _, err := h.store.GetWorkout(ctx, mine2.ID); err == nil {
		t.Error("an included removal was not applied")
	}
}

func TestAPlanThatChangedSincePreviewIsNotHalfApplied(t *testing.T) {
	h := newOnceHarness(t)
	thu := h.tickedWithFreeSaturday(t)
	ctx := context.Background()
	_, res := h.life(t, "wilant", http.MethodPost, "/api/training/life-events", strings.Replace(tripBody, "%s", `,"dryRun":true`, 1))
	if _, ok := res.change("move:" + thu.ID); !ok {
		t.Fatal("the preview did not move Thursday's session")
	}

	// Between preview and apply the rider deletes it (another tab), and a
	// stale id is sent along in skip.
	if err := h.store.DeleteWorkout(ctx, thu.ID); err != nil {
		t.Fatal(err)
	}
	status, applied := h.life(t, "wilant", http.MethodPost, "/api/training/life-events",
		strings.Replace(tripBody, "%s", `,"skip":["move:`+thu.ID+`","remove:gone"]`, 1))
	if status >= 300 {
		t.Fatalf("status %d: %s", status, applied.Error)
	}
	if applied.Applied == nil || applied.Applied.Moved != 0 || applied.Applied.Removed != 0 {
		t.Fatalf("applied = %+v, want nothing: the session is gone", applied.Applied)
	}
	if applied.Event == nil {
		t.Fatal("the event itself must still be created")
	}
}

func TestTheClientCannotSupplyTheDiff(t *testing.T) {
	h := newOnceHarness(t)
	thu := h.tickedWithFreeSaturday(t)
	// A "changes" field naming a removal is ignored: the server recomputes.
	status, _ := h.life(t, "wilant", http.MethodPost, "/api/training/life-events",
		`{"kind":"travel","startDate":"2026-10-14","endDate":"2026-10-14","option":"no_bike","changes":[{"id":"remove:`+thu.ID+`","op":"remove","workoutId":"`+thu.ID+`"}]}`)
	if status >= 300 {
		t.Fatalf("status %d", status)
	}
	if _, err := h.store.GetWorkout(context.Background(), thu.ID); err != nil {
		t.Fatalf("a diff supplied by the client was applied: %v", err)
	}
}

func TestLifeEventValidationAndOverlap(t *testing.T) {
	h := newOnceHarness(t)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"unknown kind", `{"kind":"holiday","startDate":"2026-10-08","endDate":"2026-10-09"}`, http.StatusBadRequest},
		{"illegal option", `{"kind":"travel","startDate":"2026-10-08","endDate":"2026-10-09","option":"mild"}`, http.StatusBadRequest},
		{"too long ago", `{"kind":"busy","startDate":"2026-09-01","endDate":"2026-09-02"}`, http.StatusBadRequest},
		{"too long", `{"kind":"busy","startDate":"2026-10-08","endDate":"2026-12-30"}`, http.StatusBadRequest},
		{"bad json", `{`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if status, _ := h.life(t, "wilant", http.MethodPost, "/api/training/life-events", c.body); status != c.want {
			t.Errorf("%s: status %d, want %d", c.name, status, c.want)
		}
	}

	if status, res := h.life(t, "wilant", http.MethodPost, "/api/training/life-events", `{"kind":"busy","startDate":"2026-10-20","endDate":"2026-10-22"}`); status >= 300 {
		t.Fatalf("create: %d %s", status, res.Error)
	}
	if status, _ := h.life(t, "wilant", http.MethodPost, "/api/training/life-events", `{"kind":"busy","startDate":"2026-10-22","endDate":"2026-10-25"}`); status != http.StatusConflict {
		t.Errorf("overlap of the same kind: status %d, want 409", status)
	}
	if status, _ := h.life(t, "wilant", http.MethodPost, "/api/training/life-events", `{"kind":"illness","startDate":"2026-10-22","endDate":"2026-10-25"}`); status >= 300 {
		t.Errorf("a different kind may overlap: status %d", status)
	}
}

func TestShorteningAndDeletingRefillFreedDaysWithoutTouchingScheduledWeeks(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	h.srv.AutoScheduleTick(ctx)
	weeksBefore, _ := h.store.ScheduledWeeks(ctx, h.goal.ID)

	status, res := h.life(t, "wilant", http.MethodPost, "/api/training/life-events", `{"kind":"illness","startDate":"2026-10-10","endDate":"2026-10-11","option":"proper"}`)
	if status >= 300 || res.Event == nil {
		t.Fatalf("create: %d %s", status, res.Error)
	}
	if res.Applied == nil || res.Applied.Removed != 2 {
		t.Fatalf("applied = %+v, want Saturday and Sunday removed", res.Applied)
	}
	for _, w := range h.all(t) {
		if w.Date == "2026-10-10" || w.Date == "2026-10-11" {
			t.Fatalf("%s survived the illness", w.Date)
		}
	}
	id := res.Event.ID

	// Shorten: Sunday is free again, and the plan's session comes back.
	status, res = h.life(t, "wilant", http.MethodPut, "/api/training/life-events/"+id,
		`{"kind":"illness","startDate":"2026-10-10","endDate":"2026-10-10","option":"proper","dryRun":true}`)
	if status != http.StatusOK {
		t.Fatalf("dry run shorten: %d %s", status, res.Error)
	}
	if _, ok := res.change("add:2026-10-11"); !ok {
		t.Fatalf("shortening should offer Sunday's session back: %+v", res.Diff.Changes)
	}
	if got, _ := h.store.GetLifeEvent(ctx, "wilant", id); got.End != "2026-10-11" {
		t.Fatal("a dry run edited the event")
	}
	status, res = h.life(t, "wilant", http.MethodPut, "/api/training/life-events/"+id,
		`{"kind":"illness","startDate":"2026-10-10","endDate":"2026-10-10","option":"proper"}`)
	if status != http.StatusOK || res.Applied == nil || res.Applied.Added != 1 {
		t.Fatalf("shorten: %d applied=%+v err=%s", status, res.Applied, res.Error)
	}
	sunday := false
	for _, w := range h.all(t) {
		if w.Date == "2026-10-11" {
			sunday = true
		}
		if w.Date == "2026-10-10" {
			t.Error("Saturday is still inside the event and must stay empty")
		}
	}
	if !sunday {
		t.Error("Sunday was not refilled")
	}

	// Delete: a dry run says what comes back and writes nothing; the real one does it.
	status, res = h.life(t, "wilant", http.MethodDelete, "/api/training/life-events/"+id+"?dryRun=1", "")
	if status != http.StatusOK {
		t.Fatalf("dry run delete: %d %s", status, res.Error)
	}
	if _, ok := res.change("add:2026-10-10"); !ok {
		t.Fatalf("deleting should offer Saturday back: %+v", res.Diff.Changes)
	}
	if _, err := h.store.GetLifeEvent(ctx, "wilant", id); err != nil {
		t.Fatal("a dry run deleted the event")
	}
	status, res = h.life(t, "wilant", http.MethodDelete, "/api/training/life-events/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("delete: %d %s", status, res.Error)
	}
	if _, err := h.store.GetLifeEvent(ctx, "wilant", id); err == nil {
		t.Error("the event is still there")
	}
	saturday := false
	for _, w := range h.all(t) {
		if w.Date == "2026-10-10" {
			saturday = true
		}
	}
	if !saturday {
		t.Error("Saturday was not refilled after the event was deleted")
	}
	weeksAfter, _ := h.store.ScheduledWeeks(ctx, h.goal.ID)
	if len(weeksAfter) != len(weeksBefore) {
		t.Errorf("scheduled_weeks changed: %d -> %d", len(weeksBefore), len(weeksAfter))
	}
}

func TestLengtheningAnEventRunsTheRulesOnTheNewDaysOnly(t *testing.T) {
	h := newOnceHarness(t)
	h.srv.AutoScheduleTick(context.Background())
	status, res := h.life(t, "wilant", http.MethodPost, "/api/training/life-events", `{"kind":"illness","startDate":"2026-10-08","endDate":"2026-10-08","option":"proper"}`)
	if status >= 300 {
		t.Fatalf("create: %d %s", status, res.Error)
	}
	status, res = h.life(t, "wilant", http.MethodPut, "/api/training/life-events/"+res.Event.ID,
		`{"kind":"illness","startDate":"2026-10-08","endDate":"2026-10-10","option":"proper"}`)
	if status != http.StatusOK || res.Applied == nil {
		t.Fatalf("lengthen: %d %s", status, res.Error)
	}
	if res.Applied.Removed != 1 {
		t.Errorf("removed %d, want only Saturday's session (Thursday's went with the first save)", res.Applied.Removed)
	}
}

func TestEndingEarlySetsTheEndToYesterdayAndAnEndBeforeTheStartDeletes(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	status, res := h.life(t, "wilant", http.MethodPost, "/api/training/life-events", `{"kind":"illness","startDate":"2026-10-05","endDate":"2026-10-12","option":"proper"}`)
	if status >= 300 {
		t.Fatalf("create: %d %s", status, res.Error)
	}
	id := res.Event.ID

	// "I'm back": yesterday is the 6th. The start (the 5th) is more than... not
	// moved, so the limits on a new event do not apply.
	status, res = h.life(t, "wilant", http.MethodPut, "/api/training/life-events/"+id, `{"kind":"illness","startDate":"2026-10-05","endDate":"2026-10-06","option":"proper"}`)
	if status != http.StatusOK || res.Event == nil || res.Event.EndDate != "2026-10-06" {
		t.Fatalf("end early: %d %+v %s", status, res.Event, res.Error)
	}

	status, _ = h.life(t, "wilant", http.MethodPut, "/api/training/life-events/"+id, `{"kind":"illness","startDate":"2026-10-05","endDate":"2026-10-04","option":"proper"}`)
	if status != http.StatusOK {
		t.Fatalf("an end before the start: status %d", status)
	}
	if _, err := h.store.GetLifeEvent(ctx, "wilant", id); err == nil {
		t.Error("an end before the start must delete the event")
	}
}

func TestLifeEventsAreOwnerOnly(t *testing.T) {
	h := newOnceHarness(t)
	status, res := h.life(t, "wilant", http.MethodPost, "/api/training/life-events", `{"kind":"busy","startDate":"2026-10-20","endDate":"2026-10-21"}`)
	if status >= 300 {
		t.Fatalf("create: %d %s", status, res.Error)
	}
	id := res.Event.ID

	if status, _ := h.life(t, "sam", http.MethodPut, "/api/training/life-events/"+id, `{"kind":"busy","startDate":"2026-10-20","endDate":"2026-10-30"}`); status != http.StatusNotFound {
		t.Errorf("another rider's PUT: status %d, want 404", status)
	}
	if status, _ := h.life(t, "sam", http.MethodDelete, "/api/training/life-events/"+id, ""); status != http.StatusNotFound {
		t.Errorf("another rider's DELETE: status %d, want 404", status)
	}
	resp := h.as("sam", "cyclists", http.MethodGet, "/api/training/life-events", "")
	var list struct {
		Events []map[string]any `json:"events"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	if len(list.Events) != 0 {
		t.Errorf("sam sees %d events of wilant's", len(list.Events))
	}
	// The rider is the session, never the body.
	status, res = h.life(t, "sam", http.MethodPost, "/api/training/life-events", `{"rider":"wilant","kind":"busy","startDate":"2026-11-01","endDate":"2026-11-02"}`)
	if status >= 300 {
		t.Fatalf("sam create: %d %s", status, res.Error)
	}
	if mine, _ := h.store.ListLifeEvents(context.Background(), "wilant", "2026-10-31"); len(mine) != 0 {
		t.Errorf("a body naming another rider planted an event on them: %+v", mine)
	}
	// A viewer cannot manage training at all.
	if resp := h.as("watcher", "viewers", http.MethodPost, "/api/training/life-events", `{"kind":"busy","startDate":"2026-11-01","endDate":"2026-11-02"}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer's POST: status %d, want 403", resp.StatusCode)
	}
}

func TestListShowsEventsFromAWeekAgoOnward(t *testing.T) {
	h := newOnceHarness(t)
	h.event(t, "travel", "2026-09-01", "2026-09-05", "no_bike")
	h.event(t, "illness", "2026-09-28", "2026-09-29", "proper")
	h.event(t, "busy", "2026-10-20", "2026-10-21", "")
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/life-events", "")
	var list struct {
		Events []struct {
			Kind string `json:"kind"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Events) != 1 || list.Events[0].Kind != "busy" {
		t.Fatalf("events = %+v, want only the ones from 30 September onward", list.Events)
	}
}

func TestTheWeekCarriesItsLifeEvents(t *testing.T) {
	h := newOnceHarness(t)
	h.event(t, "travel", "2026-10-08", "2026-10-09", "no_bike")
	h.event(t, "busy", "2026-10-20", "2026-10-21", "")
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/week?start=2026-10-05", "")
	var week struct {
		LifeEvents []struct {
			Kind      string `json:"kind"`
			StartDate string `json:"startDate"`
		} `json:"lifeEvents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&week); err != nil {
		t.Fatal(err)
	}
	if len(week.LifeEvents) != 1 || week.LifeEvents[0].Kind != "travel" {
		t.Fatalf("lifeEvents = %+v, want just the trip", week.LifeEvents)
	}
}

func TestLifeEventLogsCarryNoNote(t *testing.T) {
	h := newOnceHarness(t)
	var buf bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.tickedWithFreeSaturday(t)
	status, _ := h.life(t, "wilant", http.MethodPost, "/api/training/life-events", strings.Replace(tripBody, "%s", "", 1))
	if status >= 300 {
		t.Fatalf("status %d", status)
	}
	logs := buf.String()
	if strings.Contains(logs, "SECRETNOTE") {
		t.Errorf("the note was logged:\n%s", logs)
	}
	if !strings.Contains(logs, "life event") || !strings.Contains(logs, "rider=wilant") || !strings.Contains(logs, "kind=travel") {
		t.Errorf("no Info line with rider and kind:\n%s", logs)
	}
}

// With the scheduling lock held by the tick, applying 409s with the replan
// wording and changes nothing. SQLite has no advisory locks, so this needs a
// real PostgreSQL.
func TestApplyingWhileTheTickHoldsTheLockIsAConflict(t *testing.T) {
	dsn := os.Getenv("DOMESTIQUE_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set DOMESTIQUE_TEST_POSTGRES to a PostgreSQL DSN to run this")
	}
	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`DELETE FROM life_events`); err != nil {
		t.Fatal(err)
	}
	appSettings, err := settings.UseDB(db.Conn(), db.DSN(), nil)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.New(auth.Config{Mode: auth.ModeProxy, Roles: auth.RoleMapping{Rider: []string{"cyclists"}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := &api.Server{Source: db, Auth: authenticator, Training: store, Settings: appSettings,
		Clock: func() time.Time { return utcNoon(2026, time.October, 7) }}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	holding, release := make(chan struct{}), make(chan struct{})
	go api.HoldSchedulingLockForTest(context.Background(), db.Conn(), func() { close(holding); <-release })
	select {
	case <-holding:
	case <-time.After(5 * time.Second):
		t.Fatal("never took the lock")
	}
	defer close(release)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/training/life-events",
		strings.NewReader(`{"kind":"busy","startDate":"2026-10-20","endDate":"2026-10-21"}`))
	req.Header.Set("Remote-User", "wilant")
	req.Header.Set("Remote-Groups", "cyclists")
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body["error"], "being updated right now") {
		t.Fatalf("status %d body %v, want 409 with the replan wording", resp.StatusCode, body)
	}
	if events, _ := store.ListLifeEvents(context.Background(), "wilant", ""); len(events) != 0 {
		t.Errorf("an event was stored while the lock was held")
	}
}
