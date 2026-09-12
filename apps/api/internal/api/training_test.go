package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// trainingHarness mirrors crewHarness exactly — see that file's own comment
// for why training tests want their own small harness (distinct riders,
// distinct roles) rather than the big shared one in acceptance_test.go,
// which resolves every request to a single fixed identity.
type trainingHarness struct {
	t      *testing.T
	client *http.Client
	base   string
}

func newTrainingHarness(t *testing.T) *trainingHarness {
	t.Helper()

	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	trainingStore, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}

	authenticator, err := auth.New(auth.Config{
		Mode:  auth.ModeProxy,
		Roles: auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}, Viewer: []string{"guests"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := &api.Server{Auth: authenticator, Training: trainingStore}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	return &trainingHarness{t: t, client: server.Client(), base: server.URL}
}

func (h *trainingHarness) as(user, groups, method, path, body string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Remote-User", user)
	req.Header.Set("Remote-Groups", groups)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

type goalDTOOut struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Sport            string  `json:"sport"`
	EventDate        string  `json:"eventDate"`
	Priority         string  `json:"priority"`
	TargetDistanceM  float64 `json:"targetDistanceM"`
	TargetElevationM float64 `json:"targetElevationM"`
}

func decodeGoal(t *testing.T, resp *http.Response) goalDTOOut {
	t.Helper()
	var out goalDTOOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCreateGoalUsesTheSessionRiderNotTheBody(t *testing.T) {
	h := newTrainingHarness(t)

	// A body-supplied "rider" field, if there were one, would be ignored —
	// there isn't one in the request DTO at all, so this proves it the same
	// way handleUpload ignores a client-supplied uploadedBy: only "wilant"
	// (the session) ever sees this goal back.
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals",
		`{"name":"Local Gran Fondo","sport":"cycling","eventDate":"2026-06-01","priority":"A","targetDistanceM":180000}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	g := decodeGoal(t, resp)
	if g.ID != "local-gran-fondo" || g.Priority != "A" || g.TargetDistanceM != 180000 {
		t.Errorf("goal = %+v", g)
	}

	// Visible to the rider who created it...
	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/goals", "")
	var mine []goalDTOOut
	if err := json.NewDecoder(resp.Body).Decode(&mine); err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].ID != g.ID {
		t.Errorf("wilant's goals = %+v", mine)
	}

	// ...but invisible to anyone else, even another rider-role identity.
	resp = h.as("other", "cyclists", http.MethodGet, "/api/training/goals", "")
	var theirs []goalDTOOut
	if err := json.NewDecoder(resp.Body).Decode(&theirs); err != nil {
		t.Fatal(err)
	}
	if len(theirs) != 0 {
		t.Errorf("other's goals = %+v, want none", theirs)
	}
}

func TestViewerRoleCannotReachTraining(t *testing.T) {
	h := newTrainingHarness(t)
	resp := h.as("guest", "guests", http.MethodGet, "/api/training/goals", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestOnlyTheOwningRiderMayEditOrDeleteAGoal(t *testing.T) {
	h := newTrainingHarness(t)
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"Race Day"}`)
	g := decodeGoal(t, resp)

	// A different rider, even with the rider role, may not touch it — and
	// deliberately no admin override either: see isOwnTraining's own doc
	// comment for why training data does not inherit routes:edit-any.
	resp = h.as("someone-else", "cyclists", http.MethodPatch, "/api/training/goals/"+g.ID, `{"name":"Hijacked"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("edit by a different rider: status = %d, want 403", resp.StatusCode)
	}
	resp = h.as("someone-else", "admins", http.MethodDelete, "/api/training/goals/"+g.ID, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("delete by an admin who does not own it: status = %d, want 403", resp.StatusCode)
	}

	resp = h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+g.ID, `{"name":"Renamed"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edit by the owner: status = %d, want 200", resp.StatusCode)
	}
	if got := decodeGoal(t, resp); got.Name != "Renamed" {
		t.Errorf("name = %q", got.Name)
	}

	resp = h.as("wilant", "cyclists", http.MethodDelete, "/api/training/goals/"+g.ID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete by the owner: status = %d, want 200", resp.StatusCode)
	}
}

func TestRiderProfileGetBeforeSaveIsAnEmptyDTONotAnError(t *testing.T) {
	h := newTrainingHarness(t)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/profile", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var empty struct {
		FTPWatts float64 `json:"ftpWatts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&empty); err != nil {
		t.Fatal(err)
	}
	if empty.FTPWatts != 0 {
		t.Errorf("ftpWatts = %v, want 0 (unset)", empty.FTPWatts)
	}

	resp = h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile",
		`{"ftpWatts":280,"maxHr":185,"availableDays":["tue","thu","sat","sun"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save: status = %d, want 200", resp.StatusCode)
	}

	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/profile", "")
	var saved struct {
		FTPWatts      float64  `json:"ftpWatts"`
		MaxHR         int      `json:"maxHr"`
		AvailableDays []string `json:"availableDays"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	if saved.FTPWatts != 280 || saved.MaxHR != 185 || len(saved.AvailableDays) != 4 {
		t.Errorf("saved profile = %+v", saved)
	}

	// A different rider's profile is untouched and still empty.
	resp = h.as("other", "cyclists", http.MethodGet, "/api/training/profile", "")
	var others struct {
		FTPWatts float64 `json:"ftpWatts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&others); err != nil {
		t.Fatal(err)
	}
	if others.FTPWatts != 0 {
		t.Errorf("other rider's ftpWatts = %v, want 0", others.FTPWatts)
	}
}

const exampleWorkoutBody = `{
	"sport": "cycling",
	"name": "Threshold 6x3",
	"date": "2026-03-02",
	"steps": [
		{"name": "Warmup", "intensity": "warmup", "duration": "time", "seconds": 600, "target": "open"},
		{"name": "Intervals", "repeat": 6, "steps": [
			{"name": "On", "intensity": "interval", "duration": "time", "seconds": 180, "target": "power", "targetLow": 280, "targetHigh": 300},
			{"name": "Off", "intensity": "recovery", "duration": "time", "seconds": 120, "target": "power", "targetLow": 100, "targetHigh": 150}
		]},
		{"name": "Cooldown", "intensity": "cooldown", "duration": "open", "target": "open"}
	]
}`

type workoutDTOOut struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Date  string `json:"date"`
	Steps []struct {
		Name   string `json:"name"`
		Repeat int    `json:"repeat"`
		Steps  []struct {
			Name      string  `json:"name"`
			TargetLow float64 `json:"targetLow"`
		} `json:"steps"`
	} `json:"steps"`
}

func TestCreateWorkoutRoundTripsNestedRepeatSteps(t *testing.T) {
	h := newTrainingHarness(t)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts", exampleWorkoutBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var w workoutDTOOut
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		t.Fatal(err)
	}
	if w.ID != "threshold-6x3" || len(w.Steps) != 3 {
		t.Fatalf("workout = %+v", w)
	}
	if w.Steps[1].Repeat != 6 || len(w.Steps[1].Steps) != 2 {
		t.Fatalf("repeat block = %+v", w.Steps[1])
	}
	if w.Steps[1].Steps[0].TargetLow != 280 {
		t.Errorf("nested target = %+v", w.Steps[1].Steps[0])
	}

	// Read back via GET one, not just the create response.
	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+w.ID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status = %d", resp.StatusCode)
	}
	var fetched workoutDTOOut
	if err := json.NewDecoder(resp.Body).Decode(&fetched); err != nil {
		t.Fatal(err)
	}
	if len(fetched.Steps) != 3 {
		t.Errorf("fetched steps = %+v", fetched.Steps)
	}

	// A different rider cannot see or touch it.
	resp = h.as("other", "cyclists", http.MethodGet, "/api/training/workouts/"+w.ID, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("other rider get: status = %d, want 403", resp.StatusCode)
	}
	resp = h.as("other", "cyclists", http.MethodDelete, "/api/training/workouts/"+w.ID, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("other rider delete: status = %d, want 403", resp.StatusCode)
	}
}

