package scheduler

import "github.com/wncservices/domestique/apps/api/internal/workout"

// longDayIndex is the index into days of the day that carries the long
// session: the one the rider gives the most hours, ties going to the later
// day. A profile with no per-day hours is all ties, so the long day stays the
// last available day, as it always was.
func longDayIndex(days []string, profile workout.RiderProfile) int {
	long := len(days) - 1
	for i := len(days) - 2; i >= 0; i-- {
		if profile.HoursOn(days[i]) > profile.HoursOn(days[long]) {
			long = i
		}
	}
	return long
}

// dayCaps lists each day's hours, 0 for a day with no stated hours (never
// capped).
func dayCaps(days []string, profile workout.RiderProfile) []float64 {
	caps := make([]float64, len(days))
	for i, d := range days {
		caps[i] = profile.HoursOn(d)
	}
	return caps
}

// waterFill splits total across the slots by weight, never giving a slot more
// than its cap (a cap <= 0 is no cap): a slot whose share would exceed its cap
// gets the cap, and what it could not take is shared among the slots still
// under theirs, until none is over. What no slot can hold is dropped, so the
// result can sum to less than total. With no cap binding it is exactly the
// plain weighted split.
func waterFill(total float64, weights, caps []float64) []float64 {
	out := make([]float64, len(weights))
	free := make([]bool, len(weights))
	for i, w := range weights {
		free[i] = w > 0
	}
	left := total
	for left > 0 {
		var sum float64
		for i, w := range weights {
			if free[i] {
				sum += w
			}
		}
		if sum == 0 {
			break
		}
		var over []int
		for i, w := range weights {
			if free[i] && caps[i] > 0 && left*(w/sum) > caps[i] {
				over = append(over, i)
			}
		}
		if len(over) == 0 {
			for i, w := range weights {
				if free[i] {
					out[i] = left * (w / sum)
				}
			}
			break
		}
		for _, i := range over {
			out[i] = caps[i]
			left -= caps[i]
			free[i] = false
		}
	}
	return out
}
