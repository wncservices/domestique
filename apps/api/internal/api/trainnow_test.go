package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// All on the replan harness's fixed Wednesday 2026-03-25 (an explicit zone), so
// they pass under any TZ. The rider has an FTP of 250 and trains wed/fri/sat.

type tnSuggestion struct {
	Kind       string  `json:"kind"`
	Name       string  `json:"name"`
	Zone       string  `json:"zone"`
	Level      float64 `json:"level"`
	Minutes    int     `json:"minutes"`
	TSS        float64 `json:"tss"`
	Difficulty string  `json:"difficulty"`
	Why        string  `json:"why"`
	Warning    string  `json:"warning"`
}

type tnOut struct {
	Minutes     int            `json:"minutes"`
	Verdict     string         `json:"verdict"`
	Suggestions []tnSuggestion `json:"suggestions"`
}

func (o tnOut) get(kind string) (tnSuggestion, bool) {
	for _, s := range o.Suggestions {
		if s.Kind == kind {
			return s, true
		}
	}
	return tnSuggestion{}, false
}

func (o tnOut) kinds() string {
	var out []string
	for _, s := range o.Suggestions {
		out = append(out, s.Kind)
	}
	return strings.Join(out, ",")
}

// tnSetup gives wilant a profile (auto-push as asked), a goal and threshold 5.
func (h *pushHarness) tnSetup(autoPush bool) string {
	h.t.Helper()
	ctx := context.Background()
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", FTPWatts: 250, HoursPerAvailableDay: 1.5, AvailableDays: []string{"wed", "fri", "sat"},
		AutoPushWorkouts: autoPush,
	}); err != nil {
		h.t.Fatal(err)
	}
	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		h.t.Fatal(err)
	}
	h.altLevel("threshold", 5)
	return goal.ID
}

func (h *pushHarness) tnGet(user, groups, query string) (*http.Response, tnOut) {
	h.t.Helper()
	resp := h.as(user, groups, http.MethodGet, "/api/training/trainnow"+query, "")
	var out tnOut
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			h.t.Fatal(err)
		}
	}
	return resp, out
}

func (h *pushHarness) tnApply(user, groups string, minutes int, s tnSuggestion) (*http.Response, altWorkoutOut) {
	h.t.Helper()
	body := fmt.Sprintf(`{"minutes":%d,"kind":%q,"zone":%q,"level":%v}`, minutes, s.Kind, s.Zone, s.Level)
	return h.altDecode(h.as(user, groups, http.MethodPost, "/api/training/trainnow/apply", body))
}

func (h *pushHarness) tnWorkouts() []workout.Workout {
	h.t.Helper()
	all, err := h.training.ListWorkouts(context.Background(), "wilant")
	if err != nil {
		h.t.Fatal(err)
	}
	return all
}

// tnWeekZones is the structured zones the plan puts in this week, so a test
// can pick zones that will not collide with them.
func (h *pushHarness) tnWeekZones(goalID string) []string {
	h.t.Helper()
	ctx := context.Background()
	g, err := h.training.GetGoal(ctx, goalID)
	if err != nil {
		h.t.Fatal(err)
	}
	profile, _, _ := h.training.GetProfile(ctx, "wilant")
	now := replanClock()()
	plan, err := periodization.Build(g, profile, now)
	if err != nil {
		h.t.Fatal(err)
	}
	levels := map[string]float64{}
	saved, _ := h.training.ListLevels(ctx, "wilant")
	for _, l := range saved {
		levels[string(l.Zone)] = l.Level
	}
	for _, wk := range plan.Weeks {
		if wk.StartDate != replanMonday {
			continue
		}
		reqs, err := scheduler.WeekWorkouts(wk, profile, levels, "wilant", goalID, g.Sport)
		if err != nil {
			h.t.Fatal(err)
		}
		var zones []string
		for _, r := range reqs {
			if workout.IsStructuredZone(r.Zone) {
				zones = append(zones, string(r.Zone))
			}
		}
		return zones
	}
	return nil
}

