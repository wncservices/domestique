package api

import (
	"context"
	"net/http"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/morningmessage"
	"github.com/wncservices/domestique/apps/api/internal/testschedule"
	"github.com/wncservices/domestique/apps/api/internal/weather"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type morningMessageDTO struct {
	Headline string   `json:"headline"`
	Lines    []string `json:"lines"`
	// Show is false on an ordinary day; the card renders nothing then.
	Show bool `json:"show"`
}

// handleGetMorningMessage is the short "good morning" card on the Today page,
// built for the deployment's local today. The rider is the session's: there is
// no rider parameter, so nobody reads another rider's day through it.
func (s *Server) handleGetMorningMessage(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	id := auth.FromContext(r.Context())
	in, err := s.morningMessageInput(r.Context(), id.User, s.localNow())
	if err != nil {
		s.fail(w, err)
		return
	}
	m := morningmessage.Compose(in)
	writeJSON(w, http.StatusOK, morningMessageDTO{Headline: m.Headline, Lines: m.Lines, Show: m.Show})
}

// morningMessageInput gathers what one rider's morning message says. Only an unreadable
// plan is an error: a day of "rest day" for a rider whose plan could not be
// read would be wrong. Every other part degrades to being left out.
func (s *Server) morningMessageInput(ctx context.Context, rider string, now time.Time) (morningmessage.Input, error) {
	today := now.Format(dateLayout)
	in := morningmessage.Input{Today: today}

	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return in, err
	}
	var todays []workout.Workout
	for _, w := range workouts {
		if w.Date == today {
			todays = append(todays, w)
		}
	}
	in.Workouts = todays

	ridden, err := s.riddenToday(ctx, rider, today, workouts)
	if err != nil {
		s.logger().Warn("morning message: could not tell whether today was ridden", "err", err)
		ridden = nil
	}
	in.Ridden = len(todays) > 0
	for _, w := range todays {
		if !ridden[w.ID] {
			in.Ridden = false
		}
	}

	if blackout, err := s.blackoutFor(ctx, rider); err != nil {
		s.logger().Warn("morning message: could not read life events", "err", err)
	} else {
		in.Break = blackout[today]
	}

	if !in.Ridden {
		in.Weather = s.todaysWeatherReasons(ctx, rider, now, workouts, ridden)
		in.FTPTest = s.ftpTestSuggestedFor(ctx, rider, now, workouts)
	}
	return in, nil
}

// todaysWeatherReasons is why today's ride window is a bad idea, from the same
// rule the Plan page uses (weather.Suggest), for a rider who opted in to
// weather and only when it is bad. Nothing is fetched for anyone else, and a
// failed forecast just leaves the line out.
func (s *Server) todaysWeatherReasons(ctx context.Context, rider string, now time.Time, workouts []workout.Workout, ridden map[string]bool) []string {
	if s.Weather == nil || s.WeatherPrefs == nil || (s.Config != nil && !s.Config.Weather.On()) {
		return nil
	}
	today := now.Format(dateLayout)
	planned := false
	for _, w := range workouts {
		if w.Date == today {
			planned = true
		}
	}
	if !planned {
		return nil
	}
	pref, found, err := s.WeatherPrefs.Get(ctx, rider)
	if err != nil || !found {
		return nil
	}
	forecast, err := s.Weather.Forecast(ctx, pref.Location())
	if err != nil {
		s.logger().Warn("morning message: weather forecast unavailable", "err", err)
		return nil
	}
	opts := weather.Options{StartHour: pref.WindowStart, EndHour: pref.WindowEnd, Today: today, Now: s.now()}
	if profile, _, err := s.Training.GetProfile(ctx, rider); err == nil {
		opts.SmartTrainer, opts.AvailableDays = profile.SmartTrainer, profile.AvailableDays
	}
	var reasons []string
	seen := map[string]bool{}
	for _, sg := range weather.Suggest(forecast, workouts, ridden, opts) {
		if sg.Date != today {
			continue
		}
		for _, r := range sg.Reasons {
			if !seen[r] {
				seen[r] = true
				reasons = append(reasons, r)
			}
		}
	}
	return reasons
}

// ftpTestSuggestedFor is the date an FTP test is suggested for, "" for none.
// The inputs are assembled as handleGetFTPTests does; the composer only
// mentions a suggestion dated today.
func (s *Server) ftpTestSuggestedFor(ctx context.Context, rider string, now time.Time, workouts []workout.Workout) string {
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		return ""
	}
	todayStr := now.Format(dateLayout)
	var lastProtocol, lastReadDate string
	for _, wk := range workouts {
		if wk.TestProtocol == "" {
			continue
		}
		if wk.Date <= todayStr && wk.TestResultWatts != 0 && wk.Date > lastReadDate {
			lastReadDate = wk.Date
		}
		if wk.Date < todayStr && wk.TestResultWatts > 0 && wk.Date >= lastReadDate {
			lastProtocol = wk.TestProtocol
		}
	}
	plan, _, goals, err := s.focusPlan(ctx, rider, now)
	if err != nil {
		return ""
	}
	hasPower, err := s.hasRecentPower(ctx, rider, now)
	if err != nil {
		return ""
	}
	yesterday := now.AddDate(0, 0, -1).Format(dateLayout)
	var upcoming []workout.Workout
	for _, wk := range workouts {
		if wk.Date >= yesterday {
			upcoming = append(upcoming, wk)
		}
	}
	if sug := testschedule.Suggest(testschedule.Input{
		Plan: plan, Profile: profile, Upcoming: upcoming, Goals: goals,
		HasPower: hasPower, LastTestProtocol: lastProtocol, LastTestDate: lastReadDate, Now: now,
	}); sug != nil {
		return sug.Date
	}
	return ""
}
