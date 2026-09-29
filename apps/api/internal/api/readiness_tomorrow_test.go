package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Thursday. Fixed for every test in this file; "tomorrow" is 2026-03-20.
const (
	tmToday    = "2026-03-19"
	tmTomorrow = "2026-03-20"
)

type tomorrowBody struct {
	Tomorrow *struct {
		Date        string   `json:"date"`
		Risk        string   `json:"risk"`
		Reasons     []string `json:"reasons"`
		WorkoutID   string   `json:"workoutId"`
		WorkoutName string   `json:"workoutName"`
	} `json:"tomorrow"`
}

type tomorrowHarness struct {
	*autoScheduleHarness
	goalID string
}

func newTomorrowHarness(t *testing.T) *tomorrowHarness {
	t.Helper()
	h := &tomorrowHarness{autoScheduleHarness: newAutoScheduleHarness(t)}
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC) }
	goal, err := h.store.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	h.goalID = goal.ID
	return h
}

func (h *tomorrowHarness) hard(date string) workout.Workout {
	h.t.Helper()
	w, err := h.store.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: h.goalID, Sport: model.SportCycling, Name: "Threshold 3x12",
		Date: date, Description: scheduler.GeneratedDescription,
		Zone: workout.ZoneThreshold, Level: 5,
		Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600}},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return w
}

// snapshot plants the form row a rest forecast needs: CTL 40 / ATL 100 at
// the start of today projects to about −48 for tomorrow.
func (h *tomorrowHarness) snapshot(ctl, atl float64) {
	h.t.Helper()
	if _, err := h.conn.Exec(`DELETE FROM fitness_snapshots WHERE rider = 'wilant'`); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.conn.Exec(`INSERT INTO fitness_snapshots (rider, date, ctl, atl, tsb) VALUES ('wilant', ?, ?, ?, ?)`,
		tmToday, ctl, atl, ctl-atl); err != nil {
		h.t.Fatal(err)
	}
}

