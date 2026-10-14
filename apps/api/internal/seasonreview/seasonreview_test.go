package seasonreview

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func ride(id, date string, seconds, distance, ascent, load float64) workout.CompletedSession {
	return workout.CompletedSession{ID: id, Date: date, Sport: "cycling", DurationSeconds: seconds, DistanceM: distance, AscentM: ascent, TrainingLoad: load}
}

func year(y string) Period {
	return Period{Kind: KindYear, Label: y, From: y + "-01-01", To: y + "-12-31"}
}

func TestVolumeSumsTheRidesWithBothBoundaryDaysInclusive(t *testing.T) {
	got := Compute(Input{
		Period: year("2026"), Today: "2026-12-31",
		Sessions: []workout.CompletedSession{
			ride("a", "2025-12-31", 3600, 30000, 100, 50), // last year
			ride("b", "2026-01-01", 3600, 30000, 100, 50), // first day
			ride("c", "2026-06-01", 1800, 10000, 50, 20),
			ride("d", "2026-12-31", 7200, 60000, 200, 90), // last day
			ride("e", "2027-01-01", 3600, 30000, 100, 50), // next year
		},
	})
	v := got.Volume
	if v == nil {
		t.Fatal("no volume section")
	}
	if v.Rides != 3 || v.Seconds != 12600 || v.DistanceM != 100000 || v.TSS != 160 {
		t.Errorf("volume = %+v, want 3 rides, 12600 s, 100 km, 160 TSS (a 31 December ride belongs to that year)", v)
	}
}

func TestElevationIsNotAvailableWithNoAscentAndPartialWithSome(t *testing.T) {
	none := Compute(Input{Period: year("2026"), Today: "2026-12-31", Sessions: []workout.CompletedSession{
		ride("a", "2026-02-01", 3600, 1, 0, 1), ride("b", "2026-02-02", 3600, 1, 0, 1)}})
	if none.Volume.AscentRides != 0 || none.Volume.AscentM != 0 {
		t.Errorf("no ascent reported: %+v, want AscentRides 0 (the UI reads n/a)", none.Volume)
	}
	some := Compute(Input{Period: year("2026"), Today: "2026-12-31", Sessions: []workout.CompletedSession{
		ride("a", "2026-02-01", 3600, 1, 300, 1), ride("b", "2026-02-02", 3600, 1, 0, 1), ride("c", "2026-02-03", 3600, 1, 200, 1)}})
	if some.Volume.AscentRides != 2 || some.Volume.Rides != 3 || some.Volume.AscentM != 500 {
		t.Errorf("some ascent: %+v, want 500 m from 2 of 3 rides", some.Volume)
	}
}

func snaps(pairs ...any) []workout.FitnessSnapshot {
	var out []workout.FitnessSnapshot
	for i := 0; i < len(pairs); i += 3 {
		out = append(out, workout.FitnessSnapshot{Date: pairs[i].(string), CTL: pairs[i+1].(float64), TSB: pairs[i+2].(float64)})
	}
	return out
}

func TestFitnessStartPeakAndEnd(t *testing.T) {
	in := Input{
		Period: year("2026"), Today: "2026-12-31",
		Snapshots: snaps("2025-12-30", 40.0, 0.0, "2025-12-31", 42.0, 0.0, "2026-02-01", 50.0, 0.0, "2026-06-15", 80.0, 0.0, "2026-09-01", 70.0, 0.0, "2026-12-31", 60.0, 0.0),
	}
	f := Compute(in).Fitness
	if f == nil {
		t.Fatal("no fitness section")
	}
	if f.StartCTL == nil || *f.StartCTL != 42 {
		t.Errorf("start = %v, want the snapshot on 31 December (nearest on or before 1 January)", f.StartCTL)
	}
	if f.PeakCTL != 80 || f.PeakDate != "2026-06-15" {
		t.Errorf("peak = %v on %s", f.PeakCTL, f.PeakDate)
	}
	if f.EndCTL == nil || *f.EndCTL != 60 || f.EndDate != "2026-12-31" {
		t.Errorf("end = %v on %s", f.EndCTL, f.EndDate)
	}
}

