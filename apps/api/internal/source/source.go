// Package source is the route library: routes as rows, the GPX itself as a
// blob in the row.
//
// There is one implementation, and deliberately so. An earlier version could
// also read a directory of GPX files kept under git, which sounded appealing —
// review and history for free — but it meant a second storage model that could
// not do half of what the database one could: no uploads, no Komoot import,
// nowhere to link a head unit, nowhere to keep sync state. Every feature had
// to ask which kind of library it was talking to, and the answer decided
// whether the feature existed.
//
// So: a database, PostgreSQL or SQLite. Routes get in by upload or import.
package source

import (
	"context"
	"errors"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/model"
)

// ErrNotFound is returned for a slug the library does not hold.
var ErrNotFound = errors.New("no such route")

// ErrAlreadyOwned is returned for an UpdateRequest with ClaimOwner set
// against a route that no longer has an empty owner by the time the write
// actually lands — see UpdateRequest.ClaimOwner's own doc comment.
var ErrAlreadyOwned = errors.New("route already has an owner")

// CreateRequest is an upload.
type CreateRequest struct {
	// Filename is the uploaded file's name, used to derive a slug and a
	// fallback title. Optional.
	Filename string
	Name     string
	Descript string
	Tags     []string
	Targets  *[]string
	GPX      []byte
	// UploadedBy records which rider added the route.
	UploadedBy string
	// Sport defaults to model.SportCycling when empty — see
	// model.RouteMeta.EffectiveSport.
	Sport model.Sport
}

// UpdateRequest edits an existing route. Nil fields are left alone.
type UpdateRequest struct {
	Name     *string
	Descript *string
	Tags     *[]string
	Targets  *[]string
	Enabled  *bool
	// Owner is set only for the narrow claim-an-orphan case (see the API
	// package's handleUpdate) — Update itself does not decide who may claim
	// what, it just writes the value it is given.
	Owner *string
	// ClaimOwner, when true, makes the write conditional: it only takes
	// effect if the route's owner is still empty at the moment the UPDATE
	// actually runs, returning ErrAlreadyOwned otherwise. The API layer's
	// own pre-check (routeOwner, in handleUpdate) reads and rejects early
	// for the common case, but that read is not tied to the write that
	// follows it — two riders racing to claim the same orphaned route
	// could otherwise both pass that check and the second UPDATE would
	// silently overwrite the first rider's claim. This is the guard that
	// actually closes it, at the one layer that can make read-then-write
	// atomic.
	ClaimOwner bool
	// GPX replaces the track when non-nil.
	GPX []byte
	// Sport, non-nil to change it.
	Sport *model.Sport
}

// Library is what the rest of the app talks to. The one implementation is DB;
// the interface exists so tests can substitute something simpler.
type Library interface {
	Describe() string
	List(ctx context.Context) ([]model.Route, []string, error)
	Track(ctx context.Context, slug string) ([]gpx.Point, error)
	// Cues returns whatever turn-by-turn instructions the route's own GPX
	// carries — see gpx.ParseCues. Nil, not an error, when it has none.
	Cues(ctx context.Context, slug string) ([]gpx.Cue, error)
	GPX(ctx context.Context, slug string) ([]byte, error)
	Create(ctx context.Context, req CreateRequest) (model.Route, error)
	Update(ctx context.Context, slug string, req UpdateRequest) (model.Route, error)
	Delete(ctx context.Context, slug string) error
	// ElevationConfigured reports whether backfilling has anything to call —
	// see DB.elevation's own doc comment. The API layer uses this to tell
	// "recalculate elevation" apart from every other write: unlike a rename
	// or a target change, it has nothing to do if this is false, so it
	// should say so rather than silently no-op.
	ElevationConfigured() bool
	// RecalculateElevation re-runs elevation backfill against a route's own
	// currently stored GPX — see DB's own doc comment for why this needs no
	// elevation-specific logic beyond handing that GPX back to Update.
	RecalculateElevation(ctx context.Context, slug string) (model.Route, error)
}