func TestTrainNowRejectsMinutesOutsideTheRange(t *testing.T) {
	h := newReplanHarness(t)
	h.tnSetup(false)
	for _, q := range []string{"", "?minutes=", "?minutes=abc", "?minutes=29", "?minutes=181", "?minutes=-5", "?minutes=45.5"} {
		if resp, _ := h.tnGet("wilant", "cyclists", q); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET%s = %d, want 400", q, resp.StatusCode)
		}
	}
	for _, q := range []string{"?minutes=30", "?minutes=180"} {
		if resp, _ := h.tnGet("wilant", "cyclists", q); resp.StatusCode != http.StatusOK {
			t.Errorf("GET%s = %d, want 200", q, resp.StatusCode)
		}
	}
	for _, body := range []string{`{"minutes":29,"kind":"easy","zone":"endurance","level":0}`, `{"minutes":181,"kind":"easy","zone":"endurance","level":0}`, `nonsense`} {
		if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/trainnow/apply", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("apply %s = %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestTrainNowSuggestsPlannedThenEasyAllFittingAndWritesNothing(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.tnSetup(false)
	h.altRung(goal, indoorToday, "threshold", 4)
	before := h.tnWorkouts()

	resp, out := h.tnGet("wilant", "cyclists", "?minutes=90")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if out.Minutes != 90 || out.Verdict != "ready" {
		t.Errorf("context = %d min, verdict %q, want 90 and ready", out.Minutes, out.Verdict)
	}
	p, ok := out.get("planned")
	if !ok || out.Suggestions[0].Kind != "planned" || out.Suggestions[len(out.Suggestions)-1].Kind != "easy" {
		t.Fatalf("suggestions = %s, want planned first and easy last", out.kinds())
	}
	if p.Zone != "threshold" || p.Level != 5 || p.Difficulty != "Productive" || p.TSS <= 0 || p.Why == "" {
		t.Errorf("planned = %+v, want threshold at the rider's level 5, Productive, with TSS and a why", p)
	}
	for _, s := range out.Suggestions {
		if s.Minutes > 90 {
			t.Errorf("%s is %d min, over the 90 the rider has", s.Kind, s.Minutes)
		}
	}
	if e, _ := out.get("easy"); e.Minutes != 90 || e.Difficulty != "Recovery" {
		t.Errorf("easy = %+v, want 90 min, Recovery", e)
	}
	if after := h.tnWorkouts(); !reflect.DeepEqual(before, after) {
		t.Error("GET changed the rider's workouts")
	}
}

func TestTrainNowPlannedIsTheNextUndoneSessionWhenTodaysIsRiddenOrMissing(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.tnSetup(false)
	h.altRung(goal, replanFriday, "sweet_spot", 3)

	_, out := h.tnGet("wilant", "cyclists", "?minutes=90")
	p, ok := out.get("planned")
	if !ok || p.Zone != "sweet_spot" || !strings.Contains(strings.ToLower(p.Why), "next") {
		t.Fatalf("planned = %+v (%v), want Friday's sweet spot described as the next session", p, ok)
	}

	// Today's own session, once ridden, is not the base either.
	ridden := h.altRung(goal, indoorToday, "threshold", 4)
	if _, err := h.training.UpsertSession(context.Background(), workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ridden", Sport: "cycling", Date: indoorToday, DurationSeconds: 4200,
	}); err != nil {
		t.Fatal(err)
	}
	_ = ridden
	_, out = h.tnGet("wilant", "cyclists", "?minutes=90")
	if p, _ := out.get("planned"); p.Zone != "sweet_spot" {
		t.Errorf("planned = %+v, want Friday's session because today's is already ridden", p)
	}
}

func TestTrainNowATestIsNeverThePlannedBase(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.tnSetup(false)
	if _, err := h.training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: goal, Sport: model.SportCycling, Name: "FTP Test (ramp)", Date: indoorToday,
		Description: "A test.", TestProtocol: "ramp", Steps: altSteps(t, "threshold", 4),
	}); err != nil {
		t.Fatal(err)
	}
	_, out := h.tnGet("wilant", "cyclists", "?minutes=60")
	if _, ok := out.get("planned"); ok {
		t.Errorf("suggestions = %s, want no planned one built on an FTP test", out.kinds())
	}
}

func TestTrainNowRestGivesOnlyAnEasyRideCappedAtAnHour(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.tnSetup(false)
	h.altRung(goal, indoorToday, "threshold", 4)
	if err := h.training.SaveWellness(context.Background(), workout.DailyWellness{
		Rider: "wilant", Date: indoorToday, ReadinessLevel: "POOR", ReadinessScore: 10,
	}); err != nil {
		t.Fatal(err)
	}
	_, out := h.tnGet("wilant", "cyclists", "?minutes=120")
	if out.Verdict != "rest" || out.kinds() != "easy" || out.Suggestions[0].Minutes != 60 {
		t.Fatalf("verdict %q suggestions %s (%+v), want rest and one easy ride of 60 min", out.Verdict, out.kinds(), out.Suggestions)
	}
	if !strings.Contains(out.Suggestions[0].Why, "Garmin readiness is poor") {
		t.Errorf("why = %q, want the readiness reason", out.Suggestions[0].Why)
	}
}

