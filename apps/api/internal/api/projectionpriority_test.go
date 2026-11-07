package api_test

import (
	"net/http"
	"testing"
)

// weekLoad sums the projected daily load between two dates, inclusive.
func (p projectionBody) weekLoad(from, to string) float64 {
	total := 0.0
	for _, pt := range p.Points {
		if pt.Date >= from && pt.Date <= to {
			total += pt.Load
		}
	}
	return total
}

// A C race has no taper in its plan, and the projection reads the plan: the
// event week carries ordinary load and the race-day form is lower than the
// same race at priority A, whose taper freshens the rider.
func TestProjectionOfACRaceShowsItsFormWithoutATaper(t *testing.T) {
	read := func(priority string) projectionBody {
		h := newProjectionHarness(t)
		g := h.goal(t, "Club race", "2026-12-13", priority)
		status, p := h.get(t, "wilant", "?goal="+g.ID)
		if status != http.StatusOK || !p.Available || p.RaceDay == nil {
			t.Fatalf("%s: status %d, available %v (%s)", priority, status, p.Available, p.Reason)
		}
		return p
	}
	a, c := read("A"), read("C")

	// Mon 2026-12-07 .. Sat 2026-12-12 is the event week up to the race; the week before it is 11-30..12-06.
	aWeek, aBefore := a.weekLoad("2026-12-07", "2026-12-13"), a.weekLoad("2026-11-30", "2026-12-06")
	cWeek, cBefore := c.weekLoad("2026-12-07", "2026-12-13"), c.weekLoad("2026-11-30", "2026-12-06")
	if aBefore == 0 || cBefore == 0 {
		t.Fatalf("the week before the race holds no load: A %v, C %v", aBefore, cBefore)
	}
	if aWeek/aBefore > 0.7 {
		t.Errorf("priority A event week is %.2f of the week before, want a taper", aWeek/aBefore)
	}
	// The race day itself carries no load in the projection, so a Sunday race
	// loses its long ride from an otherwise ordinary week.
	if cWeek/cBefore < 0.4 || cWeek/cBefore < aWeek/aBefore+0.15 {
		t.Errorf("priority C event week is %.2f of the week before (A: %.2f), want an ordinary week", cWeek/cBefore, aWeek/aBefore)
	}
	if c.RaceDay.TSB >= a.RaceDay.TSB {
		t.Errorf("race-day form: C %v, A %v; without a taper the form is lower", c.RaceDay.TSB, a.RaceDay.TSB)
	}
}
