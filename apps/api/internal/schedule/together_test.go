package schedule

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

func togetherStore(t *testing.T, dsn string) *Store {
	t.Helper()
	s := openStore(t, dsn)
	for _, table := range []string{"crew_ride_together", "ride_together_members", "ride_together_proposals"} {
		if _, err := s.db.Exec(`DELETE FROM ` + table); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func eachTogetherEngine(t *testing.T, fn func(t *testing.T, s *Store)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, togetherStore(t, filepath.Join(t.TempDir(), "together.db"))) })
	t.Run("postgres", func(t *testing.T) { fn(t, togetherStore(t, envPostgres(t))) })
}

func TestTheTogetherFlagIsOneRowPerRiderPerCrewAndEmptyDeletesIt(t *testing.T) {
	eachTogetherEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		if err := s.SetTogether(ctx, "crew-a", "Wilant", []string{"sat", "sun"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTogether(ctx, "crew-a", "wilant", []string{"sun", "sat", "sun"}); err != nil { // replaces, de-duplicated
			t.Fatal(err)
		}
		_ = s.SetTogether(ctx, "crew-a", "sam", []string{"sat"})
		_ = s.SetTogether(ctx, "crew-b", "wilant", []string{"tue"})

		got, err := s.TogetherFor(ctx, "crew-a")
		if err != nil {
			t.Fatal(err)
		}
		want := map[string][]string{"wilant": {"sat", "sun"}, "sam": {"sat"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("crew-a = %v, want %v", got, want)
		}
		if err := s.SetTogether(ctx, "crew-a", "wilant", nil); err != nil {
			t.Fatal(err)
		}
		got, _ = s.TogetherFor(ctx, "crew-a")
		if _, ok := got["wilant"]; ok || len(got) != 1 {
			t.Errorf("after clearing, crew-a = %v, want only sam", got)
		}
		other, _ := s.TogetherFor(ctx, "crew-b")
		if !reflect.DeepEqual(other, map[string][]string{"wilant": {"tue"}}) {
			t.Errorf("crew-b = %v: clearing one crew must not touch another", other)
		}
	})
}

func newProposal(t *testing.T, s *Store, crewID, week string, riders ...string) Proposal {
	t.Helper()
	p, _, err := s.CreateProposal(context.Background(), Proposal{CrewID: crewID, WeekStart: week, Day: "2026-10-10", RouteSlug: "a-route", Riders: riders})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAProposalIsStoredOncePerCrewAndWeekSoEveryoneSeesTheSameOne(t *testing.T) {
	eachTogetherEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		first, created, err := s.CreateProposal(ctx, Proposal{CrewID: "crew-a", WeekStart: "2026-10-05", Day: "2026-10-10", RouteSlug: "a", Riders: []string{"wilant", "sam"}})
		if err != nil || !created {
			t.Fatalf("create: %v, created %v", err, created)
		}
		again, created, err := s.CreateProposal(ctx, Proposal{CrewID: "crew-a", WeekStart: "2026-10-05", Day: "2026-10-11", RouteSlug: "b", Riders: []string{"wilant", "sam"}})
		if err != nil || created {
			t.Fatalf("second create: %v, created %v, want the stored one back", err, created)
		}
		if again.ID != first.ID || again.Day != "2026-10-10" || again.RouteSlug != "a" {
			t.Errorf("second = %+v, want the first %+v", again, first)
		}
		if first.Status != ProposalOpen || len(first.Members) != 2 || first.Members[0].Status != MemberPending {
			t.Errorf("proposal = %+v, want open with two pending members", first)
		}
		got, ok, err := s.ProposalFor(ctx, "crew-a", "2026-10-05")
		if err != nil || !ok || got.ID != first.ID {
			t.Errorf("ProposalFor = %+v, %v, %v", got, ok, err)
		}
		if _, ok, _ := s.ProposalFor(ctx, "crew-a", "2026-10-12"); ok {
			t.Error("a proposal for another week was found")
		}
	})
}

func TestAStaleProposalIsReplacedButAnEndedOrAgreedOneIsFinal(t *testing.T) {
	eachTogetherEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		stale := newProposal(t, s, "crew-a", "2026-10-05", "wilant", "sam")
		if err := s.SetProposalStatus(ctx, stale.ID, ProposalStale); err != nil {
			t.Fatal(err)
		}
		fresh, created, err := s.CreateProposal(ctx, Proposal{CrewID: "crew-a", WeekStart: "2026-10-05", Day: "2026-10-11", RouteSlug: "b", Riders: []string{"wilant", "sam"}})
		if err != nil || !created || fresh.ID == stale.ID || fresh.Day != "2026-10-11" {
			t.Fatalf("replacing a stale one: %+v, created %v, %v", fresh, created, err)
		}
		if _, err := s.GetProposal(ctx, stale.ID); err != ErrNotFound {
			t.Errorf("the stale proposal is still stored: %v", err)
		}

		if err := s.SetProposalStatus(ctx, fresh.ID, ProposalEnded); err != nil {
			t.Fatal(err)
		}
		again, created, _ := s.CreateProposal(ctx, Proposal{CrewID: "crew-a", WeekStart: "2026-10-05", Day: "2026-10-12", RouteSlug: "c", Riders: []string{"wilant", "sam"}})
		if created || again.ID != fresh.ID || again.Status != ProposalEnded {
			t.Errorf("an ended proposal was proposed again: %+v, created %v", again, created)
		}
	})
}

