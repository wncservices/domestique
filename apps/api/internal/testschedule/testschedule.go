// Package testschedule decides when to suggest an FTP test. It is pure: a
// plan, a profile, the rider's workouts and goals, and a clock in, a
// suggestion or nothing out. Nothing is stored except the rider's snooze,
// which lives on the profile, so the answer is always recomputed from the
// current plan and can never be stale.
//
// The rules are the FTP tests design's "When a test is suggested", and the
// principle behind them is TrainerRoad's: test when fresh, which means the
// week after a recovery week or at the start of a new block, never in the
// taper, the peak, or a recovery week, and never close to an A event.
package testschedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/fitnesstest"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Reasons a test is suggested, in the order they are tried.
const (
	ReasonNoFTP         = "no_ftp"
	ReasonPlanStart     = "plan_start"
	ReasonEstimatedFTP  = "estimated_ftp"
	ReasonAfterRecovery = "after_recovery"
	ReasonBlockStart    = "block_start"
	ReasonStale         = "stale"
)

const (
	dateLayout = "2006-01-02"
	// staleAfterDays is how long an FTP goes unchecked before a test is
	// suggested. TrainerRoad tests every 4 to 6 weeks; this is 6.
	staleAfterDays = 42
	// aEventQuietDays is how close to an A event no test is suggested: a test
	// is a hard, all-out effort and costs a few days.
	aEventQuietDays = 14
	// justCheckedDays is how long after a ridden test no test is suggested.
	justCheckedDays = 14
	// planStartWeeks is how many weeks a new plan counts as "the start":
	// plan_start applies to plan weeks 1 and 2, so a rider whose first week
	// has no free day, or who created the goal on a Sunday, still gets it.
	planStartWeeks = 2
	// planStartQuietDays is how recent a ridden test must be for plan_start
	// to stay quiet: a test within the last 6 weeks has already set the numbers
	// this plan starts from.
	planStartQuietDays = 42
	// noPlanHorizonDays is how far ahead a rider with no plan is offered a
	// day; a two-week look is enough to find one of their available days.
	noPlanHorizonDays = 14
)

// Input is everything Suggest reads.
type Input struct {
	// Plan is the focus goal's periodization plan, nil when the rider has no
	// goal with a plan covering today.
	Plan    *periodization.Plan
	Profile workout.RiderProfile
	// Upcoming is the rider's workouts dated yesterday or later, as many as
	// are handy: it is used to find a scheduled test, the day-before key
	// sessions, and what already sits on each candidate day.
	Upcoming []workout.Workout
	// Goals are all the rider's goals, for the A-event quiet period.
	Goals []workout.Goal
	// HasPower is true when a completed session with average power exists in
	// the last 90 days. Together with an FTP on file it is what makes a test
	// meaningful: a rider with neither has no power meter to test with.
	HasPower bool
	// LastTestProtocol is the protocol of the rider's most recent completed
	// test ("" if none), so the recommendation is like for like.
	LastTestProtocol string
	// LastTestDate is the date of the rider's most recent test that has been
	// ridden and read ("" if none).
	LastTestDate string
	// Now carries the rider's zone: "today" is its calendar date.
	Now time.Time
}

// Suggestion is a test worth offering, with the day to offer it on.
type Suggestion struct {
	Reason  string
	Message string
	// Date is the suggested day, "YYYY-MM-DD".
	Date string
	// Recommended is the protocol id to pre-select.
	Recommended string
	// ReplacesWorkoutID is the plan-made session on Date that scheduling the
	// test would replace, "" when the day is empty.
	ReplacesWorkoutID string
}

// Suggest returns the test to offer, or nil.
func Suggest(in Input) *Suggestion {
	p := in.Profile
	if p.FTPWatts <= 0 && !in.HasPower {
		return nil
	}
	today := dateOf(in.Now)
	todayStr := today.Format(dateLayout)

	// A snoozed suggestion is silent until the snooze date arrives.
	if p.FTPTestSnoozedUntil != "" && todayStr < p.FTPTestSnoozedUntil {
		return nil
	}
	// A test just ridden ends the nagging for a while: otherwise the reasons
	// that hold all week (the week after recovery, a block start) would bring
	// the banner back the day after. ftp_verified_at is deliberately not part
	// of this: it cannot tell a rider's own save from the startup backfill or
	// an auto-estimate, and counting those silenced every rider for two weeks
	// after deploy and any new plan for two weeks after an estimate. It still
	// drives "stale".
	if checked := (&suggester{today: today}); checked.recent(in.LastTestDate) {
		return nil
	}
	// A test on the calendar ends the nagging: today or later counts.
	for _, w := range in.Upcoming {
		if w.TestProtocol != "" && w.Date >= todayStr {
			return nil
		}
	}

	s := &suggester{in: in, today: today, todayStr: todayStr}
	if cur, ok := s.currentWeek(); ok {
		weeks := in.Plan.Weeks
		for i := cur; i < len(weeks) && i <= cur+1; i++ {
			if s.suggestForWeek(weeks, i) {
				return s.result
			}
		}
		return nil
	}
	return s.suggestWithoutPlan()
}