// rode records a completed hour of cycling on date — the hard days a
// consecutive-days run counts must have actually been ridden.
func (h *tomorrowHarness) rode(date string) {
	h.t.Helper()
	if _, err := h.store.UpsertSession(context.Background(), workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ride-" + date, Sport: "cycling",
		Date: date, DurationSeconds: 3600,
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *tomorrowHarness) get(user, groups, query string) (int, tomorrowBody, map[string]json.RawMessage) {
	h.t.Helper()
	resp := h.as(user, groups, http.MethodGet, "/api/training/readiness"+query, "")
	var raw map[string]json.RawMessage
	var body tomorrowBody
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	_ = json.Unmarshal(buf.Bytes(), &raw)
	_ = json.Unmarshal(buf.Bytes(), &body)
	return resp.StatusCode, body, raw
}

func (h *tomorrowHarness) ease(user, groups, query string) (int, map[string]string) {
	h.t.Helper()
	resp := h.as(user, groups, http.MethodPost, "/api/training/readiness/tomorrow/ease"+query, "")
	out := map[string]string{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (h *tomorrowHarness) workouts() []workout.Workout {
	h.t.Helper()
	ws, err := h.store.ListWorkouts(context.Background(), "wilant")
	if err != nil {
		h.t.Fatal(err)
	}
	return ws
}

func TestReadinessEndpointForecastsTomorrow(t *testing.T) {
	h := newTomorrowHarness(t)
	tomorrow := h.hard(tmTomorrow)
	h.snapshot(40, 100)

	status, body, _ := h.get("wilant", "cyclists", "?today="+tmToday)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	f := body.Tomorrow
	if f == nil {
		t.Fatal("tomorrow = null, want a forecast")
	}
	if f.Date != tmTomorrow || f.Risk != "rest" || f.WorkoutID != tomorrow.ID || f.WorkoutName != "Threshold 3x12" {
		t.Errorf("tomorrow = %+v, want rest for %s / %s", f, tomorrow.ID, tmTomorrow)
	}
	if len(f.Reasons) == 0 || !strings.HasPrefix(f.Reasons[0], "tomorrow's form is projected at −") {
		t.Errorf("reasons = %v, want the projected-form reason first", f.Reasons)
	}
}

func TestReadinessEndpointOmitsTomorrowWhenNothingIsAtRisk(t *testing.T) {
	h := newTomorrowHarness(t)
	h.hard(tmTomorrow) // eligible, but no snapshot, load or hard-day run to worry about

	status, _, raw := h.get("wilant", "cyclists", "?today="+tmToday)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if _, present := raw["tomorrow"]; present {
		t.Errorf("tomorrow present as %s, want the key omitted for a ready forecast", raw["tomorrow"])
	}
}

func TestReadinessEndpointOmitsTomorrowWithNoEligibleWorkout(t *testing.T) {
	h := newTomorrowHarness(t)
	h.snapshot(40, 100) // would be a rest forecast — but there is nothing to ease

	_, _, raw := h.get("wilant", "cyclists", "?today="+tmToday)
	if _, present := raw["tomorrow"]; present {
		t.Errorf("tomorrow present as %s, want omitted with no workout tomorrow", raw["tomorrow"])
	}
}

func TestReadinessEndpointTomorrowFollowsTheTodayParameterNotTheServerClock(t *testing.T) {
	h := newTomorrowHarness(t)
	h.hard(tmTomorrow)
	h.snapshot(40, 100)

	// The browser is still on the 18th: its tomorrow is the 19th, which has
	// no workout — even though the server clock says the 19th.
	_, body, _ := h.get("wilant", "cyclists", "?today=2026-03-18")
	if body.Tomorrow != nil {
		t.Errorf("tomorrow = %+v, want none: the caller's tomorrow (the 19th) has no workout", body.Tomorrow)
	}
	// And the caller's own day is honoured when it is ahead of the server's.
	h.snapshot(40, 100)
	_, body, _ = h.get("wilant", "cyclists", "?today="+tmToday)
	if body.Tomorrow == nil || body.Tomorrow.Date != tmTomorrow {
		t.Errorf("tomorrow = %+v, want the 20th", body.Tomorrow)
	}

	status, _, _ := h.get("wilant", "cyclists", "?today=tomorrow")
	if status != http.StatusBadRequest {
		t.Errorf("a malformed today gave %d, want 400", status)
	}
}

func TestReadinessEndpointTomorrowIsOwnerOnly(t *testing.T) {
	h := newTomorrowHarness(t)
	h.hard(tmTomorrow)
	h.snapshot(40, 100)

	if status, body, _ := h.get("someone-else", "cyclists", "?today="+tmToday); status != http.StatusOK || body.Tomorrow != nil {
		t.Errorf("another rider got %d / %+v, want 200 and none of wilant's forecast", status, body.Tomorrow)
	}
	if status, _, _ := h.get("watcher", "viewers", "?today="+tmToday); status != http.StatusForbidden {
		t.Errorf("a viewer's GET got %d, want 403", status)
	}
	if status, _ := h.ease("watcher", "viewers", "?today="+tmToday); status != http.StatusForbidden {
		t.Errorf("a viewer's ease got %d, want 403", status)
	}
	// Another rider's ease never reaches wilant's workout.
	if status, _ := h.ease("someone-else", "cyclists", "?today="+tmToday); status != http.StatusConflict {
		t.Errorf("another rider's ease got %d, want 409 (nothing of their own to ease)", status)
	}
	for _, w := range h.workouts() {
		if !scheduler.IsGenerated(w) {
			t.Errorf("workout %s was changed by someone else's ease", w.ID)
		}
	}
}

func TestEaseTomorrowSwapsForAnEasyRideOnARestForecast(t *testing.T) {
	h := newTomorrowHarness(t)
	tomorrow := h.hard(tmTomorrow)
	h.snapshot(40, 100)

	status, out := h.ease("wilant", "cyclists", "?today="+tmToday)
	if status != http.StatusOK {
		t.Fatalf("status = %d (%v), want 200", status, out)
	}
	if !strings.HasPrefix(out["reason"], "Eased ahead of time — tomorrow's form is projected at −") {
		t.Errorf("reason = %q, want the applied reason", out["reason"])
	}

	got, _ := h.store.GetWorkout(context.Background(), tomorrow.ID)
	if got.Name != "Endurance ride" || !strings.Contains(got.Description, scheduler.AdjustedMarker) ||
		!strings.Contains(got.Description, "Eased ahead of time — ") || !strings.Contains(got.Description, "Threshold 3x12") {
		t.Errorf("workout = %q / %q, want an easy ride carrying the reason and what it replaced", got.Name, got.Description)
	}
	if scheduler.IsGenerated(got) {
		t.Error("an eased workout must stop being IsGenerated — that is the whole double-easing guard")
	}

	// A second click has nothing to ease, and changes nothing.
	before := h.workouts()
	if status, _ := h.ease("wilant", "cyclists", "?today="+tmToday); status != http.StatusConflict {
		t.Errorf("second ease status = %d, want 409", status)
	}
	if !reflect.DeepEqual(before, h.workouts()) {
		t.Error("a second ease changed a workout")
	}
	// And the banner is gone on the next fetch.
	if _, body, _ := h.get("wilant", "cyclists", "?today="+tmToday); body.Tomorrow != nil {
		t.Errorf("tomorrow = %+v after easing, want none", body.Tomorrow)
	}
}

func TestEaseTomorrowStepsDownOneRungOnACautionForecast(t *testing.T) {
	h := newTomorrowHarness(t)
	h.hard(tmToday)
	h.hard("2026-03-18")
	h.rode("2026-03-18")
	tomorrow := h.hard(tmTomorrow)

	_, body, _ := h.get("wilant", "cyclists", "?today="+tmToday)
	if body.Tomorrow == nil || body.Tomorrow.Risk != "caution" ||
		!reflect.DeepEqual(body.Tomorrow.Reasons, []string{"tomorrow would be your third hard day in a row"}) {
		t.Fatalf("tomorrow = %+v, want a caution for the third hard day", body.Tomorrow)
	}

	if status, out := h.ease("wilant", "cyclists", "?today="+tmToday); status != http.StatusOK {
		t.Fatalf("status = %d (%v), want 200", status, out)
	}
	got, _ := h.store.GetWorkout(context.Background(), tomorrow.ID)
	if got.Level != 4 || got.Date != tmTomorrow {
		t.Errorf("level/date = %v/%s, want one rung down (4) on the same day", got.Level, got.Date)
	}
	if !strings.Contains(got.Description, "readiness-forecast:"+tmTomorrow) ||
		!strings.Contains(got.Description, "Eased ahead of time — tomorrow would be your third hard day in a row") {
		t.Errorf("description = %q, want the forecast source marker and the reason", got.Description)
	}
	if scheduler.IsGenerated(got) {
		t.Error("a stepped-down workout must stop being IsGenerated")
	}
	if visible := riderVisibleAdjustmentText(got.Description); strings.Contains(visible, "source:") {
		t.Errorf("rider-visible text = %q, must not leak the source marker", visible)
	}
}

// The banner said caution; by the time of the click the fresh forecast is
// rest. The click must apply rest's treatment, not what the page showed.
func TestEaseTomorrowRecomputesRatherThanTrustingTheBanner(t *testing.T) {
	h := newTomorrowHarness(t)
	h.hard(tmToday)
	h.hard("2026-03-18")
	h.rode("2026-03-18")
	tomorrow := h.hard(tmTomorrow)
	// Today's hard session needs an FTP to be sized for the projection.
	if _, err := h.store.SaveProfile(context.Background(), workout.RiderProfile{Rider: "wilant", FTPWatts: 200}); err != nil {
		t.Fatal(err)
	}

	_, body, _ := h.get("wilant", "cyclists", "?today="+tmToday)
	if body.Tomorrow == nil || body.Tomorrow.Risk != "caution" {
		t.Fatalf("tomorrow = %+v, want caution first", body.Tomorrow)
	}
	h.snapshot(40, 100) // training data moves before the click

	if status, out := h.ease("wilant", "cyclists", "?today="+tmToday); status != http.StatusOK {
		t.Fatalf("status = %d (%v)", status, out)
	}
	got, _ := h.store.GetWorkout(context.Background(), tomorrow.ID)
	if got.Name != "Endurance ride" {
		t.Errorf("name = %q, want the rest treatment (swapped for an easy ride)", got.Name)
	}
}

func TestEaseTomorrowRefusesWithA409AndChangesNothing(t *testing.T) {
	type setup func(h *tomorrowHarness)
	cases := []struct {
		name  string
		setup setup
	}{
		{"the fresh forecast is ready", func(h *tomorrowHarness) { h.hard(tmTomorrow) }},
		{"tomorrow's workout is already adjusted", func(h *tomorrowHarness) {
			w := h.hard(tmTomorrow)
			d := w.Description + " " + scheduler.AdjustedMarker + " eased earlier"
			if _, err := h.store.UpdateWorkout(context.Background(), w.ID, workout.UpdateWorkoutRequest{Description: &d}); err != nil {
				t.Fatal(err)
			}
			h.snapshot(40, 100)
		}},
		{"tomorrow's workout is the rider's own", func(h *tomorrowHarness) {
			if _, err := h.store.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
				Rider: "wilant", Sport: model.SportCycling, Name: "My intervals", Date: tmTomorrow, Description: "mine",
				Zone: workout.ZoneThreshold, Level: 5,
				Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600}},
			}); err != nil {
				t.Fatal(err)
			}
			h.snapshot(40, 100)
		}},
		{"tomorrow's workout is already done", func(h *tomorrowHarness) {
			h.hard(tmTomorrow)
			if _, err := h.store.UpsertSession(context.Background(), workout.UpsertSessionRequest{
				Rider: "wilant", Provider: "garmin", ExternalID: "early", Sport: "cycling",
				Date: tmTomorrow, DurationSeconds: 3600,
			}); err != nil {
				t.Fatal(err)
			}
			h.snapshot(40, 100)
		}},
		{"tomorrow has no workout at all", func(h *tomorrowHarness) { h.snapshot(40, 100) }},
		{"tomorrow's workout is an easy one", func(h *tomorrowHarness) {
			if _, err := h.store.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
				Rider: "wilant", GoalID: h.goalID, Sport: model.SportCycling, Name: "Endurance ride", Date: tmTomorrow,
				Description: scheduler.GeneratedDescription, Zone: workout.ZoneEndurance,
				Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600}},
			}); err != nil {
				t.Fatal(err)
			}
			h.snapshot(40, 100)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newTomorrowHarness(t)
			c.setup(h)
			before := h.workouts()

			status, out := h.ease("wilant", "cyclists", "?today="+tmToday)
			if status != http.StatusConflict {
				t.Fatalf("status = %d, want 409", status)
			}
			if out["error"] == "" {
				t.Error("409 without a plain message for the rider")
			}
			if !reflect.DeepEqual(before, h.workouts()) {
				t.Error("a refused ease changed a workout")
			}
		})
	}
}

