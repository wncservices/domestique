package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/mailer"
	"github.com/wncservices/domestique/apps/api/internal/morningsummary"
	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
	"github.com/wncservices/domestique/apps/api/internal/syncschedule"
	"github.com/wncservices/domestique/apps/api/internal/testschedule"
	"github.com/wncservices/domestique/apps/api/internal/weather"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Notifier is how a message leaves this server for a rider. *mailer.Mailer is
// the one adapter today; ntfy or Gotify would be one more, with no change here.
type Notifier interface {
	Send(ctx context.Context, to, subject, body string) error
}

// NewTestMailLimiter is the budget for "send me a test": five per rider per
// fifteen minutes, enough to prove an SMTP setup, too little to use as a mail
// cannon even by a signed-in rider.
func NewTestMailLimiter() *ratelimit.Limiter { return ratelimit.New(5, 15*time.Minute) }

// summaryNoonHour is the local hour from which a late pass no longer sends: a
// "morning" summary at 15:00 is noise.
const summaryNoonHour = 12

// Why the summary is unavailable to a rider, as the UI reads it.
const (
	summaryNotConfigured = "not_configured"
	summaryNoEmail       = "no_email"
	summaryUnverified    = "email_unverified"
)

// morningSummaryTotal counts sends by outcome. A rise in failed is the thing
// worth an alert: an SMTP relay that has stopped working says nothing else.
var morningSummaryTotal = must(meter.Int64Counter(
	"domestique_morning_summary_total",
	metric.WithDescription("Morning summary emails by result (sent/failed)."),
))

func countSummary(ctx context.Context, result string) {
	morningSummaryTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

type morningSummaryDTO struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Enabled   bool   `json:"enabled"`
	// Email is the address the summary goes to: the signed-in identity's.
	Email string `json:"email,omitempty"`
}

// smtpConfigured is whether this deployment can send at all.
func (s *Server) smtpConfigured() bool {
	return s.Mailer != nil && s.MorningSummaries != nil && s.Config != nil && s.Config.Notifications.SMTP.Enabled()
}

// summaryUnavailableReason is why id cannot use the summary, "" when it can.
//
// The address must come from the identity and be one the identity provider
// vouches for: an unverified address (a rider typed it at sign-up and nobody
// confirmed it) would make this a way to send mail to a stranger. Under mode:
// none there is no address at all.
func (s *Server) summaryUnavailableReason(id auth.Identity) string {
	switch {
	case !s.smtpConfigured():
		return summaryNotConfigured
	case id.Email == "":
		return summaryNoEmail
	case !id.EmailVerified:
		return summaryUnverified
	}
	return ""
}

var summaryRefusal = map[string]string{
	summaryNotConfigured: "the morning summary is not set up on this deployment",
	summaryNoEmail:       "there is no email address for your account",
	summaryUnverified:    "your email address has not been verified by your sign-in provider",
}

// refuseSummary answers 412 and logs why at Warn: a UI error with nothing
// server-side to match it is what the observability checklist exists to prevent.
// Reason only; never the address.
func (s *Server) refuseSummary(w http.ResponseWriter, reason string) {
	s.logger().Warn("morning summary refused", "reason", reason)
	writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": summaryRefusal[reason], "reason": reason})
}

// currentSummary builds the status, refreshing what the identity says first:
// a changed verified address replaces the stored one, and an address that is
// no longer vouched for switches the summary off rather than keep writing to it.
func (s *Server) currentSummary(ctx context.Context, id auth.Identity) (morningSummaryDTO, error) {
	reason := s.summaryUnavailableReason(id)
	out := morningSummaryDTO{Available: reason == "", Reason: reason}
	if s.MorningSummaries == nil {
		return out, nil
	}
	pref, found, err := s.MorningSummaries.Get(ctx, id.User)
	if err != nil {
		return out, err
	}
	enabled := found && pref.Enabled
	switch {
	case enabled && reason == summaryUnverified:
		if err := s.MorningSummaries.Disable(ctx, id.User, s.now()); err != nil {
			return out, err
		}
		s.logger().Info("morning summary switched off: the address is no longer verified", "rider", id.User)
		enabled = false
	case enabled && reason == "" && pref.Email != id.Email:
		if err := s.MorningSummaries.SetEmail(ctx, id.User, id.Email, s.now()); err != nil {
			return out, err
		}
		pref.Email = id.Email
	}
	out.Enabled = enabled
	switch {
	case out.Available:
		out.Email = id.Email
	case enabled:
		out.Email = pref.Email
	}
	return out, nil
}