func TestTrainNowCautionTakesOneRungDownAndDropsWanted(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.tnSetup(false)
	h.altRung(goal, indoorToday, "threshold", 4)
	if err := h.training.SaveWellness(context.Background(), workout.DailyWellness{
		Rider: "wilant", Date: indoorToday, ReadinessLevel: "LOW", ReadinessScore: 40,
	}); err != nil {
		t.Fatal(err)
	}
	_, out := h.tnGet("wilant", "cyclists", "?minutes=180")
	if out.Verdict != "caution" || out.kinds() != "planned,easy" {
		t.Fatalf("verdict %q suggestions %s, want caution and planned,easy", out.Verdict, out.kinds())
	}
	if p, _ := out.get("planned"); p.Level != 4 {
		t.Errorf("planned level = %v, want 4 (the rider's 5 minus one)", p.Level)
	}
}

func TestTrainNowAThirdHardDayDropsWantedAndWarnsOnPlanned(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.tnSetup(false)
	week := h.tnWeekZones(goal)
	var free []string
	for _, z := range []string{"tempo", "sweet_spot", "threshold", "vo2max", "anaerobic"} {
		if !slices.Contains(week, z) {
			free = append(free, z)
		}
	}
	if len(week) == 0 || len(free) < 3 {
		t.Fatalf("setup: the plan's week zones are %v", week)
	}
	h.altRung(goal, indoorToday, free[2], 4)

	_, out := h.tnGet("wilant", "cyclists", "?minutes=90")
	if w, ok := out.get("wanted"); !ok || w.Zone != week[0] {
		t.Fatalf("setup: wanted = %+v (%v), want the plan's zone %s", w, ok, week[0])
	}
	if p, _ := out.get("planned"); p.Warning != "" {
		t.Errorf("no hard days yet but planned warned: %q", p.Warning)
	}

	// Two hard days, ridden, in the last seven.
	for i, date := range []string{"2026-03-22", "2026-03-24"} {
		h.altRung(goal, date, free[i], 4)
		if _, err := h.training.UpsertSession(context.Background(), workout.UpsertSessionRequest{
			Rider: "wilant", Provider: "garmin", ExternalID: "hard-" + date, Sport: "cycling", Date: date, DurationSeconds: 4800,
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, out = h.tnGet("wilant", "cyclists", "?minutes=90")
	if _, ok := out.get("wanted"); ok {
		t.Errorf("suggestions = %s, want wanted dropped after two hard days", out.kinds())
	}
	if p, ok := out.get("planned"); !ok || p.Warning != "A third hard day in 7" {
		t.Errorf("planned = %+v (%v), want the warning", p, ok)
	}
}

func TestTrainNowWantedIsAPlanZoneNotDoneYet(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.tnSetup(false)
	week := h.tnWeekZones(goal)
	if len(week) == 0 {
		t.Fatalf("setup: no week zones")
	}
	_, out := h.tnGet("wilant", "cyclists", "?minutes=90")
	if w, ok := out.get("wanted"); !ok || w.Zone != week[0] {
		t.Fatalf("wanted = %+v (%v), want %s from the plan's week", w, ok, week[0])
	}

	// Done this week (Monday's session, ridden) means it is no longer wanted.
	h.altRung(goal, replanMonday, week[0], 3)
	if _, err := h.training.UpsertSession(context.Background(), workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "monday", Sport: "cycling", Date: replanMonday, DurationSeconds: 4200,
	}); err != nil {
		t.Fatal(err)
	}
	_, out = h.tnGet("wilant", "cyclists", "?minutes=90")
	if _, ok := out.get("wanted"); ok {
		t.Errorf("suggestions = %s, want no wanted once its zone was done this week", out.kinds())
	}
}

func TestTrainNowWithNoPlanGivesOnlyEasy(t *testing.T) {
	h := newReplanHarness(t)
	h.tnSetup(false)
	_, out := h.tnGet("someone-else", "cyclists", "?minutes=90")
	if out.kinds() != "easy" {
		t.Errorf("a rider with no goal got %s, want easy only", out.kinds())
	}
}

func TestTrainNowIsForRidersOnly(t *testing.T) {
	h := newReplanHarness(t)
	h.tnSetup(false)
	if resp, _ := h.tnGet("watcher", "viewers", "?minutes=60"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer's GET = %d, want 403", resp.StatusCode)
	}
	body := `{"minutes":60,"kind":"easy","zone":"endurance","level":0}`
	if resp := h.as("watcher", "viewers", http.MethodPost, "/api/training/trainnow/apply", body); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer's apply = %d, want 403", resp.StatusCode)
	}
}

func TestApplyingASuggestionAsAnotherRiderNeverTouchesTheOwnersWorkouts(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.tnSetup(false)
	h.altRung(goal, indoorToday, "threshold", 4)
	before := h.tnWorkouts()

	_, out := h.tnGet("someone-else", "cyclists", "?minutes=60")
	if resp, _ := h.tnApply("someone-else", "cyclists", 60, out.Suggestions[0]); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply = %d", resp.StatusCode)
	}
	if after := h.tnWorkouts(); !reflect.DeepEqual(before, after) {
		t.Error("another rider's apply changed wilant's workouts")
	}
}