func TestFitnessEndIsTodayForTheCurrentPeriod(t *testing.T) {
	in := Input{
		Period: year("2026"), Today: "2026-09-10",
		Snapshots: snaps("2026-01-01", 40.0, 0.0, "2026-06-15", 80.0, 0.0, "2026-09-10", 66.0, 0.0, "2026-09-11", 99.0, 0.0),
	}
	f := Compute(in).Fitness
	if f.EndCTL == nil || *f.EndCTL != 66 || f.EndDate != "2026-09-10" {
		t.Errorf("end = %v on %s, want today's, not a later snapshot", f.EndCTL, f.EndDate)
	}
	if f.PeakCTL != 80 {
		t.Errorf("peak = %v: a snapshot after today must not count", f.PeakCTL)
	}
}

func TestFitnessStartIsAbsentWhenHistoryBeginsInsideThePeriod(t *testing.T) {
	f := Compute(Input{Period: year("2026"), Today: "2026-12-31", Snapshots: snaps("2026-03-01", 10.0, 0.0, "2026-04-01", 20.0, 0.0)}).Fitness
	if f.StartCTL != nil {
		t.Errorf("start = %v, want none: the review does not invent a start", *f.StartCTL)
	}
	if f.PeakCTL != 20 {
		t.Errorf("peak = %v", f.PeakCTL)
	}
}

func TestFTPStartAndEndAreTheLastChangeOnOrBeforeEachBoundary(t *testing.T) {
	in := Input{
		Period: year("2026"), Today: "2026-12-31",
		FTPChanges: []workout.FTPChange{
			{Date: "2025-06-01", From: 0, To: 240, Source: "manual"},
			{Date: "2025-12-31", From: 240, To: 250, Source: "detection"},
			{Date: "2026-03-10", From: 250, To: 262, Source: "test"},
			{Date: "2026-12-31", From: 262, To: 270, Source: "manual"},
			{Date: "2027-01-01", From: 270, To: 300, Source: "manual"},
		},
	}
	f := Compute(in).FTP
	if f == nil {
		t.Fatal("no FTP section")
	}
	if f.StartWatts == nil || *f.StartWatts != 250 || f.EndWatts == nil || *f.EndWatts != 270 {
		t.Errorf("start/end = %v/%v, want 250 (31 December counts as before) and 270", f.StartWatts, f.EndWatts)
	}
	if len(f.Changes) != 2 || f.Changes[0].Source != "test" || f.Changes[1].Source != "manual" || f.Changes[0].To != 262 {
		t.Errorf("changes = %+v, want the two inside the year, each with its source", f.Changes)
	}
	if f.HistoryStarts != "" {
		t.Errorf("history starts = %q on a period with a recorded start", f.HistoryStarts)
	}
}

func TestFTPSaysWhereHistoryStartsInsteadOfInventingAStart(t *testing.T) {
	in := Input{
		Period: year("2026"), Today: "2026-12-31", ProfileFTP: 270,
		FTPChanges: []workout.FTPChange{{Date: "2026-05-04", From: 0, To: 255, Source: "manual"}, {Date: "2026-08-01", From: 255, To: 270, Source: "test"}},
	}
	f := Compute(in).FTP
	if f.StartWatts != nil {
		t.Errorf("start = %v, want none before the first recorded change", *f.StartWatts)
	}
	if f.HistoryStarts != "2026-05-04" {
		t.Errorf("history starts = %q", f.HistoryStarts)
	}
	if f.EndWatts == nil || *f.EndWatts != 270 {
		t.Errorf("end = %v", f.EndWatts)
	}
}

func TestFTPUsesTheProfileOnlyWhenThereHasNeverBeenAChange(t *testing.T) {
	f := Compute(Input{Period: year("2026"), Today: "2026-12-31", ProfileFTP: 250}).FTP
	if f == nil || f.StartWatts == nil || *f.StartWatts != 250 || f.EndWatts == nil || *f.EndWatts != 250 || !f.FromProfile {
		t.Errorf("ftp = %+v, want the profile's 250 for both ends, marked as the profile's", f)
	}
	if len(f.Changes) != 0 {
		t.Errorf("changes = %+v", f.Changes)
	}
	// With a change recorded anywhere, a missing earlier value stays missing.
	later := Compute(Input{Period: year("2026"), Today: "2026-12-31", ProfileFTP: 250,
		FTPChanges: []workout.FTPChange{{Date: "2027-02-01", To: 250, Source: "manual"}}}).FTP
	if later != nil && later.StartWatts != nil {
		t.Errorf("a year before the first change has a start of %v", *later.StartWatts)
	}
	if none := Compute(Input{Period: year("2026"), Today: "2026-12-31"}).FTP; none != nil {
		t.Errorf("no FTP at all produced a section: %+v", none)
	}
}