func TestMemberStatusIsPerRiderAndRecordsTheSessionMoved(t *testing.T) {
	eachTogetherEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		p := newProposal(t, s, "crew-a", "2026-10-05", "wilant", "sam")
		if err := s.SetMemberStatus(ctx, p.ID, "Wilant", MemberAccepted, "workout-1"); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetProposal(ctx, p.ID)
		byRider := map[string]Member{}
		for _, m := range got.Members {
			byRider[m.Rider] = m
		}
		if byRider["wilant"].Status != MemberAccepted || byRider["wilant"].WorkoutID != "workout-1" || byRider["sam"].Status != MemberPending {
			t.Errorf("members = %+v, want only wilant accepted", got.Members)
		}
		if err := s.SetMemberStatus(ctx, p.ID, "nobody", MemberAccepted, ""); err != ErrNotFound {
			t.Errorf("a rider outside the proposal: err = %v, want ErrNotFound", err)
		}
	})
}

func TestRemovingAMemberClearsTheirFlagAndEndsTheProposalsTheyAreIn(t *testing.T) {
	eachTogetherEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		_ = s.SetTogether(ctx, "crew-a", "wilant", []string{"sat"})
		_ = s.SetTogether(ctx, "crew-a", "sam", []string{"sat"})
		inA := newProposal(t, s, "crew-a", "2026-10-05", "wilant", "sam")
		inB := newProposal(t, s, "crew-b", "2026-10-05", "wilant", "alex")

		if err := s.RemoveRider(ctx, "crew-a", "wilant"); err != nil {
			t.Fatal(err)
		}
		flags, _ := s.TogetherFor(ctx, "crew-a")
		if _, ok := flags["wilant"]; ok || len(flags) != 1 {
			t.Errorf("flags = %v, want only sam's", flags)
		}
		a, _ := s.GetProposal(ctx, inA.ID)
		if a.Status != ProposalEnded {
			t.Errorf("crew-a proposal = %s, want ended: a member left it", a.Status)
		}
		for _, m := range a.Members {
			if m.Rider == "wilant" {
				t.Error("the removed rider is still a member of the proposal")
			}
		}
		b, _ := s.GetProposal(ctx, inB.ID)
		if b.Status != ProposalOpen || len(b.Members) != 2 {
			t.Errorf("crew-b proposal = %+v: removal from one crew must not touch another's", b)
		}
	})
}

func TestPurgingARiderEndsEveryProposalTheyAreInAndLeavesOthersAlone(t *testing.T) {
	eachTogetherEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		_ = s.SetTogether(ctx, "crew-a", "wilant", []string{"sat"})
		_ = s.SetTogether(ctx, "crew-b", "wilant", []string{"sun"})
		_ = s.SetTogether(ctx, "crew-a", "sam", []string{"sat"})
		a := newProposal(t, s, "crew-a", "2026-10-05", "wilant", "sam")
		b := newProposal(t, s, "crew-b", "2026-10-05", "wilant", "alex")
		c := newProposal(t, s, "crew-c", "2026-10-05", "sam", "alex")

		if err := s.DeleteRider(ctx, "wilant"); err != nil {
			t.Fatal(err)
		}
		if f, _ := s.TogetherFor(ctx, "crew-a"); len(f) != 1 {
			t.Errorf("crew-a flags = %v, want only sam's", f)
		}
		if f, _ := s.TogetherFor(ctx, "crew-b"); len(f) != 0 {
			t.Errorf("crew-b flags = %v, want none", f)
		}
		for _, id := range []string{a.ID, b.ID} {
			p, _ := s.GetProposal(ctx, id)
			if p.Status != ProposalEnded {
				t.Errorf("proposal %s = %s, want ended", id, p.Status)
			}
			for _, m := range p.Members {
				if m.Rider == "wilant" {
					t.Errorf("proposal %s still lists the purged rider", id)
				}
			}
		}
		if p, _ := s.GetProposal(ctx, c.ID); p.Status != ProposalOpen || len(p.Members) != 2 {
			t.Errorf("an unrelated proposal changed: %+v", p)
		}
	})
}

func TestTogetherSchemaIsIdempotent(t *testing.T) {
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "idem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for range 2 {
		if _, err := UseDB(db.Conn(), db.DSN()); err != nil {
			t.Fatalf("UseDB: %v", err)
		}
	}
}
