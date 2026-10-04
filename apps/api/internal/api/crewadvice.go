package api

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// crewAdviceDTO is readinessResponseDTO.crewRide: advice for a crew ride today
// (or tomorrow, from the forecast) when the rider is not ready. Advice only:
// nothing here changes, moves or swaps the ride, and no "Swap" is offered.
// Mirrored by hand in apps/web/src/api/types.ts.
type crewAdviceDTO struct {
	Date      string `json:"date"`
	RouteName string `json:"routeName"`
	// Severity is the verdict: caution or rest.
	Severity string `json:"severity"`
	Advice   string `json:"advice"`
}

// enduranceCapShareOfFTP is the ceiling the caution advice quotes: 75 % of FTP,
// the top of the endurance zone the plan itself uses.
const enduranceCapShareOfFTP = 0.75

// crewRideAdvice words the advice for a fixed crew ride today, or failing that
// tomorrow, when the verdict for that day is not ready. Today's verdict is the
// readiness the rest of the page shows; tomorrow's is the forecast. The wattage
// is the rider's own, computed from their own FTP, and left out without one.
// A day the rider is away has no advice, like the forecast banner.
func (s *Server) crewRideAdvice(ctx context.Context, rider string, today time.Time, todayVerdict readiness.Verdict,
	workouts []workout.Workout, sessions []workout.CompletedSession, latest *workout.FitnessSnapshot,
	profile workout.RiderProfile, blackout map[string]bool) *crewAdviceDTO {

	todayStr := calendarDay(today).Format(dateFormat)
	tomorrowStr := calendarDay(today).AddDate(0, 0, 1).Format(dateFormat)

	var todayRide, tomorrowRide *workout.Workout
	for i := range workouts {
		w := &workouts[i]
		if w.CrewRideID == "" || adapter.WorkoutDone(*w, sessions) {
			continue
		}
		switch w.Date {
		case todayStr:
			todayRide = w
		case tomorrowStr:
			tomorrowRide = w
		}
	}

	if todayRide != nil && !blackout[todayStr] && todayVerdict != readiness.Ready {
		return adviceFor(*todayRide, "today", todayVerdict, profile.FTPWatts)
	}
	if tomorrowRide != nil && !blackout[tomorrowStr] {
		assessment := s.assessReadinessForForecast(ctx, rider, sessions, latest, today)
		forecast := outlookTomorrow(today, workouts, sessions, latest, assessment, profile)
		if forecast.Verdict != readiness.Ready {
			return adviceFor(*tomorrowRide, "tomorrow", forecast.Verdict, profile.FTPWatts)
		}
	}
	return nil
}

// adviceFor is the words. The ride itself is never changed.
func adviceFor(ride workout.Workout, when string, verdict readiness.Verdict, ftp float64) *crewAdviceDTO {
	out := &crewAdviceDTO{
		Date: ride.Date, RouteName: strings.TrimPrefix(ride.Name, crewRideNamePrefix), Severity: string(verdict),
	}
	if verdict == readiness.Rest {
		out.Advice = "Your recovery looks low. Ride at conversational pace, or skip it; your call."
		return out
	}
	out.Advice = fmt.Sprintf("Group ride %s: sit in, skip the pulls, keep it easy", when)
	if ftp > 0 {
		watts := int(math.Round(ftp*enduranceCapShareOfFTP/5) * 5)
		out.Advice += fmt.Sprintf(", stay under about %d W", watts)
	}
	out.Advice += "."
	return out
}
