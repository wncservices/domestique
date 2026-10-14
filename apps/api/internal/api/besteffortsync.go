package api

import (
	"context"
	"fmt"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/bestefforts"
	"github.com/wncservices/domestique/apps/api/internal/rideimport"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// effortAwardDays is how soon after its date a ride has to be analysed to be
// able to set a record. A sync analyses a ride within a day or two of riding
// it; anything analysed later is history arriving late (a first sync, a ride
// whose file could not be read until now, a re-link), and a record book that
// toasted those would announce the past as news.
const effortAwardDays = 3

// bestEffortAwardDTO is one new best a sync tells the rider about. Label and
// Unit are carried so the toast needs no table of its own.
type bestEffortAwardDTO struct {
	SessionID string  `json:"sessionId"`
	Metric    string  `json:"metric"`
	Label     string  `json:"label"`
	Unit      string  `json:"unit"`
	Scope     string  `json:"scope"`
	Value     float64 `json:"value"`
	Previous  float64 `json:"previous"`
}

func metricByKey(key string) (bestefforts.Metric, bool) {
	for _, m := range bestefforts.Metrics() {
		if m.Key == key {
			return m, true
		}
	}
	return bestefforts.Metric{}, false
}

func effortAwardDTO(e workout.BestEffort) bestEffortAwardDTO {
	dto := bestEffortAwardDTO{SessionID: e.SessionID, Metric: e.Metric, Scope: e.Award, Value: e.Value, Previous: e.Previous}
	if m, ok := metricByKey(e.Metric); ok {
		dto.Label, dto.Unit = m.Label, m.Unit
	}
	return dto
}

// newEfforts is what a ride added to the record book in one pass: the metrics
// that did not have a row before, with the ride's value for each. Only those
// can be awarded.
type newEfforts struct {
	session workout.CompletedSession
	values  map[string]float64
}

// storeEfforts writes a just-analysed ride's efforts. The award is decided
// later, once every ride of the pass is stored (awardNewEfforts), so the order
// the rides arrive in cannot decide which of them was the record.
func (s *Server) storeEfforts(ctx context.Context, sess workout.CompletedSession, sa workout.SessionAnalysis) (newEfforts, error) {
	efforts := bestefforts.Efforts(sa, sess.Sport)
	fresh, err := s.Training.UpsertEfforts(ctx, sa.Rider, sess.ID, sess.Sport, sess.Date, efforts)
	ne := newEfforts{session: sess, values: map[string]float64{}}
	for _, key := range fresh {
		ne.values[key] = efforts[key]
	}
	return ne, err
}

// awardNewEfforts decides the awards for the rows a pass added.
//
// A ride is eligible only when it is not an import and its date is within
// effortAwardDays of today. Each metric is judged against every other stored
// value in its scope, the rides of this very pass included: an older ride
// synced in the same batch as a newer one cannot claim a record the newer one
// beats. The log line carries rider, metric and scope; never a value, which is
// health data next to a name.
func (s *Server) awardNewEfforts(ctx context.Context, rider string, batch []newEfforts) {
	today, err := time.Parse(dateLayout, s.localDate(s.now()))
	if err != nil {
		s.logger().Warn("best efforts: could not read today's date, awarding nothing", "rider", rider)
		return
	}
	cutoff := today.AddDate(0, 0, -effortAwardDays).Format(dateLayout)

	for _, ne := range batch {
		sess := ne.session
		if sess.Provider == rideimport.Provider || sess.Date < cutoff || len(sess.Date) < 4 {
			continue
		}
		// A season is the calendar year of the ride's own date string, no
		// timezone arithmetic.
		from, to := sess.Date[:4]+"-01-01", sess.Date[:4]+"-12-31"
		for key, value := range ne.values {
			m, ok := metricByKey(key)
			if !ok {
				continue
			}
			season, err := s.Training.ScopeValues(ctx, rider, key, from, to, sess.ID)
			if err != nil {
				s.logger().Warn("best efforts: reading a season failed", "rider", rider, "metric", key, "err", err)
				continue
			}
			allTime, err := s.Training.ScopeValues(ctx, rider, key, "", "", sess.ID)
			if err != nil {
				s.logger().Warn("best efforts: reading the history failed", "rider", rider, "metric", key, "err", err)
				continue
			}
			scope := bestefforts.Award(m, value, season, allTime)
			if scope == "" {
				continue
			}
			prior := season
			if scope == bestefforts.ScopeAllTime {
				prior = allTime
			}
			if err := s.Training.SetEffortAward(ctx, sess.ID, key, scope, maxOf(prior)); err != nil {
				s.logger().Warn("best efforts: saving an award failed", "rider", rider, "metric", key, "scope", scope, "err", err)
				continue
			}
			s.logger().Info("best effort recorded", "rider", rider, "metric", key, "scope", scope)
		}
	}
}

func maxOf(values []float64) float64 {
	best := 0.0
	for _, v := range values {
		best = max(best, v)
	}
	return best
}

// pendingEffortAwards is what the rider has not been told yet.
func (s *Server) pendingEffortAwards(ctx context.Context, rider string) []bestEffortAwardDTO {
	rows, err := s.Training.UnannouncedAwards(ctx, rider)
	if err != nil {
		// Derived data: the sync itself succeeded. Warn, not Error.
		s.logger().Warn("best efforts: listing unannounced awards failed", "rider", rider, "err", err)
		return nil
	}
	out := make([]bestEffortAwardDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, effortAwardDTO(r))
	}
	return out
}

// RebuildBestEfforts regenerates the record book from the analyses when the
// table is empty, which is how a deployment that predates it gets one. It is
// derived data (session_analyses is the source of truth) and runs once: a table
// with any row is left alone. The log carries a count and nothing else.
func (s *Server) RebuildBestEfforts(ctx context.Context) error {
	if s.Training == nil {
		return nil
	}
	n, err := s.Training.RebuildBestEfforts(ctx, bestefforts.Efforts)
	if err != nil {
		return fmt.Errorf("rebuild best efforts: %w", err)
	}
	if n > 0 {
		s.logger().Info("best efforts rebuilt", "rows", n)
	}
	return nil
}