func TestDownloadWorkoutFIT(t *testing.T) {
	h := newTrainingHarness(t)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts", exampleWorkoutBody)
	w := decodeWorkoutOut(t, resp)

	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+w.ID+"/fit", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.ant.fit" {
		t.Errorf("content-type = %q", ct)
	}
	body := readAll(t, resp)
	if len(body) < 14 || string(body[8:12]) != ".FIT" {
		t.Errorf("body does not look like a FIT file: %d bytes", len(body))
	}

	// A different rider cannot download it either.
	resp = h.as("other", "cyclists", http.MethodGet, "/api/training/workouts/"+w.ID+"/fit", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("other rider fit download: status = %d, want 403", resp.StatusCode)
	}
}

func TestCreateWorkoutRejectsAnInvalidStep(t *testing.T) {
	h := newTrainingHarness(t)
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts",
		`{"name":"Bad","steps":[{"name":"Intervals","repeat":4}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an empty repeat block", resp.StatusCode)
	}
}

func decodeWorkoutOut(t *testing.T, resp *http.Response) workoutDTOOut {
	t.Helper()
	var w workoutDTOOut
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		t.Fatal(err)
	}
	return w
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestGoalPeriodization(t *testing.T) {
	h := newTrainingHarness(t)

	eventDate := time.Now().AddDate(0, 0, 70).Format("2006-01-02") // 10 weeks out
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals",
		fmt.Sprintf(`{"name":"Race Day","eventDate":%q}`, eventDate))
	g := decodeGoal(t, resp)

	resp = h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile",
		`{"hoursPerAvailableDay":1.5,"availableDays":["tue","thu","sat","sun"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save profile: status = %d", resp.StatusCode)
	}

	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/goals/"+g.ID+"/periodization", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var plan struct {
		GoalID string `json:"goalId"`
		Weeks  []struct {
			Number      int     `json:"number"`
			Phase       string  `json:"phase"`
			TargetHours float64 `json:"targetHours"`
		} `json:"weeks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	if plan.GoalID != g.ID || len(plan.Weeks) == 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if last := plan.Weeks[len(plan.Weeks)-1]; last.Phase != "taper" {
		t.Errorf("last phase = %q, want taper", last.Phase)
	}

	// A different rider cannot see this goal's plan either.
	resp = h.as("other", "cyclists", http.MethodGet, "/api/training/goals/"+g.ID+"/periodization", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("other rider: status = %d, want 403", resp.StatusCode)
	}
}

func TestGoalPeriodizationRejectsAGoalWithNoEventDate(t *testing.T) {
	h := newTrainingHarness(t)
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"No Date"}`)
	g := decodeGoal(t, resp)

	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/goals/"+g.ID+"/periodization", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
