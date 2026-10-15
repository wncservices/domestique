package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/bestefforts"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/thresholds"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// ---------- DTOs ----------
//
// Mirrored by hand in apps/web/src/api/types.ts — change them together.

// effortCellDTO is one best: its value, the ride's date and the ride.
type effortCellDTO struct {
	Value     float64 `json:"value"`
	Date      string  `json:"date"`
	SessionID string  `json:"sessionId"`
}

// effortRowDTO is one metric of the Best efforts card. A scope the rider has no
// value in is null, never a zero.
type effortRowDTO struct {
	Metric  string         `json:"metric"`
	Label   string         `json:"label"`
	Unit    string         `json:"unit"`
	AllTime *effortCellDTO `json:"allTime"`
	Season  *effortCellDTO `json:"season"`
	Last42  *effortCellDTO `json:"last42"`
}

type curvePointDTO struct {
	Seconds int     `json:"seconds"`
	Watts   float64 `json:"watts"`
}

// powerCurvesDTO is the power-duration chart: the best at each standard window,
// this season, last season, and over all time (the upper envelope).
type powerCurvesDTO struct {
	Season     []curvePointDTO `json:"season"`
	LastSeason []curvePointDTO `json:"lastSeason"`
	AllTime    []curvePointDTO `json:"allTime"`
}

type bestEffortsResponseDTO struct {
	Sport   string         `json:"sport"`
	Metrics []effortRowDTO `json:"metrics"`
	Curve   powerCurvesDTO `json:"curve"`
	// NewBests are the awards the rider has not been told about yet. The page
	// toasts them and posts them back to /announce.
	NewBests []bestEffortAwardDTO `json:"newBests"`
}

// sessionAwardDTO is a record a ride set, on a ride's analysis.
type sessionAwardDTO struct {
	Metric string `json:"metric"`
	Label  string `json:"label"`
	Scope  string `json:"scope"`
}

func effortCell(e workout.BestEffort, ok bool) *effortCellDTO {
	if !ok {
		return nil
	}
	return &effortCellDTO{Value: e.Value, Date: e.Date, SessionID: e.SessionID}
}

// handleBestEfforts is GET /api/training/best-efforts?sport=cycling|running.
// Owner-only by construction: the rider is the session's, there is no rider
// parameter to point elsewhere. Seasons are calendar years of local-date
// strings, and "last 42 days" is the window threshold detection reads, so
// "recent form" means one thing.
func (s *Server) handleBestEfforts(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User

	sport := r.URL.Query().Get("sport")
	if sport == "" {
		sport = string(model.SportCycling)
	}
	if sport != string(model.SportCycling) && sport != string(model.SportRunning) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sport must be cycling or running"})
		return
	}

	today, ok := s.today(w)
	if !ok {
		return
	}
	year := today.Year()
	scopes := effortScopes{
		season:     [2]string{strconv.Itoa(year) + "-01-01", strconv.Itoa(year) + "-12-31"},
		lastSeason: [2]string{strconv.Itoa(year-1) + "-01-01", strconv.Itoa(year-1) + "-12-31"},
		last42:     [2]string{today.AddDate(0, 0, -thresholds.DetectionWindowDays).Format(dateLayout), today.Format(dateLayout)},
	}

	out := bestEffortsResponseDTO{
		Sport:   sport,
		Metrics: []effortRowDTO{},
		Curve:   powerCurvesDTO{Season: []curvePointDTO{}, LastSeason: []curvePointDTO{}, AllTime: []curvePointDTO{}},
	}
	ctx := r.Context()
	for _, m := range bestefforts.Metrics() {
		if m.Sport != "" && m.Sport != sport {
			continue
		}
		row := effortRowDTO{Metric: m.Key, Label: m.Label, Unit: m.Unit}
		var err error
		if row.AllTime, err = s.effortIn(ctx, rider, m.Key, "", ""); err == nil {
			row.Season, err = s.effortIn(ctx, rider, m.Key, scopes.season[0], scopes.season[1])
		}
		if err == nil {
			row.Last42, err = s.effortIn(ctx, rider, m.Key, scopes.last42[0], scopes.last42[1])
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		out.Metrics = append(out.Metrics, row)

		// The curve is the power metrics only, so a run has none.
		if seconds, isPower := bestefforts.PowerSeconds(m.Key); isPower {
			if row.AllTime != nil {
				out.Curve.AllTime = append(out.Curve.AllTime, curvePointDTO{Seconds: seconds, Watts: row.AllTime.Value})
			}
			if row.Season != nil {
				out.Curve.Season = append(out.Curve.Season, curvePointDTO{Seconds: seconds, Watts: row.Season.Value})
			}
			last, err := s.effortIn(ctx, rider, m.Key, scopes.lastSeason[0], scopes.lastSeason[1])
			if err != nil {
				s.fail(w, err)
				return
			}
			if last != nil {
				out.Curve.LastSeason = append(out.Curve.LastSeason, curvePointDTO{Seconds: seconds, Watts: last.Value})
			}
		}
	}
	out.NewBests = s.pendingEffortAwards(ctx, rider)
	writeJSON(w, http.StatusOK, out)
}

