package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/fitnesstest"
	"github.com/wncservices/domestique/apps/api/internal/lifeevents"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/testschedule"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// ftpSnoozeDays is how long dismissing the FTP test suggestion silences it.
const ftpSnoozeDays = 28

// powerEvidenceDays is how far back a completed session with average power
// counts as proof the rider has a power meter to test with.
const powerEvidenceDays = 90

// ---------- DTOs ----------
//
// Mirrored by hand in apps/web/src/api/types.ts — change them together.

type ftpTestSuggestionDTO struct {
	// Reason is one of no_ftp, plan_start, estimated_ftp, after_recovery, block_start, stale.
	Reason  string `json:"reason"`
	Message string `json:"message"`
	// Date is the suggested day; Recommended the protocol id to pre-select.
	Date        string `json:"date"`
	Recommended string `json:"recommended"`
	// ReplacesWorkoutID is the plan-made session scheduling the test on Date
	// would replace, omitted when the day is empty.
	ReplacesWorkoutID string `json:"replacesWorkoutId,omitempty"`
}

// ftpTestRefDTO points at a test workout: the next scheduled one, or the last
// one ridden and read.
type ftpTestRefDTO struct {
	WorkoutID   string  `json:"workoutId"`
	Protocol    string  `json:"protocol"`
	Date        string  `json:"date"`
	ResultWatts float64 `json:"resultWatts,omitempty"`
}

type ftpTestsDTO struct {
	Protocols     []fitnesstest.Protocol `json:"protocols"`
	Suggestion    *ftpTestSuggestionDTO  `json:"suggestion,omitempty"`
	Scheduled     *ftpTestRefDTO         `json:"scheduled,omitempty"`
	LastTest      *ftpTestRefDTO         `json:"lastTest,omitempty"`
	FTPVerifiedAt string                 `json:"ftpVerifiedAt"`
}

// ---------- Handlers ----------

// handleGetFTPTests is GET /api/training/tests: the protocol menu, the test
// worth suggesting right now (if any), the next scheduled test, the last one
// read, and when FTP was last checked. Owner-only by construction: the rider
// comes from the session, so there is no id to check.
func (s *Server) handleGetFTPTests(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	ctx := r.Context()
	rider := auth.FromContext(ctx).User
	now := s.now()
	todayStr := now.Format(dateLayout)

	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}

	out := ftpTestsDTO{Protocols: fitnesstest.Protocols(), FTPVerifiedAt: profile.FTPVerifiedAt}
	var lastProtocol, lastReadDate string
	for _, wk := range workouts {
		if wk.TestProtocol == "" {
			continue
		}
		// A test that was ridden and read, even one that gave nothing, means FTP
		// was just checked: the banner stays quiet for a while afterwards.
		if wk.Date <= todayStr && wk.TestResultWatts != 0 && wk.Date > lastReadDate {
			lastReadDate = wk.Date
		}
		ref := &ftpTestRefDTO{WorkoutID: wk.ID, Protocol: wk.TestProtocol, Date: wk.Date, ResultWatts: wk.TestResultWatts}
		switch {
		case wk.Date >= todayStr:
			// ListWorkouts orders by date, so the first one found is the next.
			if out.Scheduled == nil {
				out.Scheduled = ref
			}
		case wk.TestResultWatts > 0:
			// Only a test that was ridden and read counts as "last": a skipped
			// one says nothing about which protocol suits the rider.
			if out.LastTest == nil || wk.Date >= out.LastTest.Date {
				out.LastTest = ref
				lastProtocol = wk.TestProtocol
			}
		}
	}

	plan, _, goals, err := s.focusPlan(ctx, rider, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	hasPower, err := s.hasRecentPower(ctx, rider, now)
	if err != nil {
		s.fail(w, err)
		return
	}

	events, err := s.lifeEventsFor(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
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
		Blackout: lifeevents.NoTestDays(events),
	}); sug != nil {
		out.Suggestion = &ftpTestSuggestionDTO{
			Reason: sug.Reason, Message: sug.Message, Date: sug.Date,
			Recommended: sug.Recommended, ReplacesWorkoutID: sug.ReplacesWorkoutID,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSnoozeFTPTest is POST /api/training/tests/ftp/snooze: dismissing the
// suggestion silences it for 28 days. It returns 204; the banner returns
// afterwards only if the rules still fire.
func (s *Server) handleSnoozeFTPTest(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	until := s.now().AddDate(0, 0, ftpSnoozeDays).Format(dateLayout)
	if err := s.Training.SnoozeFTPTest(r.Context(), rider, until); err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("ftp test suggestion snoozed", "rider", rider)
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Inputs ----------

// focusPlan is the plan of the goal the rider is currently training for: the
// same goal weekFocus picks for the week header (most important first, dated
// before undated), the first whose plan covers this week. It also returns all
// the rider's goals, which the test suggestion reads for the A-event quiet
// period. A rider with no plannable goal gets a nil plan, not an error.
func (s *Server) focusPlan(ctx context.Context, rider string, now time.Time) (*periodization.Plan, *workout.Goal, []workout.Goal, error) {
	goals, err := s.Training.ListGoals(ctx, rider)
	if err != nil {
		return nil, nil, nil, err
	}
	ordered := append([]workout.Goal(nil), goals...)
	sortGoalsForFocus(ordered)

	monday := periodization.MondayOf(now).Format(dateLayout)
	for _, g := range ordered {
		plan, _, err := s.reconciledPeriodizationPlan(ctx, g, rider)
		if err == periodization.ErrNoEventDate || err == periodization.ErrEventInThePast {
			continue
		}
		if err != nil {
			return nil, nil, nil, err
		}
		for _, wk := range plan.Weeks {
			if wk.StartDate == monday {
				focus := g
				return &plan, &focus, goals, nil
			}
		}
	}
	return nil, nil, goals, nil
}

// hasRecentPower is true when a cycling session with average power was
// completed in the last 90 days.
func (s *Server) hasRecentPower(ctx context.Context, rider string, now time.Time) (bool, error) {
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		return false, err
	}
	since := now.AddDate(0, 0, -powerEvidenceDays).Format(dateLayout)
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].Date > sessions[j].Date })
	for _, sess := range sessions {
		if sess.Date < since {
			break
		}
		if sess.Sport == "cycling" && sess.AvgPowerWatts > 0 {
			return true, nil
		}
	}
	return false, nil
}

