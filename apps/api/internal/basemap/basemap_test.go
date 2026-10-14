package basemap

import (
	"errors"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

func newStore(t *testing.T, dsn string) *Store {
	t.Helper()
	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	// Postgres tests share a database; start clean.
	if _, err := db.Conn().Exec(`DELETE FROM basemap_updates`); err != nil {
		t.Fatal(err)
	}
	return store
}

func validBBox() BBox {
	// Roughly Belgium — well under maxAreaSqDeg.
	return BBox{West: 2.5, South: 49.4, East: 6.4, North: 51.6}
}

func TestBBoxValidate(t *testing.T) {
	cases := []struct {
		name    string
		bbox    BBox
		wantErr bool
	}{
		{"a normal, small region is fine", validBBox(), false},
		{"west out of range", BBox{West: -181, South: 0, East: 0, North: 1}, true},
		{"east out of range", BBox{West: 0, South: 0, East: 181, North: 1}, true},
		{"south out of range", BBox{West: 0, South: -91, East: 1, North: 1}, true},
		{"north out of range", BBox{West: 0, South: 0, East: 1, North: 91}, true},
		{"west not less than east", BBox{West: 5, South: 0, East: 5, North: 1}, true},
		{"west greater than east", BBox{West: 6, South: 0, East: 5, North: 1}, true},
		{"south not less than north", BBox{West: 0, South: 5, East: 1, North: 5}, true},
		{"an entire-continent-scale box is rejected", BBox{West: -25, South: 34, East: 45, North: 72}, true},
		{"the whole planet is rejected", BBox{West: -180, South: -90, East: 180, North: 90}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.bbox.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateMaxZoom(t *testing.T) {
	cases := []struct {
		name    string
		z       int
		wantErr bool
	}{
		{"a normal zoom is fine", 14, false},
		{"zero is fine", 0, false},
		{"the cap itself is fine", maxZoomLevel, false},
		{"one past the cap is rejected", maxZoomLevel + 1, true},
		{"negative is rejected", -1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMaxZoom(tc.z)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateMaxZoom(%d) = %v, wantErr %v", tc.z, err, tc.wantErr)
			}
		})
	}
}

func TestLatestWithNoRecordsIsErrNoRecord(t *testing.T) {
	eachEngine(t, func(t *testing.T, dsn string) {
		store := newStore(t, dsn)
		if _, err := store.Latest(); err != ErrNoRecord {
			t.Errorf("Latest() err = %v, want ErrNoRecord", err)
		}
	})
}

func TestCreateThenLatestRoundTrips(t *testing.T) {
	eachEngine(t, func(t *testing.T, dsn string) {
		store := newStore(t, dsn)
		bbox := validBBox()

		id, err := store.Create(bbox, 14, "20260822", "wilant")
		if err != nil {
			t.Fatal(err)
		}
		if id == "" {
			t.Fatal("Create returned an empty id")
		}

		rec, err := store.Latest()
		if err != nil {
			t.Fatal(err)
		}
		if rec.ID != id {
			t.Errorf("ID = %q, want %q", rec.ID, id)
		}
		if rec.BBox != bbox {
			t.Errorf("BBox = %+v, want %+v", rec.BBox, bbox)
		}
		if rec.MaxZoom != 14 {
			t.Errorf("MaxZoom = %d, want 14", rec.MaxZoom)
		}
		if rec.BuildDate != "20260822" {
			t.Errorf("BuildDate = %q, want 20260822", rec.BuildDate)
		}
		if rec.RequestedBy != "wilant" {
			t.Errorf("RequestedBy = %q, want wilant", rec.RequestedBy)
		}
		if rec.Status != StatusPending {
			t.Errorf("Status = %q, want pending", rec.Status)
		}
		if rec.CompletedAt != nil {
			t.Errorf("CompletedAt = %v, want nil (still pending)", rec.CompletedAt)
		}
	})
}

func TestLatestReturnsTheMostRecentOfSeveral(t *testing.T) {
	eachEngine(t, func(t *testing.T, dsn string) {
		store := newStore(t, dsn)
		bbox := validBBox()

		first, err := store.Create(bbox, 10, "20260101", "a")
		if err != nil {
			t.Fatal(err)
		}
		// idx_basemap_updates_one_in_progress permits only one pending/running
		// row at a time (see TestCreateWhileOneIsInProgressIsRejected below) —
		// the first has to actually finish before a second can exist at all.
		if err := store.MarkSucceeded(first, 100); err != nil {
			t.Fatal(err)
		}
		second, err := store.Create(bbox, 12, "20260102", "b")
		if err != nil {
			t.Fatal(err)
		}

		rec, err := store.Latest()
		if err != nil {
			t.Fatal(err)
		}
		if rec.ID != second {
			t.Errorf("Latest() returned %q, want the second-created record %q", rec.ID, second)
		}
	})
}

func TestSetJobNameMovesToRunning(t *testing.T) {
	eachEngine(t, func(t *testing.T, dsn string) {
		store := newStore(t, dsn)
		id, err := store.Create(validBBox(), 14, "20260822", "wilant")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetJobName(id, "domestique-basemap-update-abc12"); err != nil {
			t.Fatal(err)
		}

		rec, err := store.Latest()
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status != StatusRunning {
			t.Errorf("Status = %q, want running", rec.Status)
		}
		if rec.JobName != "domestique-basemap-update-abc12" {
			t.Errorf("JobName = %q, want domestique-basemap-update-abc12", rec.JobName)
		}
	})
}

func TestMarkSucceededRecordsSizeAndCompletion(t *testing.T) {
	eachEngine(t, func(t *testing.T, dsn string) {
		store := newStore(t, dsn)
		id, err := store.Create(validBBox(), 14, "20260822", "wilant")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkSucceeded(id, 9_400_000_000); err != nil {
			t.Fatal(err)
		}

		rec, err := store.Latest()
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status != StatusSucceeded {
			t.Errorf("Status = %q, want succeeded", rec.Status)
		}
		if rec.SizeBytes != 9_400_000_000 {
			t.Errorf("SizeBytes = %d, want 9400000000", rec.SizeBytes)
		}
		if rec.CompletedAt == nil {
			t.Error("CompletedAt is nil, want it set")
		}
	})
}

// Code review finding: two Jobs racing to place a file on the same tiles
// pod, with no coordination, can corrupt the live basemap. This is the
// database-level guarantee that closes it even across two requests the
// application-level pre-check in handleBasemapUpdate could not fully rule
// out on its own — idx_basemap_updates_one_in_progress enforces it
// directly, so a second Create fails immediately rather than racing.
func TestCreateWhileOneIsInProgressIsRejected(t *testing.T) {
	eachEngine(t, func(t *testing.T, dsn string) {
		store := newStore(t, dsn)
		bbox := validBBox()

		if _, err := store.Create(bbox, 14, "20260822", "a"); err != nil {
			t.Fatal(err)
		}

		if _, err := store.Create(bbox, 14, "20260822", "b"); !errors.Is(err, ErrAlreadyInProgress) {
			t.Errorf("second Create() err = %v, want ErrAlreadyInProgress", err)
		}
	})
}

func TestCreateAfterOneFinishesIsAllowed(t *testing.T) {
	eachEngine(t, func(t *testing.T, dsn string) {
		for _, tc := range []struct {
			name string
			mark func(*Store, string) error
		}{
			{"succeeded", func(s *Store, id string) error { return s.MarkSucceeded(id, 100) }},
			{"failed", func(s *Store, id string) error { return s.MarkFailed(id, "boom") }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				store := newStore(t, dsn)
				bbox := validBBox()

				first, err := store.Create(bbox, 14, "20260822", "a")
				if err != nil {
					t.Fatal(err)
				}
				if err := tc.mark(store, first); err != nil {
					t.Fatal(err)
				}

				if _, err := store.Create(bbox, 14, "20260822", "b"); err != nil {
					t.Errorf("Create() after the first update finished = %v, want no error", err)
				}
			})
		}
	})
}

func TestMarkFailedRecordsError(t *testing.T) {
	eachEngine(t, func(t *testing.T, dsn string) {
		store := newStore(t, dsn)
		id, err := store.Create(validBBox(), 14, "20260822", "wilant")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkFailed(id, "no tiles pod found"); err != nil {
			t.Fatal(err)
		}

		rec, err := store.Latest()
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status != StatusFailed {
			t.Errorf("Status = %q, want failed", rec.Status)
		}
		if rec.Error != "no tiles pod found" {
			t.Errorf("Error = %q, want %q", rec.Error, "no tiles pod found")
		}
		if rec.CompletedAt == nil {
			t.Error("CompletedAt is nil, want it set")
		}
	})
}
