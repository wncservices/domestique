package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// moveNextFixture pins the clock to Wednesday 2026-09-30 (week ends Sunday
// 2026-10-04), gives wilant a goal and the listed available days, and returns
// a goal-linked generated-style session on today.
type moveNextFixture struct {
	h      *trainingHarness
	goalID string
	today  workout.Workout
}

func newMoveNextFixture(t *testing.T, days ...string) *moveNextFixture {
	t.Helper()
	h := newTrainingHarness(t)
	h.pinClock()
	createGoal(t, h, "wilant", "Gran Fondo", "2027-05-01", "A")
	goals, err := h.store.ListGoals(context.Background(), "wilant")
	if err != nil || len(goals) != 1 {
		t.Fatalf("goals = %v, %v", goals, err)
	}
	if _, err := h.store.SaveProfile(context.Background(), workout.RiderProfile{Rider: "wilant", AvailableDays: days, HoursPerAvailableDay: 1}); err != nil {
		t.Fatal(err)
	}
	f := &moveNextFixture{h: h, goalID: goals[0].ID}
	f.today = f.add("Tempo", "2026-09-30", workout.Flags{Origin: workout.OriginRider})
	return f
}

func (f *moveNextFixture) add(name, date string, flags workout.Flags) workout.Workout {
	f.h.t.Helper()
	wk, err := f.h.store.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: name, GoalID: f.goalID, Date: date,
		Steps: []workout.WorkoutStep{{Name: "ride", Duration: "time", Seconds: 3600, Target: "open"}},
		Flags: flags,
	})
	if err != nil {
		f.h.t.Fatal(err)
	}
	return wk
}

func (f *moveNextFixture) move(user string, id string) *http.Response {
	return f.h.as(user, "cyclists", http.MethodPost, "/api/training/workouts/"+id+"/move-next", "{}")
}

func (f *moveNextFixture) mustRefuse(id, wantContains string) {
	f.h.t.Helper()
	resp := f.move("wilant", id)
	if resp.StatusCode != http.StatusConflict {
		f.h.t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		f.h.t.Fatal(err)
	}
	if !strings.Contains(body.Error, wantContains) {
		f.h.t.Errorf("error = %q, want it to contain %q", body.Error, wantContains)
	}
	got, err := f.h.store.GetWorkout(context.Background(), id)
	if err != nil {
		f.h.t.Fatal(err)
	}
	if got.Date != "2026-09-30" {
		f.h.t.Errorf("a refused move changed the date to %s", got.Date)
	}
}

