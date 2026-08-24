package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
)

// komootProvider is this provider's key in the shared connection store.
const komootProvider = "komoot"

// KomootSession is what a signed-in Komoot client exposes about itself, so a
// connection can be stored and later resumed.
type KomootSession struct {
	UserID      string
	Token       string
	DisplayName string
}

// KomootConnector signs in to Komoot and resumes stored sessions.
//
// An interface for the same reason KomootImporter is one: it is the seam
// against a third-party API that has no contract, and tests must not depend on
// Komoot being reachable or on anyone's real account.
type KomootConnector interface {
	// Connect signs in with a password. The password is used here and
	// nowhere else — what comes back is a session to store in its place.
	Connect(ctx context.Context, email, password string) (KomootImporter, KomootSession, error)
	// Resume rebuilds a client from a stored session.
	Resume(userID, token string) KomootImporter
}

type komootConnectionDTO struct {
	Connected   bool   `json:"connected"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
	// Shared reports that a server-wide account from the environment is in
	// use. The rider did not connect it and cannot disconnect it.
	Shared bool `json:"shared"`
	// CanConnect is false when there is no encryption key, so the UI does not
	// offer a form whose result cannot be kept.
	CanConnect bool `json:"canConnect"`
}

// handleKomootConnection reports the caller's own connection.
func (s *Server) handleKomootConnection(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermKomootSync) {
		return
	}

	dto := komootConnectionDTO{CanConnect: s.Links.CanStore()}

	if link, err := s.riderLink(r); err == nil {
		dto.Connected = true
		dto.Email = link.Email
		dto.DisplayName = link.DisplayName
		dto.UpdatedAt = link.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
	} else if s.Komoot != nil {
		// No personal connection, but the deployment has one from the
		// environment. Saying so stops a rider wondering why the import works
		// when they never signed in.
		dto.Connected = true
		dto.Shared = true
	}

	writeJSON(w, http.StatusOK, dto)
}

// handleKomootConnect signs in and stores the session.
func (s *Server) handleKomootConnect(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermKomootSync) {
		return
	}
	if s.Connector == nil || !s.KomootEnabled {
		s.komootDisabled(w)
		return
	}
	if !s.Links.CanStore() {
		// Refusing is the whole point: without a key the only way to honour
		// this request would be to write the session somewhere readable.
		s.logger().Warn("komoot connect requested but this deployment has no encryption key configured")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment cannot store a Komoot connection: " + secrets.ErrNoKey.Error(),
		})
		return
	}

	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	if body.Email == "" || body.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "email and password are both required",
		})
		return
	}

	// The rider comes from the session, never the body — the same rule as
	// linking a head unit, and for the same reason.
	rider := auth.FromContext(r.Context()).User
	if rider == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "no rider in the session to attach the connection to",
		})
		return
	}
	if !s.rateLimitConnect(w, rider) {
		return
	}

	_, session, err := s.Connector.Connect(r.Context(), body.Email, body.Password)
	if err != nil {
		// Log without the password and without the error's own body, which
		// can echo the request.
		s.logger().Warn("komoot connect failed", "rider", rider)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "Komoot did not accept those details",
		})
		return
	}

	link, err := s.Links.Save(komootProvider, rider, providerlink.Connection{
		Email:       body.Email,
		DisplayName: session.DisplayName,
		ExternalID:  session.UserID,
		Secret:      session.Token,
	})
	if err != nil {
		s.logger().Error("storing komoot connection failed", "rider", rider, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "could not store the connection",
		})
		return
	}

	s.logger().Info("komoot connected", "rider", rider, "account", session.DisplayName)
	writeJSON(w, http.StatusOK, komootConnectionDTO{
		Connected:   true,
		Email:       link.Email,
		DisplayName: link.DisplayName,
		UpdatedAt:   link.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		CanConnect:  true,
	})
}

// handleKomootDisconnect forgets the caller's connection.
func (s *Server) handleKomootDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermKomootSync) {
		return
	}

	rider := auth.FromContext(r.Context()).User
	if rider == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no rider in the session"})
		return
	}
	if s.Links == nil {
		writeJSON(w, http.StatusOK, komootConnectionDTO{})
		return
	}

	if err := s.Links.Delete(komootProvider, rider); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("komoot disconnected", "rider", rider)
	writeJSON(w, http.StatusOK, komootConnectionDTO{CanConnect: s.Links.CanStore()})
}

// riderLink is the caller's stored connection, if any.
func (s *Server) riderLink(r *http.Request) (providerlink.Link, error) {
	if s.Links == nil {
		return providerlink.Link{}, providerlink.ErrNotFound
	}
	rider := auth.FromContext(r.Context()).User
	if rider == "" {
		return providerlink.Link{}, providerlink.ErrNotFound
	}
	return s.Links.Get(komootProvider, rider)
}

// komootFor returns the importer to use for this request.
//
// A rider's own connection wins over the deployment-wide one from the
// environment: if somebody went to the trouble of connecting their account,
// importing from a different account would be a surprising thing to do.
func (s *Server) komootFor(r *http.Request) KomootImporter {
	if client := s.komootOwnConnectionFor(r); client != nil {
		return client
	}
	return s.Komoot
}

// komootOwnConnectionFor resolves only the caller's own personal Komoot
// connection — never s.Komoot, the deployment-wide shared client komootFor
// falls back to when nobody has connected personally. Destructive
// operations (deleting a tour) use this instead of komootFor: "nobody
// connected personally, so act against the shared account" is the right
// default for listing and importing — every rider can already see what a
// shared account offers — but exactly the wrong one for delete, which would
// otherwise let any rider who never authenticated against the shared
// account still remove tours from it.
func (s *Server) komootOwnConnectionFor(r *http.Request) KomootImporter {
	return s.komootClientForRider(auth.FromContext(r.Context()).User)
}

// komootClientForRider is komootOwnConnectionFor without an *http.Request
// behind it — the auto-import poller has no request, just a rider name it
// read from providerlink.Store.ListRiders.
func (s *Server) komootClientForRider(rider string) KomootImporter {
	if s.Links == nil || s.Connector == nil || rider == "" {
		return nil
	}
	userID, token, err := s.Links.Secret(komootProvider, rider)
	switch {
	case err == nil:
		return s.Connector.Resume(userID, token)
	case errors.Is(err, providerlink.ErrNotFound), errors.Is(err, secrets.ErrNoKey):
		return nil
	default:
		s.logger().Warn("komoot credentials unusable", "rider", rider, "err", err)
		return nil
	}
}
