package projection

import (
	"math"
	"strings"
	"testing"
)

// weeklyCTL builds a daily series whose CTL on each given Monday is as listed.
func mondaySeries(start string, ctls ...float64) []Point {
	d := mustDate(start)
	var pts []Point
	for i, c := range ctls {
		pts = append(pts, Point{Date: d.AddDate(0, 0, 7*i).Format(dateLayout), CTL: c})
	}
	return pts
}

func TestRampsPerMondayWithBoundaryAt8(t *testing.T) {
	// 2026-10-05 is a Monday.
	r := Ramps(mondaySeries("2026-10-05", 40, 48, 56.01, 60))
	if len(r.Warnings) != 1 || r.Warnings[0].WeekStart != "2026-10-12" {
		t.Fatalf("warnings = %+v, want only the week of 12 Oct (+8.01); +8.0 must not warn", r.Warnings)
	}
	w := r.Warnings[0]
	if math.Abs(w.PerWeek-8.01) > 1e-9 || math.Abs(w.ExcessTSS-0.01*42) > 1e-9 {
		t.Errorf("warning = %+v, want perWeek 8.01, excess %v", w, 0.01*42)
	}
	if math.Abs(r.MaxPerWeek-8.01) > 1e-9 {
		t.Errorf("max = %v, want 8.01", r.MaxPerWeek)
	}
}

func TestRampExcessIsTimes42(t *testing.T) {
	r := Ramps(mondaySeries("2026-10-05", 40, 51))
	if len(r.Warnings) != 1 || math.Abs(r.Warnings[0].ExcessTSS-(11-8)*42) > 1e-9 {
		t.Errorf("warnings = %+v, want excess 126", r.Warnings)
	}
	if r.Warnings[0].WeekStart != "2026-10-05" {
		t.Errorf("week start = %s, want the Monday the week began on", r.Warnings[0].WeekStart)
	}
}

func TestRampIsExemptWhenCTLIsUnder20(t *testing.T) {
	// Weeks starting at 5 and 19.9 ramp by 14.9 and 0.1... the first two are
	// exempt (start under 20) whatever they ramp; only the week starting at 20 counts.
	r := Ramps(mondaySeries("2026-10-05", 5, 19.9, 20, 40))
	if len(r.Warnings) != 1 || r.Warnings[0].WeekStart != "2026-10-19" {
		t.Errorf("warnings = %+v, want only the week of 19 Oct (it starts at CTL 20)", r.Warnings)
	}
}

func TestRampsIgnoreNonMondaysAndShortSeries(t *testing.T) {
	if r := Ramps([]Point{{Date: "2026-10-06", CTL: 40}, {Date: "2026-10-13", CTL: 90}}); len(r.Warnings) != 0 || r.MaxPerWeek != 0 {
		t.Errorf("Tuesdays measured as a week: %+v", r)
	}
	if r := Ramps(nil); len(r.Warnings) != 0 {
		t.Errorf("nil series warned: %+v", r)
	}
}

func TestRollWithHistoryKeepsTheGapDaysRampsNeed(t *testing.T) {
	// Snapshot on Mon 5 Oct, today Thu 8 Oct: the series must still contain the
	// Monday so the current week's ramp can be read.
	pts := RollWithHistory(Input{Start: snap("2026-10-05", 40, 40), Today: "2026-10-08", Event: "2026-10-20"})
	if pts[0].Date != "2026-10-05" {
		t.Errorf("history starts %s, want the snapshot date", pts[0].Date)
	}
	if got := Roll(Input{Start: snap("2026-10-05", 40, 40), Today: "2026-10-08", Event: "2026-10-20"}); got[0].Date != "2026-10-08" {
		t.Errorf("Roll starts %s, want today", got[0].Date)
	}
}

func fatiguedInput() (Input, Band) {
	// Snapshot today with CTL 60, ATL 75: a sharp negative form that a taper can fix.
	planned := map[string]float64{}
	for d := mustDate("2026-10-05"); d.Before(mustDate("2026-11-30")); d = d.AddDate(0, 0, 1) {
		planned[d.Format(dateLayout)] = 90
	}
	return Input{Start: snap("2026-10-05", 60, 75), Planned: planned, Today: "2026-10-05", Event: "2026-11-30"}, band515
}

