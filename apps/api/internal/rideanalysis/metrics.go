// Package rideanalysis turns a decoded FIT activity into the load metrics,
// time-in-zones and power curve the training-adaptation design needs to
// judge how a ride actually went. It is pure: no I/O, no database, nothing
// that outlives one call — see docs/superpowers/specs/2026-09-27-training-
// adaptation-design.md, "Analysis — internal/rideanalysis (pure)".
//
// Nothing here persists raw ride data. A caller decodes a FIT file in
// memory, calls these functions, keeps only the derived numbers, and lets
// the decoded activity and its records be garbage collected — see the
// spec's "Privacy" section.
package rideanalysis

import (
	"math"

	"github.com/muktihari/fit/profile/basetype"
	"github.com/muktihari/fit/profile/mesgdef"
)

// Sample is one second of a ride, 1 Hz, numbered from the ride's first
// record (Seconds 0). HasPower/HasHR/HasSpeed distinguish a real reading of
// zero (never happens for power or HR while riding, but kept explicit
// rather than relying on a sentinel value) from no data for that second.
type Sample struct {
	Seconds   int
	Power     float64
	HeartRate float64
	Speed     float64
	HasPower  bool
	HasHR     bool
	HasSpeed  bool
}

// gapRepeatThresholdSeconds is the longest gap between two FIT records that
// Resample treats as a sensor hiccup worth papering over by repeating the
// last known sample. Past this, treating it as "still riding at the same
// power" would overstate the ride — a real pause (a stoplight, a stopped
// recording) is far more likely, so those seconds get zero/no-data samples
// instead. Value is the brief's own "gaps <= 5 s repeat" rule.
const gapRepeatThresholdSeconds = 5

// Resample turns a FIT activity's records — which may arrive at irregular
// intervals, with pauses, or with sensors that drop out mid-ride — into one
// Sample per second from the ride's start to its last record.
//
// A gap of gapRepeatThresholdSeconds or less between two records repeats the
// previous sample's values for the seconds in between (a brief recording
// hiccup, not a real change). A longer gap fills those seconds with
// zero-power, no-HR, no-speed samples (a real pause: coasting to a stop,
// pausing the device) rather than inventing effort that did not happen.
func Resample(records []*mesgdef.Record) []Sample {
	if len(records) == 0 {
		return nil
	}

	start := records[0].Timestamp
	secOf := func(r *mesgdef.Record) int {
		return int(r.Timestamp.Sub(start).Round(1_000_000_000).Seconds())
	}

	total := secOf(records[len(records)-1])
	samples := make([]Sample, total+1)

	// Place every actual record at its own second first. A later record
	// landing on the same second (duplicate or sub-second timestamps
	// rounding together) simply overwrites the earlier one.
	recSec := make([]int, len(records))
	for i, r := range records {
		sec := secOf(r)
		recSec[i] = sec
		samples[sec] = sampleFromRecord(r, sec)
	}

	// Fill the gaps between consecutive records.
	for i := 0; i < len(records)-1; i++ {
		gapStart, gapEnd := recSec[i], recSec[i+1]
		gap := gapEnd - gapStart
		if gap <= 1 {
			continue // adjacent seconds, nothing to fill
		}
		prev := samples[gapStart]
		repeat := gap <= gapRepeatThresholdSeconds
		for sec := gapStart + 1; sec < gapEnd; sec++ {
			if repeat {
				s := prev
				s.Seconds = sec
				samples[sec] = s
			} else {
				samples[sec] = Sample{Seconds: sec}
			}
		}
	}

	return samples
}

// sampleFromRecord reads the three metrics Analyze needs from one FIT
// record, treating the library's own invalid sentinel values as "not
// recorded" rather than as data (0 W or 0 bpm) — see this package's brief.
func sampleFromRecord(r *mesgdef.Record, sec int) Sample {
	s := Sample{Seconds: sec}
	if r.Power != basetype.Uint16Invalid {
		s.Power = float64(r.Power)
		s.HasPower = true
	}
	if r.HeartRate != basetype.Uint8Invalid {
		s.HeartRate = float64(r.HeartRate)
		s.HasHR = true
	}
	if r.EnhancedSpeed != basetype.Uint32Invalid {
		s.Speed = r.EnhancedSpeedScaled()
		s.HasSpeed = true
	}
	return s
}

// npMinSamplesWithPower is the fewest one-second power samples Coggan's
// algorithm needs before a 30 s rolling average means anything — below this
// a "normalized" power is noise dressed up as a number, so NormalizedPower
// returns 0 instead (never NaN: a short or power-less ride still needs to
// flow into the TSS/HR-load fallback chain without blowing it up).
const npMinSamplesWithPower = 30