func planned(id, date string) workout.Workout {
	return workout.Workout{ID: id, Date: date, Sport: model.SportCycling, Steps: []workout.WorkoutStep{
		{Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetOpen}}}
}

func TestPlanCountsOnlyDaysAlreadyPastAndDoneOnesFromToday(t *testing.T) {
	in := Input{
		Period: year("2026"), Today: "2026-09-10",
		Workouts: []workout.Workout{
			planned("w1", "2026-09-01"), planned("w2", "2026-09-02"), planned("w3", "2026-09-03"), planned("w4", "2026-09-04"),
			planned("w5", "2026-09-10"), // today, not ridden yet: not counted
			planned("w6", "2026-09-20"), // the future
			planned("w0", "2025-12-30"), // another year
			{ID: "undated"},             // unscheduled
		},
		Sessions: []workout.CompletedSession{
			ride("r1", "2026-09-01", 3600, 1, 0, 1), ride("r2", "2026-09-02", 3600, 1, 0, 1), ride("r3", "2026-09-04", 600, 1, 0, 1), // r3 is a partial
		},
	}
	p := Compute(in).Plan
	if p == nil {
		t.Fatal("no plan section")
	}
	if p.Planned != 4 || p.Done != 2 {
		t.Errorf("plan = %+v, want 4 planned and 2 done: today's unridden session and the future do not count", p)
	}
	if p.CompliancePct == nil || *p.CompliancePct != 50 {
		t.Errorf("compliance = %v, want 50", p.CompliancePct)
	}

	// Today's session, once ridden, counts as planned and done.
	in.Sessions = append(in.Sessions, ride("r5", "2026-09-10", 3600, 1, 0, 1))
	p = Compute(in).Plan
	if p.Planned != 5 || p.Done != 3 {
		t.Errorf("with today ridden: %+v, want 5 planned and 3 done", p)
	}
}

func TestPlanShowsNoPercentageUnderFourPlanned(t *testing.T) {
	in := Input{
		Period: year("2026"), Today: "2026-09-10",
		Workouts: []workout.Workout{planned("w1", "2026-09-01"), planned("w2", "2026-09-02"), planned("w3", "2026-09-03")},
		Sessions: []workout.CompletedSession{ride("r1", "2026-09-01", 3600, 1, 0, 1)},
	}
	p := Compute(in).Plan
	if p.Planned != 3 || p.Done != 1 || p.CompliancePct != nil {
		t.Errorf("plan = %+v, want counts without a percentage under 4 planned", p)
	}
	if none := Compute(Input{Period: year("2026"), Today: "2026-09-10"}).Plan; none != nil {
		t.Errorf("a period with nothing planned has a plan section: %+v", none)
	}
}

func TestBestEffortsAreTheAwardsDatedInThePeriod(t *testing.T) {
	in := Input{
		Period: year("2026"), Today: "2026-12-31",
		Efforts: []workout.BestEffort{
			{SessionID: "a", Metric: "power_1200", Date: "2025-12-31", Value: 300, Award: "all_time"},
			{SessionID: "b", Metric: "power_1200", Date: "2026-01-01", Value: 305, Award: "all_time"},
			{SessionID: "c", Metric: "max_hr", Date: "2026-07-01", Value: 190, Award: "season"},
			{SessionID: "d", Metric: "power_300", Date: "2026-07-02", Value: 400}, // no award
			{SessionID: "e", Metric: "power_5", Date: "2027-01-01", Value: 900, Award: "season"},
		},
	}
	got := Compute(in).BestEfforts
	if len(got) != 2 || got[0].SessionID != "b" || got[0].Label != "20-min power" || got[0].Unit != "W" || got[1].SessionID != "c" || got[1].Scope != "season" {
		t.Errorf("awards = %+v", got)
	}
	if empty := Compute(Input{Period: year("2026"), Today: "2026-12-31"}).BestEfforts; empty == nil || len(empty) != 0 {
		t.Errorf("no awards = %#v, want an empty list (a JSON array)", empty)
	}
}

