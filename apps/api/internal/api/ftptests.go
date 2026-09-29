package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/fitnesstest"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/testschedule"
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
	// Reason is one of no_ftp, after_recovery, block_start, stale.
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

	plan, goals, err := s.focusPlan(ctx, rider, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	hasPower, err := s.hasRecentPower(ctx, rider, now)
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
func (s *Server) focusPlan(ctx context.Context, rider string, now time.Time) (*periodization.Plan, []workout.Goal, error) {
	goals, err := s.Training.ListGoals(ctx, rider)
	if err != nil {
		return nil, nil, err
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
			return nil, nil, err
		}
		for _, wk := range plan.Weeks {
			if wk.StartDate == monday {
				return &plan, goals, nil
			}
		}
	}
	return nil, goals, nil
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