// npWindowSeconds is Coggan's own choice for the rolling-average window
// behind normalized power: long enough that stroke-to-stroke power spikes
// wash out, short enough to still capture a real physiological response to
// an interval.
const npWindowSeconds = 30

// NormalizedPower implements Andrew Coggan's normalized power: a 30-second
// rolling average of one-second power, raised to the fourth power, averaged,
// then fourth-rooted. The repeated raise-average-root captures that harder
// efforts cost disproportionately more than their average watts suggest —
// the whole reason NP tracks perceived difficulty better than average power
// on a variable ride.
func NormalizedPower(s []Sample) float64 {
	withPower := 0
	for _, sample := range s {
		if sample.HasPower {
			withPower++
		}
	}
	if withPower < npMinSamplesWithPower || len(s) < npWindowSeconds {
		return 0
	}

	power := make([]float64, len(s))
	for i, sample := range s {
		power[i] = sample.Power
	}

	var sum, sum4 float64
	var windows int
	for i := 0; i < len(power); i++ {
		sum += power[i]
		if i >= npWindowSeconds {
			sum -= power[i-npWindowSeconds]
		}
		if i >= npWindowSeconds-1 {
			mean := sum / float64(npWindowSeconds)
			sum4 += mean * mean * mean * mean
			windows++
		}
	}
	if windows == 0 {
		return 0
	}
	return math.Pow(sum4/float64(windows), 0.25)
}

// PowerTSS is Coggan's intensity factor and training stress score: IF is NP
// relative to threshold power, and TSS scales an hour at threshold to 100.
// A non-positive FTP (not yet set, or a data error) returns 0, 0 rather than
// dividing by it — the caller's fallback chain (spec: "Fallback order")
// moves on to the next load source instead of propagating a NaN or Inf.
func PowerTSS(seconds int, np, ftp float64) (ifactor, tss float64) {
	if ftp <= 0 {
		return 0, 0
	}
	ifactor = np / ftp
	tss = float64(seconds) * np * ifactor / (ftp * 3600) * 100
	return ifactor, tss
}

// hrThresholdFraction is the fraction of max HR the spec treats as
// threshold heart rate, mirroring FTP's role for power.
const hrThresholdFraction = 0.9

// hrLoadMaxIF caps a single second's HR-based intensity factor. Heart rate
// can drift well above threshold under heat or fatigue without power (or
// pace) rising to match — capping keeps one hot afternoon from blowing the
// whole ride's load out of proportion.
const hrLoadMaxIF = 1.5

// HRLoad is the spec's heart-rate-based training load, used when a ride has
// no power data or no FTP to score it against: per-second intensity factor
// relative to threshold HR (0.9 x max HR), squared, summed and scaled so an
// hour spent exactly at threshold scores 100 — the same target TSS uses.
// A resting HR of 0 (not yet known) falls back to half of max HR, the
// spec's own default.
func HRLoad(s []Sample, maxHR, restHR int) float64 {
	threshold := hrThresholdFraction * float64(maxHR)
	rest := float64(restHR)
	if restHR == 0 {
		rest = 0.5 * float64(maxHR)
	}
	denom := threshold - rest
	if denom <= 0 {
		return 0
	}

	var sumSquares float64
	for _, sample := range s {
		ifHR := (sample.HeartRate - rest) / denom
		if ifHR < 0 {
			ifHR = 0
		} else if ifHR > hrLoadMaxIF {
			ifHR = hrLoadMaxIF
		}
		sumSquares += ifHR * ifHR
	}
	return sumSquares / 3600 * 100
}

// powerZoneEdges are Coggan's 7-zone model as fractions of FTP, matching
// apps/web/src/utils/fitnessMath.ts's POWER_ZONE_EDGES so the Fitness page
// and this package agree on what "Z4" means. Zone 7 (index 6) is
// open-ended above the last edge.
var powerZoneEdges = [6]float64{0.55, 0.75, 0.90, 1.05, 1.20, 1.50}

// PowerZoneSeconds counts seconds per Coggan power zone. A sample with no
// power (including a gap Resample zeroed) contributes to Z1 like any other
// low-power second — it is not skipped, so the result always sums to
// len(s) exactly. FTP <= 0 puts every second in Z1 rather than dividing by
// zero.
func PowerZoneSeconds(s []Sample, ftp float64) [7]int {
	var zones [7]int
	for _, sample := range s {
		pct := 0.0
		if ftp > 0 {
			pct = sample.Power / ftp
		}
		zones[powerZoneIndex(pct)]++
	}
	return zones
}

func powerZoneIndex(pct float64) int {
	for i, edge := range powerZoneEdges {
		if pct < edge {
			return i
		}
	}
	return len(powerZoneEdges)
}