func TestGoalsInThePeriodCarryEventDayFormAndTheLongestRide(t *testing.T) {
	in := Input{
		Period: year("2026"), Today: "2026-12-31",
		Goals: []workout.Goal{
			{ID: "g1", Name: "Gran Fondo", EventDate: "2026-06-14", Priority: workout.PriorityA},
			{ID: "g2", Name: "Next year", EventDate: "2027-05-01", Priority: workout.PriorityB},
			{ID: "g3", Name: "Nothing found", EventDate: "2026-09-01", Priority: workout.PriorityC},
			{ID: "g4", Name: "Not dated"},
		},
		Snapshots: snaps("2026-06-14", 78.4, -6.2),
		Sessions: []workout.CompletedSession{
			ride("short", "2026-06-14", 1800, 15000, 0, 30),
			ride("long", "2026-06-14", 21600, 160000, 0, 280),
			ride("other", "2026-06-13", 99999, 1, 0, 1),
		},
	}
	goals := Compute(in).Goals
	if len(goals) != 2 || goals[0].ID != "g1" || goals[1].ID != "g3" {
		t.Fatalf("goals = %+v, want the two with an event date in the year, in date order", goals)
	}
	g := goals[0]
	if g.CTL == nil || *g.CTL != 78.4 || g.TSB == nil || *g.TSB != -6.2 || g.Priority != "A" {
		t.Errorf("event-day form = %+v", g)
	}
	if g.Ride == nil || g.Ride.SessionID != "long" || g.Ride.Seconds != 21600 || g.Ride.DistanceM != 160000 || g.Ride.TSS != 280 {
		t.Errorf("event-day ride = %+v, want the longest of the day", g.Ride)
	}
	if g.ProjectedCTL != nil {
		t.Error("projectedCtl is reserved and stays unset")
	}
	none := goals[1]
	if none.Ride != nil || none.CTL != nil {
		t.Errorf("a goal with no data that day = %+v, want no ride and no form (and no zero)", none)
	}
}

func TestGoalPeriodRunsFromTheGoalsCreationToItsEvent(t *testing.T) {
	g := workout.Goal{ID: "g1", Name: "Gran Fondo", EventDate: "2026-06-14", CreatedAt: "2026-01-20T08:30:00Z"}
	p, ok := GoalPeriod(g)
	if !ok || p.Kind != KindGoal || p.From != "2026-01-20" || p.To != "2026-06-14" || p.Label != "Gran Fondo" {
		t.Errorf("period = %+v %v", p, ok)
	}
	for name, bad := range map[string]workout.Goal{
		"no event date":           {CreatedAt: "2026-01-20T08:30:00Z"},
		"created after the event": {EventDate: "2026-01-01", CreatedAt: "2026-01-20T08:30:00Z"},
		"no creation time":        {EventDate: "2026-06-14"},
		"unparseable event":       {EventDate: "June", CreatedAt: "2026-01-20T08:30:00Z"},
	} {
		if _, ok := GoalPeriod(bad); ok {
			t.Errorf("%s gave a period", name)
		}
	}
}

func TestAGoalPeriodIsReviewedLikeAYear(t *testing.T) {
	g := workout.Goal{ID: "g1", Name: "Gran Fondo", EventDate: "2026-06-14", CreatedAt: "2026-01-20T08:30:00Z"}
	p, _ := GoalPeriod(g)
	got := Compute(Input{Period: p, Today: "2026-12-31", Goals: []workout.Goal{g}, Sessions: []workout.CompletedSession{
		ride("a", "2026-01-19", 3600, 1, 0, 1), ride("b", "2026-01-20", 3600, 1, 0, 1), ride("c", "2026-06-14", 3600, 1, 0, 1), ride("d", "2026-06-15", 3600, 1, 0, 1)}})
	if got.Volume.Rides != 2 {
		t.Errorf("rides = %d, want only the 20th and the event day", got.Volume.Rides)
	}
	if len(got.Goals) != 1 {
		t.Errorf("goals = %+v, want the goal itself", got.Goals)
	}
}

func TestAnEmptyPeriodHasEmptySectionsNotZeros(t *testing.T) {
	got := Compute(Input{Period: year("2019"), Today: "2026-12-31",
		Sessions: []workout.CompletedSession{ride("a", "2026-01-01", 3600, 1, 0, 1)}})
	if got.Volume != nil || got.Fitness != nil || got.FTP != nil || got.Plan != nil {
		t.Errorf("sections of an empty period = %+v %+v %+v %+v, want all absent", got.Volume, got.Fitness, got.FTP, got.Plan)
	}
	if got.BestEfforts == nil || got.Goals == nil || len(got.BestEfforts) != 0 || len(got.Goals) != 0 {
		t.Errorf("lists = %#v %#v, want empty arrays", got.BestEfforts, got.Goals)
	}
	if got.Period.Label != "2019" {
		t.Errorf("period = %+v", got.Period)
	}
}
