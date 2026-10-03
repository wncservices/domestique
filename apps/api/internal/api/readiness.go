package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// dailyWellnessDTO mirrors workout.DailyWellness for the wire — the Fitness
// page's Recovery card and today's readiness chip both read this shape. See
// apps/web/src/api/types.ts's DailyWellnessDTO, which must change alongside
// this one.
type dailyWellnessDTO struct {
	Date string `json:"date"`

	HRVLastNight float64 `json:"hrvLastNight,omitempty"`
	HRVWeeklyAvg float64 `json:"hrvWeeklyAvg,omitempty"`
	HRVStatus    string  `json:"hrvStatus,omitempty"`

	SleepSeconds int `json:"sleepSeconds,omitempty"`
	SleepScore   int `json:"sleepScore,omitempty"`

	ReadinessScore int    `json:"readinessScore,omitempty"`
	ReadinessLevel string `json:"readinessLevel,omitempty"`

	RestingHR int `json:"restingHr,omitempty"`
}

func wellnessDTO(w workout.DailyWellness) dailyWellnessDTO {
	return dailyWellnessDTO{
		Date:           w.Date,
		HRVLastNight:   w.HRVLastNight,
		HRVWeeklyAvg:   w.HRVWeeklyAvg,
		HRVStatus:      w.HRVStatus,
		SleepSeconds:   w.SleepSeconds,
		SleepScore:     w.SleepScore,
		ReadinessScore: w.ReadinessScore,
		ReadinessLevel: w.ReadinessLevel,
		RestingHR:      w.RestingHR,
	}
}

// readinessTodayDTO is today's verdict — Wellness is omitted entirely when
// there is no Garmin row for today (a Wahoo-only rider, or a watch not worn),
// which is also exactly when the verdict came from load/form rules alone.
type readinessTodayDTO struct {
	Verdict  string            `json:"verdict"`
	Reasons  []string          `json:"reasons,omitempty"`
	Wellness *dailyWellnessDTO `json:"wellness,omitempty"`
	// LifeEvent is true when today falls inside one of the rider's life events:
	// the day card shows the event instead, and the chip is hidden.
	LifeEvent bool `json:"lifeEvent,omitempty"`
}

// tomorrowForecastDTO is the look-ahead for tomorrow's hard session — only
// present when tomorrow has an eligible workout and the forecast is not
// ready. Reasons are the plain form the rules give them; the UI words the
// form reason in the future tense itself.
type tomorrowForecastDTO struct {
	Date        string   `json:"date"`
	Risk        string   `json:"risk"`
	Reasons     []string `json:"reasons,omitempty"`
	WorkoutID   string   `json:"workoutId"`
	WorkoutName string   `json:"workoutName"`
}

type readinessResponseDTO struct {
	Today    readinessTodayDTO    `json:"today"`
	Days     []dailyWellnessDTO   `json:"days"`
	Tomorrow *tomorrowForecastDTO `json:"tomorrow,omitempty"`
	// CrewRide is advice for a crew ride today, or tomorrow from the forecast,
	// when the rider is not ready. It never changes the ride.
	CrewRide *crewAdviceDTO `json:"crewRide,omitempty"`
}

// readinessDisplayDays is how many of the most recent daily_wellness rows
// the Fitness page's Recovery card shows — the spec's own "last 7 days".
const readinessDisplayDays = 7