func TestMoveNextGoesToTheNextAvailableFreeDay(t *testing.T) {
	// Thursday is taken, Friday is not a riding day, Saturday is free.
	f := newMoveNextFixture(t, "wed", "thu", "sat")
	f.add("Other", "2026-10-01", workout.Flags{Origin: workout.OriginRider})

	resp := f.move("wilant", f.today.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Workout struct {
			ID   string `json:"id"`
			Date string `json:"date"`
		} `json:"workout"`
		From string `json:"from"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Workout.Date != "2026-10-03" || out.From != "2026-09-30" || out.Workout.ID != f.today.ID {
		t.Errorf("response = %+v, want moved to 2026-10-03 from 2026-09-30", out)
	}
}

func TestMoveNextRecordsTheMoveAsTheRiders(t *testing.T) {
	f := newMoveNextFixture(t, "thu")
	if resp := f.move("wilant", f.today.ID); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got, err := f.h.store.GetWorkout(context.Background(), f.today.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Description, "moved from 2026-09-30") {
		t.Errorf("description = %q, want the move noted", got.Description)
	}
	fl := workout.CurrentFlags(got)
	if fl.MovedFrom != "2026-09-30" || fl.MovedBy != workout.MovedByRider {
		t.Errorf("flags moved = %q by %q, want 2026-09-30 by rider", fl.MovedFrom, fl.MovedBy)
	}
}

func TestMoveNextSkipsLifeEventDays(t *testing.T) {
	f := newMoveNextFixture(t, "thu", "fri", "sat")
	if _, err := f.h.store.CreateLifeEvent(context.Background(), workout.LifeEvent{
		Rider: "wilant", Kind: "busy", Start: "2026-10-01", End: "2026-10-02",
	}); err != nil {
		t.Fatal(err)
	}
	resp := f.move("wilant", f.today.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got, _ := f.h.store.GetWorkout(context.Background(), f.today.ID)
	if got.Date != "2026-10-03" {
		t.Errorf("date = %s, want 2026-10-03 (the event covers Thursday and Friday)", got.Date)
	}
}

func TestMoveNextOnTheLastFreeDayOfTheWeekChangesNothing(t *testing.T) {
	// Only Sunday is available after today, and it is taken.
	f := newMoveNextFixture(t, "wed", "sun")
	f.add("Sunday ride", "2026-10-04", workout.Flags{Origin: workout.OriginRider})
	f.mustRefuse(f.today.ID, "No free day left this week")
}

func TestMoveNextRefusals(t *testing.T) {
	t.Run("crew ride", func(t *testing.T) {
		f := newMoveNextFixture(t, "thu")
		wk, err := f.h.store.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
			Rider: "wilant", Sport: model.SportCycling, Name: "Crew", Date: "2026-09-30", CrewRideID: "ride-1",
			Flags: workout.Flags{Origin: workout.OriginCrew},
			Steps: []workout.WorkoutStep{{Name: "ride", Duration: "time", Seconds: 3600, Target: "open"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		f.mustRefuse(wk.ID, "crew")
	})
	t.Run("ftp test", func(t *testing.T) {
		f := newMoveNextFixture(t, "thu")
		wk, err := f.h.store.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
			Rider: "wilant", Sport: model.SportCycling, Name: "Test", Date: "2026-09-30", TestProtocol: "ramp",
			Flags: workout.Flags{Origin: workout.OriginTest},
			Steps: []workout.WorkoutStep{{Name: "ride", Duration: "time", Seconds: 3600, Target: "open"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		f.mustRefuse(wk.ID, "FTP test")
	})
	t.Run("already ridden", func(t *testing.T) {
		f := newMoveNextFixture(t, "thu")
		f.h.seedSession("wilant", "2026-09-30", 1)
		f.mustRefuse(f.today.ID, "already ridden")
	})
	t.Run("not today", func(t *testing.T) {
		f := newMoveNextFixture(t, "fri")
		other := f.add("Tomorrow", "2026-10-01", workout.Flags{Origin: workout.OriginRider})
		resp := f.move("wilant", other.ID)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}
		got, _ := f.h.store.GetWorkout(context.Background(), other.ID)
		if got.Date != "2026-10-01" {
			t.Errorf("date = %s, want unchanged", got.Date)
		}
	})
}

func TestMoveNextOnSundayHasNoDayLeft(t *testing.T) {
	// Today is Sunday 2026-10-04, so nothing after it falls in this week
	// (weekEnd is today), however many days the rider trains.
	f := newMoveNextFixture(t, "wed", "thu", "sat")
	sunday := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	f.h.srv.Clock = func() time.Time { return sunday }
	today := f.add("Sunday ride", "2026-10-04", workout.Flags{Origin: workout.OriginRider})

	resp := f.move("wilant", today.ID)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Error, "No free day left this week") {
		t.Errorf("error = %q, want the no-free-day message", body.Error)
	}
	got, err := f.h.store.GetWorkout(context.Background(), today.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Date != "2026-10-04" {
		t.Errorf("a refused move changed the date to %s", got.Date)
	}
}

func TestMoveNextRefusesARideHandLinkedToToday(t *testing.T) {
	f := newMoveNextFixture(t, "thu")
	ctx := context.Background()
	sess, err := f.h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "hand-linked", Sport: "cycling",
		Date: "2026-09-30", DurationSeconds: 1800,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Hand-linked to today's workout: the link, not the automatic match
	// (a 30-minute ride against a 60-minute plan), makes it ridden.
	if err := f.h.store.SetSessionLink(ctx, "wilant", sess.ID, f.today.ID); err != nil {
		t.Fatal(err)
	}
	f.mustRefuse(f.today.ID, "already ridden")
}

func TestMoveNextAllowedWhenTheRideIsLinkedAsNotPlanned(t *testing.T) {
	f := newMoveNextFixture(t, "thu")
	ctx := context.Background()
	sess, err := f.h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "not-planned", Sport: "cycling",
		Date: "2026-09-30", DurationSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	// An empty link says "not a planned session": it must not count as
	// ridden today's workout, even though the duration would match it.
	if err := f.h.store.SetSessionLink(ctx, "wilant", sess.ID, ""); err != nil {
		t.Fatal(err)
	}
	resp := f.move("wilant", f.today.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got, err := f.h.store.GetWorkout(ctx, f.today.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Date != "2026-10-01" {
		t.Errorf("date = %s, want 2026-10-01", got.Date)
	}
}

func TestMoveNextIsOwnerOnly(t *testing.T) {
	f := newMoveNextFixture(t, "thu")
	if resp := f.move("someone-else", f.today.ID); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another rider's workout: status = %d, want 404", resp.StatusCode)
	}
	if resp := f.move("wilant", "no-such-id"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id: status = %d, want 404", resp.StatusCode)
	}
	got, _ := f.h.store.GetWorkout(context.Background(), f.today.ID)
	if got.Date != "2026-09-30" {
		t.Errorf("date = %s, want unchanged", got.Date)
	}
}
