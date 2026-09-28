// Package readiness turns today's Garmin wellness, recent history, form and
// load into a verdict — ready, caution or rest — with plain-language
// reasons. Pure: plain values in (no *workout.Workout, no store, no
// network), an Assessment out. See docs/superpowers/specs/
// 2026-09-28-readiness-design.md's "Readiness rules" section for the
// thresholds; every number here traces back to a line there.
package readiness

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Verdict is how hard today should be allowed to be. Readiness only ever
// makes a day easier, never harder — there is no verdict that raises a
// planned session.
type Verdict string

const (
	Ready   Verdict = "ready"
	Caution Verdict = "caution"
	Rest    Verdict = "rest"
)

// Assessment is Assess's result: the verdict and why, in the fixed order
// the spec lists the rules — rest rules first, then any caution rules that
// also fired (see Assess's own comment for why a rest verdict can still
// carry caution reasons).
type Assessment struct {
	Verdict Verdict
	Reasons []string
}

// Day is one day's Garmin wellness row. Present is false when there is no
// Garmin row for that day at all (Wahoo-only rider, watch not worn) — every
// other field is then meaningless and ignored. Present being true does not
// mean every field has a reading: a zero score, a zero RestingHR or an
// empty status/level string each mean "no reading for this one signal" and
// must never trigger a rule on their own.
type Day struct {
	Date           string
	HRVStatus      string
	SleepSeconds   int
	SleepScore     int
	ReadinessScore int
	ReadinessLevel string
	RestingHR      int
	Present        bool
}

// Load is one day's training load (TSS or equivalent), used for the
// acute:chronic load ratio.
type Load struct {
	Date string
	Load float64
}

const dateLayout = "2006-01-02"

// Assess turns today's wellness, recent history, the latest form (TSB)
// snapshot and recent daily loads into a verdict and reasons.
//
// now is used only to judge how stale the TSB snapshot is and to anchor the
// 7/28-day load and resting-HR windows — it is deliberately not used to
// decide what "today" means for pairing today's HRV with last night's
// (that comes from the Date fields themselves), so a caller can pass
// slightly-out-of-step dates in tests without the window math moving too.
//
// With no Garmin row for today (Present false), only the form and load
// rules can fire — there is nothing else to read.
func Assess(today Day, history []Day, tsb *float64, tsbDate string, loads []Load, now time.Time) Assessment {
	nowDate := dateOnly(now)

	var restReasons, cautionReasons []string

	if today.Present {
		if r, ok := readinessRestReason(today); ok {
			restReasons = append(restReasons, r)
		} else if r, ok := readinessCautionReason(today); ok {
			cautionReasons = append(cautionReasons, r)
		}

		if r, ok := hrvRestReason(today, history); ok {
			restReasons = append(restReasons, r)
		} else if r, ok := hrvCautionReason(today); ok {
			cautionReasons = append(cautionReasons, r)
		}

		if r, ok := sleepRestReason(today); ok {
			restReasons = append(restReasons, r)
		} else if r, ok := sleepCautionReason(today); ok {
			cautionReasons = append(cautionReasons, r)
		}

		if today.RestingHR > 0 {
			if baseline, ok := restingHRBaseline(history, nowDate); ok {
				// Round once, here, and use that same rounded value for both
				// the bucket decision and the displayed text below — a
				// half-integer delta (e.g. 6.5) must not round up to 7 for
				// display while the raw 6.5 decided "caution".
				delta := math.Round(float64(today.RestingHR) - baseline)
				switch {
				case delta >= 7:
					restReasons = append(restReasons, rhrReason(delta, baseline))
				case delta >= 4:
					cautionReasons = append(cautionReasons, rhrReason(delta, baseline))
				}
			}
		}
	}

	if tsb != nil && *tsb < -30 && tsbFresh(tsbDate, nowDate) {
		restReasons = append(restReasons, formReason(*tsb))
	}

	if ratio, ok := acwr(loads, nowDate); ok && ratio >= 1.5 {
		cautionReasons = append(cautionReasons, loadReason(ratio))
	}

	switch {
	case len(restReasons) > 0:
		return Assessment{Verdict: Rest, Reasons: append(restReasons, cautionReasons...)}
	case len(cautionReasons) > 0:
		return Assessment{Verdict: Caution, Reasons: cautionReasons}
	default:
		return Assessment{Verdict: Ready}
	}
}

// --- Garmin readiness -------------------------------------------------

func readinessRestReason(d Day) (string, bool) {
	if d.ReadinessLevel != "POOR" && !(d.ReadinessScore > 0 && d.ReadinessScore < 25) {
		return "", false
	}
	return readinessReason("poor", d.ReadinessScore), true
}

func readinessCautionReason(d Day) (string, bool) {
	if d.ReadinessLevel != "LOW" && !(d.ReadinessScore >= 25 && d.ReadinessScore <= 49) {
		return "", false
	}
	return readinessReason("low", d.ReadinessScore), true
}

func readinessReason(word string, score int) string {
	if score > 0 {
		return fmt.Sprintf("Garmin readiness is %s (%d)", word, score)
	}
	return fmt.Sprintf("Garmin readiness is %s", word)
}

// --- HRV ----------------------------------------------------------------

func isLowOrPoor(status string) bool {
	return status == "LOW" || status == "POOR"
}

func isAbnormalHRV(status string) bool {
	return status == "UNBALANCED" || status == "LOW" || status == "POOR"
}