type effortScopes struct {
	season, lastSeason, last42 [2]string
}

func (s *Server) effortIn(ctx context.Context, rider, metric, from, to string) (*effortCellDTO, error) {
	e, ok, err := s.Training.BestIn(ctx, rider, metric, from, to)
	if err != nil {
		return nil, err
	}
	return effortCell(e, ok), nil
}

// today is the rider's local date, as a time at midnight UTC for date arithmetic.
func (s *Server) today(w http.ResponseWriter) (time.Time, bool) {
	day, err := time.Parse(dateLayout, s.localDate(s.now()))
	if err != nil {
		s.fail(w, err)
		return time.Time{}, false
	}
	return day, true
}

type announceRequestDTO struct {
	Awards []struct {
		SessionID string `json:"sessionId"`
		Metric    string `json:"metric"`
	} `json:"awards"`
}

// maxAnnounce bounds one announce: a rider has nine metrics a ride, and a page
// load lists what is pending, not a history.
const maxAnnounce = 200

// handleAnnounceBestEfforts is POST /api/training/best-efforts/announce: the
// page has shown these awards, so they are not listed as new again. The rider
// is the session's, so naming someone else's award changes nothing.
func (s *Server) handleAnnounceBestEfforts(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User

	var body announceRequestDTO
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if len(body.Awards) > maxAnnounce {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "too many awards in one request"})
		return
	}
	told := make([]workout.BestEffort, 0, len(body.Awards))
	for _, a := range body.Awards {
		told = append(told, workout.BestEffort{SessionID: a.SessionID, Metric: a.Metric})
	}
	if err := s.Training.MarkAwardsAnnounced(r.Context(), rider, told); err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("best efforts announced", "rider", rider, "count", len(told))
	writeJSON(w, http.StatusOK, map[string]int{"announced": len(told)})
}

// awardsSince loads a rider's awards on or after sinceDate once, keyed by
// session id and in display order, so a fitness or week response can attach
// them to every ride with a map lookup. A failure degrades: rides come back
// without their chips, logged at Warn, because the request itself succeeded.
func (s *Server) awardsSince(ctx context.Context, rider, sinceDate string) map[string][]sessionAwardDTO {
	rows, err := s.Training.ListAwards(ctx, rider, sinceDate, "")
	if err != nil {
		s.logger().Warn("reading ride awards failed", "rider", rider, "err", err)
		return nil
	}
	order := map[string]int{}
	for i, m := range bestefforts.Metrics() {
		order[m.Key] = i
	}
	sort.SliceStable(rows, func(i, j int) bool { return order[rows[i].Metric] < order[rows[j].Metric] })
	out := map[string][]sessionAwardDTO{}
	for _, e := range rows {
		a := sessionAwardDTO{Metric: e.Metric, Scope: e.Award}
		if m, ok := metricByKey(e.Metric); ok {
			a.Label = m.Label
		}
		out[e.SessionID] = append(out[e.SessionID], a)
	}
	return out
}