// Today's own readiness has eased today's session; tomorrow's ease must
// reach tomorrow's workout only, and leave today's exactly as it was.
func TestEaseTomorrowNeverTouchesTodaysWorkoutEvenWhenTodaysReadinessAlreadyEasedIt(t *testing.T) {
	h := newTomorrowHarness(t)
	ctx := context.Background()
	todays := h.hard(tmToday)
	tomorrow := h.hard(tmTomorrow)
	if err := h.store.SaveWellness(ctx, workout.DailyWellness{
		Rider: "wilant", Date: tmToday, ReadinessLevel: "POOR", ReadinessScore: 10,
	}); err != nil {
		t.Fatal(err)
	}

	h.srv.AdaptWorkouts(ctx)
	easedToday, _ := h.store.GetWorkout(ctx, todays.ID)
	if scheduler.IsGenerated(easedToday) {
		t.Fatal("setup: today's readiness should already have eased today's session")
	}

	status, out := h.ease("wilant", "cyclists", "?today="+tmToday)
	if status != http.StatusOK {
		t.Fatalf("status = %d (%v), want 200: a rest day today makes tomorrow a caution", status, out)
	}
	afterToday, _ := h.store.GetWorkout(ctx, todays.ID)
	if !reflect.DeepEqual(easedToday, afterToday) {
		t.Errorf("today's workout changed from %+v to %+v", easedToday, afterToday)
	}
	got, _ := h.store.GetWorkout(ctx, tomorrow.ID)
	if got.Level != 4 || scheduler.IsGenerated(got) {
		t.Errorf("tomorrow = level %v, generated=%v, want stepped down and no longer generated", got.Level, scheduler.IsGenerated(got))
	}
}

