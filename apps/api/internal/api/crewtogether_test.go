package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// proposalOut is one element of GET /api/training/ride-together.
type proposalOut struct {
	ID         string `json:"id"`
	CrewID     string `json:"crewId"`
	CrewName   string `json:"crewName"`
	WeekStart  string `json:"weekStart"`
	Day        string `json:"day"`
	RouteSlug  string `json:"routeSlug"`
	RouteName  string `json:"routeName"`
	Status     string `json:"status"`
	YourStatus string `json:"yourStatus"`
	Members    []struct {
		Rider  string `json:"rider"`
		Status string `json:"status"`
	} `json:"members"`
}

// togetherSetup is a crew of wilant, sam and alex on Wednesday 2026-10-07. wilant
// has a 3 h generated long ride on Sunday, sam on Saturday, alex none. Hill Loop
// (4.3 h) is too long to share; Medium Loop (3 h) fits.
type togetherSetup struct {
	cpSetup
	medium       string
	wilant, sam  workout.Workout
	wGoal, sGoal workout.Goal
}

func (h *crewPlanHarness) togetherSetup() togetherSetup {
	h.t.Helper()
	ts := togetherSetup{cpSetup: h.setup()}
	medium := h.seedRoute("Medium Loop", "wilant", 70, 600, ts.crewID)
	ts.medium = medium.Slug
	ts.wGoal, ts.sGoal = h.planFor("wilant"), h.planFor("sam")
	ctx := context.Background()
	mk := func(rider string, goal workout.Goal, date string) workout.Workout {
		req := genZone(rider, goal.ID, "Long ride", date, workout.ZoneEndurance, 3*3600)
		w, err := h.training.CreateWorkout(ctx, req)
		if err != nil {
			h.t.Fatal(err)
		}
		return w
	}
	ts.wilant, ts.sam = mk("wilant", ts.wGoal, cpSunday), mk("sam", ts.sGoal, cpSaturday)
	return ts
}

func (h *crewPlanHarness) optIn(rider, crewID string, days ...string) {
	h.t.Helper()
	b, _ := json.Marshal(map[string]any{"days": days})
	resp := h.as(rider, "cyclists", http.MethodPut, "/api/crews/"+crewID+"/together", string(b))
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("%s opting in: status = %d", rider, resp.StatusCode)
	}
}