type suggester struct {
	in       Input
	today    time.Time
	todayStr string
	result   *Suggestion
}

// currentWeek is the index of the plan week containing today.
func (s *suggester) currentWeek() (int, bool) {
	if s.in.Plan == nil {
		return 0, false
	}
	for i, w := range s.in.Plan.Weeks {
		start, ok := parseDate(w.StartDate)
		if ok && !s.today.Before(start) && s.today.Before(start.AddDate(0, 0, 7)) {
			return i, true
		}
	}
	return 0, false
}

// suggestForWeek fills s.result and reports true when week i is a candidate,
// a reason applies, and a day exists.
func (s *suggester) suggestForWeek(weeks []periodization.Week, i int) bool {
	w := weeks[i]
	start, ok := parseDate(w.StartDate)
	if !ok || !s.candidateWeek(w, start) {
		return false
	}
	reason, ok := s.reasonFor(weeks, i)
	if !ok {
		return false
	}
	date, replaces, ok := s.chooseDay(start, start.AddDate(0, 0, 6))
	if !ok {
		return false
	}
	s.result = s.build(reason, date, replaces)
	return true
}

// suggestWithoutPlan is the no-plan case: only "no FTP" and "stale" apply,
// on the next available day.
func (s *suggester) suggestWithoutPlan() *Suggestion {
	reason, ok := s.reasonFor(nil, 0)
	if !ok {
		return nil
	}
	date, replaces, ok := s.chooseDay(s.today.AddDate(0, 0, 1), s.today.AddDate(0, 0, noPlanHorizonDays))
	if !ok {
		return nil
	}
	return s.build(reason, date, replaces)
}

// candidateWeek is a Base or Build week that is not a recovery week and does
// not run into the quiet period before an A event. Taper and peak never
// qualify.
func (s *suggester) candidateWeek(w periodization.Week, start time.Time) bool {
	if w.Recovery || (w.Phase != periodization.PhaseBase && w.Phase != periodization.PhaseBuild) {
		return false
	}
	// A week is out when its last day falls inside the quiet period: being
	// conservative here costs a week's delay, the alternative costs a hard
	// test right before the goal race.
	end := start.AddDate(0, 0, 6)
	for _, g := range s.in.Goals {
		if g.Priority != workout.PriorityA {
			continue
		}
		event, ok := parseDate(g.EventDate)
		if !ok {
			continue
		}
		if !end.Before(event.AddDate(0, 0, -aEventQuietDays)) && !start.After(event) {
			return false
		}
	}
	return true
}

// nearAEvent is the same quiet period, judged for a single day.
func (s *suggester) nearAEvent(day time.Time) bool {
	for _, g := range s.in.Goals {
		if g.Priority != workout.PriorityA {
			continue
		}
		event, ok := parseDate(g.EventDate)
		if ok && !day.Before(event.AddDate(0, 0, -aEventQuietDays)) && !day.After(event) {
			return true
		}
	}
	return false
}

// reasonFor is the first table row that applies to week i (with weeks nil for
// a rider with no plan, where only the FTP rows can).
func (s *suggester) reasonFor(weeks []periodization.Week, i int) (string, bool) {
	p := s.in.Profile
	if p.FTPWatts <= 0 {
		return ReasonNoFTP, true
	}
	// The start of a plan is a natural assessment point, whatever the FTP's
	// verified date says: it is what TrainerRoad and similar do.
	if weeks != nil && weeks[i].Number >= 1 && weeks[i].Number <= planStartWeeks && !s.testWithin(planStartQuietDays) {
		return ReasonPlanStart, true
	}
	// An FTP the app guessed from rides and no ridden test has ever confirmed.
	if p.FTPEstimated && s.in.LastTestDate == "" {
		return ReasonEstimatedFTP, true
	}
	if weeks != nil {
		if i > 0 && weeks[i-1].Recovery {
			return ReasonAfterRecovery, true
		}
		// The plan starts at this week, so week 0 has no known predecessor:
		// a block start needs a visible step up from something that was not
		// Build.
		if weeks[i].Phase == periodization.PhaseBuild && i > 0 && weeks[i-1].Phase != periodization.PhaseBuild {
			return ReasonBlockStart, true
		}
	}
	if s.staleDays() > staleAfterDays {
		return ReasonStale, true
	}
	return "", false
}

