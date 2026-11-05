package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/targets"
	"github.com/wncservices/domestique/apps/api/internal/wahoo"
)

// wahooRoutes resolves one rider's Wahoo client from their stored
// connection, refreshing an expired access token along the way.
//
// Every failure here is "this rider cannot push to Wahoo," never "the push
// is broken" — one account failing must leave the rest of the plan alone,
// the same contract garminCourses keeps.
func (s *Server) wahooRoutes(ctx context.Context, rider string) (targets.WahooRoutes, error) {
	token, err := s.wahooAccessToken(ctx, rider)
	if err != nil {
		return nil, err
	}
	return wahooSessionClient{client: s.Wahoo, token: token}, nil
}

// wahooAccessToken resolves one rider's working Wahoo access token,
// refreshing it first if it has expired. The one "get me a token for this
// rider" step both push (wahooRoutes, above) and sync-back
// (wahooroutes.go's wahooSessionFor) need — pulled out here since it used
// to live only inside wahooRoutes, before sync-back needed the token on its
// own without also getting wrapped in wahooSessionClient.
//
// Unlike Garmin, an expired token is not the end of the connection: Wahoo
// issued a refresh token for exactly this, so a stale access token is
// refreshed transparently here rather than asking the rider to reconnect —
// reconnecting is reserved for when the refresh token itself no longer
// works (revoked, or Wahoo expired it for inactivity).
func (s *Server) wahooAccessToken(ctx context.Context, rider string) (string, error) {
	if s.Wahoo == nil {
		return "", errors.New("this deployment has no Wahoo app credentials configured")
	}
	if s.Links == nil || rider == "" {
		return "", errors.New("no Wahoo sign-in to use")
	}

	session, err := s.storedWahooSession(rider)
	if err != nil {
		return "", err
	}
	if !session.Expired() {
		return session.AccessToken, nil
	}

	// If Wahoo rotates refresh tokens (unconfirmed; the client handles both),
	// two callers refreshing the same expired session spend one token twice:
	// the second is refused, and a refresh whose result is not saved takes the
	// rotated token with it. Per process only: another replica can still race. One refresh
	// at a time per rider, and the stored session is read again once the lock
	// is held: if another caller has already refreshed, its token is used.
	unlock := wahooRefreshLocks.lock(rider)
	defer unlock()
	session, err = s.storedWahooSession(rider)
	if err != nil {
		return "", err
	}
	if !session.Expired() {
		return session.AccessToken, nil
	}

	refreshed, err := s.Wahoo.Refresh(ctx, session.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("%s's Wahoo sign-in could not be refreshed: reconnect it in Settings: %w", rider, err)
	}

	// #nosec G117 -- gosec flags AccessToken as a marshaled secret-looking
	// field; this JSON exists only long enough to reach providerlink.Save
	// a few lines down, which seals it with the app's AES-256-GCM key
	// before it reaches the database. Same false positive as
	// handleWahooCallback's identical marshal in wahooconnect.go.
	sealed, err := json.Marshal(refreshed)
	if err != nil {
		return "", fmt.Errorf("encoding the refreshed wahoo session: %w", err)
	}
	// Save is a full upsert, not a patch — Email/DisplayName have to be
	// carried forward explicitly or a refresh silently blanks them.
	link, err := s.Links.Get(wahooProvider, rider)
	if err != nil {
		return "", fmt.Errorf("re-reading the wahoo connection for %s: %w", rider, err)
	}
	if _, err := s.Links.Save(wahooProvider, rider, providerlink.Connection{
		Email: link.Email, DisplayName: link.DisplayName, Secret: string(sealed),
	}); err != nil {
		return "", fmt.Errorf("storing the refreshed wahoo session: %w", err)
	}

	return refreshed.AccessToken, nil
}

// storedWahooSession reads one rider's Wahoo session from the sealed store.
func (s *Server) storedWahooSession(rider string) (wahoo.Session, error) {
	_, secret, err := s.Links.Secret(wahooProvider, rider)
	if err != nil {
		return wahoo.Session{}, fmt.Errorf("%s has not connected Wahoo: %w", rider, err)
	}
	var session wahoo.Session
	if err := json.Unmarshal([]byte(secret), &session); err != nil {
		return wahoo.Session{}, fmt.Errorf("the stored Wahoo sign-in for %s is unreadable: %w", rider, err)
	}
	return session, nil
}

// wahooRefreshLocks holds one lock per rider for the refresh in
// wahooAccessToken. It is package-level, not a Server field, because the
// refresh is the only thing it guards and every caller goes through this
// package; a rider's key is the only state it keeps, so it stays small.
var wahooRefreshLocks keyedMutex

// keyedMutex is a set of mutexes, one per key, created on first use.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// lock takes the mutex for key and returns the function that releases it.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*sync.Mutex{}
	}
	m, ok := k.locks[key]
	if !ok {
		m = &sync.Mutex{}
		k.locks[key] = m
	}
	k.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// wahooSessionClient adapts *wahoo.Client (whose route methods take an
// access token per call, since one Client is shared deployment-wide across
// every rider) to targets.WahooRoutes (whose methods take none, resolved
// once per push by wahooRoutes above) — the same bridging role
// LiveGarmin.resume's returned *garmin.Client plays for Garmin, just as an
// explicit wrapper instead of structural fit, because a shared client
// genuinely needs the token as a parameter where Garmin's per-rider client
// instance does not.
type wahooSessionClient struct {
	client *wahoo.Client
	token  string
}

func (c wahooSessionClient) CreateRoute(ctx context.Context, route targets.WahooRoute) (string, error) {
	return c.client.CreateRoute(ctx, c.token, toWahooRouteRequest(route))
}

func (c wahooSessionClient) UpdateRoute(ctx context.Context, id string, route targets.WahooRoute) (string, error) {
	return c.client.UpdateRoute(ctx, c.token, id, toWahooRouteRequest(route))
}

func (c wahooSessionClient) DeleteRoute(ctx context.Context, id string) error {
	return c.client.DeleteRoute(ctx, c.token, id)
}

func toWahooRouteRequest(route targets.WahooRoute) wahoo.RouteRequest {
	return wahoo.RouteRequest{
		ExternalID:  route.ExternalID,
		Name:        route.Name,
		Description: route.Description,
		UpdatedAt:   route.UpdatedAt,
		DistanceM:   route.DistanceM,
		AscentM:     route.AscentM,
		StartLat:    route.StartLat,
		StartLng:    route.StartLng,
		Filename:    route.Filename,
		FIT:         route.FIT,
		Sport:       route.Sport,
	}
}
