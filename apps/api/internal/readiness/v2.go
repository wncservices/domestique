package readiness

import (
	"fmt"
	"math"
	"time"
)

// Signal sources. The verdict counts distinct sources, so two ambers from one
// source (a poor sleep score and a sleep shortfall are both the same night)
// are one piece of evidence, not two.
const (
	SourceHRV        = "hrv"
	SourceSleep      = "sleep"
	SourceRestingHR  = "resting_hr"
	SourceGarmin     = "garmin"
	SourceLoad       = "load"
	SourceSubjective = "subjective"
)

// Severities of a signal.
const (
	SeverityAmber  = "amber"
	SeveritySevere = "severe"
)

// CheckIn is the rider's morning answers, 1 (worst) to 5. 0 means the
// question was not answered and is never a signal.
type CheckIn struct{ Legs, Mood, SleepQuality int }

// Input is everything Evaluate reads. History may be in any order and holds up
// to 67 days before Today (60 for the HRV band plus the week the 7-night mean
// reaches back).
type Input struct {
	Today   Day
	History []Day
	TSB     *float64
	TSBDate string
	Loads   []Load
	Survey  []SurveyDay
	CheckIn *CheckIn // nil = not answered today
	Now     time.Time
}

const (
	hrvBandNights   = 60
	hrvBandMin      = 14 // nights with a reading before a rider's own range means anything
	hrvMeanMin      = 3  // readings in the 7-night window before its mean is trusted
	hrvBandSDs      = 0.5
	sleepMedianDays = 28
	sleepMedianMin  = 7
	sleepDebtNights = 3
	sleepDebtSecs   = 9000 // 2.5 h
)

// Evaluate is the readiness verdict: any severe signal is Rest, amber signals
// from two or more distinct sources are Caution, a morning where everything is
// at or above the rider's own usual is Push, and everything else is Ready. A
// Ready (or Push-less) day with a single amber still lists it, so the rider
// sees it, but nothing acts on it.
func Evaluate(in Input) Assessment {
	today := in.Today
	nowDate := dateOnly(in.Now)
	var severe = findings{severity: SeveritySevere}
	var amber = findings{severity: SeverityAmber}

	band7, baseMean, baseSD, bandOK := HRVBand(in.History, today)
	debtMedian, medianOK := sleepMedian(in.History, today)

	if today.Present {
		if r, sig, ok := readinessRestReason(today); ok {
			severe.add(SourceGarmin, r, sig)
		} else if r, sig, ok := readinessCautionReason(today); ok {
			amber.add(SourceGarmin, r, sig)
		}

		if bandOK {
			// The rider's own range replaces Garmin's status words.
			lo := baseMean - hrvBandSDs*baseSD
			if r, sig, ok := hrvBandSevere(today, in.History, lo); ok {
				severe.add(SourceHRV, r, sig)
			} else if band7 < lo {
				amber.add(SourceHRV, "HRV is below your usual range", bandSignal(band7, baseMean))
			}
		} else if r, sig, ok := hrvRestReason(today, in.History); ok {
			severe.add(SourceHRV, r, sig)
		} else if r, sig, ok := hrvCautionReason(today); ok {
			amber.add(SourceHRV, r, sig)
		}

		if r, sig, ok := sleepRestReason(today); ok {
			severe.add(SourceSleep, r, sig)
		} else if r, sig, ok := sleepCautionReason(today); ok {
			amber.add(SourceSleep, r, sig)
		}
		if medianOK {
			if short, ok := sleepShortfall(in.History, today, debtMedian); ok && short >= sleepDebtSecs {
				amber.add(SourceSleep, sleepDebtReason(short), sleepDebtSignal(short))
			}
		}

		if today.RestingHR > 0 {
			if baseline, ok := restingHRBaseline(in.History, nowDate); ok {
				// Round once, here, and use that same rounded value for both
				// the bucket decision and the displayed text below — a
				// half-integer delta (e.g. 6.5) must not round up to 7 for
				// display while the raw 6.5 decided "amber".
				delta := math.Round(float64(today.RestingHR) - baseline)
				switch {
				case delta >= 7:
					severe.add(SourceRestingHR, rhrReason(delta, baseline), rhrSignal(today.RestingHR, delta, baseline))
				case delta >= 4:
					amber.add(SourceRestingHR, rhrReason(delta, baseline), rhrSignal(today.RestingHR, delta, baseline))
				}
			}
		}
	}

	if in.TSB != nil && *in.TSB < -30 && tsbFresh(in.TSBDate, nowDate) {
		severe.add(SourceLoad, formReason(*in.TSB), formSignal(*in.TSB))
	}

	// The load that decides how today may go is what came before today.
	// Counted through today, the ride the verdict is about raises its own
	// acute load: finish the session and the same day turns "take care".
	if ratio, ok := acwr(in.Loads, nowDate.AddDate(0, 0, -1)); ok && ratio >= 1.5 {
		amber.add(SourceLoad, loadReason(ratio), loadSignal(ratio))
	}

	if r, sig, ok := surveyCaution(in.Survey, nowDate); ok {
		amber.add(SourceSubjective, r, sig)
	}
	if r, sig, sev, ok := checkInSignal(in.CheckIn); ok {
		if sev == SeveritySevere {
			severe.add(SourceSubjective, r, sig)
		} else {
			amber.add(SourceSubjective, r, sig)
		}
	}

	reasons := append(append([]string(nil), severe.reasons...), amber.reasons...)
	signals := append(append([]Signal(nil), severe.signals...), amber.signals...)
	if len(reasons) == 0 {
		reasons, signals = nil, nil
	}

	switch {
	case len(severe.reasons) > 0:
		return Assessment{Verdict: Rest, Reasons: reasons, Signals: signals}
	case distinctSources(amber.signals) >= 2:
		return Assessment{Verdict: Caution, Reasons: reasons, Signals: signals}
	case len(amber.reasons) == 0 && isPush(in, bandOK, band7, baseMean, debtMedian, medianOK):
		return Assessment{Verdict: Push}
	default:
		return Assessment{Verdict: Ready, Reasons: reasons, Signals: signals}
	}
}