func TestApplyReplacesTodaysUntouchedPlanSessionInPlaceAndRevertUndoesIt(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.tnSetup(false)
	orig := h.altRung(goal, indoorToday, "threshold", 4)
	levelsBefore, _ := h.training.ListLevels(context.Background(), "wilant")

	_, out := h.tnGet("wilant", "cyclists", "?minutes=75")
	p, _ := out.get("planned")
	resp, dto := h.tnApply("wilant", "cyclists", 75, p)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply = %d", resp.StatusCode)
	}
	if dto.ID != orig.ID || dto.Date != orig.Date || dto.GoalID != goal {
		t.Errorf("replaced = %+v, want the same id, date and goal", dto)
	}
	got := h.stored(orig.ID)
	if got.Name != p.Name || string(got.Zone) != p.Zone || got.Level != p.Level {
		t.Errorf("stored = %s/%s/%v, want the suggestion %+v", got.Name, got.Zone, got.Level, p)
	}
	if got.PlannedSnapshot == nil || got.PlannedSnapshot.Name != orig.Name || !reflect.DeepEqual(got.PlannedSnapshot.Steps, orig.Steps) {
		t.Errorf("snapshot = %+v, want the plan's version", got.PlannedSnapshot)
	}
	if !strings.Contains(got.Description, scheduler.SwappedMarker+" for a 75-minute day, was "+orig.Name+" (") {
		t.Errorf("description = %q, want the swap note for a 75-minute day", got.Description)
	}
	if scheduler.IsGenerated(got) {
		t.Error("a replaced session must be rider-touched")
	}
	if n := len(h.tnWorkouts()); n != 1 {
		t.Errorf("%d workouts, want the one, replaced in place", n)
	}
	if levelsAfter, _ := h.training.ListLevels(context.Background(), "wilant"); !reflect.DeepEqual(levelsBefore, levelsAfter) {
		t.Error("applying a suggestion moved a level")
	}

	if resp, _ := h.altRevert("wilant", "cyclists", orig.ID); resp.StatusCode != http.StatusOK {
		t.Fatalf("revert = %d", resp.StatusCode)
	}
	back := h.stored(orig.ID)
	if back.Name != orig.Name || back.Level != orig.Level || !reflect.DeepEqual(back.Steps, orig.Steps) || back.PlannedSnapshot != nil {
		t.Errorf("reverted = %+v, want the plan's version", back)
	}
}

