package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// adjustmentFor is the latest stored reason for one of wilant's workouts.
func adjustmentFor(t *testing.T, store *workout.DB, id string) (workout.Adjustment, bool) {
	t.Helper()
	got, err := store.LatestAdjustments(context.Background(), "wilant", workout.SubjectWorkout, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := got[id]
	return a, ok
}

func adjustmentRows(t *testing.T, h *autoScheduleHarness) int {
	t.Helper()
	var n int
	if err := h.conn.QueryRow(`SELECT COUNT(1) FROM adjustments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// decodeInputs reads a row's inputs as the typed struct its rule documents.
func decodeInputs[T any](t *testing.T, a workout.Adjustment) T {
	t.Helper()
	var out T
	raw, err := json.Marshal(a.Inputs)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	if err != nil {
		t.Fatalf("inputs %v: %v", a.Inputs, err)
	}
	return out
}

// cautionToday plants a caution-level day: HRV unbalanced with numbers, and a
// hard structured session today to ease.
func (h *tomorrowHarness) cautionToday() workout.Workout {
	h.t.Helper()
	if err := h.store.SaveWellness(context.Background(), workout.DailyWellness{
		Rider: "wilant", Date: tmToday, HRVStatus: "UNBALANCED", HRVLastNight: 41, HRVWeeklyAvg: 52,
	}); err != nil {
		h.t.Fatal(err)
	}
	return h.hard(tmToday)
}

func TestAnAutomaticMoveRecordsItsRuleOnceAndASecondPassAddsNothing(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC) }
	goal, _ := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{Rider: "wilant", HoursPerAvailableDay: 1.5, AvailableDays: []string{"tue", "fri", "sat"}}); err != nil {
		t.Fatal(err)
	}
	tempo, _ := h.store.CreateWorkout(ctx, generated("wilant", goal.ID, "Tempo ride", "2026-03-24", 3600))
	easy, _ := h.store.CreateWorkout(ctx, generated("wilant", goal.ID, "Endurance ride", "2026-03-27", 3600))
	_, _ = h.store.CreateWorkout(ctx, generated("wilant", goal.ID, "Long ride", "2026-03-28", 5400))

	h.srv.AdaptWorkouts(ctx)

	a, ok := adjustmentFor(t, h.store, tempo.ID)
	if !ok || a.Rule != why.MissedMoved || a.Day != "2026-03-26" {
		t.Fatalf("adjustment = %+v (found %v), want missed_moved on the clock's day", a, ok)
	}
	in := decodeInputs[why.MissedMovedInputs](t, a)
	if in.From != "2026-03-24" || in.To != "2026-03-27" || !in.ReplacedEasy {
		t.Errorf("inputs = %+v", in)
	}
	moved, _ := h.store.GetWorkout(ctx, tempo.ID)
	if !strings.Contains(moved.Description, a.Text) {
		t.Errorf("stored text %q is not the sentence in the description %q", a.Text, moved.Description)
	}
	if _, ok := adjustmentFor(t, h.store, easy.ID); ok {
		t.Error("the easy day that was given up kept a row")
	}
	if n := adjustmentRows(t, h); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}

	h.srv.AdaptWorkouts(ctx)
	if n := adjustmentRows(t, h); n != 1 {
		t.Errorf("rows after a second pass = %d, want still 1", n)
	}
}

func TestAReadinessEasingRecordsTheHRVNumbers(t *testing.T) {
	h := newTomorrowHarness(t)
	orig := h.cautionToday()

	h.srv.AdaptWorkouts(context.Background())

	a, ok := adjustmentFor(t, h.store, orig.ID)
	if !ok || a.Rule != why.ReadinessCaution {
		t.Fatalf("adjustment = %+v (found %v), want readiness_caution", a, ok)
	}
	in := decodeInputs[why.ReadinessInputs](t, a)
	if in.Verdict != "caution" || len(in.Signals) != 1 || in.Signals[0].Kind != "hrv" {
		t.Fatalf("inputs = %+v", in)
	}
	if got := in.Signals[0].Numbers; len(got) != 2 || got[0] != 41 || got[1] != 52 {
		t.Errorf("hrv numbers = %v, want [41 52] from the wellness row", got)
	}
	if in.Indoor {
		t.Error("an outdoor session was recorded as kept indoors")
	}
	if !strings.HasPrefix(a.Text, "Eased one level — ") {
		t.Errorf("text = %q", a.Text)
	}
}

func TestKeepingASessionIndoorIsPartOfTheEasingRow(t *testing.T) {
	h := newTomorrowHarness(t)
	h.saveProfile(true)
	if err := h.store.SaveWellness(context.Background(), workout.DailyWellness{
		Rider: "wilant", Date: tmToday, HRVStatus: "UNBALANCED", HRVLastNight: 41, HRVWeeklyAvg: 52,
	}); err != nil {
		t.Fatal(err)
	}
	orig := tomorrowLongHard(h, tmToday)
	makeIndoor(t, h.store, orig.ID)

	h.srv.AdaptWorkouts(context.Background())

	a, ok := adjustmentFor(t, h.store, orig.ID)
	if !ok {
		t.Fatal("no adjustment recorded")
	}
	if !decodeInputs[why.ReadinessInputs](t, a).Indoor {
		t.Errorf("inputs = %v, want indoor recorded as part of the easing", a.Inputs)
	}
	facts := why.Facts(a.Rule, a.Inputs)
	if last := facts[len(facts)-1]; last.Label != "Indoor" {
		t.Errorf("facts = %v, want the indoor row", facts)
	}
	if n := adjustmentRows(t, h.autoScheduleHarness); n != 1 {
		t.Errorf("rows = %d: keeping it indoor must not be a second change", n)
	}
}

func TestAChangeThatCannotBeAppliedRecordsNothing(t *testing.T) {
	h := newTomorrowHarness(t)
	if err := h.store.SaveWellness(context.Background(), workout.DailyWellness{
		Rider: "wilant", Date: tmToday, HRVStatus: "UNBALANCED", HRVLastNight: 41, HRVWeeklyAvg: 52,
	}); err != nil {
		t.Fatal(err)
	}
	// A running sweet-spot session is a structured zone with no ladder for its
	// sport, so the step-down is chosen and then cannot be built.
	w, err := h.store.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: h.goalID, Sport: model.SportRunning, Name: "Sweet spot run",
		Date: tmToday, Description: scheduler.GeneratedDescription, Zone: workout.ZoneSweetSpot, Level: 5,
		Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600}},
	})
	if err != nil {
		t.Fatal(err)
	}

	h.srv.AdaptWorkouts(context.Background())

	if got, _ := h.store.GetWorkout(context.Background(), w.ID); strings.Contains(got.Description, scheduler.AdjustedMarker) {
		t.Fatalf("the change landed after all: %q", got.Description)
	}
	if n := adjustmentRows(t, h.autoScheduleHarness); n != 0 {
		t.Errorf("rows = %d, want none: nothing changed, so there is nothing to explain", n)
	}
}

func TestAFailedRecordWriteIsAWarningAndTheChangeStands(t *testing.T) {
	h := newTomorrowHarness(t)
	orig := h.cautionToday()
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := h.conn.Exec(`DROP TABLE adjustments`); err != nil {
		t.Fatal(err)
	}

	h.srv.AdaptWorkouts(context.Background())

	got, _ := h.store.GetWorkout(context.Background(), orig.ID)
	if !strings.Contains(got.Description, scheduler.AdjustedMarker) || got.Level != 4 {
		t.Errorf("the easing did not stand: level %v, %q", got.Level, got.Description)
	}
	var warned bool
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "adjustment") {
			warned = true
			if !strings.Contains(line, "rule=readiness_caution") {
				t.Errorf("warning %q does not name the rule", line)
			}
		}
	}
	if !warned {
		t.Errorf("no Warn about the failed record write in:\n%s", logs.String())
	}
}

// TestAdaptationLogsCarryNoHealthValues is the privacy rule: nothing the rider
// told a watch about their body sits next to their name in a log line. The
// reason text of a readiness change quotes HRV status and sleep, so the log
// carries the rule id instead.
func TestAdaptationLogsCarryNoHealthValues(t *testing.T) {
	h := newTomorrowHarness(t)
	if err := h.store.SaveWellness(context.Background(), workout.DailyWellness{
		Rider: "wilant", Date: tmToday, HRVStatus: "UNBALANCED", HRVLastNight: 41, HRVWeeklyAvg: 52,
		SleepScore: 53, SleepSeconds: 5*3600 + 20*60, ReadinessScore: 44, ReadinessLevel: "LOW", RestingHR: 61,
	}); err != nil {
		t.Fatal(err)
	}
	h.hard(tmToday)
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{} // timestamps carry digits of their own
			}
			return a
		},
	}))

	h.srv.AdaptWorkouts(context.Background())

	out := logs.String()
	if !strings.Contains(out, "workout adapted automatically") || !strings.Contains(out, "rule=readiness_caution") || !strings.Contains(out, "rider=wilant") {
		t.Fatalf("the adaptation log line is missing or has no rule id:\n%s", out)
	}
	for _, banned := range []string{"HRV", "unbalanced", "slept", "sleep", "score", "53", "44", "61", "41", "52", "reason=", "Eased one level"} {
		if strings.Contains(out, banned) {
			t.Errorf("log contains %q next to a rider:\n%s", banned, out)
		}
	}
}

func TestTomorrowsEasingRecordsTheForecastThatTheRiderConfirmed(t *testing.T) {
	h := newTomorrowHarness(t)
	h.saveProfile(false)
	orig := tomorrowLongHard(h, tmTomorrow)
	h.snapshot(40, 100)

	if status, out := h.ease("wilant", "cyclists", "?today="+tmToday); status != 200 {
		t.Fatalf("status = %d (%v)", status, out)
	}

	a, ok := adjustmentFor(t, h.store, orig.ID)
	if !ok || a.Rule != why.ReadinessTomorrow {
		t.Fatalf("adjustment = %+v (found %v), want readiness_tomorrow", a, ok)
	}
	in := decodeInputs[why.ReadinessInputs](t, a)
	if in.Verdict != "rest" || len(in.Signals) == 0 {
		t.Errorf("inputs = %+v, want the forecast's verdict and reasons", in)
	}
	for _, sig := range in.Signals {
		if sig.Label == "" || sig.Label == "Forecast" {
			t.Errorf("signal %+v has a generic label; each should say what it is about", sig)
		}
	}
	if in.Signals[0].Label != "Projected form" || in.Signals[0].Kind != "form" {
		t.Errorf("first signal = %+v, want the projected-form one", in.Signals[0])
	}
	if !strings.HasPrefix(a.Text, "Eased ahead of time — ") {
		t.Errorf("text = %q", a.Text)
	}
}