func distinctSources(sigs []Signal) int {
	seen := map[string]bool{}
	for _, s := range sigs {
		seen[s.Source] = true
	}
	return len(seen)
}

// isPush is the green-light test: the HRV band exists and the 7-night mean is
// at or above its centre, last night's sleep reached the rider's median, and
// a check-in answered today did not say the legs are poor. The caller has
// already established there is no severe or amber signal.
func isPush(in Input, bandOK bool, mean7, baseMean, sleepMed float64, medianOK bool) bool {
	if !in.Today.Present || !bandOK || mean7 < baseMean {
		return false
	}
	if !medianOK || in.Today.SleepSeconds <= 0 || float64(in.Today.SleepSeconds) < sleepMed {
		return false
	}
	if c := in.CheckIn; c != nil && c.Legs > 0 && c.Legs < 3 {
		return false
	}
	return true
}

// --- HRV own range ------------------------------------------------------------

// HRVBand is the rider's own HRV range in ln space. baseMean and baseSD are
// the mean and population standard deviation of ln(HRVLastNight) over the 60
// nights before today, and need at least 14 nights with a reading. mean7 is
// the mean of ln over the 7 nights ending today (today included when it has a
// reading) and needs at least 3 readings, so one lucky night is not a trend.
// ok is false when either is missing.
func HRVBand(history []Day, today Day) (mean7, baseMean, baseSD float64, ok bool) {
	todayDate, good := parseDate(today.Date)
	if !good {
		return 0, 0, 0, false
	}
	byDate := hrvByDate(history)
	cutoff := todayDate.AddDate(0, 0, -hrvBandNights)

	var base []float64
	for ds, v := range byDate {
		d, _ := parseDate(ds)
		if d.Before(todayDate) && !d.Before(cutoff) {
			base = append(base, math.Log(v))
		}
	}
	if len(base) < hrvBandMin {
		return 0, 0, 0, false
	}
	baseMean = meanOf(base)
	var ss float64
	for _, v := range base {
		ss += (v - baseMean) * (v - baseMean)
	}
	baseSD = math.Sqrt(ss / float64(len(base)))

	var week []float64
	for i := 0; i < 7; i++ {
		d := todayDate.AddDate(0, 0, -i)
		v := byDate[d.Format(dateLayout)]
		if i == 0 {
			v = today.HRVLastNight
		}
		if v > 0 {
			week = append(week, math.Log(v))
		}
	}
	if len(week) < hrvMeanMin {
		return 0, 0, 0, false
	}
	return meanOf(week), baseMean, baseSD, true
}

func hrvByDate(history []Day) map[string]float64 {
	m := map[string]float64{}
	for _, h := range history {
		if h.HRVLastNight > 0 {
			if _, ok := parseDate(h.Date); ok {
				m[h.Date] = h.HRVLastNight
			}
		}
	}
	return m
}