// staleDays is days since FTP was last verified; 0 when unknown, so a profile
// with no date (which the startup backfill should have dated) is never nagged.
func (s *suggester) staleDays() int {
	verified, ok := parseDate(s.in.Profile.FTPVerifiedAt)
	if !ok {
		return 0
	}
	return int(s.today.Sub(verified).Hours() / 24)
}

// chooseDay is the first of the rider's available days in [from, to], from
// tomorrow at the earliest, that follows no key session and holds nothing
// the rider built. A plan-made session on the day is fine and is reported so
// scheduling can replace it.
func (s *suggester) chooseDay(from, to time.Time) (date, replaces string, ok bool) {
	tomorrow := s.today.AddDate(0, 0, 1)
	if from.Before(tomorrow) {
		from = tomorrow
	}
	byDate := map[string][]workout.Workout{}
	for _, w := range s.in.Upcoming {
		byDate[w.Date] = append(byDate[w.Date], w)
	}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if !s.available(d) || s.nearAEvent(d) {
			continue
		}
		key := d.Format(dateLayout)
		prev := d.AddDate(0, 0, -1).Format(dateLayout)
		afterKey := false
		for _, w := range byDate[prev] {
			if scheduler.IsKeySession(w) {
				afterKey = true
			}
		}
		if afterKey {
			continue
		}
		riderBuilt, planMade := false, ""
		for _, w := range byDate[key] {
			if isPlanMade(w) {
				if planMade == "" {
					planMade = w.ID
				}
			} else {
				riderBuilt = true
			}
		}
		if riderBuilt {
			continue
		}
		return key, planMade, true
	}
	return "", "", false
}

// available reports whether the rider trains on d. A rider who has not stated
// their days is treated as free any day, rather than never being suggested a
// test at all.
func (s *suggester) available(d time.Time) bool {
	days := s.in.Profile.AvailableDays
	if len(days) == 0 {
		return true
	}
	name := strings.ToLower(d.Weekday().String()[:3])
	for _, day := range days {
		if day == name {
			return true
		}
	}
	return false
}

// isPlanMade mirrors internal/api's own definition of a session the scheduler
// generated, adjusted or not: the only kind a test may replace.
func isPlanMade(w workout.Workout) bool {
	return w.GoalID != "" && strings.HasPrefix(w.Description, scheduler.GeneratedDescription)
}

func (s *suggester) build(reason, date, replaces string) *Suggestion {
	out := &Suggestion{Reason: reason, Date: date, ReplacesWorkoutID: replaces, Recommended: s.recommended()}
	switch reason {
	case ReasonNoFTP:
		out.Message = "Set your FTP with a test"
	case ReasonPlanStart:
		out.Message = fmt.Sprintf("Start your plan with an FTP test. %s would be ideal", weekdayOf(date))
	case ReasonEstimatedFTP:
		out.Message = "Your FTP is an estimate from your rides. A test would pin it down"
	case ReasonAfterRecovery:
		day := "Tuesday"
		if d, ok := parseDate(date); ok {
			day = d.Weekday().String()
		}
		out.Message = fmt.Sprintf("Time for an FTP test. %s after your recovery week would be ideal", day)
	case ReasonBlockStart:
		out.Message = "A new block starts. Test now so its targets are right"
	case ReasonStale:
		out.Message = fmt.Sprintf("It's been %d weeks since your FTP was checked", s.staleDays()/7)
	}
	return out
}

// recommended is the protocol to pre-select: the last test's (like with
// like), else the ramp when FTP is known to start it from, else the
// 20-minute test, which needs no guess.
func (s *suggester) recommended() string {
	if fitnesstest.ValidProtocol(s.in.LastTestProtocol) {
		return s.in.LastTestProtocol
	}
	if s.in.Profile.FTPWatts > 0 {
		return fitnesstest.ProtocolRamp
	}
	return fitnesstest.ProtocolTwentyMinute
}

// weekdayOf names the weekday of date, "Tuesday" when it cannot be parsed.
func weekdayOf(date string) string {
	if d, ok := parseDate(date); ok {
		return d.Weekday().String()
	}
	return "Tuesday"
}

// testWithin reports whether a test was ridden fewer than days ago.
func (s *suggester) testWithin(days int) bool {
	d, ok := parseDate(s.in.LastTestDate)
	return ok && int(s.today.Sub(d).Hours()/24) < days
}

// dateOf is the calendar date of t in t's own zone, as a UTC midnight so day
// arithmetic never crosses a DST boundary.
func dateOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func parseDate(s string) (time.Time, bool) {
	d, err := time.Parse(dateLayout, s)
	return d, err == nil
}

// recent reports whether date (YYYY-MM-DD, "" for none) is fewer than
// justCheckedDays before today. A date in the future counts as recent.
func (s *suggester) recent(date string) bool {
	d, ok := parseDate(date)
	if !ok {
		return false
	}
	return int(s.today.Sub(d).Hours()/24) < justCheckedDays
}
