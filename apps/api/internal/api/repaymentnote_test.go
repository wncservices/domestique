package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/ledger"
)

// Each way the ledger changes a session gets its own sentence, so a rider can
// tell a longer ride from a harder one.
func TestRepaymentNoteSaysWhatChanged(t *testing.T) {
	cases := []struct {
		a    ledger.Action
		want string
	}{
		{ledger.Action{Kind: "lengthen"}, "Longer than planned, to catch up on missed training."},
		{ledger.Action{Kind: "upgrade"}, "Harder than planned, to catch up on missed training."},
		{ledger.Action{Kind: "push", Level: 4}, "A step harder today: you are fresh and behind this week."},
		{ledger.Action{Kind: "push"}, "A bit longer today: you are fresh and behind this week."},
	}
	for _, c := range cases {
		if got := repaymentNote(c.a); got != c.want {
			t.Errorf("repaymentNote(%+v) = %q, want %q", c.a, got, c.want)
		}
	}
}
