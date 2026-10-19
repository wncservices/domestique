// Package weekplan holds the one rule for how hard days sit in a week, and the
// planner that swaps a hard session with an easier one when a rider wakes up
// tired. It is pure: dates are YYYY-MM-DD strings and nothing reads a clock or
// a store, so the caller supplies the week and applies the result.
package weekplan

import (
	"math"
	"sort"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const dateLayout = "2006-01-02"

// Hard reports whether w is a day that must not touch another: a structured
// hard session, an FTP test, or a crew ride (the crew's day, usually long).
func Hard(w workout.Workout) bool {
	return scheduler.IsHardSession(w) || w.TestProtocol != "" || w.CrewRideID != ""
}

// shift returns date moved by days, or "" when date does not parse.
func shift(date string, days int) string {
	t, err := time.Parse(dateLayout, date)
	if err != nil {
		return ""
	}
	return t.AddDate(0, 0, days).Format(dateLayout)
}

func contains(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// HardNeighbour reports whether a Hard session whose id is not in ignore sits
// on the day before or after date. A date that does not parse fails closed
// (true): a day we cannot place is not one to put a hard session beside.
func HardNeighbour(date string, week []workout.Workout, ignore ...string) bool {
	prev, next := shift(date, -1), shift(date, 1)
	if prev == "" {
		return true
	}
	for _, w := range week {
		if (w.Date == prev || w.Date == next) && !contains(ignore, w.ID) && Hard(w) {
			return true
		}
	}
	return false
}

// EveOfFixed reports whether an FTP test or a crew ride is the day after date:
// the day before a fixed commitment stays easy. A date that does not parse
// fails closed (true).
func EveOfFixed(date string, week []workout.Workout) bool {
	next := shift(date, 1)
	if next == "" {
		return true
	}
	for _, w := range week {
		if w.Date == next && (w.TestProtocol != "" || w.CrewRideID != "") {
			return true
		}
	}
	return false
}

// SwapInput is everything PlanSwap reads. The caller checks the readiness
// verdict first: PlanSwap does not take it.
type SwapInput struct {
	Today         string            // YYYY-MM-DD
	Sunday        string            // last day of this week
	Week          []workout.Workout // the rider's workouts Mon-Sun of this week
	Done          map[string]bool   // workout id -> already ridden
	ManualPushed  map[string]bool   // workout id -> has a manual Garmin copy
	NoTestDays    map[string]bool   // days a hard session may not be put on
	TomorrowReady bool              // tomorrow's outlook is Ready
}

// Swap names the two sessions to exchange and the day each moves to.
type Swap struct{ HardID, EasyID, HardTo, EasyTo string }

// Swappable reports whether w may be moved by the readiness swap: generated
// and untouched, a cycling session with no route, test or crew ride, never
// swapped before, and with no manual Garmin copy (the push pass never
// withdraws one, so moving it would leave a stale copy on the device).
func Swappable(w workout.Workout, manualPushed map[string]bool) bool {
	if !scheduler.IsGenerated(w) || w.RouteSlug != "" || w.TestProtocol != "" || w.CrewRideID != "" {
		return false
	}
	if w.Sport != "" && w.Sport != model.SportCycling {
		return false
	}
	for _, r := range w.Flags.AdaptedRules {
		if r == why.ReshuffleReadiness {
			return false
		}
	}
	return !manualPushed[w.ID]
}

// hardOn reports whether a Hard session whose id is not in ignore sits on date.
func hardOn(date string, week []workout.Workout, ignore ...string) bool {
	for _, w := range week {
		if w.Date == date && !contains(ignore, w.ID) && Hard(w) {
			return true
		}
	}
	return false
}

func seconds(w workout.Workout) float64 { return workout.PlannedSeconds(w.Steps) }

// PlanSwap picks today's hard session and an easier session later this week to
// trade places with. It reports false when there is nothing to do or nowhere
// safe to put the hard session.
//
// The target day is the earliest allowed one from the day after tomorrow, so a
// recovery day sits between; tomorrow is a fallback and only when the caller
// says tomorrow looks Ready. An allowed day has no hard neighbour (counting the
// two sessions at their new dates), is not a no-test day and is not the eve of
// a test or crew ride. Ties go to the longer session, then the lower id.
func PlanSwap(in SwapInput) (Swap, bool) {
	var hard []workout.Workout
	for _, w := range in.Week {
		if w.Date == in.Today && Swappable(w, in.ManualPushed) &&
			scheduler.IsHardSession(w) && !in.Done[w.ID] {
			hard = append(hard, w)
		}
	}
	if len(hard) == 0 {
		return Swap{}, false
	}
	sort.Slice(hard, func(i, j int) bool {
		if si, sj := seconds(hard[i]), seconds(hard[j]); si != sj {
			return si > sj
		}
		return hard[i].ID < hard[j].ID
	})
	h := hard[0]

	tomorrow := shift(in.Today, 1)
	var later, soon []workout.Workout
	for _, w := range in.Week {
		if w.Date <= in.Today || w.Date > in.Sunday || w.ID == h.ID ||
			!Swappable(w, in.ManualPushed) || scheduler.IsKeySession(w) ||
			in.Done[w.ID] || seconds(w) > seconds(h) {
			continue
		}
		if in.NoTestDays[w.Date] || EveOfFixed(w.Date, in.Week) || hardOn(w.Date, in.Week, h.ID) ||
			HardNeighbour(w.Date, in.Week, h.ID, w.ID) {
			continue
		}
		if w.Date == tomorrow {
			soon = append(soon, w)
		} else {
			later = append(later, w)
		}
	}
	pool := later
	if len(pool) == 0 && in.TomorrowReady {
		pool = soon
	}
	if len(pool) == 0 {
		return Swap{}, false
	}
	sort.Slice(pool, func(i, j int) bool {
		if pool[i].Date != pool[j].Date {
			return pool[i].Date < pool[j].Date
		}
		di, dj := math.Abs(seconds(pool[i])-seconds(h)), math.Abs(seconds(pool[j])-seconds(h))
		if di != dj {
			return di < dj
		}
		return pool[i].ID < pool[j].ID
	})
	e := pool[0]
	return Swap{HardID: h.ID, EasyID: e.ID, HardTo: e.Date, EasyTo: h.Date}, true
}
