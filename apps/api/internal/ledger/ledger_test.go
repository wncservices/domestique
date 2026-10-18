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
	rolled, forgiven := Settle(60, 20, 300, false, false)
	if !near(rolled, 30) || !near(forgiven, 30) {
		t.Fatalf("got (%v, %v), want (30, 30)", rolled, forgiven)
	}
	// own below the cap rolls whole; the carried-in part is never re-rolled.
	rolled, forgiven = Settle(25, 20, 300, false, false)
	if !near(rolled, 5) || !near(forgiven, 20) {
		t.Fatalf("got (%v, %v), want (5, 20)", rolled, forgiven)
	}
	// a reduced week receives nothing; the whole debt is forgiven.
	rolled, forgiven = Settle(60, 0, 300, true, false)
	if rolled != 0 || !near(forgiven, 60) {
		t.Fatalf("got (%v, %v), want (0, 60)", rolled, forgiven)
	}
	// debt that is all carry-in rolls nothing.
	if rolled, _ = Settle(20, 20, 300, false, false); rolled != 0 {
		t.Fatalf("carried-in debt rolled %v", rolled)
	}
}

// A recovery or taper week's shortfall was the plan's easing, not a debt: it
// rolls into nothing even when the next week is a full one.
func TestSettleForgivesAReducedLastWeekWhole(t *testing.T) {
	rolled, forgiven := Settle(60, 0, 300, false, true)
	if rolled != 0 || !near(forgiven, 60) {
		t.Fatalf("got (%v, %v), want (0, 60)", rolled, forgiven)
	}
}

func TestSettleFirstWeek(t *testing.T) {
	if r, f := Settle(0, 0, 300, false, false); r != 0 || f != 0 {
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

func TestOpenDaysAndEffectiveTarget(t *testing.T) {
	days := []string{"mon", "wed", "fri", "sun"}
	if got := OpenDays(days, "2026-10-05", nil); got != 4 {
		t.Errorf("OpenDays = %d, want 4", got)
	}
	if got := OpenDays(days, "2026-10-05", map[string]bool{"2026-10-07": true, "2026-10-06": true}); got != 3 {
		t.Errorf("OpenDays with wed out = %d, want 3", got)
	}
	cases := []struct {
		target    float64
		open, now int
		want      float64
	}{
		{400, 4, 4, 400}, {400, 4, 3, 300}, {400, 4, 0, 0}, {400, 4, 5, 400}, {400, 0, 2, 400},
	}
	for _, c := range cases {
		if got := EffectiveTarget(c.target, c.open, c.now); got != c.want {
			t.Errorf("EffectiveTarget(%v,%d,%d) = %v, want %v", c.target, c.open, c.now, got, c.want)
		}
	}
}