func TestTaperFindsTheFirstStartThatReachesTheBandLow(t *testing.T) {
	in, band := fatiguedInput()
	s := TaperWhatIf(in, band)
	if s == nil || !s.Reaches {
		t.Fatalf("taper = %+v, want a start day that reaches the band", s)
	}
	// Re-derive by hand: every shorter taper must miss, the chosen one must hit.
	for _, d := range TaperDays {
		tsb := tsbWithTaper(in, d)
		if d < s.Days && math.Round(tsb) >= band.Low {
			t.Errorf("a %d-day taper already reaches the band (%v) but %d was chosen", d, tsb, s.Days)
		}
		if d == s.Days && (math.Round(tsb) < band.Low || math.Abs(tsb-s.TSB) > 1e-9) {
			t.Errorf("chosen %d-day taper lands %v, reported %v", d, tsb, s.TSB)
		}
	}
	if !strings.Contains(s.Text, "taper") || !strings.Contains(s.Text, "days out") {
		t.Errorf("text = %q", s.Text)
	}
}

func TestTaperSaysSoWhenNoneReaches(t *testing.T) {
	in, band := fatiguedInput()
	// A plan so heavy that even 21 days at 60 % of it cannot clear the hole.
	for date := range in.Planned {
		in.Planned[date] = 400
	}
	s := TaperWhatIf(in, band)
	if s == nil || s.Reaches || !strings.Contains(s.Text, "No taper") {
		t.Errorf("taper = %+v, want one saying none reaches the band", s)
	}
}

func TestTaperIsNotOfferedUnderSevenDaysOut(t *testing.T) {
	in, band := fatiguedInput()
	in.Event = "2026-10-11" // 6 days
	if s := TaperWhatIf(in, band); s != nil {
		t.Errorf("taper offered for a 6-day-out event: %+v", s)
	}
	in.Event = "2026-10-12" // 7 days: offered
	if s := TaperWhatIf(in, band); s == nil {
		t.Error("taper not offered 7 days out")
	}
}

func TestTaperSkipsStartsEarlierThanToday(t *testing.T) {
	in, band := fatiguedInput()
	in.Event = "2026-10-15" // 10 days: 14 and 21 are not available
	s := TaperWhatIf(in, band)
	if s == nil || s.Days > 10 {
		t.Errorf("taper = %+v, want a start no earlier than today", s)
	}
}

func TestSuggestPrefersTheTaperThenTheRampAndOffersNothingForTheRest(t *testing.T) {
	in, band := fatiguedInput()
	ramp := Ramp{MaxPerWeek: 11, Warnings: []RampWarning{{WeekStart: "2026-11-02", PerWeek: 11, ExcessTSS: 126}}}

	if s := Suggest(in, Verdict{Key: KeyFatigued}, band, ramp); s == nil || !strings.Contains(s.Text, "taper") {
		t.Errorf("fatigued suggestion = %+v, want the taper", s)
	}
	s := Suggest(in, Verdict{Key: KeyOnTrack}, band, ramp)
	if s == nil || s.ExcessTSS != 126 || s.Text != "Week of 2 Nov ramps +11 CTL, about 126 TSS over a sustainable build" {
		t.Errorf("ramp suggestion = %+v", s)
	}
	if s := Suggest(in, Verdict{Key: KeyFresh}, band, Ramp{}); s != nil {
		t.Errorf("fresh with no ramp: %+v, want none", s)
	}
	if s := Suggest(in, Verdict{Key: KeyUndertrained}, band, Ramp{}); s != nil {
		t.Errorf("undertrained: %+v, want none (no fix inside the plan)", s)
	}
	short := in
	short.Event = "2026-10-08"
	if s := Suggest(short, Verdict{Key: KeyFatigued}, band, ramp); s != nil {
		t.Errorf("event under 7 days away: %+v, want none", s)
	}
}
