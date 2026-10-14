// Package seasonreview summarises one period of a rider's training: volume,
// fitness, FTP, plan compliance, records set and goals. It is pure. The caller
// reads the stored rows and says what "today" is; nothing here touches a store
// or a clock, and every boundary is a "YYYY-MM-DD" string compared as a string,
// so a ride dated 31 December belongs to that year in every timezone.
//
// A section the data cannot speak to is absent (nil), never a zero: a rider
// with no recorded FTP history has no FTP start, not a start of 0 W.
//
// Design: docs/superpowers/specs/2026-09-29-season-review-design.md.
package seasonreview

import (
	"math"
	"sort"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/bestefforts"
	"github.com/wncservices/domestique/apps/api/internal/compliance"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Period kinds.
const (
	KindYear = "year"
	KindGoal = "goal"
)

const dateLayout = "2006-01-02"

// Period is the stretch being reviewed; From and To are inclusive dates.
type Period struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// Input is everything Compute reads. Sessions, Snapshots, Workouts and Goals
// are the rider's own, any dates; Compute picks the ones in range. FTPChanges is
// the whole history, because the start of a period is the last change before
// it. Efforts are the rider's best-effort rows (awards are picked out of them).
type Input struct {
	Period Period
	// Today is the rider's local date. A period that has not ended is reviewed
	// up to today.
	Today      string
	Sessions   []workout.CompletedSession
	Snapshots  []workout.FitnessSnapshot
	FTPChanges []workout.FTPChange
	// ProfileFTP is the FTP on the profile now, used only when there has never
	// been a recorded change.
	ProfileFTP float64
	Workouts   []workout.Workout
	Efforts    []workout.BestEffort
	Goals      []workout.Goal
}

// Review is the answer. The JSON shape is the API's (GET /api/training/season).
type Review struct {
	Period      Period       `json:"period"`
	Volume      *Volume      `json:"volume,omitempty"`
	Fitness     *Fitness     `json:"fitness,omitempty"`
	FTP         *FTP         `json:"ftp,omitempty"`
	Plan        *Plan        `json:"plan,omitempty"`
	BestEfforts []Award      `json:"bestEfforts"`
	Goals       []GoalResult `json:"goals"`
}

// Volume is what the rides in the period add up to. A ride that reported no
// ascent is not flat: AscentRides says how many did, and the total is only as
// good as that.
type Volume struct {
	Rides       int     `json:"rides"`
	Seconds     float64 `json:"seconds"`
	DistanceM   float64 `json:"distanceM"`
	AscentM     float64 `json:"ascentM"`
	AscentRides int     `json:"ascentRides"`
	TSS         float64 `json:"tss"`
}

// Fitness is CTL at the start, its peak and where it ended. StartCTL is absent
// when the history begins inside the period.
type Fitness struct {
	StartCTL *float64 `json:"startCtl,omitempty"`
	PeakCTL  float64  `json:"peakCtl"`
	PeakDate string   `json:"peakDate"`
	EndCTL   *float64 `json:"endCtl,omitempty"`
	EndDate  string   `json:"endDate,omitempty"`
}

// FTP is the period's FTP story. StartWatts is absent before the first recorded
// change, and HistoryStarts then says when recording began. FromProfile marks
// the one case where the profile's value stands in: a rider whose FTP has never
// changed on record, in a period that includes today.
type FTP struct {
	StartWatts    *float64    `json:"startWatts,omitempty"`
	EndWatts      *float64    `json:"endWatts,omitempty"`
	Changes       []FTPChange `json:"changes"`
	HistoryStarts string      `json:"historyStarts,omitempty"`
	FromProfile   bool        `json:"fromProfile,omitempty"`
}

// FTPChange is one move inside the period.
type FTPChange struct {
	Date   string  `json:"date"`
	From   float64 `json:"from"`
	To     float64 `json:"to"`
	Source string  `json:"source"`
}

// Plan is how much of the plan was ridden, over the days already behind us.
// CompliancePct is absent under four planned sessions: one missed session out
// of two is not a percentage worth printing.
type Plan struct {
	Planned       int  `json:"planned"`
	Done          int  `json:"done"`
	CompliancePct *int `json:"compliancePct,omitempty"`
}

// MinPlannedForPercent is how many planned sessions it takes to show a percent.
const MinPlannedForPercent = 4

// Award is a record set in the period.
type Award struct {
	SessionID string  `json:"sessionId"`
	Metric    string  `json:"metric"`
	Label     string  `json:"label"`
	Unit      string  `json:"unit"`
	Scope     string  `json:"scope"`
	Value     float64 `json:"value"`
	Previous  float64 `json:"previous"`
	Date      string  `json:"date"`
}

// GoalResult is a goal whose event falls in the period.
type GoalResult struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	EventDate string `json:"eventDate"`
	Priority  string `json:"priority"`
	// CTL and TSB are the rider's form on the event day, absent when there is
	// no snapshot for it (the event is still ahead, or before the history).
	CTL *float64 `json:"ctl,omitempty"`
	TSB *float64 `json:"tsb,omitempty"`
	// Ride is the longest session of the event day, absent when none was found.
	Ride *EventRide `json:"ride,omitempty"`
	// ProjectedCTL is reserved: when race-day projection stores its figure it
	// goes beside the actual. Never set today, so the UI needs no reshuffle.
	ProjectedCTL *float64 `json:"projectedCtl,omitempty"`
}

