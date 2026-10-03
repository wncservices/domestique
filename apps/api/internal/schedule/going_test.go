package schedule

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

// eachEngine runs fn on SQLite and, when DOMESTIQUE_TEST_POSTGRES is set, on
// PostgreSQL: the going table has to behave the same on both.
func eachEngine(t *testing.T, fn func(t *testing.T, s *Store)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, goingStore(t, filepath.Join(t.TempDir(), "going.db"))) })
	t.Run("postgres", func(t *testing.T) {
		dsn := envPostgres(t)
		fn(t, goingStore(t, dsn))
	})
}

func envPostgres(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(postgresEnv)
	if dsn == "" {
		t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
	}
	return dsn
}

func goingStore(t *testing.T, dsn string) *Store {
	t.Helper()
	s := openStore(t, dsn)
	for _, table := range []string{"crew_ride_going"} {
		if _, err := s.db.Exec(`DELETE FROM ` + table); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func newRide(t *testing.T, s *Store, crewID, date string) Ride {
	t.Helper()
	r, err := s.Create(context.Background(), crewID, "a-route", date, "", "owner")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGoingIsRecordedOncePerRiderAndRide(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ride := newRide(t, s, "crew-a", "2026-10-10")
		for range 2 { // a second call is a no-op, not an error
			if err := s.Go(ctx, ride.ID, "Wilant"); err != nil {
				t.Fatalf("Go: %v", err)
			}
		}
		if err := s.Go(ctx, ride.ID, "sam"); err != nil {
			t.Fatal(err)
		}
		got, err := s.Going(ctx, ride.ID)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"sam", "wilant"}; !reflect.DeepEqual(got, want) {
			t.Errorf("going = %v, want %v (normalised, sorted, once each)", got, want)
		}
		is, err := s.IsGoing(ctx, ride.ID, "wilant")
		if err != nil || !is {
			t.Errorf("IsGoing(wilant) = %v, %v, want true", is, err)
		}
		is, _ = s.IsGoing(ctx, ride.ID, "nobody")
		if is {
			t.Error("IsGoing(nobody) = true: absence is not going")
		}
	})
}

func TestLeaveRemovesOnlyThatRidersRow(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ride := newRide(t, s, "crew-a", "2026-10-10")
		_ = s.Go(ctx, ride.ID, "wilant")
		_ = s.Go(ctx, ride.ID, "sam")
		if err := s.Leave(ctx, ride.ID, "wilant"); err != nil {
			t.Fatal(err)
		}
		if err := s.Leave(ctx, ride.ID, "wilant"); err != nil { // leaving twice is fine
			t.Fatal(err)
		}
		got, _ := s.Going(ctx, ride.ID)
		if !reflect.DeepEqual(got, []string{"sam"}) {
			t.Errorf("going = %v, want only sam", got)
		}
	})
}

func TestGoingByRideReadsEveryRideFromADate(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		past := newRide(t, s, "crew-a", "2026-10-01")
		soon := newRide(t, s, "crew-a", "2026-10-10")
		_ = s.Go(ctx, past.ID, "wilant")
		_ = s.Go(ctx, soon.ID, "wilant")
		_ = s.Go(ctx, soon.ID, "sam")
		got, err := s.GoingFrom(ctx, "2026-10-05")
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string][]string{soon.ID: {"sam", "wilant"}}; !reflect.DeepEqual(got, want) {
			t.Errorf("GoingFrom = %v, want %v", got, want)
		}
	})
}

func TestDeletingARideRemovesItsGoingRows(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ride := newRide(t, s, "crew-a", "2026-10-10")
		other := newRide(t, s, "crew-a", "2026-10-17")
		_ = s.Go(ctx, ride.ID, "wilant")
		_ = s.Go(ctx, other.ID, "wilant")
		if err := s.Delete(ctx, ride.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Going(ctx, ride.ID); len(got) != 0 {
			t.Errorf("going rows of a deleted ride = %v, want none", got)
		}
		if got, _ := s.Going(ctx, other.ID); len(got) != 1 {
			t.Errorf("the other ride lost its going rows: %v", got)
		}
	})
}

func TestDeletingASeriesOrACrewRemovesGoingRowsOfTheRidesItDrops(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		series, rides, err := s.CreateSeries(ctx, "crew-a", "a-route", 1, "2026-10-10", "2026-10-24", "", "owner")
		if err != nil || len(rides) != 3 {
			t.Fatalf("series: %v, %d rides", err, len(rides))
		}
		for _, r := range rides {
			_ = s.Go(ctx, r.ID, "wilant")
		}
		if _, err := s.DeleteSeries(ctx, series.ID, "2026-10-17"); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Going(ctx, rides[0].ID); len(got) != 1 {
			t.Errorf("the ride before the cut lost its going rows: %v", got)
		}
		for _, r := range rides[1:] {
			if got, _ := s.Going(ctx, r.ID); len(got) != 0 {
				t.Errorf("going rows of a deleted series ride = %v, want none", got)
			}
		}
		if err := s.DeleteForCrew(ctx, "crew-a"); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Going(ctx, rides[0].ID); len(got) != 0 {
			t.Errorf("going rows of a deleted crew's ride = %v, want none", got)
		}
	})
}

func TestRemoveRiderDropsThatRidersGoingRowsInThatCrewOnly(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		inA := newRide(t, s, "crew-a", "2026-10-10")
		inB := newRide(t, s, "crew-b", "2026-10-10")
		_ = s.Go(ctx, inA.ID, "wilant")
		_ = s.Go(ctx, inA.ID, "sam")
		_ = s.Go(ctx, inB.ID, "wilant")
		if err := s.RemoveRider(ctx, "crew-a", "wilant"); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Going(ctx, inA.ID); !reflect.DeepEqual(got, []string{"sam"}) {
			t.Errorf("crew-a going = %v, want only sam", got)
		}
		if got, _ := s.Going(ctx, inB.ID); !reflect.DeepEqual(got, []string{"wilant"}) {
			t.Errorf("crew-b going = %v, want wilant untouched", got)
		}
	})
}

func TestGoingSchemaIsIdempotent(t *testing.T) {
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