// handleGetReadiness returns today's readiness verdict and the last week of
// Garmin wellness — owner-only, the same permission every other training
// endpoint gates on. Today's Assessment is built the same way adaptation
// itself builds it (see assessReadiness) so the chip a rider sees always
// agrees with what actually eased today's session.
func (s *Server) handleGetReadiness(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	today, ok := parseTodayParam(w, r, s.now())
	if !ok {
		return
	}

	sessions, err := s.Training.ListSessions(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	latest, err := s.latestFitness(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}

	assessment := s.assessReadiness(r.Context(), rider, sessions, latest)

	sinceDate := s.now().AddDate(0, 0, -(readinessDisplayDays - 1)).Format("2006-01-02")
	rows, err := s.Training.ListWellness(r.Context(), rider, sinceDate)
	if err != nil {
		s.fail(w, err)
		return
	}

	todayStr := s.now().Format("2006-01-02")
	out := readinessResponseDTO{
		Today: readinessTodayDTO{Verdict: string(assessment.Verdict), Reasons: assessment.Reasons},
		Days:  make([]dailyWellnessDTO, 0, len(rows)),
	}
	for _, row := range rows {
		dto := wellnessDTO(row)
		out.Days = append(out.Days, dto)
		if row.Date == todayStr {
			today := dto
			out.Today.Wellness = &today
		}
	}

	blackout, err := s.blackoutFor(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	out.Today.LifeEvent = blackout[today.Format(dateLayout)]

	forecast, target, ok, err := s.tomorrowFor(r.Context(), rider, today, sessions, latest)
	if err != nil {
		s.fail(w, err)
		return
	}
	// No advisory for a day the rider is away.
	if blackout[today.AddDate(0, 0, 1).Format(dateLayout)] {
		ok = false
	}
	if ok && forecast.Verdict != readiness.Ready {
		out.Tomorrow = &tomorrowForecastDTO{
			Date: today.AddDate(0, 0, 1).Format(dateFormat), Risk: string(forecast.Verdict),
			Reasons: forecast.Reasons, WorkoutID: target.ID, WorkoutName: target.Name,
		}
	}
	workouts, err := s.Training.ListWorkouts(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	profile, _, err := s.Training.GetProfile(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	out.CrewRide = s.crewRideAdvice(r.Context(), rider, today, assessment.Verdict, workouts, sessions, latest, profile, blackout)
	writeJSON(w, http.StatusOK, out)
}

// parseTodayParam reads ?today=YYYY-MM-DD, the caller's own idea of today —
// the same precedent handleUpcomingRides sets for ?from=, for the same
// reason: the browser always knows its local day and the server's process
// zone is no guarantee. Omitted, it falls back to the server's UTC today. A
// malformed or out-of-window value is a 400 and ok is false.
func parseTodayParam(w http.ResponseWriter, r *http.Request, fallback time.Time) (time.Time, bool) {
	raw := r.URL.Query().Get("today")
	if raw == "" {
		return calendarDay(fallback.UTC()), true
	}
	t, err := time.Parse(dateFormat, raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "today must be a date in YYYY-MM-DD form"})
		return time.Time{}, false
	}
	// Bounded to a day either side of the server's UTC today, which covers
	// every real zone (from twelve hours behind UTC to fourteen ahead).
	// Unbounded, a rider could aim the ease at an arbitrary future day, and
	// a browser clock a day behind would make "tomorrow" the server's today.
	server := calendarDay(fallback.UTC())
	if t.Before(server.AddDate(0, 0, -1)) || t.After(server.AddDate(0, 0, 1)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "today is more than a day away from the server's date"})
		return time.Time{}, false
	}
	return t, true
}

func (s *Server) latestFitness(ctx context.Context, rider string) (*workout.FitnessSnapshot, error) {
	snapshots, err := s.Training.ListFitnessSnapshots(ctx, rider)
	if err != nil || len(snapshots) == 0 {
		return nil, err
	}
	return &snapshots[len(snapshots)-1], nil
}

// tomorrowFor reads what forecastTomorrow needs and runs it — fresh each
// time, anchored to today (the caller's day), never a cached shape. Both the
// GET banner and the ease action go through here so they cannot disagree
// about what tomorrow looks like.
func (s *Server) tomorrowFor(ctx context.Context, rider string, today time.Time, sessions []workout.CompletedSession, latest *workout.FitnessSnapshot) (readiness.Assessment, workout.Workout, bool, error) {
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return readiness.Assessment{}, workout.Workout{}, false, err
	}
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		return readiness.Assessment{}, workout.Workout{}, false, err
	}
	todayAssessment := s.assessReadinessForForecast(ctx, rider, sessions, latest, today)
	forecast, target, ok := forecastTomorrow(today, workouts, sessions, latest, todayAssessment, profile)
	return forecast, target, ok, nil
}

