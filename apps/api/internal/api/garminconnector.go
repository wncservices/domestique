package api

import (
	"context"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/fitworkout"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/targets"
)

// GarminConsumer is the OAuth1 consumer pair a sign-in is signed with.
//
// One pair per deployment, not per rider: it identifies the *application* to
// Garmin, and every rider's sign-in is signed with the same one. Where it
// comes from is the Server's business (see garminConsumer); the connector is
// handed one and uses it.
type GarminConsumer struct {
	Key    string
	Secret string
}

// Configured reports whether both halves are present. One without the other
// signs nothing, so it counts as absent.
func (c GarminConsumer) Configured() bool { return c.Key != "" && c.Secret != "" }

// GarminConnector signs riders in to Garmin Connect.
//
// An interface for the same reason KomootConnector is one: it is the seam
// against a third-party service that has no contract, and tests must not
// depend on Garmin being reachable or on anyone's real account.
type GarminConnector interface {
	// Connect signs in with a password. The password is used here and nowhere
	// else — what comes back is a session to store in its place.
	Connect(ctx context.Context, consumer GarminConsumer, email, password string) (garmin.Session, error)
	// Devices lists the head units on an account, from a stored session. No
	// password: this is what the session is for.
	Devices(ctx context.Context, consumer GarminConsumer, session garmin.Session) ([]garmin.Device, error)
	// Courses returns a client that can push and remove courses, from that
	// same stored session.
	Courses(consumer GarminConsumer, session garmin.Session) (targets.Courses, error)
	// ListCourses lists every course already on the account — sync-back and
	// duplicate detection, not scoped to anything this app itself pushed.
	ListCourses(ctx context.Context, consumer GarminConsumer, session garmin.Session) ([]garmin.Course, error)
	// DownloadGPX fetches one of those courses' tracks, to bring it into the
	// library as a new route.
	DownloadGPX(ctx context.Context, consumer GarminConsumer, session garmin.Session, courseID string) ([]byte, error)
	// ListActivities lists a rider's own recently completed activities —
	// the metrics-ingestion direction, alongside ListCourses' own
	// sync-back for routes. See docs/training-plan.md's own account of
	// why this needed its own client method (activitylist-service, not
	// course-service) and its own research pass.
	ListActivities(ctx context.Context, consumer GarminConsumer, session garmin.Session) ([]garmin.Activity, error)
	// PushWorkout creates a structured workout on a connected account and
	// returns Garmin's id for it — the workout-builder counterpart to
	// Courses, and not folded into that same method: a course push goes
	// through internal/targets' Target interface because internal/sync's
	// diff engine drives it, while a workout push is a rider's own one-shot
	// action with no diff engine behind it (see docs/training-plan.md's own
	// "Structured workouts and the providers" for why this needed its own
	// research and its own client method rather than reusing the FIT course
	// path).
	PushWorkout(ctx context.Context, consumer GarminConsumer, session garmin.Session, name, sport string, steps []fitworkout.Step) (string, error)
	// RestingHeartRate returns a rider's resting heart rate for one date
	// from Connect's own wellness data — see garmin.Client.RestingHeartRate's
	// own doc comment for why this is a real overnight measurement, not a
	// workout's average HR, and why a zero result is a normal "no reading"
	// rather than a failure.
	RestingHeartRate(ctx context.Context, consumer GarminConsumer, session garmin.Session, date time.Time) (int, error)
	// Biometrics reads the rider's max heart rate, running threshold pace and
	// cycling FTP as Connect holds them — what lets the fitness profile be
	// filled in without asking. Best-effort per field: see
	// garmin.Client.Biometrics for why a partial result comes back alongside
	// an error.
	Biometrics(ctx context.Context, consumer GarminConsumer, session garmin.Session, now time.Time) (garmin.Biometrics, error)
	// UpdateWorkout replaces a workout PushWorkout created; it fails with
	// garmin.ErrWorkoutGone when the rider has since deleted it in Connect.
	UpdateWorkout(ctx context.Context, consumer GarminConsumer, session garmin.Session, remoteID, name, sport string, steps []fitworkout.Step) error
	// DeleteWorkout removes one; garmin.ErrWorkoutGone means it was already gone.
	DeleteWorkout(ctx context.Context, consumer GarminConsumer, session garmin.Session, remoteID string) error
	// ScheduleWorkout places a workout on a calendar date ("YYYY-MM-DD") and
	// returns the id of that calendar entry (possibly empty — see
	// garmin.Client.ScheduleWorkout).
	ScheduleWorkout(ctx context.Context, consumer GarminConsumer, session garmin.Session, remoteID, date string) (string, error)
	// UnscheduleWorkout removes a calendar entry ScheduleWorkout returned.
	UnscheduleWorkout(ctx context.Context, consumer GarminConsumer, session garmin.Session, scheduleID string) error
}

// LiveGarmin is the real connector: it talks to Garmin.
//
// It lives here rather than in internal/garmin because it exists to satisfy
// this package's interface. internal/garmin stays a client with no opinion
// about how a session is stored.
type LiveGarmin struct {
	// Log receives the one thing that is worth knowing and not worth failing
	// over: that the profile lookup did not work. Nil is fine.
	Log func(msg string, args ...any)
}

