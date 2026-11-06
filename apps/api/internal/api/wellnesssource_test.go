package api

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/applehealthlink"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/testdb"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestReadinessReadsOnlyTheCurrentSource(t *testing.T) {
	db, err := source.OpenDB(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	training, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	apple, err := applehealthlink.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Training: training, AppleHealth: apple}
	ctx := t.Context()

	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		d := start.AddDate(0, 0, i).Format("2006-01-02")
		if err := training.SaveWellness(ctx, workout.DailyWellness{Rider: "wilant", Date: d, RestingHR: 50}); err != nil {
			t.Fatal(err)
		}
	}
	// Apple nights fall on other dates: (rider, date) is the key, so a night
	// both sources reported would be one row, the later write.
	for i := 0; i < 3; i++ {
		d := start.AddDate(0, 0, 30+i).Format("2006-01-02")
		if err := training.SaveWellness(ctx, workout.DailyWellness{Rider: "wilant", Date: d, RestingHR: 44, Source: "apple"}); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := s.readinessWellness(ctx, "wilant", "")
	if err != nil || len(rows) != 20 {
		t.Fatalf("unlinked: rows = %d, err = %v, want the 20 Garmin rows", len(rows), err)
	}

	if _, err := apple.Regenerate(ctx, "wilant"); err != nil {
		t.Fatal(err)
	}
	rows, err = s.readinessWellness(ctx, "wilant", "")
	if err != nil || len(rows) != 3 {
		t.Fatalf("linked: rows = %d, err = %v, want the 3 Apple rows", len(rows), err)
	}
	for _, r := range rows {
		if r.Source != "apple" {
			t.Errorf("row %s source = %q, want apple", r.Date, r.Source)
		}
	}

	if err := apple.Revoke(ctx, "wilant"); err != nil {
		t.Fatal(err)
	}
	rows, err = s.readinessWellness(ctx, "wilant", "")
	if err != nil || len(rows) != 20 {
		t.Fatalf("revoked: rows = %d, err = %v, want the 20 Garmin rows back", len(rows), err)
	}
	if got := s.wellnessSource(ctx, "wilant"); got != "garmin" {
		t.Errorf("source after revoke = %q, want garmin", got)
	}
}

func TestWellnessSourceWithoutAStoreIsGarmin(t *testing.T) {
	s := &Server{}
	if got := s.wellnessSource(t.Context(), "wilant"); got != "garmin" {
		t.Errorf("source = %q, want garmin", got)
	}
}