func (h *crewPlanHarness) proposals(rider string) []proposalOut {
	h.t.Helper()
	resp := h.as(rider, "cyclists", http.MethodGet, "/api/training/ride-together", "")
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("ride-together: status = %d", resp.StatusCode)
	}
	var out struct {
		Proposals []proposalOut `json:"proposals"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	return out.Proposals
}

func TestOnlyAnApprovedMemberSetsTheirOwnTogetherDays(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	put := func(rider, body string) *http.Response {
		return h.as(rider, "cyclists", http.MethodPut, "/api/crews/"+s.crewID+"/together", body)
	}

	if resp := put("outsider", `{"days":["sat"]}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a non-member: status = %d, want 403", resp.StatusCode)
	}
	if resp := put("wilant", `{"days":["saturday"]}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a bad weekday: status = %d, want 400", resp.StatusCode)
	}
	if resp := put("wilant", `{`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a bad body: status = %d, want 400", resp.StatusCode)
	}
	if resp := h.as("wilant", "guests", http.MethodPut, "/api/crews/"+s.crewID+"/together", `{"days":["sat"]}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer: status = %d, want 403", resp.StatusCode)
	}
	if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/crews/nope/together", `{"days":["sat"]}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown crew: status = %d, want 404", resp.StatusCode)
	}

	// A body naming another rider is ignored: the row is the session's.
	if resp := put("wilant", `{"days":["sun","sat"],"rider":"sam"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	got, _ := h.sched.TogetherFor(context.Background(), s.crewID)
	if len(got) != 1 || strings.Join(got["wilant"], ",") != "sat,sun" {
		t.Errorf("stored = %v, want only wilant's own row, sorted", got)
	}

	// Empty deletes it.
	put("wilant", `{"days":[]}`)
	if got, _ := h.sched.TogetherFor(context.Background(), s.crewID); len(got) != 0 {
		t.Errorf("stored = %v, want opted out", got)
	}
}

func TestMembersSeeEachOthersTogetherDaysAndNobodyElseDoes(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	h.seedPrivateData("wilant")
	h.optIn("wilant", s.crewID, "sat", "sun")

	type crewOut struct {
		ID       string `json:"id"`
		Together []struct {
			Rider string   `json:"rider"`
			Days  []string `json:"days"`
		} `json:"together"`
	}
	read := func(rider string) (crewOut, string) {
		resp := h.as(rider, "cyclists", http.MethodGet, "/api/crews", "")
		body := bodyOf(t, resp)
		var all []crewOut
		if err := json.Unmarshal([]byte(body), &all); err != nil {
			t.Fatal(err)
		}
		for _, c := range all {
			if c.ID == s.crewID {
				return c, body
			}
		}
		t.Fatalf("%s sees no crew", rider)
		return crewOut{}, ""
	}

	sam, body := read("sam")
	if len(sam.Together) != 1 || sam.Together[0].Rider != "wilant" || strings.Join(sam.Together[0].Days, ",") != "sat,sun" {
		t.Errorf("sam sees %+v, want wilant open Saturday and Sunday", sam.Together)
	}
	assertNothingPrivate(t, "GET crews", body)
	if outsider, _ := read("outsider"); len(outsider.Together) != 0 {
		t.Errorf("a non-member sees %+v, want no flags", outsider.Together)
	}
}

func TestAProposalIsComputedOnReadAndBothRidersSeeTheSameOne(t *testing.T) {
	h := newCrewPlanHarness(t)
	ts := h.togetherSetup()
	h.seedPrivateData("wilant")
	h.optIn("wilant", ts.crewID, "sat", "sun")
	h.optIn("sam", ts.crewID, "sat", "sun")
	before := h.stored("sam")

	mine := h.proposals("wilant")
	if len(mine) != 1 {
		t.Fatalf("proposals = %+v, want one", mine)
	}
	p := mine[0]
	if p.Day != cpSaturday || p.RouteSlug != ts.medium || p.RouteName != "Medium Loop" || p.WeekStart != cpMonday || p.CrewName != "Sunday Club" {
		t.Errorf("proposal = %+v, want Saturday on the Medium Loop", p)
	}
	if p.Status != "open" || p.YourStatus != "pending" || len(p.Members) != 2 {
		t.Errorf("proposal = %+v, want open with two pending members", p)
	}
	theirs := h.proposals("sam")
	if len(theirs) != 1 || theirs[0].ID != p.ID || theirs[0].YourStatus != "pending" {
		t.Errorf("sam's proposals = %+v, want the same one (%s)", theirs, p.ID)
	}
	if again := h.proposals("wilant"); len(again) != 1 || again[0].ID != p.ID {
		t.Errorf("a second read made another proposal: %+v", again)
	}

	// Reading plans to compute it writes nothing about anyone.
	after := h.stored("sam")
	if len(after) != len(before) || after[0].UpdatedAt != before[0].UpdatedAt || after[0].Date != before[0].Date {
		t.Error("reading the proposal changed sam's plan")
	}
}

// Only the documented fields cross: week, day, route, riders and their status.
func TestAProposalCarriesNothingOfAnyRidersPlan(t *testing.T) {
	h := newCrewPlanHarness(t)
	ts := h.togetherSetup()
	h.seedPrivateData("wilant")
	h.seedPrivateData("sam")
	h.optIn("wilant", ts.crewID, "sat", "sun")
	h.optIn("sam", ts.crewID, "sat", "sun")

	for _, rider := range []string{"wilant", "sam"} {
		resp := h.as(rider, "cyclists", http.MethodGet, "/api/training/ride-together", "")
		body := bodyOf(t, resp)
		assertNothingPrivate(t, "ride-together for "+rider, body)
		assertOnlyKeys(t, "ride-together for "+rider, body, "proposals")
		var wrapper struct {
			Proposals []json.RawMessage `json:"proposals"`
		}
		_ = json.Unmarshal([]byte(body), &wrapper)
		for _, raw := range wrapper.Proposals {
			assertOnlyKeys(t, "a proposal", string(raw),
				"id", "crewId", "crewName", "weekStart", "day", "routeSlug", "routeName", "status", "yourStatus", "members")
		}
		for _, secret := range []string{"Long ride", "plannedSeconds", "tss", "10800", "workout", "description", "zone"} {
			if strings.Contains(strings.ToLower(body), strings.ToLower(secret)) {
				t.Errorf("%s: the response mentions %q, which is plan detail", rider, secret)
			}
		}
	}
}

func TestNoProposalWithoutTwoOpenRidersEachWithAQualifyingRide(t *testing.T) {
	t.Run("only one is open to it", func(t *testing.T) {
		h := newCrewPlanHarness(t)
		ts := h.togetherSetup()
		h.optIn("wilant", ts.crewID, "sat", "sun")
		if got := h.proposals("wilant"); len(got) != 0 {
			t.Errorf("proposals = %+v, want none", got)
		}
	})
	t.Run("one has no long ride", func(t *testing.T) {
		h := newCrewPlanHarness(t)
		ts := h.togetherSetup()
		h.optIn("wilant", ts.crewID, "sat", "sun")
		h.optIn("alex", ts.crewID, "sat", "sun") // alex has no plan at all
		if got := h.proposals("wilant"); len(got) != 0 {
			t.Errorf("proposals = %+v, want none", got)
		}
	})
	t.Run("one has a fixed crew ride that week", func(t *testing.T) {
		h := newCrewPlanHarness(t)
		ts := h.togetherSetup()
		h.optIn("wilant", ts.crewID, "sat", "sun")
		h.optIn("sam", ts.crewID, "sat", "sun")
		h.mustGo("sam", ts.ride)
		if got := h.proposals("wilant"); len(got) != 0 {
			t.Errorf("proposals = %+v, want none: sam has a ride to join and wilant is left alone", got)
		}
	})
	t.Run("a hard session the day before Saturday pushes it to Sunday", func(t *testing.T) {
		h := newCrewPlanHarness(t)
		ts := h.togetherSetup()
		if _, err := h.training.CreateWorkout(context.Background(), genZone("sam", ts.sGoal.ID, "Threshold", cpFriday, workout.ZoneThreshold, 5400)); err != nil {
			t.Fatal(err)
		}
		h.optIn("wilant", ts.crewID, "sat", "sun")
		h.optIn("sam", ts.crewID, "sat", "sun")
		got := h.proposals("wilant")
		if len(got) != 1 || got[0].Day != cpSunday {
			t.Errorf("proposals = %+v, want Sunday", got)
		}
	})
	t.Run("no route is visible to everyone", func(t *testing.T) {
		h := newCrewPlanHarness(t)
		ts := h.togetherSetup()
		// Only wilant may see this one, and it is the only one of the right length.
		for _, r := range []string{ts.medium} {
			if err := h.db.Delete(context.Background(), r); err != nil {
				t.Fatal(err)
			}
		}
		h.seedRoute("Private Medium", "wilant", 70, 600) // shared with nobody
		h.optIn("wilant", ts.crewID, "sat", "sun")
		h.optIn("sam", ts.crewID, "sat", "sun")
		if got := h.proposals("wilant"); len(got) != 0 {
			t.Errorf("proposals = %+v, want none: a route has to be visible to every rider", got)
		}
	})
}

func TestAProposalGoesStaleWhenItsPremiseStopsHoldingAndIsHidden(t *testing.T) {
	setup := func(t *testing.T) (*crewPlanHarness, togetherSetup, proposalOut) {
		h := newCrewPlanHarness(t)
		ts := h.togetherSetup()
		h.optIn("wilant", ts.crewID, "sat", "sun")
		h.optIn("sam", ts.crewID, "sat", "sun")
		p := h.proposals("wilant")
		if len(p) != 1 {
			t.Fatalf("setup: proposals = %+v", p)
		}
		return h, ts, p[0]
	}
	t.Run("a session was deleted", func(t *testing.T) {
		h, ts, _ := setup(t)
		if err := h.training.DeleteWorkout(context.Background(), ts.sam.ID); err != nil {
			t.Fatal(err)
		}
		if got := h.proposals("wilant"); len(got) != 0 {
			t.Errorf("proposals = %+v, want it hidden", got)
		}
	})
	t.Run("a flag was removed", func(t *testing.T) {
		h, ts, _ := setup(t)
		h.optIn("sam", ts.crewID)
		if got := h.proposals("wilant"); len(got) != 0 {
			t.Errorf("proposals = %+v, want it hidden", got)
		}
	})
	t.Run("the day was filled", func(t *testing.T) {
		h, ts, _ := setup(t)
		if _, err := h.training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
			Rider: "wilant", Sport: model.SportCycling, Name: "My own ride", Date: cpSaturday, Description: "mine", Steps: step(3600),
		}); err != nil {
			t.Fatal(err)
		}
		// Saturday is no longer free for wilant: the week is proposed again for Sunday.
		got := h.proposals("wilant")
		if len(got) != 1 || got[0].Day != cpSunday {
			t.Errorf("proposals = %+v, want a fresh one for Sunday", got)
		}
		_ = ts
	})
}

func TestLeavingTheCrewEndsTheProposalAndNobodyIsLeftWithAHalfAgreement(t *testing.T) {
	h := newCrewPlanHarness(t)
	ts := h.togetherSetup()
	h.optIn("wilant", ts.crewID, "sat", "sun")
	h.optIn("sam", ts.crewID, "sat", "sun")
	p := h.proposals("wilant")[0]

	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/crews/"+ts.crewID+"/members/sam", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove member: status = %d", resp.StatusCode)
	}
	if got := h.proposals("wilant"); len(got) != 0 {
		t.Errorf("proposals = %+v, want it ended", got)
	}
	stored, err := h.sched.GetProposal(context.Background(), p.ID)
	if err != nil || string(stored.Status) != "ended" {
		t.Errorf("stored = %+v, %v, want ended", stored, err)
	}
	if got, _ := h.sched.TogetherFor(context.Background(), ts.crewID); len(got) != 1 {
		t.Errorf("flags = %v, want only wilant's", got)
	}
}

func TestPurgingARiderEndsTheProposalsTheyAreIn(t *testing.T) {
	h := newCrewPlanHarness(t)
	ts := h.togetherSetup()
	h.optIn("wilant", ts.crewID, "sat", "sun")
	h.optIn("sam", ts.crewID, "sat", "sun")
	p := h.proposals("wilant")[0]

	if _, err := h.srv.PurgeRiderDataForTest(context.Background(), "sam"); err != nil {
		t.Fatal(err)
	}
	stored, _ := h.sched.GetProposal(context.Background(), p.ID)
	if string(stored.Status) != "ended" {
		t.Errorf("status = %s, want ended", stored.Status)
	}
	for _, m := range stored.Members {
		if m.Rider == "sam" {
			t.Error("the purged rider is still a member")
		}
	}
	if got := h.proposals("wilant"); len(got) != 0 {
		t.Errorf("proposals = %+v, want none", got)
	}
}

func TestComputingAProposalLogsNoHealthValuesNextToARider(t *testing.T) {
	h := newCrewPlanHarness(t)
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ts := h.togetherSetup()
	if _, err := h.training.SaveProfile(context.Background(), workout.RiderProfile{
		Rider: "sam", FTPWatts: 2718, MaxHR: 1893, HoursPerAvailableDay: 2, AvailableDays: []string{"sat"},
	}); err != nil {
		t.Fatal(err)
	}
	h.optIn("wilant", ts.crewID, "sat", "sun")
	h.optIn("sam", ts.crewID, "sat", "sun")
	h.proposals("wilant")
	for _, secret := range []string{"2718", "1893"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log holds %s:\n%s", secret, logs.String())
		}
	}
}