// forecastSignal gives one of the forecast's reason sentences a label that says
// what it is about, keyed on the wording readiness.ForecastTomorrow uses. A
// sentence it does not recognise is labelled "Forecast" rather than guessed at.
func forecastSignal(reason string) why.Signal {
	kind, label := "forecast", "Forecast"
	switch {
	case strings.Contains(reason, "form is projected"):
		kind, label = "form", "Projected form"
	case strings.Contains(reason, "needed to rest today"):
		kind, label = "readiness", "Rest day today"
	case strings.Contains(reason, "load this week"):
		kind, label = "load", "Load with today counted"
	case strings.Contains(reason, "third hard day"):
		kind, label = "load", "Hard days in a row"
	}
	return why.Signal{Kind: kind, Label: label, Value: reason}
}

// easeTomorrowRefusedMessage is what a rider sees when the fresh forecast no
// longer calls for easing, or tomorrow's session is no longer one this may
// touch. One plain message for every such case: the rider's next step is the
// same (reload and look), and the server does not need to explain which of
// its internal checks tripped.
const easeTomorrowRefusedMessage = "Tomorrow's session no longer needs easing, or has already been changed — nothing was changed."

// handleEaseTomorrow applies the forecast: it recomputes it from scratch
// (never trusting what the banner showed when the page loaded — training
// data moves), and if tomorrow's session is still at risk eases that one
// workout through the same Downgrade/StepDown application today's readiness
// uses. It refuses with 409 rather than improvise when there is nothing
// eligible or the fresh forecast is ready. Tomorrow only: the target comes
// from forecastTomorrow, which only ever looks at tomorrow's date.
func (s *Server) handleEaseTomorrow(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	today, ok := parseTodayParam(w, r, s.now())
	if !ok {
		return
	}

	sessions, err := s.Training.ListSessions(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	latest, err := s.latestFitness(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	forecast, target, ok, err := s.tomorrowFor(r.Context(), rider, today, sessions, latest)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok || forecast.Verdict == readiness.Ready {
		s.logger().Warn("ease tomorrow refused: nothing to ease", "rider", rider, "eligible", ok)
		writeJSON(w, http.StatusConflict, map[string]string{"error": easeTomorrowRefusedMessage})
		return
	}

	profile, _, err := s.Training.GetProfile(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	reason := "Eased ahead of time — " + strings.Join(forecast.Reasons, "; ")
	// A rider-confirmed click on an automatic suggestion: the reason and the
	// inputs are the forecast's, not the rider's, so it is recorded like any
	// automatic change. The forecast's reasons already carry their numbers
	// (projected form, load ratio), so each is kept as its own signal.
	signals := make([]why.Signal, 0, len(forecast.Reasons))
	for _, r := range forecast.Reasons {
		signals = append(signals, forecastSignal(r))
	}
	change := adapter.Change{
		WorkoutID: target.ID, Reason: reason,
		Why: why.NewRecord(why.ReadinessTomorrow, reason, why.ReadinessInputs{Verdict: string(forecast.Verdict), Signals: signals}),
	}
	if forecast.Verdict == readiness.Rest {
		change.Downgrade = true
	} else {
		change.StepDown = true
		change.StepDownSourceID = "readiness-forecast:" + target.Date
	}
	what, err := s.applyChange(r.Context(), rider, target, map[string]workout.Workout{target.ID: target}, profile, change)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("tomorrow's workout eased by the rider", "workout", target.ID, "rider", rider, "change", what)
	writeJSON(w, http.StatusOK, map[string]string{"reason": reason})
}
