// Package thresholds turns a rider's recently analysed rides into FTP, max
// heart rate and threshold pace findings, and decides — per field — whether
// the change should apply itself or only be suggested.
//
// Pure, like internal/fitnesstest.EstimateFTP and internal/autoprofile.Apply
// before it: rides and a profile snapshot in, []Finding out, nothing else.
// See docs/superpowers/specs/2026-09-28-threshold-detection-design.md,
// "What is detected" and "When a value changes" — every formula, window and
// boundary here is binding from that spec, not a judgement call.
package thresholds

import (
	"fmt"
	"time"
)

// Ride is one analysed session, as much of it as detection needs. PowerCurve
// keys are seconds (5, 60, 300, 1200, 3600), matching
// internal/rideanalysis.PowerCurve's own windows. BestSpeed1200/1800 are m/s,
// matching internal/rideanalysis.BestSpeeds.
type Ride struct {
	SessionID     string
	Date          string // "YYYY-MM-DD"
	Sport         string // "cycling" | "running"
	PowerCurve    map[int]float64
	MaxHR         int
	BestSpeed1200 float64
	BestSpeed1800 float64
}

// Profile is the rider's current fitness profile, as much of it as detection
// needs to decide direction and auto-apply eligibility.
type Profile struct {
	FTPWatts              float64
	FTPEstimated          bool
	MaxHR                 int
	MaxHREstimated        bool
	ThresholdPaceSecPerKM float64
	PaceEstimated         bool
}

// Finding is one detected change to the rider's profile.
type Finding struct {
	Field           string // "ftp" | "max_hr" | "threshold_pace"
	Value           float64
	Previous        float64
	Direction       string // "up" | "down"
	SourceSessionID string
	SourceDate      string
	Reason          string
	// Auto is true when the field was empty or already an estimate and the
	// direction is up — the case internal/autoprofile.Apply-style code can
	// write on its own. Down never auto-applies, even for an estimated
	// field: a downward move is always a suggestion a rider confirms.
	Auto bool
}

// detectionWindowDays is the window "What is detected" reads rides from:
// rides dated within the 42 days up to and including today.
const detectionWindowDays = 42

// historyWindowDays is how far back the down-direction rule looks, both for
// "does enough history exist to trust silence" and for "did anything in
// that stretch come close" — see the spec's "When a value changes" table
// and this task's resolution: "the rider has analysed rides dated >= 90
// days ago AND none within the last 90 days reaches 95% of the current
// value."
const historyWindowDays = 90

// upFactor / downFactor are the FTP and threshold-pace thresholds: an
// estimate has to beat the current value by 3% to count as a real
// improvement (not sensor/analysis noise), and a downward move only fires
// once nothing recent gets within 5% of the current value.
const (
	upFactor   = 1.03
	downFactor = 0.95
)

// maxHRUpDelta is the flat +1 bpm max-HR threshold — a percentage doesn't
// make sense for a value this small and already an integer.
const maxHRUpDelta = 1

// maxHRSpikeThreshold mirrors internal/rideanalysis.MaxHR's own sensor-spike
// guard (see AGENTS.md's "Sensor spikes / a single weird ride" review
// focus, which names both packages). A Ride's MaxHR should already have
// been through that filter before it reaches here, but this package treats
// an untrusted-looking input the same way regardless of who built it,
// rather than assuming the caller got it right.
const maxHRSpikeThreshold = 230

// paceFallbackFactor turns a run's best 20-minute speed into a threshold
// estimate when the run has no 30-minute best to use directly.
const paceFallbackFactor = 0.97

// powerCurveKey5Min etc. name the PowerCurve keys this package reads, so the
// raw seconds don't appear unexplained at each call site.
const (
	powerCurveKey5Min  = 300
	powerCurveKey20Min = 1200
	powerCurveKey60Min = 3600
)

// Detect returns every finding across the three fields, in a fixed order
// (ftp, max_hr, threshold_pace) regardless of ride order, so callers (a sync
// result, a test) see a deterministic list.
func Detect(rides []Ride, p Profile, now time.Time) []Finding {
	today := dateOnly(now)

	var findings []Finding
	if f, ok := detectFTP(rides, p, today); ok {
		findings = append(findings, f)
	}
	if f, ok := detectMaxHR(rides, p, today); ok {
		findings = append(findings, f)
	}
	if f, ok := detectPace(rides, p, today); ok {
		findings = append(findings, f)
	}
	return findings
}

