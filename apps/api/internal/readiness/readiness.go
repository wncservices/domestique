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

	"github.com/wncservices/domestique/apps/api/internal/why"
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
	// Signals is Reasons again with the numbers kept apart from the prose:
	// one Signal per reason, same order, so "HRV low two nights" can be shown
	// with the 38 and 41 behind it. Reasons stays the sentence every existing
	// caller reads; nothing has to parse it, and nothing reads Signals but the
	// "Why?" record.
	Signals []Signal
}

// Signal is one reason with its numbers: a kind (hrv, sleep, readiness,
// resting_hr, form, load), a label, the value as it is displayed, and the
// raw numbers behind it. It is internal/why's type so the record can carry
// it without converting.
type Signal = why.Signal

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
	// HRVLastNight and HRVWeeklyAvg are the readings behind HRVStatus, in
	// milliseconds; zero means no reading. They feed only the numbers a
	// Signal shows, never a rule: HRVStatus is Garmin's own judgement and
	// stays what decides.
	HRVLastNight float64
	HRVWeeklyAvg float64
	Present      bool
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
	return AssessWithSurvey(today, history, tsb, tsbDate, loads, nil, now)
}

// SurveyDay is what a rider said about one day's rides in the post-ride
// survey: how the legs were ("fresh", "normal", "heavy") and how the rest of
// life was ("low", "normal", "high"), either "" when unanswered. A caller with
// several rides on one day folds them into one entry, heavy and high winning.
type SurveyDay struct {
	Date   string
	Legs   string
	Stress string
}

// AssessWithSurvey is Assess with the survey days of the last few days. The
// survey adds one caution reason at most (see surveyCaution) and never a rest:
// how the legs felt is the rider's word, worth a step down, not a day off.
func AssessWithSurvey(today Day, history []Day, tsb *float64, tsbDate string, loads []Load, survey []SurveyDay, now time.Time) Assessment {
	nowDate := dateOnly(now)

	var rest, caution findings

	if today.Present {
		if r, sig, ok := readinessRestReason(today); ok {
			rest.add(r, sig)
		} else if r, sig, ok := readinessCautionReason(today); ok {
			caution.add(r, sig)
		}

		if r, sig, ok := hrvRestReason(today, history); ok {
			rest.add(r, sig)
		} else if r, sig, ok := hrvCautionReason(today); ok {
			caution.add(r, sig)
		}

		if r, sig, ok := sleepRestReason(today); ok {
			rest.add(r, sig)
		} else if r, sig, ok := sleepCautionReason(today); ok {
			caution.add(r, sig)
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
					rest.add(rhrReason(delta, baseline), rhrSignal(today.RestingHR, delta, baseline))
				case delta >= 4:
					caution.add(rhrReason(delta, baseline), rhrSignal(today.RestingHR, delta, baseline))
				}
			}
		}
	}

	if tsb != nil && *tsb < -30 && tsbFresh(tsbDate, nowDate) {
		rest.add(formReason(*tsb), formSignal(*tsb))
	}

	// The load that decides how today may go is what came before today.
	// Counted through today, the ride the verdict is about raises its own
	// acute load: finish the session and the same day turns "take care".
	if ratio, ok := acwr(loads, nowDate.AddDate(0, 0, -1)); ok && ratio >= 1.5 {
		caution.add(loadReason(ratio), loadSignal(ratio))
	}

	if r, sig, ok := surveyCaution(survey, nowDate); ok {
		caution.add(r, sig)
	}

	switch {
	case len(rest.reasons) > 0:
		return Assessment{
			Verdict: Rest,
			Reasons: append(rest.reasons, caution.reasons...),
			Signals: append(rest.signals, caution.signals...),
		}
	case len(caution.reasons) > 0:
		return Assessment{Verdict: Caution, Reasons: caution.reasons, Signals: caution.signals}
	default:
		return Assessment{Verdict: Ready}
	}
}

// findings collects reasons and their signals in step, so the two can never
// drift out of line: every reason is added together with its numbers.
type findings struct {
	reasons []string
	signals []Signal
}

func (f *findings) add(reason string, sig Signal) {
	f.reasons = append(f.reasons, reason)
	f.signals = append(f.signals, sig)
}

// --- Garmin readiness -------------------------------------------------

func readinessRestReason(d Day) (string, Signal, bool) {
	poor := d.ReadinessLevel == "POOR" || (d.ReadinessScore > 0 && d.ReadinessScore < 25)
	if !poor {
		return "", Signal{}, false
	}
	return readinessReason("poor", d.ReadinessScore), readinessSignal("poor", d.ReadinessScore), true
}

func readinessCautionReason(d Day) (string, Signal, bool) {
	low := d.ReadinessLevel == "LOW" || (d.ReadinessScore >= 25 && d.ReadinessScore <= 49)
	if !low {
		return "", Signal{}, false
	}
	return readinessReason("low", d.ReadinessScore), readinessSignal("low", d.ReadinessScore), true
}