// hrvRestReason fires when today and the night before both read LOW or
// POOR. A missing "yesterday" row (no history entry dated exactly one day
// before today) is treated as not-low, so this rule needs both nights
// present to fire.
func hrvRestReason(today Day, history []Day) (string, bool) {
	if !isLowOrPoor(today.HRVStatus) {
		return "", false
	}
	todayDate, ok := parseDate(today.Date)
	if !ok {
		return "", false
	}
	yesterday := todayDate.AddDate(0, 0, -1)
	for _, h := range history {
		hd, ok := parseDate(h.Date)
		if ok && hd.Equal(yesterday) && isLowOrPoor(h.HRVStatus) {
			return "HRV has been low for two nights", true
		}
	}
	return "", false
}

func hrvCautionReason(today Day) (string, bool) {
	if !isAbnormalHRV(today.HRVStatus) {
		return "", false
	}
	return fmt.Sprintf("HRV is %s today", strings.ToLower(today.HRVStatus)), true
}

// --- Sleep ----------------------------------------------------------------

func sleepRestReason(d Day) (string, bool) {
	if d.SleepScore <= 0 || d.SleepScore >= 40 {
		return "", false
	}
	return sleepReason(d), true
}

func sleepCautionReason(d Day) (string, bool) {
	if d.SleepScore < 40 || d.SleepScore > 59 {
		return "", false
	}
	return sleepReason(d), true
}

func sleepReason(d Day) string {
	return fmt.Sprintf("you slept %s (sleep score %d)", formatDuration(d.SleepSeconds), d.SleepScore)
}

func formatDuration(seconds int) string {
	h := seconds / 3600
	m := (seconds % 3600) / 60
	return fmt.Sprintf("%dh%02d", h, m)
}

// --- Resting heart rate ---------------------------------------------------

// restingHRBaseline is the median of the resting HR readings (> 0) from the
// 28 days strictly before referenceDate, and needs at least 7 of them —
// otherwise the RHR rules stay silent.
func restingHRBaseline(history []Day, referenceDate time.Time) (float64, bool) {
	cutoff := referenceDate.AddDate(0, 0, -28)
	var readings []float64
	for _, h := range history {
		if h.RestingHR <= 0 {
			continue
		}
		hd, ok := parseDate(h.Date)
		if !ok {
			continue
		}
		if hd.Before(referenceDate) && !hd.Before(cutoff) {
			readings = append(readings, float64(h.RestingHR))
		}
	}
	if len(readings) < 7 {
		return 0, false
	}
	return median(readings), true
}

func median(vals []float64) float64 {
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// rhrReason takes delta already rounded by the caller — the same rounded
// value that decided rest vs. caution — so the number in the sentence never
// disagrees with the verdict it explains. baseline is rounded here, once,
// purely for display.
func rhrReason(delta, baseline float64) string {
	return fmt.Sprintf("resting heart rate is %d above your usual %d", int(delta), int(math.Round(baseline)))
}

// --- Form (TSB) -------------------------------------------------------

// tsbFresh is whether the TSB snapshot is at most 2 days old as of
// referenceDate — the existing rule moved here from adapter.detectFatigue,
// unchanged: TrainingPeaks' PMC treats a stale snapshot as not describing
// how the rider feels today.
func tsbFresh(tsbDate string, referenceDate time.Time) bool {
	d, ok := parseDate(tsbDate)
	if !ok {
		return false
	}
	age := referenceDate.Sub(d)
	return age >= 0 && age <= 2*24*time.Hour
}

func formReason(tsb float64) string {
	return fmt.Sprintf("your form is %s", formatSigned(tsb))
}

// formatSigned rounds to the nearest integer and, for a negative value,
// uses a real Unicode minus sign rather than a hyphen — "−34", not "-34".
func formatSigned(v float64) string {
	r := math.Round(v)
	if r < 0 {
		return fmt.Sprintf("−%d", int(-r))
	}
	return fmt.Sprintf("%d", int(r))
}

// --- Load (ACWR) -------------------------------------------------------

// acwr is the acute:chronic load ratio: mean daily load over the 7 days up
// to and including referenceDate, divided by the mean over the 28 days up
// to and including referenceDate. A day with no matching Load counts as
// zero load in both means. The gate is calendar coverage, not the count of
// days with an actual entry: the rule only applies once the earliest date
// anywhere in loads reaches back at least 21 days before referenceDate —
// otherwise a rider training 4-5 days a week (rest days simply have no Load
// entry) would never accumulate 21 distinct dated entries and the rule
// would stay dead forever. Days without an entry still count as zero load
// in both means, unchanged.
func acwr(loads []Load, referenceDate time.Time) (float64, bool) {
	start28 := referenceDate.AddDate(0, 0, -27)
	start7 := referenceDate.AddDate(0, 0, -6)

	var earliest time.Time
	haveEarliest := false
	var sum7, sum28 float64
	for _, l := range loads {
		ld, ok := parseDate(l.Date)
		if !ok {
			continue
		}
		if !haveEarliest || ld.Before(earliest) {
			earliest = ld
			haveEarliest = true
		}
		if ld.Before(start28) || ld.After(referenceDate) {
			continue
		}
		sum28 += l.Load
		if !ld.Before(start7) {
			sum7 += l.Load
		}
	}
	if !haveEarliest || referenceDate.Sub(earliest) < 21*24*time.Hour {
		return 0, false
	}
	mean28 := sum28 / 28
	if mean28 == 0 {
		return 0, false
	}
	return (sum7 / 7) / mean28, true
}

func loadReason(ratio float64) string {
	return fmt.Sprintf("your load this week is %.1f× your usual", ratio)
}

// --- Dates -------------------------------------------------------------

func parseDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
