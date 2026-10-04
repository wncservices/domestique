package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/ridestart"
)

// This file is the rider's side of the ride start point: where a route
// generated for a planned ride begins. Routes are personal location data and
// this is the closest thing to a home address the app is ever given, so:
//   - it is opt-in by row and deletable, like the weather town;
//   - it is stored at three decimals (about 110 m), never the precise value;
//   - no endpoint here, or anywhere, returns it: the answer is whether one is
//     set and its town. Only the route-candidate generator reads the
//     coordinates, and the loops it returns start and end there as any
//     route does;
//   - nothing logs a coordinate, the town or the rider beside it.

// rideStartDTO deliberately has no coordinate fields.
type rideStartDTO struct {
	Set   bool   `json:"set"`
	Place string `json:"place,omitempty"`
}

// rideStartRider is the caller after the checks every ride-start endpoint
// shares. The rider comes from the session, never the body.
func (s *Server) rideStartRider(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.require(w, r, auth.PermManageTraining) {
		return "", false
	}
	if s.RideStarts == nil {
		s.logger().Warn("ride start request refused: no ride start store on this server")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "saving a ride start is not available on this deployment",
		})
		return "", false
	}
	return auth.FromContext(r.Context()).User, true
}

func (s *Server) handleGetRideStart(w http.ResponseWriter, r *http.Request) {
	rider, ok := s.rideStartRider(w, r)
	if !ok {
		return
	}
	p, found, err := s.RideStarts.Get(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rideStartDTO{Set: found, Place: p.Place})
}

// handleSetRideStart saves or moves the start. The town and coordinates come
// from the route builder's location chooser in the browser; the store rounds
// them.
func (s *Server) handleSetRideStart(w http.ResponseWriter, r *http.Request) {
	rider, ok := s.rideStartRider(w, r)
	if !ok {
		return
	}
	var body struct {
		Place string   `json:"place"`
		Lat   *float64 `json:"lat"`
		Lon   *float64 `json:"lon"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.Lat == nil || body.Lon == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "lat and lon are required"})
		return
	}
	if err := s.RideStarts.Set(r.Context(), rider, body.Place, *body.Lat, *body.Lon); err != nil {
		if errors.Is(err, ridestart.ErrInvalid) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.fail(w, err)
		return
	}
	// No rider, place or coordinate: this line says only that it happened.
	s.logger().Info("ride start saved")
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteRideStart is the opt-out. Idempotent.
func (s *Server) handleDeleteRideStart(w http.ResponseWriter, r *http.Request) {
	rider, ok := s.rideStartRider(w, r)
	if !ok {
		return
	}
	if err := s.RideStarts.Delete(r.Context(), rider); err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("ride start removed")
	w.WriteHeader(http.StatusNoContent)
}