// dateOnly strips now down to a UTC midnight timestamp carrying only its
// calendar date, in now's own location. Every window below is calendar-date
// arithmetic (AddDate), never wall-clock time, and comparing everything in
// UTC after that extraction keeps the result identical regardless of the
// process's TZ environment variable — only the *year/month/day now's own
// location reports* matters, and that is fixed by whoever built now.
func dateOnly(now time.Time) time.Time {
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// parseDate parses a ride's "YYYY-MM-DD" date into the same UTC-midnight
// shape dateOnly produces, so the two compare directly. A ride with an
// unparseable date is excluded from every window rather than panicking —
// analysed data should never contain one, but a defensively-skipped ride
// beats a crashed detection run.
func parseDate(s string) (time.Time, bool) {
	d, err := time.Parse("2006-01-02", s)
	return d, err == nil
}

// inWindow reports whether date falls within the n days up to and including
// end (end itself, and the n-1 days before it).
func inWindow(date, end time.Time, days int) bool {
	start := end.AddDate(0, 0, -(days - 1))
	return !date.Before(start) && !date.After(end)
}

// olderThanWindow reports whether date falls strictly before the n-day
// window ending at end — the complement of inWindow, used for "history
// reaches back this far" checks.
func olderThanWindow(date, end time.Time, days int) bool {
	start := end.AddDate(0, 0, -(days - 1))
	return date.Before(start)
}

// weekday reads a "YYYY-MM-DD" date string's weekday name ("Saturday"), for
// reason wording. An unparseable date (should not happen for a ride that
// already passed parseDate) falls back to the raw string rather than
// panicking.
func weekday(date string) string {
	d, ok := parseDate(date)
	if !ok {
		return date
	}
	return d.Weekday().String()
}

// roundInt rounds to the nearest integer, the shape every value in this
// package's Findings is reported in (nearest watt, nearest second, integer
// bpm).
func roundInt(v float64) float64 {
	if v >= 0 {
		return float64(int(v + 0.5))
	}
	return -float64(int(-v + 0.5))
}

// ftpCandidate is one ride's own eFTP estimate: the highest of the three
// formulas the spec gives, plus which one won (for Reason wording) and the
// raw number that formula is built from (for the reason's parenthetical).
type ftpCandidate struct {
	value float64
	label string
	raw   float64
}

// rideFTPCandidate computes a single cycling ride's eFTP estimate: the
// highest of 0.95 x best 20-minute power, best 60-minute power, and
// critical power from the best 5- and 20-minute powers (only when both
// exist and P300 > P1200 — otherwise CP's own algebra would produce a
// number below the 20-minute estimate it is supposed to refine, which is
// not a valid critical-power model). ok is false for a non-cycling ride, or
// one with neither a 20- nor 60-minute best to build an estimate from.
func rideFTPCandidate(r Ride) (ftpCandidate, bool) {
	if r.Sport != "cycling" {
		return ftpCandidate{}, false
	}
	p20, has20 := r.PowerCurve[powerCurveKey20Min]
	p60, has60 := r.PowerCurve[powerCurveKey60Min]
	p5, has5 := r.PowerCurve[powerCurveKey5Min]
	if !has20 && !has60 {
		return ftpCandidate{}, false
	}

	var best ftpCandidate
	if has20 {
		best = ftpCandidate{value: 0.95 * p20, label: "20-minute effort", raw: p20}
	}
	if has60 && p60 > best.value {
		best = ftpCandidate{value: p60, label: "60-minute effort", raw: p60}
	}
	if has5 && has20 && p5 > p20 {
		cp := (p20*powerCurveKey20Min - p5*powerCurveKey5Min) / (powerCurveKey20Min - powerCurveKey5Min)
		if cp > best.value {
			best = ftpCandidate{value: cp, label: "5- and 20-minute efforts", raw: cp}
		}
	}
	if best.value <= 0 {
		return ftpCandidate{}, false
	}
	return best, true
}

// bestFTPInWindow scans rides for the highest eFTP candidate whose date
// falls in the n-day window ending at today, per rideFTPCandidate. found is
// false when no cycling ride in the window has a usable power curve. Ties
// keep the first ride encountered, for determinism.
func bestFTPInWindow(rides []Ride, today time.Time, days int) (candidate ftpCandidate, source Ride, found bool) {
	for _, r := range rides {
		d, ok := parseDate(r.Date)
		if !ok || !inWindow(d, today, days) {
			continue
		}
		c, ok := rideFTPCandidate(r)
		if !ok {
			continue
		}
		if !found || c.value > candidate.value {
			candidate, source, found = c, r, true
		}
	}
	return candidate, source, found
}

// hasFTPHistoryBefore reports whether any cycling ride with a usable power
// curve is dated strictly before the n-day window ending at today — "does
// the rider's history reach back far enough to trust that a down move isn't
// just a lack of data."
func hasFTPHistoryBefore(rides []Ride, today time.Time, days int) bool {
	for _, r := range rides {
		d, ok := parseDate(r.Date)
		if !ok || !olderThanWindow(d, today, days) {
			continue
		}
		if _, ok := rideFTPCandidate(r); ok {
			return true
		}
	}
	return false
}

func detectFTP(rides []Ride, p Profile, today time.Time) (Finding, bool) {
	if c, source, found := bestFTPInWindow(rides, today, detectionWindowDays); found &&
		c.value >= p.FTPWatts*upFactor {
		return Finding{
			Field:           "ftp",
			Value:           roundInt(c.value),
			Previous:        p.FTPWatts,
			Direction:       "up",
			SourceSessionID: source.SessionID,
			SourceDate:      source.Date,
			Reason:          fmt.Sprintf("from %s's %s (%d W)", weekday(source.Date), c.label, int(roundInt(c.raw))),
			Auto:            p.FTPWatts == 0 || p.FTPEstimated,
		}, true
	}

	// Down direction: a suggestion only, and only once there is enough
	// history to say the silence means something (see hasFTPHistoryBefore's
	// own comment), never for a profile with no current value to compare
	// against.
	if p.FTPWatts <= 0 {
		return Finding{}, false
	}
	if !hasFTPHistoryBefore(rides, today, historyWindowDays) {
		return Finding{}, false
	}
	// A down finding needs an actual recent effort to point to — "reaches
	// back 90 days but the rider hasn't ridden at all lately" is a real
	// gap, but it is not evidence the rider's FTP has *dropped*, so it must
	// not manufacture a Value of 0.
	recent, source, recentFound := bestFTPInWindow(rides, today, historyWindowDays)
	if !recentFound || recent.value >= p.FTPWatts*downFactor {
		return Finding{}, false
	}
	return Finding{
		Field:           "ftp",
		Value:           roundInt(recent.value),
		Previous:        p.FTPWatts,
		Direction:       "down",
		SourceSessionID: source.SessionID,
		SourceDate:      source.Date,
		Reason:          fmt.Sprintf("no effort near %d W in the last 90 days", int(roundInt(p.FTPWatts))),
		Auto:            false,
	}, true
}

func detectMaxHR(rides []Ride, p Profile, today time.Time) (Finding, bool) {
	var best int
	var source Ride
	found := false
	for _, r := range rides {
		d, ok := parseDate(r.Date)
		if !ok || !inWindow(d, today, detectionWindowDays) {
			continue
		}
		if r.MaxHR <= 0 || r.MaxHR > maxHRSpikeThreshold {
			continue
		}
		if !found || r.MaxHR > best {
			best, source, found = r.MaxHR, r, true
		}
	}
	if !found || best < p.MaxHR+maxHRUpDelta {
		return Finding{}, false
	}
	return Finding{
		Field:           "max_hr",
		Value:           float64(best),
		Previous:        float64(p.MaxHR),
		Direction:       "up",
		SourceSessionID: source.SessionID,
		SourceDate:      source.Date,
		Reason:          fmt.Sprintf("peak heart rate %d on %s's ride", best, weekday(source.Date)),
		Auto:            p.MaxHR == 0 || p.MaxHREstimated,
	}, true
}

// paceCandidate is one run's own threshold-pace estimate, in speed (m/s) so
// comparisons match the spec's own "speed >= current speed x 1.03" wording
// without an extra inversion, plus which best it came from (for Reason
// wording).
type paceCandidate struct {
	speed float64
	label string
}

// ridePaceCandidate reads a single running ride's best 30-minute speed, or
// 0.97 x its best 20-minute speed when no 30-minute best exists. ok is
// false for a non-running ride, or one with neither best.
func ridePaceCandidate(r Ride) (paceCandidate, bool) {
	if r.Sport != "running" {
		return paceCandidate{}, false
	}
	if r.BestSpeed1800 > 0 {
		return paceCandidate{speed: r.BestSpeed1800, label: "30-minute best"}, true
	}
	if r.BestSpeed1200 > 0 {
		return paceCandidate{speed: r.BestSpeed1200 * paceFallbackFactor, label: "20-minute best"}, true
	}
	return paceCandidate{}, false
}

// bestPaceInWindow is bestFTPInWindow's pace equivalent: the fastest
// (highest-speed) candidate among running rides dated in the n-day window
// ending at today. Ties keep the first ride encountered.
func bestPaceInWindow(rides []Ride, today time.Time, days int) (candidate paceCandidate, source Ride, found bool) {
	for _, r := range rides {
		d, ok := parseDate(r.Date)
		if !ok || !inWindow(d, today, days) {
			continue
		}
		c, ok := ridePaceCandidate(r)
		if !ok {
			continue
		}
		if !found || c.speed > candidate.speed {
			candidate, source, found = c, r, true
		}
	}
	return candidate, source, found
}

// hasPaceHistoryBefore is hasFTPHistoryBefore's pace equivalent.
func hasPaceHistoryBefore(rides []Ride, today time.Time, days int) bool {
	for _, r := range rides {
		d, ok := parseDate(r.Date)
		if !ok || !olderThanWindow(d, today, days) {
			continue
		}
		if _, ok := ridePaceCandidate(r); ok {
			return true
		}
	}
	return false
}

// currentPaceSpeed converts the profile's stored sec/km back to m/s so it
// can be compared against a candidate's speed directly — the spec's own up
// rule is phrased in speed ("speed >= current speed x 1.03"), and doing the
// comparison in speed avoids a second inversion on the candidate side. A
// not-yet-set pace (0) has no corresponding speed; comparisons against 0
// speed are satisfied by anything positive, which is exactly the "empty
// field, any estimate is an improvement" rule.
func currentPaceSpeed(paceSecPerKM float64) float64 {
	if paceSecPerKM <= 0 {
		return 0
	}
	return 1000 / paceSecPerKM
}

// speedToPaceSecPerKM is the spec's own conversion, sec/km = 1000 / speed.
// A non-positive speed (no candidate found) has no meaningful pace and
// returns 0 rather than dividing by zero.
func speedToPaceSecPerKM(speed float64) float64 {
	if speed <= 0 {
		return 0
	}
	return 1000 / speed
}

// formatPaceMinSec renders a sec/km pace as "m:ss/km", the shape a rider
// reads a pace in (never raw seconds) — rounded to the nearest second first,
// with the usual minutes/seconds carry so e.g. 239.6 renders "4:00", not
// "3:60".
func formatPaceMinSec(secPerKM float64) string {
	total := int(roundInt(secPerKM))
	if total < 0 {
		total = 0
	}
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

func detectPace(rides []Ride, p Profile, today time.Time) (Finding, bool) {
	currentSpeed := currentPaceSpeed(p.ThresholdPaceSecPerKM)

	if c, source, found := bestPaceInWindow(rides, today, detectionWindowDays); found &&
		c.speed >= currentSpeed*upFactor {
		return Finding{
			Field:           "threshold_pace",
			Value:           roundInt(speedToPaceSecPerKM(c.speed)),
			Previous:        p.ThresholdPaceSecPerKM,
			Direction:       "up",
			SourceSessionID: source.SessionID,
			SourceDate:      source.Date,
			Reason:          fmt.Sprintf("%s on %s's run", c.label, weekday(source.Date)),
			Auto:            p.ThresholdPaceSecPerKM == 0 || p.PaceEstimated,
		}, true
	}

	if p.ThresholdPaceSecPerKM <= 0 {
		return Finding{}, false
	}
	if !hasPaceHistoryBefore(rides, today, historyWindowDays) {
		return Finding{}, false
	}
	// As with FTP: no qualifying recent run means no evidence of a decline,
	// not a Value of 0 to suggest.
	recent, source, recentFound := bestPaceInWindow(rides, today, historyWindowDays)
	if !recentFound || recent.speed >= currentSpeed*downFactor {
		return Finding{}, false
	}
	return Finding{
		Field:           "threshold_pace",
		Value:           roundInt(speedToPaceSecPerKM(recent.speed)),
		Previous:        p.ThresholdPaceSecPerKM,
		Direction:       "down",
		SourceSessionID: source.SessionID,
		SourceDate:      source.Date,
		Reason:          fmt.Sprintf("no run near %s /km in the last 90 days", formatPaceMinSec(p.ThresholdPaceSecPerKM)),
		Auto:            false,
	}, true
}