func TestApplyAddsBesideEverythingThatIsNotAnUntouchedPlanSession(t *testing.T) {
	cases := map[string]func(h *pushHarness, goal string) workout.Workout{
		"a rider-built session": func(h *pushHarness, goal string) workout.Workout {
			w, err := h.training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
				Rider: "wilant", Sport: model.SportCycling, Name: "My ride", Date: indoorToday, Description: "mine",
				Steps: altSteps(t, "threshold", 4),
			})
			if err != nil {
				t.Fatal(err)
			}
			return w
		},
		"an adjusted session": func(h *pushHarness, goal string) workout.Workout {
			w := h.altRung(goal, indoorToday, "threshold", 4)
			desc := w.Description + " " + scheduler.AdjustedMarker + " eased."
			got, err := h.training.UpdateWorkout(context.Background(), w.ID, workout.UpdateWorkoutRequest{Description: &desc})
			if err != nil {
				t.Fatal(err)
			}
			return got
		},
		"a swapped session": func(h *pushHarness, goal string) workout.Workout {
			w := h.altRung(goal, indoorToday, "threshold", 4)
			if resp, _ := h.altSwap("wilant", "cyclists", w.ID, "easier"); resp.StatusCode != http.StatusOK {
				t.Fatalf("swap = %d", resp.StatusCode)
			}
			return h.stored(w.ID)
		},
		"an FTP test": func(h *pushHarness, goal string) workout.Workout {
			w, err := h.training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
				Rider: "wilant", GoalID: goal, Sport: model.SportCycling, Name: "FTP Test (ramp)", Date: indoorToday,
				Description: "A test.", TestProtocol: "ramp", Steps: altSteps(t, "threshold", 4),
			})
			if err != nil {
				t.Fatal(err)
			}
			return w
		},
		"nothing": func(h *pushHarness, goal string) workout.Workout { return workout.Workout{} },
	}
	for name, blocker := range cases {
		t.Run(name, func(t *testing.T) {
			h := newReplanHarness(t)
			goal := h.tnSetup(false)
			existing := blocker(h, goal)
			before := len(h.tnWorkouts())

			_, out := h.tnGet("wilant", "cyclists", "?minutes=90")
			easy, ok := out.get("easy")
			if !ok {
				t.Fatal("no easy suggestion")
			}
			resp, dto := h.tnApply("wilant", "cyclists", 90, easy)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("apply = %d", resp.StatusCode)
			}
			if existing.ID != "" && dto.ID == existing.ID {
				t.Fatalf("the existing session %s was overwritten", existing.ID)
			}
			if got := len(h.tnWorkouts()); got != before+1 {
				t.Errorf("%d workouts, want %d: one added", got, before+1)
			}
			added := h.stored(dto.ID)
			if added.Date != indoorToday || added.GoalID != goal || added.Description != "Chosen by you for a day with 90 minutes." ||
				added.Zone != workout.ZoneEndurance || workout.PlannedSeconds(added.Steps) != 90*60 {
				t.Errorf("added = %+v, want an endurance ride today on the focus goal with the Chosen-by-you note", added)
			}
			if scheduler.IsGenerated(added) || added.PlannedSnapshot != nil {
				t.Error("an added session must not look plan-made and has nothing to revert to")
			}
			if existing.ID != "" && !reflect.DeepEqual(h.stored(existing.ID), existing) {
				t.Errorf("the existing session changed: %+v -> %+v", existing, h.stored(existing.ID))
			}
		})
	}
}

func TestApplyAddsASecondRideOnADayThatAlreadyHasARiddenOne(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.tnSetup(false)
	ridden := h.altRung(goal, indoorToday, "threshold", 4)
	if _, err := h.training.UpsertSession(context.Background(), workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ridden", Sport: "cycling", Date: indoorToday, DurationSeconds: 4200,
	}); err != nil {
		t.Fatal(err)
	}
	_, out := h.tnGet("wilant", "cyclists", "?minutes=45")
	easy, _ := out.get("easy")
	if resp, dto := h.tnApply("wilant", "cyclists", 45, easy); resp.StatusCode != http.StatusOK || dto.ID == ridden.ID {
		t.Fatalf("apply = %d id=%s, want 200 and a new workout", resp.StatusCode, dto.ID)
	}
	if !reflect.DeepEqual(h.stored(ridden.ID), ridden) {
		t.Error("the ridden session was overwritten")
	}
}

func TestApplyWithAStaleSuggestionIs409AndChangesNothing(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.tnSetup(false)
	h.altRung(goal, indoorToday, "threshold", 4)
	before := h.tnWorkouts()

	for _, s := range []tnSuggestion{
		{Kind: "planned", Zone: "vo2max", Level: 9},     // the day moved on
		{Kind: "easy", Zone: "endurance", Level: 3},     // a level that is not endurance's
		{Kind: "nonsense", Zone: "endurance", Level: 0}, // not a suggestion at all
	} {
		if resp, _ := h.tnApply("wilant", "cyclists", 45, s); resp.StatusCode != http.StatusConflict {
			t.Errorf("stale %+v = %d, want 409", s, resp.StatusCode)
		}
	}
	if after := h.tnWorkouts(); !reflect.DeepEqual(before, after) {
		t.Error("a stale apply changed the workouts")
	}
}