// Tomorrow morning's own readiness pass must skip a workout the rider
// already eased tonight — the guard is the existing AdjustedMarker rule.
func TestTomorrowMorningsOwnReadinessPassSkipsAWorkoutEasedTheNightBefore(t *testing.T) {
	h := newTomorrowHarness(t)
	ctx := context.Background()
	tomorrow := h.hard(tmTomorrow)
	h.snapshot(40, 100)
	if status, out := h.ease("wilant", "cyclists", "?today="+tmToday); status != http.StatusOK {
		t.Fatalf("status = %d (%v)", status, out)
	}
	eased, _ := h.store.GetWorkout(ctx, tomorrow.ID)

	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 20, 7, 0, 0, 0, time.UTC) }
	if err := h.store.SaveWellness(ctx, workout.DailyWellness{
		Rider: "wilant", Date: tmTomorrow, ReadinessLevel: "POOR", ReadinessScore: 5,
	}); err != nil {
		t.Fatal(err)
	}
	h.srv.AdaptWorkouts(ctx)

	after, _ := h.store.GetWorkout(ctx, tomorrow.ID)
	if !reflect.DeepEqual(eased, after) {
		t.Errorf("the morning pass changed an already-eased workout: %+v -> %+v", eased, after)
	}
}

// spyHandler records every attribute of every record, so a test can assert
// exactly which keys and values a log line carried.
type spyHandler struct{ records *[]spyRecord }
type spyRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

