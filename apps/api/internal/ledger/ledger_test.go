package ledger

import (
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/why"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestDebt(t *testing.T) {
	if got := Debt(300, 10, 200, 60); !near(got, 50) {
		t.Fatalf("Debt = %v, want 50", got)
	}
	if got := Debt(100, 0, 200, 60); got != 0 {
		t.Fatalf("overridden week must floor at 0, got %v", got)
	}
}

func TestTargetScalesByBlackoutDays(t *testing.T) {
	days := []string{"mon", "wed", "fri", "sat"}
	// 2026-10-12 is a Monday; Wednesday the 14th is blacked out.
	got := Target(400, days, "2026-10-12", map[string]bool{"2026-10-14": true})
	if !near(got, 300) {
		t.Fatalf("Target = %v, want 300", got)
	}
	if got := Target(400, nil, "2026-10-12", nil); got != 0 {
		t.Fatalf("no days must give 0, got %v", got)
	}
}

func TestSettle(t *testing.T) {
	// own = 60-20 = 40, capped at 10% of 300 = 30.
	rolled, forgiven := Settle(60, 20, 300, false)
	if !near(rolled, 30) || !near(forgiven, 30) {
		t.Fatalf("got (%v, %v), want (30, 30)", rolled, forgiven)
	}
	// own below the cap rolls whole; the carried-in part is never re-rolled.
	rolled, forgiven = Settle(25, 20, 300, false)
	if !near(rolled, 5) || !near(forgiven, 20) {
		t.Fatalf("got (%v, %v), want (5, 20)", rolled, forgiven)
	}
	// a reduced week receives nothing; the whole debt is forgiven.
	rolled, forgiven = Settle(60, 0, 300, true)
	if rolled != 0 || !near(forgiven, 60) {
		t.Fatalf("got (%v, %v), want (0, 60)", rolled, forgiven)
	}
	// debt that is all carry-in rolls nothing.
	if rolled, _ = Settle(20, 20, 300, false); rolled != 0 {
		t.Fatalf("carried-in debt rolled %v", rolled)
	}
}

func TestSettleFirstWeek(t *testing.T) {
	if r, f := Settle(0, 0, 300, false); r != 0 || f != 0 {
		t.Fatalf("got (%v, %v)", r, f)
	}
}

func TestRepayable(t *testing.T) {
	base := enduranceWorkout("a", "2026-10-15", 3600)
	if !Repayable(base, nil) {
		t.Fatal("plain generated session must be repayable")
	}
	repaid := base
	repaid.Flags = base.Flags.WithAdaptation(why.LoadRepaid)
	if Repayable(repaid, nil) {
		t.Fatal("a session already repaid must not be lengthened again")
	}
	if Repayable(base, map[string]bool{base.Date: true}) {
		t.Fatal("blackout day must not be repayable")
	}
}