func TestAnAddedSessionSurvivesReplan(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.tnSetup(false)
	_, out := h.tnGet("wilant", "cyclists", "?minutes=45")
	easy, _ := out.get("easy")
	_, dto := h.tnApply("wilant", "cyclists", 45, easy)
	added := h.stored(dto.ID)
	if added.GoalID != goal {
		t.Fatalf("setup: goal = %q", added.GoalID)
	}

	if resp, _ := h.replan("wilant"); resp.StatusCode != http.StatusOK {
		t.Fatalf("replan = %d", resp.StatusCode)
	}
	got, err := h.training.GetWorkout(context.Background(), dto.ID)
	if err != nil || !reflect.DeepEqual(got, added) {
		t.Errorf("replan changed or deleted the session the rider chose: %+v (err %v)", got, err)
	}
}

func TestApplyPushesTodaysCopyOnlyWhenAutoPushIsOnAndUpdatesAnExistingOne(t *testing.T) {
	t.Run("a new session is pushed with auto-push on", func(t *testing.T) {
		h := newReplanHarness(t)
		h.tnSetup(true)
		_, out := h.tnGet("wilant", "cyclists", "?minutes=45")
		easy, _ := out.get("easy")
		h.garmin.workoutCalls = nil
		if resp, _ := h.tnApply("wilant", "cyclists", 45, easy); resp.StatusCode != http.StatusOK {
			t.Fatalf("apply = %d", resp.StatusCode)
		}
		if got := h.calls(); got != "create garmin-workout-1; schedule garmin-workout-1 "+indoorToday {
			t.Errorf("calls = %q, want today's session created and scheduled", got)
		}
	})
	t.Run("nothing is pushed with auto-push off", func(t *testing.T) {
		h := newReplanHarness(t)
		h.tnSetup(false)
		_, out := h.tnGet("wilant", "cyclists", "?minutes=45")
		easy, _ := out.get("easy")
		h.garmin.workoutCalls = nil
		h.tnApply("wilant", "cyclists", 45, easy)
		if got := h.calls(); got != "" {
			t.Errorf("calls = %q, want none", got)
		}
	})
	t.Run("a replaced session that was already pushed updates its copy once", func(t *testing.T) {
		h := newReplanHarness(t)
		goal := h.tnSetup(true)
		orig := h.altRung(goal, indoorToday, "threshold", 4)
		h.push(orig.ID)
		h.garmin.workoutCalls = nil
		_, out := h.tnGet("wilant", "cyclists", "?minutes=75")
		p, _ := out.get("planned")
		if resp, dto := h.tnApply("wilant", "cyclists", 75, p); resp.StatusCode != http.StatusOK || dto.ID != orig.ID {
			t.Fatalf("apply = %d id=%s", resp.StatusCode, dto.ID)
		}
		if got := h.calls(); got != "update garmin-workout-1" {
			t.Errorf("calls = %q, want the one copy updated once", got)
		}
	})
}

func TestApplyLogsRiderWorkoutKindAndOutcomeOnly(t *testing.T) {
	h := newReplanHarness(t)
	h.tnSetup(false)
	if err := h.training.SaveWellness(context.Background(), workout.DailyWellness{
		Rider: "wilant", Date: indoorToday, ReadinessLevel: "LOW", ReadinessScore: 40,
	}); err != nil {
		t.Fatal(err)
	}
	var records []spyRecord
	h.srv.Log = slog.New(spyHandler{records: &records})

	_, out := h.tnGet("wilant", "cyclists", "?minutes=45")
	easy, _ := out.get("easy")
	if resp, _ := h.tnApply("wilant", "cyclists", 45, easy); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply = %d", resp.StatusCode)
	}
	found := false
	for _, r := range records {
		for k := range r.attrs {
			if k != "rider" && k != "workout" && k != "kind" && k != "outcome" && k != "err" {
				t.Errorf("log %q carries attribute %q; only rider, workout, kind, outcome and err may appear", r.msg, k)
			}
		}
		if r.msg == "trainnow applied" {
			found = true
			if r.attrs["rider"] != "wilant" || r.attrs["kind"] != "easy" || r.attrs["outcome"] != "added" || r.attrs["workout"] == "" {
				t.Errorf("apply log attrs = %v", r.attrs)
			}
		}
	}
	if !found {
		t.Error("no 'trainnow applied' log line")
	}
}
