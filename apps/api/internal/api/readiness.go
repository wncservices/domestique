package api

import (
	"net/http"

	"github.com/wncservices/domestique/apps/api/internal/auth"
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
}

type readinessResponseDTO struct {
	Today readinessTodayDTO  `json:"today"`
	Days  []dailyWellnessDTO `json:"days"`
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

	sessions, err := s.Training.ListSessions(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	snapshots, err := s.Training.ListFitnessSnapshots(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	var latest *workout.FitnessSnapshot
	if len(snapshots) > 0 {
		latest = &snapshots[len(snapshots)-1]
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
	writeJSON(w, http.StatusOK, out)
}
