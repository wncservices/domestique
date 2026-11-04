package workout

import (
	"testing"
	"time"
)

func TestExplanationClaimSaveGetRelease(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		if _, ok, err := db.GetExplanation(ctx, "k"); err != nil || ok {
			t.Fatalf("empty get = %v, %v", ok, err)
		}
		if ok, err := db.ClaimExplanation(ctx, "k"); err != nil || !ok {
			t.Fatalf("first claim = %v, %v", ok, err)
		}
		if ok, err := db.ClaimExplanation(ctx, "k"); err != nil || ok {
			t.Fatalf("second claim = %v, %v; want refused", ok, err)
		}
		if _, ok, _ := db.GetExplanation(ctx, "k"); ok {
			t.Fatal("a claim reads as present")
		}
		if err := db.ReleaseExplanation(ctx, "k"); err != nil {
			t.Fatal(err)
		}
		if ok, err := db.ClaimExplanation(ctx, "k"); err != nil || !ok {
			t.Fatalf("claim after release = %v, %v", ok, err)
		}
		if err := db.SaveExplanation(ctx, "k", `{"summary":"s"}`); err != nil {
			t.Fatal(err)
		}
		if text, ok, err := db.GetExplanation(ctx, "k"); err != nil || !ok || text != `{"summary":"s"}` {
			t.Fatalf("get = %q, %v, %v", text, ok, err)
		}
		// Release never removes saved text, and a saved key cannot be claimed.
		if err := db.ReleaseExplanation(ctx, "k"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := db.GetExplanation(ctx, "k"); !ok {
			t.Fatal("release removed saved text")
		}
		if ok, err := db.ClaimExplanation(ctx, "k"); err != nil || ok {
			t.Fatalf("claim of a saved key = %v, %v", ok, err)
		}
	})
}

func TestAStaleExplanationClaimIsTakenOver(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		now := time.Date(2026, 10, 5, 6, 30, 0, 0, time.UTC)
		db.Clock = func() time.Time { return now }
		if ok, _ := db.ClaimExplanation(ctx, "k"); !ok {
			t.Fatal("first claim refused")
		}
		now = now.Add(time.Minute)
		if ok, _ := db.ClaimExplanation(ctx, "k"); ok {
			t.Fatal("a fresh claim was taken over")
		}
		now = now.Add(10 * time.Minute)
		if ok, err := db.ClaimExplanation(ctx, "k"); err != nil || !ok {
			t.Fatalf("a stale claim was not taken over: %v, %v", ok, err)
		}
	})
}