// Connect signs in and returns the session to keep in the password's place.
//
// The profile lookup afterwards is deliberately not fatal. It is what puts a
// name on the connection, and it is also the first call that proves the
// OAuth1 token can be exchanged for a bearer — but it is an undocumented
// endpoint, and refusing a sign-in that otherwise worked because Garmin moved
// a profile URL would be the wrong trade. The connection is kept; the name is
// the email until the rider reconnects.
func (l LiveGarmin) Connect(ctx context.Context, consumer GarminConsumer, email, password string) (garmin.Session, error) {
	if !consumer.Configured() {
		return garmin.Session{}, garmin.ErrNoConsumer
	}

	client := garmin.New()
	// Supplied rather than read from the environment by the client, so the
	// pair an admin pasted into the UI is the one that signs the request.
	client.SetConsumer(consumer.Key, consumer.Secret)

	if err := client.Login(ctx, email, password); err != nil {
		return garmin.Session{}, err
	}

	session := client.Session()
	if profile, err := client.Profile(ctx); err == nil {
		session.DisplayName = profile.Name()
	} else if l.Log != nil {
		l.Log("garmin profile lookup failed; the connection is kept without a name", "err", err)
	}
	return session, nil
}

// resume rebuilds a signed-in client from a stored session.
//
// Devices and Courses both need exactly this and nothing more. They arrived
// from separate branches doing it identically, which is the moment to put it
// in one place rather than keep two copies in step.
//
// The consumer is needed even though no password is: every call underneath
// begins by exchanging the OAuth1 token for a bearer, and that exchange is
// signed with it.
func (l LiveGarmin) resume(consumer GarminConsumer, session garmin.Session) (*garmin.Client, error) {
	if !consumer.Configured() {
		return nil, garmin.ErrNoConsumer
	}

	client := garmin.New()
	client.SetConsumer(consumer.Key, consumer.Secret)
	client.Resume(session)
	return client, nil
}

// Devices lists the head units registered to a connected account.
func (l LiveGarmin) Devices(ctx context.Context, consumer GarminConsumer, session garmin.Session) ([]garmin.Device, error) {
	client, err := l.resume(consumer, session)
	if err != nil {
		return nil, err
	}
	return client.Devices(ctx)
}

// Courses returns a client for pushing courses to a connected account.
func (l LiveGarmin) Courses(consumer GarminConsumer, session garmin.Session) (targets.Courses, error) {
	return l.resume(consumer, session)
}

// ListCourses lists every course already on the account.
func (l LiveGarmin) ListCourses(ctx context.Context, consumer GarminConsumer, session garmin.Session) ([]garmin.Course, error) {
	client, err := l.resume(consumer, session)
	if err != nil {
		return nil, err
	}
	return client.ListCourses(ctx)
}

// DownloadGPX fetches one course's track as GPX.
func (l LiveGarmin) DownloadGPX(ctx context.Context, consumer GarminConsumer, session garmin.Session, courseID string) ([]byte, error) {
	client, err := l.resume(consumer, session)
	if err != nil {
		return nil, err
	}
	return client.DownloadGPX(ctx, courseID)
}

// ListActivities lists a rider's own recently completed activities.
func (l LiveGarmin) ListActivities(ctx context.Context, consumer GarminConsumer, session garmin.Session) ([]garmin.Activity, error) {
	client, err := l.resume(consumer, session)
	if err != nil {
		return nil, err
	}
	return client.Activities(ctx, 0)
}

// PushWorkout creates a structured workout on a connected account.
func (l LiveGarmin) PushWorkout(ctx context.Context, consumer GarminConsumer, session garmin.Session, name, sport string, steps []fitworkout.Step) (string, error) {
	client, err := l.resume(consumer, session)
	if err != nil {
		return "", err
	}
	return client.CreateWorkout(ctx, name, sport, steps)
}

// RestingHeartRate fetches one day's resting heart rate from a connected
// account's wellness data.
func (l LiveGarmin) RestingHeartRate(ctx context.Context, consumer GarminConsumer, session garmin.Session, date time.Time) (int, error) {
	client, err := l.resume(consumer, session)
	if err != nil {
		return 0, err
	}
	return client.RestingHeartRate(ctx, date)
}

// Biometrics fetches the rider's physiology numbers from a connected account.
func (l LiveGarmin) Biometrics(ctx context.Context, consumer GarminConsumer, session garmin.Session, now time.Time) (garmin.Biometrics, error) {
	client, err := l.resume(consumer, session)
	if err != nil {
		return garmin.Biometrics{}, err
	}
	return client.Biometrics(ctx, now)
}

// UpdateWorkout replaces a workout already on a connected account.
func (l LiveGarmin) UpdateWorkout(ctx context.Context, consumer GarminConsumer, session garmin.Session, remoteID, name, sport string, steps []fitworkout.Step) error {
	client, err := l.resume(consumer, session)
	if err != nil {
		return err
	}
	return client.UpdateWorkout(ctx, remoteID, name, sport, steps)
}

// DeleteWorkout removes a workout from a connected account.
func (l LiveGarmin) DeleteWorkout(ctx context.Context, consumer GarminConsumer, session garmin.Session, remoteID string) error {
	client, err := l.resume(consumer, session)
	if err != nil {
		return err
	}
	return client.DeleteWorkout(ctx, remoteID)
}

// ScheduleWorkout places a workout on a connected account's calendar.
func (l LiveGarmin) ScheduleWorkout(ctx context.Context, consumer GarminConsumer, session garmin.Session, remoteID, date string) (string, error) {
	client, err := l.resume(consumer, session)
	if err != nil {
		return "", err
	}
	return client.ScheduleWorkout(ctx, remoteID, date)
}

// UnscheduleWorkout removes a calendar entry from a connected account.
func (l LiveGarmin) UnscheduleWorkout(ctx context.Context, consumer GarminConsumer, session garmin.Session, scheduleID string) error {
	client, err := l.resume(consumer, session)
	if err != nil {
		return err
	}
	return client.UnscheduleWorkout(ctx, scheduleID)
}
