// Package projection rolls a rider's fitness forward from their latest
// snapshot to an event date, using the sessions they have ridden and the
// workouts they have planned, and judges the result against a target form.
// It is pure: callers hand it the loads and it hands back numbers, so the
// endpoint stays a thin assembly of reads and nothing here touches a store.
package projection

import (
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// MaxDays caps how far ahead a projection runs. Beyond it the plan is
// guesswork stacked on guesswork, and the series would be needlessly long.
const MaxDays = 400

const dateLayout = "2006-01-02"

// Input is everything Roll needs. Dates are YYYY-MM-DD strings, as in
// workout.ComputeFitness, and Start's CTL/ATL are the values at the start of
// its date.
type Input struct {
	Start workout.FitnessSnapshot
	// Actual is the completed sessions' TrainingLoad summed per date. A date
	// present here wins over Planned, even at 0, so a ridden day never counts
	// its planned workout as well.
	Actual map[string]float64
	// Planned is the planned workouts' estimated TSS summed per date.
	Planned map[string]float64
	Today   string
	Event   string
}

// Point is one day's state at the start of that day, plus the load the day
// itself contributes (which is not in CTL/ATL/TSB until the next point).
type Point struct {
	Date string
	CTL  float64
	ATL  float64
	TSB  float64
	Load float64
}

// Roll runs workout.RollFitness day by day from the snapshot to the event and
// returns one point per day from today to the event. TSB is CTL - ATL at the
// start of the day, so the event's point is what the rider has at the start
// line, before the event's own load, which is left at 0 and never rolled.
//
// Per day before today only completed sessions count (0 on an empty day);
// from today on, completed sessions if any are recorded, else the planned
// estimate. An event that is today, past, unparseable or further than MaxDays
// away yields no points. A snapshot dated after today is treated as dated today.
func Roll(in Input) []Point {
	all := RollWithHistory(in)
	for i, p := range all {
		if p.Date >= in.Today {
			return all[i:]
		}
	}
	return nil
}

// RollWithHistory is Roll with the days between the snapshot and today kept
// in front, for the one reader that needs them: the weekly ramp of the current
// week begins at a Monday that is usually already past.
func RollWithHistory(in Input) []Point {
	today, err := time.Parse(dateLayout, in.Today)
	if err != nil {
		return nil
	}
	event, err := time.Parse(dateLayout, in.Event)
	if err != nil {
		return nil
	}
	days := int(event.Sub(today).Hours() / 24)
	if days <= 0 || days > MaxDays {
		return nil
	}
	cursor, err := time.Parse(dateLayout, in.Start.Date)
	if err != nil || cursor.After(today) {
		cursor = today
	}

	ctl, atl := in.Start.CTL, in.Start.ATL
	points := make([]Point, 0, days+1+int(today.Sub(cursor).Hours()/24))
	for d := cursor; !d.After(event); d = d.AddDate(0, 0, 1) {
		date := d.Format(dateLayout)
		p := Point{Date: date, CTL: ctl, ATL: atl, TSB: ctl - atl}
		switch a, ridden := in.Actual[date]; {
		case d.Equal(event):
			// The start line: the event's own load is not the rider's yet.
		case ridden:
			p.Load = a
		case !d.Before(today):
			p.Load = in.Planned[date]
		}
		points = append(points, p)
		ctl, atl = workout.RollFitness(ctl, atl, p.Load)
	}
	return points
}