func (s *Server) handleGetMorningSummary(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) {
		return
	}
	out, err := s.currentSummary(r.Context(), auth.FromContext(r.Context()))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePutMorningSummary turns the summary on or off. The body is {enabled}
// and nothing else: the address is the identity's, never the request's.
func (s *Server) handlePutMorningSummary(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) {
		return
	}
	id := auth.FromContext(r.Context())
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil || body.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `the body must be {"enabled": true|false}`})
		return
	}

	if *body.Enabled {
		if reason := s.summaryUnavailableReason(id); reason != "" {
			s.refuseSummary(w, reason)
			return
		}
		if err := s.MorningSummaries.Enable(r.Context(), id.User, id.Email, s.now()); err != nil {
			s.fail(w, err)
			return
		}
		s.logger().Info("morning summary switched on", "rider", id.User)
	} else if s.MorningSummaries != nil {
		// Turning it off never needs the feature to be available.
		if err := s.MorningSummaries.Disable(r.Context(), id.User, s.now()); err != nil {
			s.fail(w, err)
			return
		}
		s.logger().Info("morning summary switched off", "rider", id.User)
	}

	out, err := s.currentSummary(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTestMorningSummary sends one short message to the rider's own address,
// so an admin can prove the SMTP setup works. Never to an address from the
// request.
func (s *Server) handleTestMorningSummary(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) {
		return
	}
	id := auth.FromContext(r.Context())
	if reason := s.summaryUnavailableReason(id); reason != "" {
		s.refuseSummary(w, reason)
		return
	}
	if s.TestMailLimiter != nil && !s.TestMailLimiter.Allow(id.User) {
		s.logger().Info("morning summary test refused: rate limited", "rider", id.User)
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many test emails: try again in a few minutes"})
		return
	}

	body := "This is a test message from Domestique.\n\nIf you can read it, your morning summary will reach you.\n"
	if base := s.publicURL(); base != "" {
		body += "\nChange or turn off the morning summary: " + strings.TrimRight(base, "/") + "/training/fitness\n"
	}
	if err := s.Mailer.Send(r.Context(), id.Email, "Domestique: test message", body); err != nil {
		stage, code := mailFailure(err)
		s.logger().Warn("morning summary: test send failed", "rider", id.User, "stage", stage, "code", code)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "the test email could not be sent (" + mailFailureText(stage, code) + ")",
		})
		return
	}
	s.logger().Info("morning summary test sent", "rider", id.User)
	w.WriteHeader(http.StatusNoContent)
}

// mailFailure is all that is ever reported of a failed send: the stage and the
// numeric reply code. Servers echo the recipient in their errors, so nothing
// they said is passed on.
func mailFailure(err error) (stage string, code int) {
	var se *mailer.SendError
	if errors.As(err, &se) {
		return se.Stage, se.Code
	}
	return "unknown", 0
}

func mailFailureText(stage string, code int) string {
	if code != 0 {
		return "smtp " + stage + " failed, code " + strconv.Itoa(code)
	}
	return "smtp " + stage + " failed"
}

// ---------- the pass after the morning sync ----------

