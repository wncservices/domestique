package api_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// 2026-10-07 is a Wednesday. The week is cleared down to what each test plants,
// so nothing else in it is missed, made up or repaid.
const (
	reoptToday = "2026-10-07"
	reoptFri   = "2026-10-09"
	reoptSat   = "2026-10-10"
)

type reoptHarness struct {
	*ledgerPushHarness
	hard workout.Workout
	easy workout.Workout // on Friday
}

// newReoptHarness plants a hard tempo session today and a one-hour endurance
// ride on Friday, on a rider with a connected Garmin and auto-push on.
func newReoptHarness(t *testing.T) *reoptHarness {
	t.Helper()
	h := &reoptHarness{ledgerPushHarness: newLedgerPushHarness(t)}
	ctx := context.Background()
	for _, w := range h.thisWeek(t) {
		if err := h.store.DeleteWorkout(ctx, w.ID); err != nil {
			t.Fatal(err)
		}
	}
	// The week built on Monday afternoon was pushed to the fake account; what
	// the tests look for is only what the morning does.
	h.push.garmin.remoteWorkouts, h.push.garmin.calendar = nil, nil
	h.hard = plantTodaysTempo(t, h.ledgerHarness, reoptToday, 3, 4)
	h.easy = h.plantEndurance(t, reoptFri, 1)
	return h
}

func (h *reoptHarness) plantEndurance(t *testing.T, date string, hours float64) workout.Workout {
	t.Helper()
	profile, _, _ := h.store.GetProfile(context.Background(), "wilant")
	req := scheduler.BuildEnduranceSession(hours, false, model.SportCycling, profile)
	req.Rider, req.GoalID, req.Date = "wilant", h.goal.ID, date
	req.Description = scheduler.GeneratedDescription
	w, err := h.store.CreateWorkout(context.Background(), api.LegacyFlagged(req))
	if err != nil {
		t.Fatal(err)
	}
	h.backdate(t)
	return w
}