func (h spyHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h spyHandler) Handle(_ context.Context, r slog.Record) error {
	rec := spyRecord{level: r.Level, msg: r.Message, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool { rec.attrs[a.Key] = a.Value.String(); return true })
	*h.records = append(*h.records, rec)
	return nil
}
func (h spyHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h spyHandler) WithGroup(string) slog.Handler      { return h }

func TestSyncTickLogsATomorrowAdvisoryWithRiderAndVerdictOnly(t *testing.T) {
	h := newTomorrowHarness(t)
	ctx := context.Background()
	h.hard(tmTomorrow)
	h.snapshot(40, 100)
	var records []spyRecord
	h.srv.Log = slog.New(spyHandler{&records})
	before := h.workouts()

	h.srv.AdaptWorkouts(ctx)

	var found []spyRecord
	for _, r := range records {
		if r.msg == "tomorrow's session may need easing" {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("advisory lines = %d (%+v), want exactly 1", len(found), records)
	}
	r := found[0]
	if r.level != slog.LevelInfo {
		t.Errorf("level = %v, want Info", r.level)
	}
	if !reflect.DeepEqual(r.attrs, map[string]string{"rider": "wilant", "risk": "rest"}) {
		t.Errorf("attrs = %v, want rider and verdict word only — no form, load or ratio", r.attrs)
	}
	if !reflect.DeepEqual(before, h.workouts()) {
		t.Error("the advisory pass changed a workout; it must only observe")
	}
}

func TestSyncTickLogsNothingWhenTomorrowIsReady(t *testing.T) {
	h := newTomorrowHarness(t)
	h.hard(tmTomorrow)
	var records []spyRecord
	h.srv.Log = slog.New(spyHandler{&records})

	h.srv.AdaptWorkouts(context.Background())

	for _, r := range records {
		if strings.Contains(r.msg, "tomorrow") {
			t.Errorf("unexpected advisory %+v for a ready forecast", r)
		}
	}
}

// ?today= is the caller's own day, but only within a day either side of the
// server's UTC today (which covers every real zone, UTC-12 to UTC+14): past
// that a rider could point the ease at an arbitrary future day, and a badly
// wrong browser clock would make "tomorrow" the server's today.
func TestTodayParameterIsBoundedToOneDayEitherSideOfUTCNow(t *testing.T) {
	cases := []struct {
		name  string
		today string
		want  int
	}{
		{"malformed", "tomorrow", http.StatusBadRequest},
		{"not a real date", "2026-02-30", http.StatusBadRequest},
		{"two days ahead", "2026-03-21", http.StatusBadRequest},
		{"far ahead", "2027-01-01", http.StatusBadRequest},
		{"two days behind", "2026-03-17", http.StatusBadRequest},
		{"a day behind, the far-west edge", "2026-03-18", http.StatusOK},
		{"the server's own day", tmToday, http.StatusOK},
		{"a day ahead, the far-east edge", tmTomorrow, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newTomorrowHarness(t)
			h.hard("2026-03-21")
			h.snapshot(40, 100)
			before := h.workouts()

			if status, _, _ := h.get("wilant", "cyclists", "?today="+c.today); status != c.want {
				t.Errorf("GET status = %d, want %d", status, c.want)
			}
			status, _ := h.ease("wilant", "cyclists", "?today="+c.today)
			if c.want == http.StatusBadRequest && status != http.StatusBadRequest {
				t.Errorf("ease status = %d, want 400", status)
			}
			if c.want == http.StatusBadRequest && !reflect.DeepEqual(before, h.workouts()) {
				t.Error("a rejected ease changed a workout")
			}
		})
	}
}

