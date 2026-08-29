package targets

import (
	"context"
	"errors"
	"fmt"

	"github.com/wncservices/domestique/apps/api/internal/fitcourse"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/model"
)

// Garmin pushes courses to one rider's Garmin Connect account.
//
// There is no self-serve Garmin API — the official Courses API is Connect
// Developer Program only — so this drives the same course-service endpoints
// Connect's own Training → Courses → Import button uses, with the OAuth2
// bearer from the rider's stored sign-in. Grey area, and it can break on any
// Garmin deploy: when it does, one push fails, the route stays in the library
// and nothing else stops working.
//
// FIT rather than GPX, deliberately. A GPX course navigates as a breadcrumb
// line and says nothing at a junction; a FIT course carries turn cues.
type Garmin struct {
	Account model.Account

	// Track returns the route's points. The adapter builds the file itself
	// because the shape a provider wants is the adapter's business.
	Track func(ctx context.Context, slug string) ([]gpx.Point, error)
	// Courses is the signed-in client for this account's rider. Resolved per
	// push rather than held, so a session that was refreshed or removed since
	// the server started is the one used.
	Courses func(rider string) (Courses, error)
	// TurnCues asks for cues inferred from the track's geometry. Off unless
	// the deployment asked: an inferred cue at the wrong junction is worse
	// than no cue at all.
	TurnCues bool
	// ClimbCues asks for climb category cues inferred from the track's
	// elevation profile. Off unless the deployment asked, for the same
	// reason TurnCues is.
	ClimbCues bool
	// Log receives what is worth knowing and not worth failing over. Nil is
	// fine.
	Log func(msg string, args ...any)
}

var errGarminNotWired = errors.New(
	"garmin push needs a signed-in account: connect Garmin in Settings")

// Create uploads the route as a new course.
func (g *Garmin) Create(ctx context.Context, route model.Route) (string, error) {
	client, data, err := g.prepare(ctx, route)
	if err != nil {
		return "", err
	}
	return client.ImportCourse(ctx, courseFilename(route), data)
}

// Update replaces a course.
//
// Connect has no replace: a course is imported, and the old one deleted
// afterwards. That order matters. Importing first means a failure leaves the
// rider with the course they already had and the state untouched, so the next
// push tries again. Deleting first would take the route off the device and
// then fail to put anything back.
//
// If the delete fails after a successful import the new id is still returned
// with no error: the state has to move on, or every later push would import
// another copy. What is left behind is one stale course, which is why it is
// logged.
func (g *Garmin) Update(ctx context.Context, remoteID string, route model.Route) (string, error) {
	client, data, err := g.prepare(ctx, route)
	if err != nil {
		return "", err
	}

	newID, err := client.ImportCourse(ctx, courseFilename(route), data)
	if err != nil {
		return "", err
	}

	if remoteID != "" && remoteID != newID {
		if err := client.DeleteCourse(ctx, remoteID); err != nil && g.Log != nil {
			g.Log("garmin: the replaced course could not be removed",
				"course", remoteID, "route", route.Slug, "err", err)
		}
	}
	return newID, nil
}

// Delete removes a course from the account.
func (g *Garmin) Delete(ctx context.Context, remoteID string) error {
	client, err := g.client()
	if err != nil {
		return err
	}
	return client.DeleteCourse(ctx, remoteID)
}

// prepare resolves the client and renders the route as a FIT course.
func (g *Garmin) prepare(ctx context.Context, route model.Route) (Courses, []byte, error) {
	client, err := g.client()
	if err != nil {
		return nil, nil, err
	}
	if g.Track == nil {
		return nil, nil, errGarminNotWired
	}

	points, err := g.Track(ctx, route.Slug)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the track for %s: %w", route.Slug, err)
	}

	data, err := fitcourse.Encode(points, fitcourse.Options{
		Name:      route.Name,
		Sport:     fitcourse.SportFromString(string(route.EffectiveSport())),
		TurnCues:  g.TurnCues,
		ClimbCues: g.ClimbCues,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("building a course file for %s: %w", route.Slug, err)
	}
	return client, data, nil
}

func (g *Garmin) client() (Courses, error) {
	if g.Courses == nil {
		return nil, errGarminNotWired
	}
	client, err := g.Courses(g.Account.Rider)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errGarminNotWired
	}
	return client, nil
}

// courseFilename names the upload. Connect shows the name from inside the FIT
// file, so this only has to be a plausible filename — but a slug is a better
// thing to see in a network log than "course.fit".
func courseFilename(route model.Route) string {
	if route.Slug == "" {
		return "course.fit"
	}
	return route.Slug + ".fit"
}