func meanOf(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// hrvBandSevere is "below the band on today and yesterday": both nights' ln
// under lo. A missing reading on either night is not below anything.
func hrvBandSevere(today Day, history []Day, lo float64) (string, Signal, bool) {
	todayDate, ok := parseDate(today.Date)
	if !ok || today.HRVLastNight <= 0 || math.Log(today.HRVLastNight) >= lo {
		return "", Signal{}, false
	}
	prev := hrvByDate(history)[todayDate.AddDate(0, 0, -1).Format(dateLayout)]
	if prev <= 0 || math.Log(prev) >= lo {
		return "", Signal{}, false
	}
	sig := Signal{
		Kind: "hrv", Label: "HRV below your range two nights",
		Value:   fmt.Sprintf("%.0f, %.0f ms vs range from %.0f", prev, today.HRVLastNight, math.Exp(lo)),
		Numbers: []float64{prev, today.HRVLastNight, math.Exp(lo)},
	}
	return "HRV has been below your usual range for two nights", sig, true
}

func bandSignal(mean7, baseMean float64) Signal {
	return Signal{
		Kind: "hrv", Label: "HRV 7-night average",
		Value:   fmt.Sprintf("%.0f ms vs usual %.0f", math.Exp(mean7), math.Exp(baseMean)),
		Numbers: []float64{math.Exp(mean7), math.Exp(baseMean)},
	}
}

// --- Sleep against the rider's own median -------------------------------------

// sleepMedian is the median SleepSeconds (> 0) of the 28 nights strictly
// before today, with at least 7 readings.
func sleepMedian(history []Day, today Day) (float64, bool) {
	todayDate, ok := parseDate(today.Date)
	if !ok {
		return 0, false
	}
	cutoff := todayDate.AddDate(0, 0, -sleepMedianDays)
	seen := map[string]bool{}
	var vals []float64
	for _, h := range history {
		if h.SleepSeconds <= 0 || seen[h.Date] {
			continue
		}
		d, ok := parseDate(h.Date)
		if ok && d.Before(todayDate) && !d.Before(cutoff) {
			seen[h.Date] = true
			vals = append(vals, float64(h.SleepSeconds))
		}
	}
	if len(vals) < sleepMedianMin {
		return 0, false
	}
	return median(vals), true
}

// sleepShortfall sums (median − slept) over today and the two nights before
// it, counting only nights with a reading. Longer nights subtract, so a good
// night in the window offsets a bad one. ok is false with no night at all.
func sleepShortfall(history []Day, today Day, med float64) (float64, bool) {
	todayDate, ok := parseDate(today.Date)
	if !ok {
		return 0, false
	}
	by := map[string]int{}
	for _, h := range history {
		if h.SleepSeconds > 0 {
			by[h.Date] = h.SleepSeconds
		}
	}
	var short float64
	n := 0
	for i := 0; i < sleepDebtNights; i++ {
		secs := by[todayDate.AddDate(0, 0, -i).Format(dateLayout)]
		if i == 0 {
			secs = today.SleepSeconds
		}
		if secs > 0 {
			short += med - float64(secs)
			n++
		}
	}
	return short, n > 0
}

func sleepDebtReason(short float64) string {
	return fmt.Sprintf("you are %s short on sleep over the last 3 nights", formatDuration(int(short)))
}

func sleepDebtSignal(short float64) Signal {
	return Signal{
		Kind: "sleep", Label: "Sleep shortfall, 3 nights",
		Value: formatDuration(int(short)), Numbers: []float64{short},
	}
}

// --- Check-in ---------------------------------------------------------------------

// checkInSignal turns the morning answers into at most one subjective signal.
// Legs = 1 is severe: it is the one question that is a direct statement about
// the day's work. A 2 on legs, mood or sleep quality is amber, once however
// many are 2. A mood or sleep quality of 1 is read as amber too (it is "2 or
// below", and only legs may be severe on its own). 0 is unanswered and 3 or
// more is no signal.
func checkInSignal(c *CheckIn) (reason string, sig Signal, severity string, ok bool) {
	if c == nil {
		return "", Signal{}, "", false
	}
	if c.Legs == 1 {
		return "you said your legs feel very bad this morning",
			Signal{Kind: "checkin", Label: "Check-in", Value: "legs 1 of 5", Numbers: []float64{1}},
			SeveritySevere, true
	}
	low := func(v int) bool { return v >= 1 && v <= 2 }
	switch {
	case low(c.Legs):
		return "you said your legs feel poor this morning", Signal{Kind: "checkin", Label: "Check-in", Value: "legs 2 of 5", Numbers: []float64{2}}, SeverityAmber, true
	case low(c.Mood):
		return "you said your mood is low this morning", Signal{Kind: "checkin", Label: "Check-in", Value: fmt.Sprintf("mood %d of 5", c.Mood), Numbers: []float64{float64(c.Mood)}}, SeverityAmber, true
	case low(c.SleepQuality):
		return "you said you slept badly", Signal{Kind: "checkin", Label: "Check-in", Value: fmt.Sprintf("sleep %d of 5", c.SleepQuality), Numbers: []float64{float64(c.SleepQuality)}}, SeverityAmber, true
	}
	return "", Signal{}, "", false
}