// EventRide is the session ridden on an event day.
type EventRide struct {
	SessionID string  `json:"sessionId"`
	Sport     string  `json:"sport"`
	Seconds   float64 `json:"seconds"`
	DistanceM float64 `json:"distanceM"`
	TSS       float64 `json:"tss"`
}

func inRange(date, from, to string) bool { return date >= from && date <= to }

func minDate(a, b string) string {
	if a < b {
		return a
	}
	return b
}

func ptr(v float64) *float64 { return &v }

// Compute reviews in.Period.
func Compute(in Input) Review {
	p := in.Period
	// A period that has not ended is reviewed up to today.
	end := minDate(p.To, in.Today)

	review := Review{Period: p, BestEfforts: []Award{}, Goals: []GoalResult{}}
	review.Volume = volume(in.Sessions, p.From, p.To)
	review.Fitness = fitness(in.Snapshots, p.From, end)
	review.FTP = ftp(in, end)
	review.Plan = plan(in, end)
	review.BestEfforts = awards(in.Efforts, p.From, p.To)
	review.Goals = goals(in)
	return review
}

func volume(sessions []workout.CompletedSession, from, to string) *Volume {
	var v Volume
	for _, s := range sessions {
		if !inRange(s.Date, from, to) {
			continue
		}
		v.Rides++
		v.Seconds += s.DurationSeconds
		v.DistanceM += s.DistanceM
		v.TSS += s.TrainingLoad
		if s.AscentM > 0 {
			v.AscentRides++
			v.AscentM += s.AscentM
		}
	}
	if v.Rides == 0 {
		return nil
	}
	return &v
}