func readinessSignal(word string, score int) Signal {
	sig := Signal{Kind: "readiness", Label: "Garmin readiness", Value: word}
	if score > 0 {
		sig.Value = fmt.Sprintf("%s (%d)", word, score)
		sig.Numbers = []float64{float64(score)}
	}
	return sig
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
func hrvRestReason(today Day, history []Day) (string, Signal, bool) {
	if !isLowOrPoor(today.HRVStatus) {
		return "", Signal{}, false
	}
	todayDate, ok := parseDate(today.Date)
	if !ok {
		return "", Signal{}, false
	}
	yesterday := todayDate.AddDate(0, 0, -1)
	for _, h := range history {
		hd, ok := parseDate(h.Date)
		if ok && hd.Equal(yesterday) && isLowOrPoor(h.HRVStatus) {
			return "HRV has been low for two nights", hrvSignal("HRV low two nights", today, h.HRVLastNight), true
		}
	}
	return "", Signal{}, false
}

func hrvCautionReason(today Day) (string, Signal, bool) {
	if !isAbnormalHRV(today.HRVStatus) {
		return "", Signal{}, false
	}
	status := strings.ToLower(today.HRVStatus)
	return fmt.Sprintf("HRV is %s today", status), hrvSignal("HRV "+status, today, 0), true
}

// hrvSignal shows the readings behind an HRV reason, oldest first —
// "38, 41 ms vs usual 52" — as far as there are readings: a night with no
// reading (0) is left out, and with none at all the value falls back to the
// status word rather than printing zeros.
func hrvSignal(label string, today Day, earlier float64) Signal {
	sig := Signal{Kind: "hrv", Label: label}
	var shown []string
	if earlier > 0 {
		shown = append(shown, fmt.Sprintf("%.0f", earlier))
		sig.Numbers = append(sig.Numbers, earlier)
	}
	if today.HRVLastNight > 0 {
		shown = append(shown, fmt.Sprintf("%.0f", today.HRVLastNight))
		sig.Numbers = append(sig.Numbers, today.HRVLastNight)
	}
	if len(shown) == 0 {
		sig.Value = strings.ToLower(today.HRVStatus)
		return sig
	}
	sig.Value = strings.Join(shown, ", ") + " ms"
	if today.HRVWeeklyAvg > 0 {
		sig.Value += fmt.Sprintf(" vs usual %.0f", today.HRVWeeklyAvg)
		sig.Numbers = append(sig.Numbers, today.HRVWeeklyAvg)
	}
	return sig
}

// --- Sleep ----------------------------------------------------------------

func sleepRestReason(d Day) (string, Signal, bool) {
	if d.SleepScore <= 0 || d.SleepScore >= 40 {
		return "", Signal{}, false
	}
	return sleepReason(d), sleepSignal(d), true
}

func sleepCautionReason(d Day) (string, Signal, bool) {
	if d.SleepScore < 40 || d.SleepScore > 59 {
		return "", Signal{}, false
	}
	return sleepReason(d), sleepSignal(d), true
}

func sleepSignal(d Day) Signal {
	return Signal{
		Kind: "sleep", Label: "Sleep",
		Value:   fmt.Sprintf("%s, score %d", formatDuration(d.SleepSeconds), d.SleepScore),
		Numbers: []float64{float64(d.SleepSeconds), float64(d.SleepScore)},
	}
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
func rhrSignal(rhr int, delta, baseline float64) Signal {
	return Signal{
		Kind: "resting_hr", Label: "Resting heart rate",
		Value:   fmt.Sprintf("%d bpm, %d above your usual %d", rhr, int(delta), int(math.Round(baseline))),
		Numbers: []float64{float64(rhr), delta, math.Round(baseline)},
	}
}

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

func formSignal(tsb float64) Signal {
	return Signal{Kind: "form", Label: "Form", Value: formatSigned(tsb), Numbers: []float64{tsb}}
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
//
// Below minChronicLoad a day the ratio is not read at all: the denominator is
// so small that a single ride after a break reads as a fourfold spike, and
// the rider who has rested for weeks is told to take care.
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
	if mean28 < minChronicLoad {
		return 0, false
	}
	return (sum7 / 7) / mean28, true
}

// minChronicLoad is the 28-day mean daily load (TSS) below which the
// acute:chronic ratio says nothing: 10 a day is about an hour of endurance
// riding a week and a half, and a load that light is no base to spike from.
const minChronicLoad = 10.0

func loadSignal(ratio float64) Signal {
	return Signal{Kind: "load", Label: "Load this week", Value: fmt.Sprintf("%.1f× your usual", ratio), Numbers: []float64{ratio}}
}

func loadReason(ratio float64) string {
	return fmt.Sprintf("your load this week is %.1f× your usual", ratio)
}

// --- Survey -----------------------------------------------------------

// surveyCaution is the heavy-legs rule. Looking at the two-day windows that end
// today or yesterday (latest first), it fires when the legs were heavy on both
// days, or when they were heavy on the later day and stress was high on both.
// A single heavy report, stress on its own, a gap day between two reports, or
// reports that ended before yesterday do nothing. High stress on both days of
// a heavy pair is added to the wording.
func surveyCaution(survey []SurveyDay, nowDate time.Time) (string, Signal, bool) {
	byDate := make(map[string]SurveyDay, len(survey))
	for _, d := range survey {
		byDate[d.Date] = d
	}
	for back := 0; back <= 1; back++ {
		later := nowDate.AddDate(0, 0, -back)
		earlier := later.AddDate(0, 0, -1)
		l, lok := byDate[later.Format(dateLayout)]
		e, eok := byDate[earlier.Format(dateLayout)]
		if !lok || !eok || l.Legs != "heavy" {
			continue
		}
		bothHigh := l.Stress == "high" && e.Stress == "high"
		days := earlier.Weekday().String() + " and " + later.Weekday().String()
		switch {
		case e.Legs == "heavy":
			reason := "you reported heavy legs on " + days
			if bothHigh {
				reason += ", both high-stress days"
			}
			return reason, Signal{Kind: "survey_legs", Label: "Legs", Value: "heavy " + days}, true
		case bothHigh:
			return "heavy legs after two high-stress days", Signal{
				Kind: "survey_legs", Label: "Legs",
				Value: "heavy " + later.Weekday().String() + ", high stress " + days,
			}, true
		}
	}
	return "", Signal{}, false
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