// ---------- Scheduling ----------

// maxFTPGuess and minFTPGuess bound a typed FTP guess for the ramp: outside
// them it is a typo, and a ramp built on it would be useless or unrideable.
const (
	minFTPGuess = 50
	maxFTPGuess = 1000
)

type buildFTPTestRequestDTO struct {
	Protocol string `json:"protocol"`
	// Date schedules the test on that day ("YYYY-MM-DD"); empty builds an
	// unscheduled workout, as before.
	Date string `json:"date"`
	// EstimatedFTP is the ramp's starting point when the profile has no FTP.
	// It is used to build the workout and never saved.
	EstimatedFTP float64 `json:"estimatedFtp"`
}

// handleBuildFTPTest is POST /api/training/tests/ftp. With no body it builds
// the unscheduled 20-minute test it always did. With a protocol and a date it
// schedules that test on that day: the workout is linked to the focus goal (so
// scheduling treats the day as taken) with a description that is not the
// generated one (so replan leaves it), and the day's plan-made session, if
// any, is replaced. Only plan-made sessions are ever replaced: a session the
// rider built, or one another rider owns, is untouched.
func (s *Server) handleBuildFTPTest(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	ctx := r.Context()
	rider := auth.FromContext(ctx).User

	// A missing body is the old "just build one" request; a malformed one is
	// a client bug worth saying so about.
	var body buildFTPTestRequestDTO
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.EstimatedFTP != 0 && (body.EstimatedFTP < minFTPGuess || body.EstimatedFTP > maxFTPGuess) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "estimatedFtp must be between " + strconv.Itoa(minFTPGuess) + " and " + strconv.Itoa(maxFTPGuess) + " watts"})
		return
	}
	now := s.now()
	today := now.Format(dateLayout)
	if body.Date != "" {
		day, err := time.Parse(dateLayout, body.Date)
		if err != nil || day.Format(dateLayout) != body.Date {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date must be YYYY-MM-DD"})
			return
		}
	}

	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	protocol := body.Protocol
	if protocol == "" {
		protocol = fitnesstest.ProtocolTwentyMinute
		if body.Date != "" && profile.FTPWatts > 0 {
			protocol = fitnesstest.ProtocolRamp
		}
	}
	if !fitnesstest.ValidProtocol(protocol) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown protocol"})
		return
	}

	ftp := body.EstimatedFTP
	if ftp == 0 {
		ftp = profile.FTPWatts
	}
	if protocol == fitnesstest.ProtocolRamp && ftp <= 0 {
		s.logger().Warn("ftp test not scheduled: the ramp needs an FTP to start from", "rider", rider)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "The ramp test starts from your FTP. Set one, or type a rough guess."})
		return
	}

	if body.Date != "" {
		if body.Date < today {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "That day is already in the past."})
			return
		}
		away, err := s.blackoutFor(ctx, rider)
		if err != nil {
			s.fail(w, err)
			return
		}
		if away[body.Date] {
			s.logger().Info("ftp test not scheduled: that day is inside a life event", "rider", rider)
			writeJSON(w, http.StatusConflict, map[string]string{"error": "You are away that day."})
			return
		}
		held, err := s.Training.ListWorkouts(ctx, rider)
		if err != nil {
			s.fail(w, err)
			return
		}
		for _, wk := range held {
			if wk.Date == body.Date && wk.CrewRideID != "" {
				s.logger().Info("ftp test not scheduled: that day is a crew ride", "rider", rider)
				writeJSON(w, http.StatusConflict, map[string]string{"error": "That day is your crew ride."})
				return
			}
		}
		ridden, err := s.riddenOn(ctx, rider, body.Date)
		if err != nil {
			s.fail(w, err)
			return
		}
		if ridden {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "You have already ridden that day."})
			return
		}
		// A ride with a route on that day is not replaced (it is the rider's own
		// choice, and deleting it strands its loop), and two sessions on one day
		// is what replacing exists to prevent.
		if routed, err := s.dayHasRoutedRide(ctx, rider, body.Date); err != nil {
			s.fail(w, err)
			return
		} else if routed {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "That day has a ride with a route. Remove the route first, or pick another day."})
			return
		}
	}

	req, _ := fitnesstest.BuildTestWorkout(protocol, ftp)
	req.Rider, req.TestProtocol, req.Date = rider, protocol, body.Date
	if body.Date != "" {
		_, focus, _, err := s.focusPlan(ctx, rider, now)
		if err != nil {
			s.fail(w, err)
			return
		}
		if focus != nil {
			req.GoalID = focus.ID
		}
	}
	wk, err := s.Training.CreateWorkout(ctx, req)
	if err != nil {
		s.fail(w, err)
		return
	}

	replaced := 0
	if body.Date != "" {
		replaced, err = s.replacePlanMadeOn(ctx, rider, body.Date, wk.ID)
		if err != nil {
			s.logger().Error("ftp test not scheduled: the day's plan-made session could not be removed", "rider", rider, "err", err)
			// Take the test back out, so a retry cannot leave two tests on the
			// day. Whatever sessions were removed before the failure stay
			// removed; the tick does not refill a week it has filled, so this
			// is the smaller of the two evils.
			if delErr := s.Training.DeleteWorkout(ctx, wk.ID); delErr != nil {
				s.logger().Error("ftp test rollback failed", "rider", rider, "err", delErr)
			}
			s.fail(w, err)
			return
		}
		// The rider may have a generated workout the day before that a hard
		// test should now follow with an easy one, and the Garmin account
		// should carry the test in place of what it replaced.
		s.adaptRider(ctx, rider)
		if profile.AutoPushWorkouts {
			s.pushWorkoutsForRider(ctx, rider)
		}
	}
	s.logger().Info("ftp test workout built", "rider", rider, "protocol", protocol, "scheduled", body.Date != "", "replaced", replaced)
	// Re-read: adaptRider may have touched neighbours, not this workout, but
	// the response should be what is stored.
	if stored, err := s.Training.GetWorkout(ctx, wk.ID); err == nil {
		wk = stored
	}
	writeJSON(w, http.StatusCreated, workoutDTOFrom(wk))
}

