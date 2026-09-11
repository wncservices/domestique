package workout

import "math"

// CompletedSession is a finished session pulled back from a rider's own
// Garmin or Wahoo account after the fact — the read side of Phase B in
// docs/training-plan.md, the counterpart to the Workout a rider builds by
// hand. ExternalID plus Provider is how a re-pull stays idempotent: the
// same activity fetched twice upserts the same row rather than duplicating
// it, the same reasoning a Komoot import's own `komoot:<id>` tag already
// gives for exactly this problem on the routes side.
type CompletedSession struct {
	ID         string
	Rider      string
	Provider   string
	ExternalID string
	Sport      string
	// Date is when the session happened, "YYYY-MM-DD" — the day it counts
	// toward for fitness accounting, not a timestamp.
	Date            string
	DurationSeconds float64
	DistanceM       float64
	// AvgHR and AvgPowerWatts are 0 when the source activity did not carry
	// them — a HR-only or power-only file is the ordinary case, not an
	// error.
	AvgHR         int
	AvgPowerWatts float64
	// TrainingLoad is computed at ingestion time by TrainingLoad below, not
	// recomputed later — a rider's FTP or max HR changing should not
	// silently rewrite the load history of sessions already recorded
	// against the numbers that were true at the time.
	TrainingLoad float64
	CreatedAt    string
}

// UpsertSessionRequest records one completed session. Rider must be set by
// the caller from the authenticated session, same rule as every other
// write in this package.
type UpsertSessionRequest struct {
	Rider           string
	Provider        string
	ExternalID      string
	Sport           string
	Date            string
	DurationSeconds float64
	DistanceM       float64
	AvgHR           int
	AvgPowerWatts   float64
	TrainingLoad    float64
}

// FitnessSnapshot is one rider's computed training load for one day —
// TrainingPeaks' own Performance Management Chart values: CTL ("fitness",
// a ~42-day exponentially-weighted average of daily load), ATL
// ("fatigue", ~7-day), and TSB ("form", CTL minus ATL, carried from the
// day before — see ComputeFitness's own doc comment for why yesterday's
// values are what "how fresh does today start" actually means).
type FitnessSnapshot struct {
	Rider string
	Date  string
	CTL   float64
	ATL   float64
	TSB   float64
}

// DailyLoad is one calendar day's total training load — the sum of every
// completed session's own TrainingLoad on that date, 0 for a rest day.
// ComputeFitness needs a real 0 for a rest day, not a gap: the
// exponentially-weighted average has to decay across calendar time, not
// only across days that happen to have a session, or CTL/ATL would read as
// though a two-week gap between two rides was a single day.
type DailyLoad struct {
	Date string
	Load float64
}

// ctlDays and atlDays are TrainingPeaks' own published time constants for
// the Performance Management Chart's two moving averages.
const (
	ctlDays = 42.0
	atlDays = 7.0
)

// ComputeFitness turns a rider's daily loads into a CTL/ATL/TSB snapshot
// per day. Pure and deterministic — the same reasoning
// internal/periodization gives for being a plain function rather than
// anything stateful: give it the same loads, get the same snapshots, every
// time, which is what makes "why did my form read differently yesterday"
// answerable by inspection rather than by trusting a black box.
//
// loads must already be sorted ascending by date and cover every day in
// range with no gaps — see DailyLoad's own doc comment.
//
// TSB for a given day is computed from CTL/ATL *before* that day's load is
// folded in: it is how fresh the rider was starting that day, the number a
// rider planning today's session actually wants, not how fatigued today's
// own session left them.
func ComputeFitness(loads []DailyLoad) []FitnessSnapshot {
	ctlAlpha := 1 - math.Exp(-1/ctlDays)
	atlAlpha := 1 - math.Exp(-1/atlDays)

	out := make([]FitnessSnapshot, 0, len(loads))
	var ctl, atl float64
	for _, d := range loads {
		out = append(out, FitnessSnapshot{Date: d.Date, CTL: ctl, ATL: atl, TSB: ctl - atl})
		ctl += (d.Load - ctl) * ctlAlpha
		atl += (d.Load - atl) * atlAlpha
	}
	return out
}

// TrainingLoad estimates one session's training load — TrainingPeaks' TSS
// in spirit, from whatever data is actually available, in the same
// decreasing-precision order docs/training-plan.md's own "Reading the
// industry" section describes a duration × intensity score needing:
// power-based when both the session's average power and the rider's FTP
// are known, heart-rate-based when only HR data and a max HR are, and a
// flat duration-only estimate otherwise — the worst-precision fallback,
// but a real number a rider can still see trend over time, rather than
// refusing to record a session at all for lacking a power meter.
//
// The formula (hours × relative-intensity² × 100) is Coggan's own TSS
// shape for the power case; the HR case borrows the same shape with HR
// reserve as a stand-in for intensity factor, a coarser proxy real
// TRIMP-style HR training-load formulas refine further than this does.
func TrainingLoad(durationSeconds, avgPowerWatts float64, avgHR int, profile RiderProfile) float64 {
	hours := durationSeconds / 3600
	switch {
	case avgPowerWatts > 0 && profile.FTPWatts > 0:
		intensity := avgPowerWatts / profile.FTPWatts
		return hours * intensity * intensity * 100
	case avgHR > 0 && profile.MaxHR > 0:
		intensity := float64(avgHR) / float64(profile.MaxHR)
		return hours * intensity * intensity * 100
	default:
		return hours * 50
	}
}