// sendMorningSummaries mails every opted-in rider their day, once, after the
// first sync slot of the local day. Called from the metrics pass after it has
// been recorded as done, so nothing here can delay or fail a sync.
//
//   - Only the day's first slot sends, so the 21:00 pass never does; and a
//     pass that runs late (the process was down at 06:30) still sends until
//     12:00 local, then skips.
//   - "Today" is the schedule's zone, never the server's.
//   - The day is claimed with a compare-and-set before the send, so two
//     replicas, a restart, or a failed send cannot produce a second email. One
//     attempt per rider per day: no retry, the next day's slot is the next try.
//   - One rider's failure never stops another's.
//   - A log line holds the rider's name and, for a failure, the stage and the
//     code. Never the address, the subject, the verdict or a reason.
func (s *Server) sendMorningSummaries(ctx context.Context, sched syncschedule.Schedule) {
	if s.MorningSummaries == nil {
		return
	}
	loc := sched.Location()
	now := s.now().In(loc)
	slot := sched.Last(now).In(loc)
	if !sched.FirstOfDay(slot) || slot.Format(dateLayout) != now.Format(dateLayout) {
		return
	}
	if now.Hour() >= summaryNoonHour {
		return
	}

	prefs, err := s.MorningSummaries.ListEnabled(ctx)
	if err != nil {
		s.logger().Warn("morning summary: listing riders failed", "err", err)
		return
	}
	if len(prefs) == 0 {
		return
	}
	if !s.smtpConfigured() {
		s.logger().Warn("morning summary: not configured, riders are opted in but nothing can send", "riders", len(prefs))
		return
	}

	today := now.Format(dateLayout)
	var sent, failed int
	for _, p := range prefs {
		if ctx.Err() != nil {
			break
		}
		claimed, err := s.MorningSummaries.Claim(ctx, p.Rider, today)
		if err != nil {
			s.logger().Warn("morning summary: could not claim the day", "rider", p.Rider, "err", err)
			continue
		}
		if !claimed {
			continue
		}
		in, err := s.morningSummaryInput(ctx, p.Rider, now)
		if err != nil {
			s.logger().Warn("morning summary: could not read the plan", "rider", p.Rider, "err", err)
			countSummary(ctx, "failed")
			failed++
			continue
		}
		subject, body := morningsummary.Compose(in)
		if err := s.Mailer.Send(ctx, p.Email, subject, body); err != nil {
			stage, code := mailFailure(err)
			s.logger().Warn("morning summary: send failed", "rider", p.Rider, "stage", stage, "code", code)
			countSummary(ctx, "failed")
			failed++
			continue
		}
		countSummary(ctx, "sent")
		sent++
	}
	if sent+failed > 0 {
		s.logger().Info("morning summary pass finished", "sent", sent, "failed", failed)
	}
}

// morningSummaryInput gathers what one rider's email says. Only an unreadable
// plan is an error: a day of "rest day" for a rider whose plan could not be
// read would be wrong. Every other part degrades to being left out.
func (s *Server) morningSummaryInput(ctx context.Context, rider string, now time.Time) (morningsummary.Input, error) {
	today := now.Format(dateLayout)
	in := morningsummary.Input{Today: today, AppURL: s.publicURL()}

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
		s.logger().Warn("morning summary: could not tell whether today was ridden", "err", err)
		ridden = nil
	}
	in.Ridden = len(todays) > 0
	for _, w := range todays {
		if !ridden[w.ID] {
			in.Ridden = false
		}
	}

	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		s.logger().Warn("morning summary: could not read recent sessions", "err", err)
	} else if latest, err := s.latestFitness(ctx, rider); err != nil {
		s.logger().Warn("morning summary: could not read fitness", "err", err)
	} else {
		in.Verdict = s.assessReadinessAt(ctx, rider, sessions, latest, now).Verdict
	}

	in.Eased = s.sessionEasedToday(ctx, rider, today, todays)
	if !in.Ridden {
		in.Weather = s.todaysWeatherReasons(ctx, rider, now, workouts, ridden)
		in.FTPTest = s.ftpTestSuggestedFor(ctx, rider, now, workouts)
	}
	return in, nil
}

// easingRules are the automatic rules that make a planned session easier: the
// ones the email may say "already eased" for. A moved session, a threshold
// update and a level recalibration do not make today's session easier.
var easingRules = map[why.Rule]bool{
	why.ReadinessRest: true, why.ReadinessCaution: true, why.ReadinessTomorrow: true,
	why.FatigueStruggles: true, why.FatigueOverload: true, why.StruggleStepDown: true, why.FTPTestEve: true,
}

func (s *Server) sessionEasedToday(ctx context.Context, rider, today string, todays []workout.Workout) bool {
	if len(todays) == 0 {
		return false
	}
	ids := make([]string, 0, len(todays))
	for _, w := range todays {
		ids = append(ids, w.ID)
	}
	latest, err := s.Training.LatestAdjustments(ctx, rider, workout.SubjectWorkout, ids)
	if err != nil {
		s.logger().Warn("morning summary: could not read plan changes", "err", err)
		return false
	}
	for _, a := range latest {
		if a.Day == today && easingRules[a.Rule] {
			return true
		}
	}
	return false
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
		s.logger().Warn("morning summary: weather forecast unavailable", "err", err)
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
