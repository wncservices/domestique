package api

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/projection"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// loadInput is what sizes a plan in TSS from the rider's own fitness. It
// returns nil, nil when the plan has to stay on the hours model: no FTP (the
// load history is then a duration guess), no hours to spend, or no fitness
// snapshot yet. Design: docs/superpowers/specs/2026-10-18-training-load-model-design.md.
func (s *Server) loadInput(ctx context.Context, g workout.Goal, rider string, profile workout.RiderProfile) (*periodization.LoadInput, error) {
	if !(profile.FTPWatts > 0) || !(profile.HoursPerAvailableDay*float64(len(profile.AvailableDays)) > 0) {
		return nil, nil
	}
	// A failed read costs the TSS sizing, not the plan: every plan reader goes
	// through here, so an error would take the whole training area down.
	snap, err := s.latestFitness(ctx, rider)
	if err != nil {
		s.logger().Warn("load input unavailable, planning from hours", "rider", rider, "stage", "fitness")
		return nil, nil
	}
	if snap == nil {
		return nil, nil
	}
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		s.logger().Warn("load input unavailable, planning from hours", "rider", rider, "stage", "sessions")
		return nil, nil
	}
	return loadInputFrom(g, profile, snap, sessions, s.localNow()), nil
}

// loadInputFrom is loadInput's pure part.
func loadInputFrom(g workout.Goal, profile workout.RiderProfile, snap *workout.FitnessSnapshot, sessions []workout.CompletedSession, now time.Time) *periodization.LoadInput {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	in := &periodization.LoadInput{CTL: snap.CTL, ATL: snap.ATL, MaxRamp: maxRampFor(profile.ExperienceLevel)}

	// A snapshot is a start-of-day value. A stale one is rolled to today with
	// no load, the same recurrence the fitness history uses.
	if d, err := time.Parse(dateLayout, snap.Date); err == nil {
		for day := d; day.Before(today); day = day.AddDate(0, 0, 1) {
			in.CTL, in.ATL = workout.RollFitness(in.CTL, in.ATL, 0)
		}
	}

	in.TypicalIF = typicalIF(sessions, today)
	in.HistoryWeeks, in.BestRecentWeekTSS = historyAndBestWeek(sessions, today)

	if g.EventDate != "" {
		hours, _ := projection.EventDuration(g.TargetDistanceM, g.TargetElevationM)
		band := projection.BandFor(hours)
		in.TargetCTL = projection.TargetCTL(hours)
		in.TaperTSB = (band.Low + band.High) / 2
	}
	return in
}

func maxRampFor(experience string) float64 {
	switch strings.ToLower(strings.TrimSpace(experience)) {
	case "beginner":
		return 4
	case "advanced":
		return 8
	}
	return 6
}

// typicalIF is the intensity factor the rider's last 42 days imply:
// load = hours x IF^2 x 100. Under 3 hours of history says too little.
func typicalIF(sessions []workout.CompletedSession, today time.Time) float64 {
	from := today.AddDate(0, 0, -42).Format(dateLayout)
	to := today.Format(dateLayout)
	var load, hours float64
	for _, sess := range sessions {
		if sess.Date > from && sess.Date <= to {
			load += sess.TrainingLoad
			hours += sess.DurationSeconds / 3600
		}
	}
	if hours < 3 {
		return 0.70
	}
	return math.Min(0.85, math.Max(0.55, math.Sqrt(load/(hours*100))))
}

// historyAndBestWeek is the whole weeks since the first session, and the
// highest Monday-Sunday load among the last four complete weeks.
func historyAndBestWeek(sessions []workout.CompletedSession, today time.Time) (int, float64) {
	if len(sessions) == 0 {
		return 0, 0
	}
	first := ""
	for _, sess := range sessions {
		if first == "" || sess.Date < first {
			first = sess.Date
		}
	}
	weeks := 0
	if d, err := time.Parse(dateLayout, first); err == nil && d.Before(today) {
		weeks = int(today.Sub(d).Hours() / 24 / 7)
	}

	thisMonday := periodization.MondayOf(today)
	perWeek := map[string]float64{}
	for _, sess := range sessions {
		d, err := time.Parse(dateLayout, sess.Date)
		if err != nil || !d.Before(thisMonday) || d.Before(thisMonday.AddDate(0, 0, -28)) {
			continue
		}
		perWeek[periodization.MondayOf(d).Format(dateLayout)] += sess.TrainingLoad
	}
	best := 0.0
	for _, v := range perWeek {
		best = math.Max(best, v)
	}
	return weeks, best
}