// riddenOn reports whether the rider completed a cycling session on date.
func (s *Server) riddenOn(ctx context.Context, rider, date string) (bool, error) {
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		return false, err
	}
	for _, sess := range sessions {
		if sess.Date == date && sess.Sport == "cycling" {
			return true, nil
		}
	}
	return false, nil
}

// replacePlanMadeOn deletes the rider's plan-made workouts on date, other than
// keep, and takes their Garmin copies off the account. Best-effort on Garmin,
// like every other deletion path.
func (s *Server) replacePlanMadeOn(ctx context.Context, rider, date, keep string) (int, error) {
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, wk := range workouts {
		if wk.Date != date || wk.ID == keep || !isPlanMade(wk) {
			continue
		}
		s.removeWorkoutFromGarmin(ctx, wk)
		if err := s.Training.DeleteWorkout(ctx, wk.ID); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// ---------- Result capture ----------

// ftpTestResultDTO is one FTP test a sync just read: syncMetricsResultDTO's
// "ftpTests", what the UI toasts.
type ftpTestResultDTO struct {
	WorkoutID string `json:"workoutId"`
	Protocol  string `json:"protocol"`
	// FTPWatts is the FTP the test measured; 0 when the ride was unreadable.
	FTPWatts float64 `json:"ftpWatts"`
	// Source is "test" when the finding came from this test, or "rides" when
	// a real breakthrough in the rider's ordinary rides outranked it: then
	// FTPWatts is the rides' value (the one applied or suggested) and
	// TestWatts what the test itself measured.
	Source    string  `json:"source"`
	TestWatts float64 `json:"testWatts,omitempty"`
	// Date is the day the test was ridden.
	Date string `json:"date"`
	// Outcome is applied, suggested, confirmed (within 1% of the FTP on
	// file) or unreadable.
	Outcome string `json:"outcome"`
}

// ftpTestRide is a test ride read on this sync, before detection has said
// what to do with it.
type ftpTestRide struct {
	workoutID, protocol, sessionID, date string
	ftp                                  float64
	readable                             bool
}

// readFTPTestRides finds analysed rides linked to a test workout that has no
// result yet and records each one's result on the workout, once. SetTestResult
// is a compare-and-set on "no result", so a repeated or concurrent sync reads
// a ride exactly once and only that call reports it. A ride with nothing
// usable is recorded with the unreadable marker rather than left blank, or
// every later sync would report it again.
func (s *Server) readFTPTestRides(ctx context.Context, rider string, sessions []workout.CompletedSession, now time.Time) ([]ftpTestRide, error) {
	planned, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return nil, err
	}
	open := map[string]workout.Workout{}
	for _, wk := range planned {
		if wk.TestProtocol != "" && wk.TestResultWatts == 0 {
			open[wk.ID] = wk
		}
	}
	if len(open) == 0 {
		return nil, nil
	}

	since := now.AddDate(0, 0, -2*90).Format(dateLayout)
	analyses, err := s.Training.ListAnalyses(ctx, rider, since)
	if err != nil {
		return nil, err
	}
	sessionByID := make(map[string]workout.CompletedSession, len(sessions))
	for _, sess := range sessions {
		sessionByID[sess.ID] = sess
	}

	var out []ftpTestRide
	for _, a := range analyses {
		wk, ok := open[a.WorkoutID]
		if !ok {
			continue
		}
		sess, ok := sessionByID[a.SessionID]
		if !ok {
			continue
		}
		curve := make(map[int]float64, len(a.PowerCurve))
		for k, v := range a.PowerCurve {
			if sec, err := strconv.Atoi(k); err == nil {
				curve[sec] = v
			}
		}
		value, readable := fitnesstest.FTPFromTest(wk.TestProtocol, curve)
		stored := float64(workout.TestResultUnreadable)
		if readable {
			value = math.Round(value)
			stored = value
		}
		wrote, err := s.Training.SetTestResult(ctx, wk.ID, stored)
		if err != nil {
			return nil, err
		}
		if !wrote {
			continue
		}
		delete(open, wk.ID) // one ride per test
		if readable {
			// Verified as soon as the result is stored, not after detection: if
			// detection fails the sync errors, the result is never reported, and
			// FTP was still checked. The finding itself is stateless and comes
			// back on the next sync.
			if err := s.Training.MarkFTPVerified(ctx, rider, sess.Date); err != nil {
				s.logger().Warn("marking FTP verified from a test failed", "rider", rider, "err", err)
			}
		}
		out = append(out, ftpTestRide{
			workoutID: wk.ID, protocol: wk.TestProtocol, sessionID: sess.ID, date: sess.Date,
			ftp: value, readable: readable,
		})
	}
	return out, nil
}

// freshTestSessions is the set of readable test rides' session ids.
func freshTestSessions(rides []ftpTestRide) map[string]bool {
	out := map[string]bool{}
	for _, r := range rides {
		if r.readable {
			out[r.sessionID] = true
		}
	}
	return out
}

// ftpTestResults turns the rides read this sync, and what detection did with
// them, into the sync result's "ftpTests". "Confirmed" is the absence of any
// finding.
func (s *Server) ftpTestResults(rider string, rides []ftpTestRide, tdr thresholdDetectionResult) []ftpTestResultDTO {
	var out []ftpTestResultDTO
	for _, r := range rides {
		dto := ftpTestResultDTO{WorkoutID: r.workoutID, Protocol: r.protocol, FTPWatts: r.ftp, Source: "test", Date: r.date, Outcome: "unreadable"}
		if r.readable {
			dto.Outcome = tdr.TestOutcomes[r.sessionID]
			if dto.Outcome == "" {
				dto.Outcome = "confirmed"
				// No finding from this test but FTP still had one, and it came
				// from the rider's ordinary rides: a breakthrough outranked the
				// test. Report that finding's own number and fate, not the
				// test's, which would toast a value nothing was done with.
				if tdr.FTPOutcome != "" && !tdr.FTPFromTest {
					dto.Outcome, dto.Source = tdr.FTPOutcome, "rides"
					dto.TestWatts, dto.FTPWatts = r.ftp, tdr.FTPValue
				}
			}
		}
		// Rider and protocol and outcome only: no watts next to a name.
		s.logger().Info("ftp test result read", "rider", rider, "protocol", r.protocol, "outcome", dto.Outcome)
		out = append(out, dto)
	}
	return out
}

// ---------- Test-day preparation ----------

// easeBeforeFTPTests swaps a generated, unadjusted hard session dated the day
// before a scheduled FTP test for an easy one. It runs inside adaptRider, so
// it follows every schedule and replan: a replan rebuilds the day from scratch
// and this puts the easing straight back, which is why the easing is a rule
// applied each pass and not something remembered.
//
// workouts is adaptRider's own snapshot, and alreadyChanged the ids that pass
// has already changed, so a workout is never adjusted twice in one pass.
func (s *Server) easeBeforeFTPTests(ctx context.Context, rider string, workouts []workout.Workout, profile workout.RiderProfile, alreadyChanged map[string]bool) {
	today := s.now().Format(dateLayout)
	var ridden map[string]bool
	for _, wk := range workouts {
		if alreadyChanged[wk.ID] || wk.Date < today || !scheduler.NeedsEasingBeforeTest(wk, workouts) {
			continue
		}
		// A session the rider has already ridden today cannot be eased.
		if wk.Date == today {
			if ridden == nil {
				var err error
				if ridden, err = s.riddenToday(ctx, rider, today, workouts); err != nil {
					s.logger().Warn("adapt: could not tell whether today's session was ridden", "rider", rider, "err", err)
					return
				}
			}
			if ridden[wk.ID] {
				continue
			}
		}

		easy := scheduler.EasyVariant(wk, profile)
		note := adapter.Note(adapter.Change{Reason: scheduler.EasedBeforeTestReason})
		description := wk.Description + " " + note + " Replaces: " + wk.Name + "."
		update := workout.UpdateWorkoutRequest{
			Name: &easy.Name, Steps: &easy.Steps, Zone: &easy.Zone, Level: &easy.Level, Description: &description,
		}
		s.keepIndoor(&update, wk, profile, easy.Zone)
		if _, err := s.Training.UpdateWorkout(ctx, wk.ID, update); err != nil {
			s.logger().Warn("adapt: could not ease the day before an FTP test", "workout", wk.ID, "rider", rider, "err", err)
			continue
		}
		test := testOnDay(workouts, wk.Date)
		s.recordAdjustment(ctx, rider, wk.ID, why.NewRecord(why.FTPTestEve, scheduler.EasedBeforeTestReason,
			why.FTPTestEveInputs{TestDate: test.Date, Protocol: test.TestProtocol}), update.Indoor != nil && *update.Indoor)
		s.logger().Info("workout adapted automatically", "workout", wk.ID, "rider", rider, "change", "eased before an FTP test")
	}
}

// testOnDay is the FTP test dated the day after date, the one that made that
// day an easy one. NeedsEasingBeforeTest has already established it exists.
func testOnDay(workouts []workout.Workout, date string) workout.Workout {
	day, err := time.Parse(dateLayout, date)
	if err != nil {
		return workout.Workout{}
	}
	next := day.AddDate(0, 0, 1).Format(dateLayout)
	for _, w := range workouts {
		if w.TestProtocol != "" && w.Date == next {
			return w
		}
	}
	return workout.Workout{}
}

// dayHasRoutedRide is whether any of the rider's workouts on date links a route.
func (s *Server) dayHasRoutedRide(ctx context.Context, rider, date string) (bool, error) {
	all, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return false, err
	}
	for _, wk := range all {
		if wk.Date == date && wk.RouteSlug != "" {
			return true, nil
		}
	}
	return false, nil
}