func (h *reoptHarness) get(t *testing.T, id string) workout.Workout {
	t.Helper()
	w, err := h.store.GetWorkout(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// cautionMorning gives today two amber signals: a Caution verdict.
func (h *reoptHarness) cautionMorning(t *testing.T) {
	t.Helper()
	w := garmin.Wellness{Date: reoptToday, HRVStatus: "UNBALANCED", HRVLastNight: 41, HRVWeeklyAvg: 52, ReadinessLevel: "LOW", ReadinessScore: 40}
	if h.push.garmin.wellnessByDate == nil {
		h.push.garmin.wellnessByDate = map[string]garmin.Wellness{}
	}
	h.push.garmin.wellnessByDate[reoptToday] = w
	if err := h.store.SaveWellness(context.Background(), workout.DailyWellness{
		Rider: "wilant", Date: reoptToday, HRVStatus: w.HRVStatus, HRVLastNight: w.HRVLastNight,
		HRVWeeklyAvg: w.HRVWeeklyAvg, ReadinessLevel: w.ReadinessLevel, ReadinessScore: w.ReadinessScore,
	}); err != nil {
		t.Fatal(err)
	}
}

func hasRule(w workout.Workout, r why.Rule) bool {
	for _, x := range w.Flags.AdaptedRules {
		if x == r {
			return true
		}
	}
	return false
}

func (h *reoptHarness) onDevice() []string {
	var out []string
	for _, n := range h.push.garmin.remoteWorkouts {
		out = append(out, n)
	}
	return out
}

func TestCautionMorningSwapsTodaysHardSessionWithALaterEasyDay(t *testing.T) {
	h := newReoptHarness(t)
	ctx := context.Background()
	h.cautionMorning(t)

	h.at(7, 7, 0)
	h.tick()

	hard, easy := h.get(t, h.hard.ID), h.get(t, h.easy.ID)
	if hard.Date != reoptFri || easy.Date != reoptToday {
		t.Fatalf("dates: hard %s, easy %s; want %s and %s", hard.Date, easy.Date, reoptFri, reoptToday)
	}
	if !hasRule(hard, why.ReshuffleReadiness) || !hasRule(easy, why.ReshuffleReadiness) {
		t.Errorf("rules: hard %v, easy %v", hard.Flags.AdaptedRules, easy.Flags.AdaptedRules)
	}
	if hard.Flags.MovedFrom != reoptToday || easy.Flags.MovedFrom != reoptFri || hard.Flags.MovedBy != workout.MovedByAuto {
		t.Errorf("moved flags: hard %+v, easy %+v", hard.Flags, easy.Flags)
	}
	if !strings.Contains(hard.Description, "moved from "+reoptToday+" — swapped with an easier day.") {
		t.Errorf("hard description %q carries no move note", hard.Description)
	}
	if !strings.Contains(easy.Description, "moved from "+reoptFri+" — swapped with a harder day.") {
		t.Errorf("easy description %q carries no move note", easy.Description)
	}
	// Readiness eased the ride that now sits on today, in the same tick.
	if !hasRule(easy, why.ReadinessCaution) || secs(easy) >= secs(h.easy) {
		t.Errorf("today's easy ride was not shortened: rules %v, %v s of %v s", easy.Flags.AdaptedRules, secs(easy), secs(h.easy))
	}
	// A swap locks nothing: the hard session can still be eased on its new day.
	if len(hard.Flags.EasedBy()) != 0 || !scheduler.IsGenerated(hard) {
		t.Errorf("the swap locked the hard session: %+v", hard.Flags)
	}
	adj, err := h.store.LatestAdjustments(ctx, "wilant", workout.SubjectWorkout, []string{hard.ID})
	if err != nil || adj[hard.ID].Rule != why.ReshuffleReadiness || why.Title(adj[hard.ID].Rule) != "Swapped with an easier day" {
		t.Errorf("hard session's reason: %v %+v", err, adj[hard.ID])
	}
	// The head unit holds today's easy ride and not the hard session.
	if got := h.onDevice(); len(got) != 1 || got[0] != easy.Name {
		t.Errorf("head unit holds %v, want only %q", got, easy.Name)
	}
}

func TestReoptimiseIsIdempotentAcrossTicks(t *testing.T) {
	h := newReoptHarness(t)
	h.cautionMorning(t)
	h.at(7, 7, 0)
	h.tick()
	hard, easy := h.get(t, h.hard.ID), h.get(t, h.easy.ID)
	rows := adjustmentRows(t, h.autoScheduleHarness)

	h.at(7, 7, 30)
	h.tick()
	h.tick()

	if again := h.get(t, h.hard.ID); again.Date != hard.Date || again.Description != hard.Description {
		t.Errorf("hard session changed on a later tick: %+v", again)
	}
	if again := h.get(t, h.easy.ID); again.Date != easy.Date || again.Description != easy.Description {
		t.Errorf("easy session changed on a later tick: %+v", again)
	}
	if n := adjustmentRows(t, h.autoScheduleHarness); n != rows {
		t.Errorf("adjustment rows = %d, want %d", n, rows)
	}
}

// A rider who checks in at 13:00 gets no swap, only the usual easing.
func TestReoptimiseOutsideTheMorningSlotDoesNothing(t *testing.T) {
	h := newReoptHarness(t)
	h.cautionMorning(t)

	h.at(7, 13, 0)
	h.tick()

	if hard := h.get(t, h.hard.ID); hard.Date != reoptToday || hasRule(hard, why.ReshuffleReadiness) {
		t.Errorf("hard session moved outside the morning slot: %s %v", hard.Date, hard.Flags.AdaptedRules)
	}
	if easy := h.get(t, h.easy.ID); easy.Date != reoptFri || hasRule(easy, why.ReshuffleReadiness) {
		t.Errorf("easy session moved outside the morning slot: %s %v", easy.Date, easy.Flags.AdaptedRules)
	}
}

func TestReadyMorningSwapsNothing(t *testing.T) {
	h := newReoptHarness(t)
	goodMorning(t, h.ledgerHarness, 7, h.push.garmin)

	h.at(7, 7, 0)
	h.tick()

	if hard := h.get(t, h.hard.ID); hard.Date != reoptToday || hasRule(hard, why.ReshuffleReadiness) {
		t.Errorf("hard session moved on a good morning: %s %v", hard.Date, hard.Flags.AdaptedRules)
	}
	if easy := h.get(t, h.easy.ID); easy.Date != reoptFri {
		t.Errorf("easy session moved on a good morning: %s", easy.Date)
	}
}

// A copy the rider sent by hand is never withdrawn, so moving the session it
// belongs to would strand it on the wrong day: the next candidate is used.
func TestAManuallyPushedSessionIsNeverMoved(t *testing.T) {
	h := newReoptHarness(t)
	sat := h.plantEndurance(t, reoptSat, 1)
	if err := h.store.SavePush(context.Background(), workout.Push{
		WorkoutID: h.easy.ID, Provider: "garmin", RemoteID: "mine", ContentHash: "h", Origin: workout.PushOriginManual,
	}); err != nil {
		t.Fatal(err)
	}
	h.cautionMorning(t)

	h.at(7, 7, 0)
	h.tick()

	if easy := h.get(t, h.easy.ID); easy.Date != reoptFri || hasRule(easy, why.ReshuffleReadiness) {
		t.Errorf("the manually pushed session moved: %s %v", easy.Date, easy.Flags.AdaptedRules)
	}
	if got := h.get(t, sat.ID); got.Date != reoptToday {
		t.Errorf("the next candidate did not take today: %s", got.Date)
	}
	if hard := h.get(t, h.hard.ID); hard.Date != reoptSat {
		t.Errorf("the hard session is on %s, want %s", hard.Date, reoptSat)
	}
}

// Only dates, notes and flags change: a swap never touches a session's content
// or whether it is ridden indoors.
func TestSwapKeepsEachSessionsIndoorState(t *testing.T) {
	h := newReoptHarness(t)
	ctx := context.Background()
	yes := true
	if _, err := h.store.UpdateWorkout(ctx, h.hard.ID, workout.UpdateWorkoutRequest{Indoor: &yes}); err != nil {
		t.Fatal(err)
	}
	h.backdate(t)
	hardBefore, easyBefore := h.get(t, h.hard.ID), h.get(t, h.easy.ID)
	if !hardBefore.Indoor || easyBefore.Indoor {
		t.Fatalf("setup: hard indoor %v, easy indoor %v", hardBefore.Indoor, easyBefore.Indoor)
	}
	h.cautionMorning(t)

	h.at(7, 7, 0)
	h.srv.ReoptimiseForTest(ctx)

	hard, easy := h.get(t, h.hard.ID), h.get(t, h.easy.ID)
	if hard.Date != reoptFri || easy.Date != reoptToday {
		t.Fatalf("setup: no swap: %s %s", hard.Date, easy.Date)
	}
	if !hard.Indoor || easy.Indoor {
		t.Errorf("indoor state changed: hard %v, easy %v", hard.Indoor, easy.Indoor)
	}
	for _, c := range []struct {
		name          string
		before, after workout.Workout
	}{{"hard", hardBefore, hard}, {"easy", easyBefore, easy}} {
		if c.after.Name != c.before.Name || c.after.Zone != c.before.Zone || c.after.Level != c.before.Level ||
			workout.ContentHash(c.after) != workout.ContentHash(c.before) {
			t.Errorf("%s session's content changed: %+v -> %+v", c.name, c.before, c.after)
		}
	}
}

// The hard session was already on the head unit from an earlier tick: the push
// pass withdraws the copy, now dated in the future, and sends today's session.
func TestAMovedAutoCopyIsWithdrawnAndTodayIsPushed(t *testing.T) {
	h := newReoptHarness(t)
	goodMorning(t, h.ledgerHarness, 7, h.push.garmin)
	h.at(7, 6, 45)
	h.tick()
	if got := h.onDevice(); len(got) != 1 || got[0] != h.hard.Name {
		t.Fatalf("setup: head unit holds %v, want the hard session %q", got, h.hard.Name)
	}

	h.cautionMorning(t)
	h.at(7, 7, 30)
	h.tick()

	easy := h.get(t, h.easy.ID)
	if easy.Date != reoptToday {
		t.Fatalf("setup: no swap, easy is on %s", easy.Date)
	}
	if got := h.onDevice(); len(got) != 1 || got[0] != easy.Name {
		t.Errorf("head unit holds %v, want only today's %q", got, easy.Name)
	}
	for entry, date := range h.push.garmin.calendar {
		if date != reoptToday {
			t.Errorf("calendar entry %s is on %s, want only today", entry, date)
		}
	}
	if push, have, _ := h.store.GetPush(context.Background(), h.hard.ID, "garmin"); have {
		t.Errorf("the moved hard session still has a push record: %+v", push)
	}
}

// If the second write fails the first is put back as it was, and no reason is
// left for a swap that did not happen.
func TestAFailedSecondWriteRestoresTheFirst(t *testing.T) {
	h := newReoptHarness(t)
	ctx := context.Background()
	before := h.get(t, h.hard.ID)
	h.cautionMorning(t)
	if _, err := h.conn.Exec(`CREATE TRIGGER reopt_fail BEFORE UPDATE ON workouts WHEN NEW.id = '` + h.easy.ID + `'
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}

	h.at(7, 7, 0)
	h.srv.ReoptimiseForTest(ctx)

	hard := h.get(t, h.hard.ID)
	if hard.Date != before.Date || hard.Description != before.Description || !hard.Flags.Equivalent(before.Flags) || hasRule(hard, why.ReshuffleReadiness) {
		t.Errorf("hard session was not restored:\nbefore %+v\nafter  %+v", before, hard)
	}
	if hard.Flags.MovedFrom != "" || hard.Flags.MovedBy != workout.MovedNone {
		t.Errorf("restored session still notes a move: %+v", hard.Flags)
	}
	if easy := h.get(t, h.easy.ID); easy.Date != reoptFri {
		t.Errorf("easy session moved: %s", easy.Date)
	}
	if n := adjustmentRows(t, h.autoScheduleHarness); n != 0 {
		t.Errorf("adjustment rows = %d, want 0", n)
	}
}

// reoptLines returns the log lines the pass wrote.
func reoptLines(t *testing.T, logs *bytes.Buffer) []string {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "reoptimise") {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		t.Fatalf("no reoptimise log line:\n%s", logs.String())
	}
	return lines
}

// Past the verdict check even a failure reveals the morning was a Caution or
// Rest one, so no line may carry the verdict, a reason or the rider.
func assertNoVerdictOrRider(t *testing.T, lines []string) {
	t.Helper()
	for _, l := range lines {
		lower := strings.ToLower(l)
		for _, banned := range []string{"caution", "rest", "ready", "verdict", "readiness", "wilant"} {
			if strings.Contains(lower, banned) {
				t.Errorf("log line %q carries %q", l, banned)
			}
		}
	}
}

func TestReoptimiseLogsCarryNoVerdict(t *testing.T) {
	h := newReoptHarness(t)
	logs := captureLogs(h.ledgerHarness)
	h.cautionMorning(t)

	h.at(7, 7, 0)
	h.tick()
	assertNoVerdictOrRider(t, reoptLines(t, logs))
}

// The Warn paths are the ones that name a rider by habit: a failed swap, a
// failed read.
func TestReoptimiseFailureLogsNameNoRider(t *testing.T) {
	h := newReoptHarness(t)
	logs := captureLogs(h.ledgerHarness)
	h.cautionMorning(t)
	if _, err := h.conn.Exec(`CREATE TRIGGER reopt_fail BEFORE UPDATE ON workouts WHEN NEW.id = '` + h.easy.ID + `'
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	h.at(7, 7, 0)
	h.srv.ReoptimiseForTest(context.Background())
	lines := reoptLines(t, logs)
	if !strings.Contains(strings.Join(lines, "\n"), "level=WARN") {
		t.Fatalf("expected a Warn line, got %v", lines)
	}
	assertNoVerdictOrRider(t, lines)
}

// The pass decides "done" as the adaptation pass does: a ride that matched the
// session but was cut short ("incomplete") is a miss however long it lasted,
// so the hard session is still there to swap.
func TestAnIncompleteRideDoesNotCountAsDone(t *testing.T) {
	h := newReoptHarness(t)
	ctx := context.Background()
	h.cautionMorning(t)
	sess, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "cut-short", Sport: "cycling",
		Date: reoptToday, DurationSeconds: 4 * 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: sess.ID, Rider: "wilant", WorkoutID: h.hard.ID, Outcome: "incomplete",
	}); err != nil {
		t.Fatal(err)
	}
	h.at(7, 7, 0)
	h.srv.ReoptimiseForTest(ctx)
	if got := h.get(t, h.hard.ID).Date; got != reoptFri {
		t.Fatalf("hard session on %s, want it swapped to %s", got, reoptFri)
	}
}
