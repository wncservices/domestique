package workout

import (
	"sync"
	"testing"
)

func TestLedgerClaimIsOncePerDay(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		claimed, created, err := db.ClaimLedgerDay(ctx, "Wilant", "2026-10-12", "2026-10-12")
		if err != nil || !claimed || !created {
			t.Fatalf("first claim = %v, %v, %v; want claimed and created", claimed, created, err)
		}
		claimed, created, err = db.ClaimLedgerDay(ctx, "wilant", "2026-10-12", "2026-10-12")
		if err != nil || claimed || created {
			t.Fatalf("same day = %v, %v, %v; want neither", claimed, created, err)
		}
		var before string
		if err := db.db.QueryRowContext(ctx, db.query(`SELECT created_at FROM load_ledger WHERE rider = ? AND week_start = ?`), "wilant", "2026-10-12").Scan(&before); err != nil || before == "" {
			t.Fatalf("created_at = %q, err %v; want set", before, err)
		}
		claimed, created, err = db.ClaimLedgerDay(ctx, "wilant", "2026-10-12", "2026-10-13")
		if err != nil || !claimed || created {
			t.Fatalf("next day = %v, %v, %v; want claimed, not created", claimed, created, err)
		}
		var after string
		if err := db.db.QueryRowContext(ctx, db.query(`SELECT created_at FROM load_ledger WHERE rider = ? AND week_start = ?`), "wilant", "2026-10-12").Scan(&after); err != nil || after != before {
			t.Fatalf("created_at moved: %q -> %q (err %v)", before, after, err)
		}
	})
}

func TestLedgerClaimRacesSafely(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		var wg sync.WaitGroup
		results := make([]bool, 2)
		errs := make([]error, 2)
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], _, errs[i] = db.ClaimLedgerDay(ctx, "wilant", "2026-10-12", "2026-10-14")
			}()
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
		}
		if results[0] == results[1] {
			t.Fatalf("claims = %v; exactly one must win", results)
		}
	})
}

func TestLedgerOpenAndForgiveRoundTrip(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		if _, ok, err := db.GetLedgerWeek(ctx, "wilant", "2026-10-12"); err != nil || ok {
			t.Fatalf("unopened week = ok %v, err %v", ok, err)
		}
		if err := db.SetLedgerForgiven(ctx, "wilant", "2026-10-12", 9); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := db.GetLedgerWeek(ctx, "wilant", "2026-10-12"); ok {
			t.Fatal("forgiving an unopened week created a row")
		}
		if err := db.OpenLedgerWeek(ctx, "Wilant", "2026-10-12", 400, 12.5, 5, true); err != nil {
			t.Fatal(err)
		}
		if _, _, err := db.ClaimLedgerDay(ctx, "wilant", "2026-10-12", "2026-10-13"); err != nil {
			t.Fatal(err)
		}
		if err := db.SetLedgerForgiven(ctx, "wilant", "2026-10-12", 7); err != nil {
			t.Fatal(err)
		}
		if err := db.OpenLedgerWeek(ctx, "wilant", "2026-10-12", 410, 12.5, 6, true); err != nil {
			t.Fatal(err)
		}
		got, ok, err := db.GetLedgerWeek(ctx, "wilant", "2026-10-12")
		if err != nil || !ok {
			t.Fatalf("get = ok %v, err %v", ok, err)
		}
		want := LedgerWeek{Rider: "wilant", WeekStart: "2026-10-12", TargetTSS: 410, CarriedInTSS: 12.5, OpenDays: 6, Reduced: true, ForgivenTSS: 7, LastPassDate: "2026-10-13"}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

func TestDeleteRiderRemovesLedger(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		for _, r := range []string{"wilant", "other"} {
			if err := db.OpenLedgerWeek(ctx, r, "2026-10-12", 400, 0, 0, false); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.DeleteRider(ctx, "Wilant"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := db.GetLedgerWeek(ctx, "wilant", "2026-10-12"); ok {
			t.Error("ledger survived DeleteRider")
		}
		if _, ok, _ := db.GetLedgerWeek(ctx, "other", "2026-10-12"); !ok {
			t.Error("another rider's ledger was purged")
		}
	})
}

// The week's one upgrade is remembered on its row, and reopening the week
// (a retry after a crash) does not forget it.
func TestLedgerUpgradeIsMarkedOncePerWeek(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		if err := db.OpenLedgerWeek(ctx, "wilant", "2026-10-12", 400, 0, 5, false); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := db.GetLedgerWeek(ctx, "wilant", "2026-10-12"); got.UpgradedAt != "" {
			t.Fatalf("upgraded_at = %q before any upgrade", got.UpgradedAt)
		}
		if err := db.MarkLedgerUpgraded(ctx, "Wilant", "2026-10-12", "2026-10-14"); err != nil {
			t.Fatal(err)
		}
		if err := db.MarkLedgerUpgraded(ctx, "wilant", "2026-10-12", "2026-10-15"); err != nil {
			t.Fatal(err)
		}
		if err := db.OpenLedgerWeek(ctx, "wilant", "2026-10-12", 400, 0, 5, false); err != nil {
			t.Fatal(err)
		}
		got, _, _ := db.GetLedgerWeek(ctx, "wilant", "2026-10-12")
		if got.UpgradedAt != "2026-10-14" {
			t.Errorf("upgraded_at = %q, want the first day kept", got.UpgradedAt)
		}
	})
}
