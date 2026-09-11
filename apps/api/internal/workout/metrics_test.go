package workout

import (
	"math"
	"testing"
)

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestComputeFitnessZeroLoadStaysAtZero(t *testing.T) {
	loads := []DailyLoad{{Date: "2026-01-01"}, {Date: "2026-01-02"}, {Date: "2026-01-03"}}
	snaps := ComputeFitness(loads)
	for _, s := range snaps {
		if s.CTL != 0 || s.ATL != 0 || s.TSB != 0 {
			t.Errorf("%+v: want all zero for an all-rest history", s)
		}
	}
}

// A single day's load raises ATL (7-day time constant) much faster than
// CTL (42-day) — the entire point of tracking both separately. This is the
// property that actually matters, not any one exact number.
func TestComputeFitnessATLRisesFasterThanCTL(t *testing.T) {
	loads := []DailyLoad{{Date: "2026-01-01", Load: 100}, {Date: "2026-01-02"}}
	snaps := ComputeFitness(loads)
	if len(snaps) != 2 {
		t.Fatalf("snapshots = %d, want 2", len(snaps))
	}
	// Day 2's CTL/ATL reflect day 1's single 100-load session.
	day2 := snaps[1]
	if day2.ATL <= day2.CTL {
		t.Errorf("ATL (%v) should have risen further than CTL (%v) after one hard day", day2.ATL, day2.CTL)
	}
	if day2.ATL <= 0 || day2.CTL <= 0 {
		t.Errorf("both should be positive after a 100-load day: ctl=%v atl=%v", day2.CTL, day2.ATL)
	}
}

// TSB for a given day reflects the day *before* it, not that day's own
// session — the property that makes it useful at all: a rider reads it as
// "how fresh do I start today," not "how tired did today leave me."
func TestComputeFitnessTSBLagsByOneDay(t *testing.T) {
	loads := []DailyLoad{{Date: "2026-01-01"}, {Date: "2026-01-02", Load: 200}, {Date: "2026-01-03"}}
	snaps := ComputeFitness(loads)
	// Day 2 (the hard day itself) must still show day 1's form — 0, since
	// day 1 had no load — not already discounted for the load day 2 is
	// about to accumulate.
	if snaps[1].TSB != 0 {
		t.Errorf("day 2 TSB = %v, want 0 (day 2's own load must not affect day 2's own TSB)", snaps[1].TSB)
	}
	// Day 3 should show reduced form, reflecting day 2's hard session.
	if snaps[2].TSB >= 0 {
		t.Errorf("day 3 TSB = %v, want negative (day 2's hard session should show up as reduced form the day after)", snaps[2].TSB)
	}
}

// A long steady stream of identical daily loads converges: CTL and ATL
// both approach the load value itself, and TSB approaches 0 — a rider
// training at a constant, sustainable load eventually reaches a steady
// state, not an ever-rising or ever-falling number.
func TestComputeFitnessConvergesUnderConstantLoad(t *testing.T) {
	var loads []DailyLoad
	for i := 0; i < 200; i++ {
		loads = append(loads, DailyLoad{Date: string(rune('a' + i%26)), Load: 50})
	}
	snaps := ComputeFitness(loads)
	last := snaps[len(snaps)-1]
	if !almostEqual(math.Round(last.CTL), 50) {
		t.Errorf("CTL after 200 days of constant load 50 = %v, want ~50", last.CTL)
	}
	if !almostEqual(math.Round(last.ATL), 50) {
		t.Errorf("ATL after 200 days of constant load 50 = %v, want ~50", last.ATL)
	}
}

func TestTrainingLoadPrefersPowerThenHRThenDuration(t *testing.T) {
	oneHour := 3600.0

	// Power + FTP known: intensity 1.0 (at threshold) for an hour should
	// land at exactly 100 — Coggan's own definition of 1 hour at FTP.
	atThreshold := TrainingLoad(oneHour, 250, 0, RiderProfile{FTPWatts: 250})
	if !almostEqual(atThreshold, 100) {
		t.Errorf("1h at FTP = %v, want 100", atThreshold)
	}

	// No power, HR known: falls back to the HR-based estimate.
	hrOnly := TrainingLoad(oneHour, 0, 150, RiderProfile{MaxHR: 190})
	wantHR := 1.0 * (150.0 / 190.0) * (150.0 / 190.0) * 100
	if !almostEqual(hrOnly, wantHR) {
		t.Errorf("HR-only load = %v, want %v", hrOnly, wantHR)
	}

	// Neither: flat duration-only fallback, still proportional to time.
	durationOnly := TrainingLoad(2*oneHour, 0, 0, RiderProfile{})
	if durationOnly <= 0 {
		t.Errorf("duration-only load = %v, want positive", durationOnly)
	}
	if half := TrainingLoad(oneHour, 0, 0, RiderProfile{}); durationOnly != 2*half {
		t.Errorf("duration-only load should scale linearly with time: 2h=%v, 1h*2=%v", durationOnly, 2*half)
	}
}

func TestFillDailyLoadsProducesNoGaps(t *testing.T) {
	loads, err := fillDailyLoads(map[string]float64{"2026-01-01": 10, "2026-01-05": 20}, "2026-01-01", "2026-01-05")
	if err != nil {
		t.Fatal(err)
	}
	if len(loads) != 5 {
		t.Fatalf("loads = %d, want 5 (one per day, no gaps)", len(loads))
	}
	for i, want := range []float64{10, 0, 0, 0, 20} {
		if loads[i].Load != want {
			t.Errorf("day %d: load = %v, want %v", i, loads[i].Load, want)
		}
	}
}
