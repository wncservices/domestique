package rideimport

import (
	"errors"
	"math"
	"time"

	"github.com/muktihari/fit/profile/basetype"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/rideanalysis"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

var (
	// ErrSport is a FIT of a sport the library does not train: a swim, a ski
	// day. Counted as skipped, never an error for the job.
	ErrSport = errors.New("rideimport: not a cycling or running activity")
	// ErrUnreadable is a file that is not a FIT activity with a record stream.
	ErrUnreadable = errors.New("rideimport: not a readable FIT activity")
)

// Ride is one imported activity as the pipeline sees it: what the file says
// about itself, and its analysis. It carries no file bytes, no coordinates and
// no per-second stream; those never leave Parse.
type Ride struct {
	Sport string
	// Date is the ride's local day. Provider sync stores the provider's own
	// start-time day, so the same ride has to land on the same date to be
	// recognised as already here.
	Date string
	// StartUTC is the start as an RFC 3339 UTC instant; StartUnix the same in
	// seconds, which is what makes a re-import idempotent.
	StartUTC  string
	StartUnix int64
	// Duration is the timer's time (pauses excluded, what a head unit shows as
	// ride time) and Elapsed the clock's; a provider may carry either.
	Duration, Elapsed, Distance float64
	AvgPower                    float64
	AvgHR                       int
	Analysis                    rideanalysis.Analysis
}

// Parse decodes one FIT and analyses it with no plan. The profile is the
// rider's current one: training load and TSS are scored against today's FTP,
// not the one true on the day, the trade-off CompletedSession's own doc
// already accepts. It reads nothing but the bytes it is given.
func Parse(fit []byte, p workout.RiderProfile) (Ride, error) {
	act, err := rideanalysis.DecodeFIT(fit)
	if err != nil || act == nil || len(act.Records) == 0 {
		return Ride{}, ErrUnreadable
	}

	sess := pickSession(act)
	sport, ok := sportOf(sess, act)
	if !ok {
		return Ride{}, ErrSport
	}

	start := act.Records[0].Timestamp
	if sess != nil && validTime(sess.StartTime) {
		start = sess.StartTime
	}
	if !validTime(start) {
		return Ride{}, ErrUnreadable
	}
	start = start.UTC()

	r := Ride{
		Sport:     sport,
		StartUTC:  start.Format(time.RFC3339),
		StartUnix: start.Unix(),
		Date:      start.Add(localOffset(act)).Format("2006-01-02"),
	}
	r.Duration, r.Elapsed, r.Distance, r.AvgPower, r.AvgHR = summarise(sess, act)

	r.Analysis = rideanalysis.Analyze(rideanalysis.Input{
		Activity: act,
		Summary:  rideanalysis.Summary{DurationSeconds: r.Duration, AvgPower: r.AvgPower, AvgHR: r.AvgHR},
		Profile:  p,
		Sport:    sport,
	})
	return r, nil
}

// pickSession is the file's own summary of the ride. A multisport file has
// several; the longest one of a supported sport stands for the file.
func pickSession(act *filedef.Activity) *mesgdef.Session {
	var best *mesgdef.Session
	var bestSeconds float64
	for _, s := range act.Sessions {
		if _, ok := sportOf(s, act); !ok {
			continue
		}
		secs := timerSeconds(s)
		if best == nil || secs > bestSeconds {
			best, bestSeconds = s, secs
		}
	}
	if best == nil && len(act.Sessions) > 0 {
		return act.Sessions[0]
	}
	return best
}

// sportOf maps a FIT sport onto the two the library has. Indoor and outdoor
// are both cycling; so are virtual rides and e-bikes. A file with no session
// message can only be told by its power meter.
func sportOf(s *mesgdef.Session, act *filedef.Activity) (string, bool) {
	if s == nil {
		for _, rec := range act.Records {
			if rec.Power != basetype.Uint16Invalid && rec.Power > 0 {
				return string(model.SportCycling), true
			}
		}
		return "", false
	}
	switch s.Sport {
	case typedef.SportCycling, typedef.SportEBiking:
		return string(model.SportCycling), true
	case typedef.SportFitnessEquipment:
		if s.SubSport == typedef.SubSportIndoorCycling {
			return string(model.SportCycling), true
		}
	case typedef.SportRunning:
		return string(model.SportRunning), true
	}
	return "", false
}

// validTime guards against the zero value and the FIT epoch an invalid
// timestamp decodes to.
func validTime(t time.Time) bool { return !t.IsZero() && t.Year() >= 2000 }

// localOffset is local_timestamp minus timestamp from the file's activity
// message, which is how a FIT says what the wall clock read. Absent or
// implausible, the day is the UTC one.
func localOffset(act *filedef.Activity) time.Duration {
	a := act.Activity
	if a == nil || !validTime(a.Timestamp) || !validTime(a.LocalTimestamp) {
		return 0
	}
	off := a.LocalTimestamp.Sub(a.Timestamp)
	if off < -24*time.Hour || off > 24*time.Hour {
		return 0
	}
	return off
}

func timerSeconds(s *mesgdef.Session) float64 {
	if s.TotalTimerTime != basetype.Uint32Invalid {
		return float64(s.TotalTimerTime) / 1000
	}
	return 0
}

// summarise reads duration, distance and averages from the session message,
// falling back to the record stream for whatever the file did not say.
func summarise(s *mesgdef.Session, act *filedef.Activity) (duration, elapsed, distance, avgPower float64, avgHR int) {
	recSeconds := 0.0
	if n := len(act.Records); n > 1 {
		// One record a second: n seconds of ride span n-1 seconds of timestamps.
		recSeconds = act.Records[n-1].Timestamp.Sub(act.Records[0].Timestamp).Seconds() + 1
	}
	if s != nil {
		duration = timerSeconds(s)
		if s.TotalElapsedTime != basetype.Uint32Invalid {
			elapsed = float64(s.TotalElapsedTime) / 1000
		}
		if s.TotalDistance != basetype.Uint32Invalid {
			distance = float64(s.TotalDistance) / 100
		}
		if s.AvgPower != basetype.Uint16Invalid {
			avgPower = float64(s.AvgPower)
		}
		if s.AvgHeartRate != basetype.Uint8Invalid {
			avgHR = int(s.AvgHeartRate)
		}
	}
	if duration <= 0 {
		duration = recSeconds
	}
	if elapsed <= 0 {
		elapsed = math.Max(duration, recSeconds)
	}
	if avgPower <= 0 || avgHR <= 0 {
		var pSum, hSum float64
		var pN, hN int
		for _, rec := range act.Records {
			if rec.Power != basetype.Uint16Invalid {
				pSum += float64(rec.Power)
				pN++
			}
			if rec.HeartRate != basetype.Uint8Invalid && rec.HeartRate > 0 {
				hSum += float64(rec.HeartRate)
				hN++
			}
		}
		if avgPower <= 0 && pN > 0 {
			avgPower = math.Round(pSum / float64(pN))
		}
		if avgHR <= 0 && hN > 0 {
			avgHR = int(math.Round(hSum / float64(hN)))
		}
	}
	return duration, elapsed, distance, avgPower, avgHR
}