// Two skipped hard days are not a run: tomorrow is not "the third hard day".
func TestSkippedHardDaysDoNotMakeTomorrowAThirdHardDay(t *testing.T) {
	h := newTomorrowHarness(t)
	h.hard(tmToday)
	h.hard("2026-03-18") // never ridden
	h.hard(tmTomorrow)

	_, body, raw := h.get("wilant", "cyclists", "?today="+tmToday)
	if body.Tomorrow != nil {
		t.Errorf("tomorrow = %+v, want none (raw %s)", body.Tomorrow, raw["tomorrow"])
	}
}

// A caution the click could only answer with a 500 must never be offered.
func TestNoCautionBannerForAWorkoutWithNoLadderToStepDown(t *testing.T) {
	h := newTomorrowHarness(t)
	if _, err := h.store.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: h.goalID, Sport: model.SportCycling, Name: "Intervals", Date: tmTomorrow,
		Description: scheduler.GeneratedDescription, Zone: "intervals", Level: 3,
		Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600}},
	}); err != nil {
		t.Fatal(err)
	}
	h.hard(tmToday)
	h.hard("2026-03-18")
	h.rode("2026-03-18")

	_, body, _ := h.get("wilant", "cyclists", "?today="+tmToday)
	if body.Tomorrow != nil {
		t.Errorf("tomorrow = %+v, want none: nothing to step down to", body.Tomorrow)
	}
	if status, _ := h.ease("wilant", "cyclists", "?today="+tmToday); status != http.StatusConflict {
		t.Errorf("ease status = %d, want 409, not a 500", status)
	}
}
