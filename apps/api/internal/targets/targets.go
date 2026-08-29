// Package targets holds the provider adapters.
//
// Each target owns one account. Adapters are intentionally dumb: the diff
// engine decides what to do, the adapter only knows how to talk to its provider.
package targets

import (
	"context"
	"fmt"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/model"
)

// Target is one rider's account on one provider.
type Target interface {
	// Create pushes a new route and returns the provider's id for it.
	Create(ctx context.Context, route model.Route) (string, error)
	// Update replaces an existing route and returns the (possibly new) id.
	Update(ctx context.Context, remoteID string, route model.Route) (string, error)
	// Delete removes a route from the account.
	Delete(ctx context.Context, remoteID string) error
}

// Courses is the slice of a provider's client an adapter needs to push.
//
// An interface so the adapters can be tested without a provider, and so
// internal/garmin stays a client with no opinion about syncing.
type Courses interface {
	// ImportCourse uploads a course file and returns the provider's id.
	ImportCourse(ctx context.Context, filename string, data []byte) (string, error)
	// DeleteCourse removes one.
	DeleteCourse(ctx context.Context, id string) error
}

// Implemented reports whether pushes to a provider actually work yet, so the
// UI can say "not wired up" rather than offering a push that always fails.
func Implemented(p model.Provider) bool {
	switch p {
	case model.ProviderGarmin, model.ProviderWahoo:
		return true
	default:
		return false
	}
}

// Factory builds adapters with what they need to do real work: the route's
// points, and a signed-in client for the account's rider.
//
// A struct rather than more arguments to Build because the list will keep
// growing as providers land, and because a caller with none of it — the CLI,
// which has no rider sessions — can still build adapters that fail with
// something a person can act on rather than a nil dereference.
type Factory struct {
	// Track returns a route's points by slug.
	Track func(ctx context.Context, slug string) ([]gpx.Point, error)
	// Cues returns whatever turn-by-turn instructions the route's own GPX
	// carries, for TurnCues to prefer over a derived guess. Nil is the
	// ordinary case; see fitcourse.NativeTurns.
	Cues func(ctx context.Context, slug string) ([]gpx.Cue, error)
	// Garmin resolves the signed-in Garmin client for a rider.
	Garmin func(rider string) (Courses, error)
	// Wahoo resolves the signed-in Wahoo client for a rider. Takes a
	// context because resolving one can mean refreshing an expired access
	// token — see Wahoo's own Routes field doc comment.
	Wahoo func(ctx context.Context, rider string) (WahooRoutes, error)
	// TurnCues asks adapters for cues inferred from the track's geometry.
	TurnCues bool
	// ClimbCues asks adapters for climb category cues inferred from the
	// track's elevation profile.
	ClimbCues bool
	// Log receives what is worth knowing and not worth failing over.
	Log func(msg string, args ...any)
}

// Build returns the adapter for an account.
func (f Factory) Build(account model.Account) (Target, error) {
	switch account.Provider {
	case model.ProviderGarmin:
		return &Garmin{
			Account:   account,
			Track:     f.Track,
			Cues:      f.Cues,
			Courses:   f.Garmin,
			TurnCues:  f.TurnCues,
			ClimbCues: f.ClimbCues,
			Log:       f.Log,
		}, nil
	case model.ProviderWahoo:
		return &Wahoo{
			Account: account,
			Track:   f.Track,
			Routes:  f.Wahoo,
			Log:     f.Log,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", account.Provider)
	}
}

// Build returns an adapter with nothing wired to it.
//
// Kept for callers that have no sessions to offer. A Garmin push through this
// fails with "connect Garmin in Settings", which is the truth: the adapter is
// implemented, this caller just cannot reach an account.
func Build(account model.Account) (Target, error) { return Factory{}.Build(account) }
