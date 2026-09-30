// Package compliance answers "did the rider do what was planned?" for one
// day, the question the Plan page's week strip colours every tile by. Pure
// on purpose — no store, no clock — so the thresholds below are pinned by a
// table test rather than by whatever a handler happened to pass in.
package compliance

import "github.com/wncservices/domestique/apps/api/internal/workout"

type Status string

const (
	StatusDone      Status = "done"
	StatusPartial   Status = "partial"
	StatusMissed    Status = "missed"
	StatusRest      Status = "rest"
	StatusUpcoming  Status = "upcoming"
	StatusUnplanned Status = "unplanned"
)

// The same 80% line TrainingPeaks draws for "green": close enough that a
// ride cut short by a flat tyre still counts, not so loose that a coffee
// spin passes for a long ride.
const (
	doneRatio    = 0.8
	partialRatio = 0.3
)

// Day classifies date. Dates are "YYYY-MM-DD", so string comparison is
// calendar order. Only sessions of a planned sport count toward the ratio:
// a run on a bike day is training, but it is not the session that was
// asked for.
func Day(date, today string, planned []workout.Workout, completed []workout.CompletedSession) Status {
	if len(planned) == 0 {
		if len(completed) > 0 {
			return StatusUnplanned
		}
		return StatusRest
	}

	sports := make(map[string]bool, len(planned))
	var plannedSeconds float64
	var test bool
	for _, w := range planned {
		sports[string(w.Sport)] = true
		plannedSeconds += workout.PlannedSeconds(w.Steps)
		test = test || w.TestProtocol != ""
	}
	var matched float64
	var anyMatch bool
	for _, s := range completed {
		if sports[s.Sport] {
			matched += s.DurationSeconds
			anyMatch = true
		}
	}

	// Open or distance-only steps have no honest duration (see
	// workout.PlannedSeconds); any matching ride is the best evidence there is.
	// Nor does an FTP test: the ramp is ridden to failure, so it always ends
	// short of its planned steps, and a test ridden is a test done.
	if plannedSeconds == 0 || test {
		if anyMatch {
			return StatusDone
		}
	} else {
		ratio := matched / plannedSeconds
		if ratio >= doneRatio {
			return StatusDone
		}
		if ratio >= partialRatio {
			return StatusPartial
		}
	}
	if date < today {
		return StatusMissed
	}
	return StatusUpcoming
}
