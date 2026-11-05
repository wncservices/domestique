// Package calendarfeed turns a rider's planned workouts into the private
// calendar feed, and holds the hashed token that makes the feed reachable
// without a session.
//
// A calendar app subscribes from its own servers, with no cookie, so the URL
// is the credential. That is why what goes in is so narrow: planned sessions
// only. Events takes workouts and nothing else, so no health value, fitness
// figure, route or place can reach a file that a third-party calendar server
// keeps; that is a property of the function's inputs, not a filter on its
// output.
package calendarfeed

import (
	"sort"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/ics"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const (
	// lookbackDays is how far back the feed reaches: a rider checking last
	// week's session in their calendar finds it, and a subscription is not
	// weighed down by a year of history.
	lookbackDays = 14
	// maxEvents caps the feed. The season plan fills weeks up to the goal, so
	// the real count is a few hundred at most; this stops a runaway.
	maxEvents = 500
	// defaultTimedLength is how long a timed event lasts when the workout has
	// no time-based steps to add up.
	defaultTimedLength = time.Hour
	dateLayout         = "2006-01-02"
)

// Events maps workouts to calendar events: those dated from lookbackDays before
// today (a date in loc) onward, at most maxEvents, in date order.
//
// When there are more than maxEvents, the sessions that matter are kept first:
// everything from today onward, nearest first, then whatever room is left goes
// to the most recent past ones. Cutting the list from the front instead would
// drop a long plan's latest sessions and keep history nobody is looking at.
//
// startHour is the one-method seam to the rider's ride window: nil, or
// (_, false), gives an all-day event; (hour, true) a timed one starting at that
// wall-clock hour in loc and lasting the workout's planned time. The zone is
// the deployment's because no per-rider zone exists.
func Events(ws []workout.Workout, now time.Time, loc *time.Location, startHour func(rider string) (int, bool), appURL string) []ics.Event {
	today := now.In(loc).Format(dateLayout)
	cutoff := now.In(loc).AddDate(0, 0, -lookbackDays).Format(dateLayout)

	var upcoming, past []workout.Workout
	for _, w := range ws {
		if w.Date == "" || w.Date < cutoff {
			continue
		}
		if _, err := time.Parse(dateLayout, w.Date); err != nil {
			continue
		}
		if w.Date >= today {
			upcoming = append(upcoming, w)
		} else {
			past = append(past, w)
		}
	}
	byDate := func(list []workout.Workout) {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Date != list[j].Date {
				return list[i].Date < list[j].Date
			}
			return list[i].ID < list[j].ID
		})
	}
	byDate(upcoming)
	byDate(past)

	kept := upcoming
	if len(kept) > maxEvents {
		kept = kept[:maxEvents]
	}
	if room := maxEvents - len(kept); room > 0 {
		// past is in date order, so its newest sessions are at the end.
		if len(past) > room {
			past = past[len(past)-room:]
		}
		kept = append(kept, past...)
	}
	byDate(kept)

	out := make([]ics.Event, 0, len(kept))
	for _, w := range kept {
		out = append(out, event(w, loc, startHour, appURL))
	}
	return out
}

func event(w workout.Workout, loc *time.Location, startHour func(string) (int, bool), appURL string) ics.Event {
	day, _ := time.ParseInLocation(dateLayout, w.Date, loc)
	y, m, d := day.Date()

	ev := ics.Event{
		// A constant host part, not the deployment's, so moving the app to a
		// new domain does not turn every event into a duplicate.
		UID:         "workout-" + w.ID + "@domestique",
		Summary:     w.Name,
		Description: description(w, appURL),
		Categories:  []string{"TRAINING"},
	}
	// UpdatedAt rather than the clock, so the file is byte-stable between edits.
	if t, err := time.Parse(time.RFC3339, w.UpdatedAt); err == nil {
		ev.Stamp, ev.Modified = t, t
	}

	if startHour != nil {
		if hour, ok := startHour(w.Rider); ok && hour >= 0 && hour <= 23 {
			// time.Date resolves the wall-clock hour in loc, so it is right
			// either side of a daylight-saving change.
			ev.Start = time.Date(y, m, d, hour, 0, 0, 0, loc)
			length := time.Duration(workout.PlannedSeconds(w.Steps) * float64(time.Second))
			if length <= 0 {
				length = defaultTimedLength
			}
			ev.End = ev.Start.Add(length)
			return ev
		}
	}
	ev.AllDay = true
	ev.Start = time.Date(y, m, d, 0, 0, 0, 0, loc)
	ev.End = time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	return ev
}

// description is zone, duration, target summary, the rider's own words, then a
// link to the Plan page. A line with nothing to say is left out.
func description(w workout.Workout, appURL string) string {
	var lines []string
	if z := workout.ZoneLabel(w.Zone); z != "" {
		lines = append(lines, "Zone: "+z)
	}
	if d := workout.DurationLabel(workout.PlannedSeconds(w.Steps)); d != "" {
		lines = append(lines, "Duration: "+d)
	}
	if len(w.Steps) > 0 {
		lines = append(lines, "Target: "+workout.Summary(w.Steps))
	}
	if own := strings.TrimSpace(w.Description); own != "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, own)
	}
	if appURL != "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, strings.TrimRight(appURL, "/")+"/plan")
	}
	return strings.Join(lines, "\n")
}
