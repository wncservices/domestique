package api_test

import (
	"context"
	"net/http"
	"testing"
)

// Crew ids are reused: a deleted "Sunday Club" frees its id for a new, unrelated
// crew of the same name. Nothing the old crew held may reach the new one.
func TestARecreatedCrewInheritsNoTogetherFlagsOrProposals(t *testing.T) {
	h := newCrewPlanHarness(t)
	ts := h.togetherSetup()
	h.optIn("wilant", ts.crewID, "sat", "sun")
	h.optIn("sam", ts.crewID, "sat", "sun")
	if got := h.proposals("wilant"); len(got) != 1 {
		t.Fatalf("setup: proposals = %+v", got)
	}

	if resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/crews/"+ts.crewID, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete crew: status = %d", resp.StatusCode)
	}
	again := h.seedCrew("Sunday Club", "wilant", "sam")
	if again != ts.crewID {
		t.Fatalf("setup: the id was not reused (%q vs %q)", again, ts.crewID)
	}

	flags, _ := h.sched.TogetherFor(context.Background(), again)
	if len(flags) != 0 {
		t.Errorf("the new crew's members are opted in already: %v", flags)
	}
	if _, ok, _ := h.sched.ProposalFor(context.Background(), again, cpMonday); ok {
		t.Error("the new crew holds the old crew's proposal")
	}
	for _, rider := range []string{"wilant", "sam"} {
		if got := h.proposals(rider); len(got) != 0 {
			t.Errorf("%s still sees %+v", rider, got)
		}
	}
}
