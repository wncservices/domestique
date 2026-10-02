package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func (h *seasonHarness) refreshRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.conn.QueryRow(`SELECT COUNT(1) FROM adjustments WHERE rule = 'season_refresh'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// seasonBuiltAhead plans a season, backdates it, and returns the
// week ten weeks out, ready to be refreshed when the clock reaches the
// Wednesday before it.
func (h *seasonHarness) seasonBuiltAhead(t *testing.T, ftp float64, weeksAhead int) (time.Time, []workout.Workout) {
	t.Helper()
	ctx := context.Background()
	days := []string{"mon", "tue", "wed", "thu", "fri", "sat"}
	h.profile(t, days, ftp)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: "2027-04-14"}); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(ctx)
	h.backdate(t)
	thisMon, _ := weekBounds(h.now)
	target := thisMon.AddDate(0, 0, 7*weeksAhead)
	week := h.week(t, target)
	if len(week) == 0 {
		t.Fatal("no sessions in the target week")
	}
	return target, week
}

func TestARefreshedSessionRecordsWhyOnceAndOnlyWhenItChanged(t *testing.T) {
	h := newSeasonHarness(t)
	ctx := context.Background()
	target, week := h.seasonBuiltAhead(t, 0, 20)

	// A structured session, whose level is about to move.
	var subject workout.Workout
	for _, w := range week {
		if workout.IsStructuredZone(w.Zone) && w.Level > 0 {
			subject = w
			break
		}
	}
	if subject.ID == "" {
		t.Fatalf("no structured session in the target week: %+v", week)
	}
	edited := week[0]
	if edited.ID == subject.ID {
		edited = week[1]
	}
	newName := "My own name"
	if _, err := h.store.UpdateWorkout(ctx, edited.ID, workout.UpdateWorkoutRequest{Name: &newName}); err != nil {
		t.Fatal(err)
	}
	// FTP becomes known and the level rises, so the rebuilt session differs.
	h.profile(t, []string{"mon", "tue", "wed", "thu", "fri", "sat"}, 250)
	if err := h.store.SaveLevel(ctx, workout.ProgressionLevel{
		Rider: "wilant", Sport: subject.Sport, Zone: subject.Zone, Level: subject.Level + 1, Reason: "seeded",
	}); err != nil {
		t.Fatal(err)
	}
	h.now = utcNoon(target.Year(), target.Month(), target.Day()).AddDate(0, 0, -5)
	h.srv.AutoScheduleTick(ctx)

	got, _ := h.store.GetWorkout(ctx, subject.ID)
	if got.Level == subject.Level {
		t.Fatalf("the session was not rebuilt at a new level: %+v", got)
	}
	a, ok := adjustmentFor(t, h.store, subject.ID)
	if !ok || a.Rule != why.SeasonRefresh {
		t.Fatalf("adjustment = %+v (found %v), want season_refresh", a, ok)
	}
	in := decodeInputs[why.SeasonRefreshInputs](t, a)
	if in.LevelFrom != subject.Level || in.LevelTo != got.Level || in.NameFrom != subject.Name || in.NameTo != got.Name || in.FTPTo != 250 {
		t.Errorf("inputs = %+v", in)
	}
	if !strings.HasPrefix(a.Text, "Rebuilt ") || !strings.Contains(a.Text, "was ") {
		t.Errorf("text = %q, want the 'Rebuilt <day> ... was <old level>' sentence", a.Text)
	}
	if got.Description != scheduler.GeneratedDescription {
		t.Errorf("description = %q, want it left as the generated one", got.Description)
	}
	if _, ok := adjustmentFor(t, h.store, edited.ID); ok {
		t.Error("a session the rider edited has a refresh row")
	}
	rows := h.refreshRows(t)
	if rows == 0 {
		t.Fatal("no season_refresh rows")
	}

	// A later pass changes nothing and records nothing.
	h.now = h.now.Add(time.Hour)
	h.srv.AutoScheduleTick(ctx)
	if again := h.refreshRows(t); again != rows {
		t.Errorf("rows after a second pass = %d, want %d", again, rows)
	}
}

func TestARefreshThatChangesNothingRecordsNothing(t *testing.T) {
	h := newSeasonHarness(t)
	target, _ := h.seasonBuiltAhead(t, 250, 20)
	h.now = utcNoon(target.Year(), target.Month(), target.Day()).AddDate(0, 0, -5)
	h.srv.AutoScheduleTick(context.Background())
	if n := h.refreshRows(t); n != 0 {
		t.Errorf("rows = %d, want none: nothing about the sessions changed", n)
	}
}