// hrZoneEdges are the spec's 5-zone HR model as fractions of max HR,
// matching fitnessMath.ts's HR_ZONE_EDGES[1:] (it drops the 0.5 floor since,
// per the brief, "below 50% counts in Z1" the same as 50-60% does — there is
// no zone below Z1).
var hrZoneEdges = [4]float64{0.60, 0.70, 0.80, 0.90}

// HRZoneSeconds counts seconds per HR zone. maxHR <= 0 puts every second in
// Z1 rather than dividing by zero, and (like PowerZoneSeconds) a sample with
// no HR data contributes to Z1, so the result always sums to len(s).
func HRZoneSeconds(s []Sample, maxHR int) [5]int {
	var zones [5]int
	for _, sample := range s {
		pct := 0.0
		if maxHR > 0 {
			pct = sample.HeartRate / float64(maxHR)
		}
		zones[hrZoneIndex(pct)]++
	}
	return zones
}

func hrZoneIndex(pct float64) int {
	for i, edge := range hrZoneEdges {
		if pct < edge {
			return i
		}
	}
	return len(hrZoneEdges)
}

// powerCurveWindows are the durations the Fitness page's power curve
// tracks: a sprint, a minute, five minutes, twenty minutes (a common FTP
// proxy) and an hour.
var powerCurveWindows = [5]int{5, 60, 300, 1200, 3600}

// PowerCurve returns the best average power sustained over each of
// powerCurveWindows. A window longer than the ride has no meaningful best
// average, so it is left out of the map entirely rather than reported as 0
// (which would look like a real, very easy effort).
func PowerCurve(s []Sample) map[int]float64 {
	power := make([]float64, len(s))
	for i, sample := range s {
		power[i] = sample.Power
	}

	curve := make(map[int]float64)
	for _, window := range powerCurveWindows {
		if len(power) < window || window <= 0 {
			continue
		}
		curve[window] = bestRollingMean(power, window)
	}
	return curve
}

// bestRollingMean returns the highest mean of any contiguous window-sized
// slice of power, computed with a running sum so a 3600 s window over a
// multi-hour ride stays O(n) rather than O(n*window).
func bestRollingMean(power []float64, window int) float64 {
	var sum float64
	for i := 0; i < window; i++ {
		sum += power[i]
	}
	best := sum / float64(window)
	for i := window; i < len(power); i++ {
		sum += power[i] - power[i-window]
		if mean := sum / float64(window); mean > best {
			best = mean
		}
	}
	return best
}

// maxHRSpikeThreshold is the highest heart-rate reading MaxHR treats as
// real. A chest strap or optical sensor occasionally reports a brief,
// physiologically impossible spike (a loose strap, a cadence-lock glitch);
// see the design spec's Data section and AGENTS.md's "Sensor spikes /
// a single weird ride" review focus — max HR must not be that spike.
const maxHRSpikeThreshold = 230

// MaxHR returns the highest heart-rate sample among samples that carry a
// reading, ignoring any above maxHRSpikeThreshold — 0 when no sample has HR
// data at all (never a real ride's own 0 bpm).
func MaxHR(s []Sample) int {
	max := 0
	for _, sample := range s {
		if !sample.HasHR || sample.HeartRate > maxHRSpikeThreshold {
			continue
		}
		if hr := int(sample.HeartRate); hr > max {
			max = hr
		}
	}
	return max
}

// speedCurveWindows are the two windows the threshold-detection design
// needs a best average speed for: 20 and 30 minutes, the same running-pace
// windows a rider's threshold pace is judged against — see docs/superpowers/
// specs/2026-09-28-threshold-detection-design.md's Data section.
const (
	speedWindow1200Seconds = 1200
	speedWindow1800Seconds = 1800
)

// BestSpeeds returns the best 20- and 30-minute average speeds (m/s),
// reusing PowerCurve's own bestRollingMean helper rather than a second
// rolling-average implementation. Either return is 0 when the ride has no
// speed data at all, or is shorter than that window — a window longer than
// the ride has no meaningful best average, the same reasoning PowerCurve
// gives for leaving a too-long window out of its map entirely.
func BestSpeeds(s []Sample) (best1200, best1800 float64) {
	hasSpeed := false
	for _, sample := range s {
		if sample.HasSpeed {
			hasSpeed = true
			break
		}
	}
	if !hasSpeed {
		return 0, 0
	}

	speed := make([]float64, len(s))
	for i, sample := range s {
		speed[i] = sample.Speed
	}

	if len(speed) >= speedWindow1200Seconds {
		best1200 = bestRollingMean(speed, speedWindow1200Seconds)
	}
	if len(speed) >= speedWindow1800Seconds {
		best1800 = bestRollingMean(speed, speedWindow1800Seconds)
	}
	return best1200, best1800
}