func sortedSnapshots(in []workout.FitnessSnapshot) []workout.FitnessSnapshot {
	out := append([]workout.FitnessSnapshot(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// onOrBefore is the last snapshot dated on or before date.
func onOrBefore(snaps []workout.FitnessSnapshot, date string) (workout.FitnessSnapshot, bool) {
	var found workout.FitnessSnapshot
	ok := false
	for _, s := range snaps {
		if s.Date > date {
			break
		}
		found, ok = s, true
	}
	return found, ok
}

func fitness(all []workout.FitnessSnapshot, from, end string) *Fitness {
	snaps := sortedSnapshots(all)
	var f Fitness
	seen := false
	for _, s := range snaps {
		if !inRange(s.Date, from, end) {
			continue
		}
		if !seen || s.CTL > f.PeakCTL {
			f.PeakCTL, f.PeakDate = s.CTL, s.Date
		}
		seen = true
	}
	if !seen {
		return nil
	}
	if s, ok := onOrBefore(snaps, from); ok {
		f.StartCTL = ptr(s.CTL)
	}
	if s, ok := onOrBefore(snaps, end); ok {
		f.EndCTL, f.EndDate = ptr(s.CTL), s.Date
	}
	return &f
}

func ftp(in Input, end string) *FTP {
	changes := append([]workout.FTPChange(nil), in.FTPChanges...)
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Date != changes[j].Date {
			return changes[i].Date < changes[j].Date
		}
		if changes[i].ChangedAt != changes[j].ChangedAt {
			return changes[i].ChangedAt < changes[j].ChangedAt
		}
		return changes[i].ID < changes[j].ID
	})
	p := in.Period

	if len(changes) == 0 {
		// No change was ever recorded. The profile's value is all there is, and
		// it is a statement about now: only a period that includes today may use
		// it, and it says so.
		if in.ProfileFTP > 0 && inRange(in.Today, p.From, p.To) {
			return &FTP{StartWatts: ptr(in.ProfileFTP), EndWatts: ptr(in.ProfileFTP), Changes: []FTPChange{}, FromProfile: true}
		}
		return nil
	}

	f := FTP{Changes: []FTPChange{}, HistoryStarts: ""}
	var start, last *workout.FTPChange
	for i := range changes {
		c := &changes[i]
		if c.Date <= p.From {
			start = c
		}
		if c.Date <= end {
			last = c
		}
		if inRange(c.Date, p.From, end) {
			f.Changes = append(f.Changes, FTPChange{Date: c.Date, From: c.From, To: c.To, Source: c.Source})
		}
	}
	if start != nil {
		f.StartWatts = ptr(start.To)
	} else {
		f.HistoryStarts = changes[0].Date
	}
	if last != nil {
		f.EndWatts = ptr(last.To)
	}
	if f.StartWatts == nil && f.EndWatts == nil {
		// The period is entirely before the first recorded change: there is
		// nothing to say about it, and saying when history starts would be an
		// answer to a question the period did not ask.
		return nil
	}
	return &f
}

func plan(in Input, end string) *Plan {
	p := in.Period
	byDay := map[string][]workout.Workout{}
	for _, w := range in.Workouts {
		if w.Date != "" && inRange(w.Date, p.From, end) {
			byDay[w.Date] = append(byDay[w.Date], w)
		}
	}
	ridden := map[string][]workout.CompletedSession{}
	for _, s := range in.Sessions {
		if _, planned := byDay[s.Date]; planned {
			ridden[s.Date] = append(ridden[s.Date], s)
		}
	}

	var out Plan
	for date, ws := range byDay {
		status := compliance.Day(date, in.Today, ws, ridden[date])
		// Days behind us count. Today counts only once it is done: a session
		// still to be ridden is not a miss yet.
		if date > in.Today || (date == in.Today && status != compliance.StatusDone) {
			continue
		}
		out.Planned += len(ws)
		if status == compliance.StatusDone {
			out.Done += len(ws)
		}
	}
	if out.Planned == 0 {
		return nil
	}
	if out.Planned >= MinPlannedForPercent {
		pct := int(math.Round(100 * float64(out.Done) / float64(out.Planned)))
		out.CompliancePct = &pct
	}
	return &out
}

func awards(efforts []workout.BestEffort, from, to string) []Award {
	order := map[string]int{}
	for i, m := range bestefforts.Metrics() {
		order[m.Key] = i
	}
	out := []Award{}
	for _, e := range efforts {
		if e.Award == "" || !inRange(e.Date, from, to) {
			continue
		}
		a := Award{SessionID: e.SessionID, Metric: e.Metric, Scope: e.Award, Value: e.Value, Previous: e.Previous, Date: e.Date}
		for _, m := range bestefforts.Metrics() {
			if m.Key == e.Metric {
				a.Label, a.Unit = m.Label, m.Unit
			}
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		if out[i].SessionID != out[j].SessionID {
			return out[i].SessionID < out[j].SessionID
		}
		return order[out[i].Metric] < order[out[j].Metric]
	})
	return out
}

func goals(in Input) []GoalResult {
	p := in.Period
	snaps := map[string]workout.FitnessSnapshot{}
	for _, s := range in.Snapshots {
		snaps[s.Date] = s
	}
	out := []GoalResult{}
	for _, g := range in.Goals {
		if g.EventDate == "" || !inRange(g.EventDate, p.From, p.To) {
			continue
		}
		r := GoalResult{ID: g.ID, Name: g.Name, EventDate: g.EventDate, Priority: string(g.Priority)}
		if s, ok := snaps[g.EventDate]; ok {
			r.CTL, r.TSB = ptr(s.CTL), ptr(s.TSB)
		}
		var best *workout.CompletedSession
		for i := range in.Sessions {
			s := &in.Sessions[i]
			if s.Date == g.EventDate && (best == nil || s.DurationSeconds > best.DurationSeconds) {
				best = s
			}
		}
		if best != nil {
			r.Ride = &EventRide{SessionID: best.ID, Sport: best.Sport, Seconds: best.DurationSeconds, DistanceM: best.DistanceM, TSS: best.TrainingLoad}
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].EventDate != out[j].EventDate {
			return out[i].EventDate < out[j].EventDate
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// GoalPeriod is a goal's own season: from the day it was created, the anchor of
// its plan, to its event. ok is false for a goal that has no event date, was
// created after it, or whose dates cannot be read.
func GoalPeriod(g workout.Goal) (Period, bool) {
	if len(g.CreatedAt) < len(dateLayout) {
		return Period{}, false
	}
	from := g.CreatedAt[:len(dateLayout)]
	if _, err := time.Parse(dateLayout, from); err != nil {
		return Period{}, false
	}
	if _, err := time.Parse(dateLayout, g.EventDate); err != nil {
		return Period{}, false
	}
	if from > g.EventDate {
		return Period{}, false
	}
	return Period{Kind: KindGoal, Label: g.Name, From: from, To: g.EventDate}, true
}
