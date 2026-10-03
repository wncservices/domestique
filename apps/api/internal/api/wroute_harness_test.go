package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/ridestart"
	"github.com/wncservices/domestique/apps/api/internal/routing"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// wrNow is a Saturday morning, 08:00 in Brussels, with the zone explicit so
// nothing here depends on the machine's TZ.
var wrNow = time.Date(2026, 10, 3, 8, 0, 0, 0, time.FixedZone("CEST", 2*3600))

// wrHarness runs the server with the stores the workout-route feature uses,
// and a clock and log capture a test can read.
type wrHarness struct {
	t      *testing.T
	client *http.Client
	base   string
	srv    *api.Server
	db     *source.DB

	training *workout.DB
	starts   *ridestart.Store
	engine   *wrEngine
	logs     *syncBuffer

	mu  sync.Mutex
	now time.Time
}

func newWRHarness(t *testing.T, mutate ...func(*wrHarness, *api.Server)) *wrHarness {
	t.Helper()
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	training, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	starts, err := ridestart.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.New(auth.Config{
		Mode:  auth.ModeProxy,
		Roles: auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}, Viewer: []string{"guests"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	h := &wrHarness{t: t, db: db, training: training, starts: starts, logs: &syncBuffer{}, now: wrNow, engine: newWREngine()}
	srv := &api.Server{
		Source:     db,
		Auth:       authenticator,
		Training:   training,
		RideStarts: starts,
		Routing:    h.engine,
		Clock:      h.clock,
		// The time attribute is dropped so a test scanning the log for a
		// coordinate cannot match a digit run in a timestamp.
		Log: slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{
			Level: slog.LevelDebug,
			ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey {
					return slog.Attr{}
				}
				return a
			},
		})),
	}
	for _, m := range mutate {
		m(h, srv)
	}
	h.srv = srv
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	h.client, h.base = server.Client(), server.URL
	return h
}

func (h *wrHarness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

// as sends a request as a rider in the "cyclists" group.
func (h *wrHarness) as(user, method, path, body string) *http.Response {
	h.t.Helper()
	return h.asGroup(user, "cyclists", method, path, body)
}

func (h *wrHarness) asGroup(user, groups, method, path, body string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Remote-User", user)
	req.Header.Set("Remote-Groups", groups)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (h *wrHarness) body(resp *http.Response) string {
	h.t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(raw)
}

// wrCall is what the fake routing engine was asked.
type wrCall struct {
	Start     routing.LatLng
	LengthM   float64
	Seed      int
	Profile   string
	Hilliness int
}

// wrEngine is a fake routing engine: it never touches the network. By
// default it returns a synthetic square loop around the start, a tenth over
// the length asked (as the real one overshoots), climbing in a way that
// varies with the seed so candidates differ. behave overrides that per call.
type wrEngine struct {
	mu     sync.Mutex
	calls  []wrCall
	behave func(c wrCall, n int) (routing.Path, error)
	// blockUntilCancel makes every call wait for its request to be cancelled,
	// the way a slow engine does.
	blockUntilCancel bool
}

func newWREngine() *wrEngine { return &wrEngine{} }

func (e *wrEngine) Route(context.Context, []routing.LatLng, string) (routing.Path, error) {
	return routing.Path{}, errors.New("fake engine: Route is not used here")
}

func (e *wrEngine) RoundTrip(ctx context.Context, start routing.LatLng, lengthM float64, seed int, profile string, hilliness int) (routing.Path, error) {
	e.mu.Lock()
	c := wrCall{Start: start, LengthM: lengthM, Seed: seed, Profile: profile, Hilliness: hilliness}
	e.calls = append(e.calls, c)
	n := len(e.calls)
	behave := e.behave
	e.mu.Unlock()
	if e.blockUntilCancel {
		<-ctx.Done()
	}
	if err := ctx.Err(); err != nil {
		return routing.Path{}, err
	}
	if behave != nil {
		return behave(c, n)
	}
	return wrLoop(start, lengthM*1.1, float64(seed%5)*lengthM*1.1/1000*4), nil
}

func (e *wrEngine) Calls() []wrCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]wrCall(nil), e.calls...)
}

// wrLoop is a synthetic square of about perimeterM that starts and ends at
// start, with a single tent of ascentM over the whole loop. Points are 50 m
// apart. Nothing about it is a real road.
func wrLoop(start routing.LatLng, perimeterM, ascentM float64) routing.Path {
	side := perimeterM / 4
	dLat := side / 111320
	dLon := side / (111320 * math.Cos(start.Lat*math.Pi/180))
	corners := [][2]float64{
		{start.Lat, start.Lon}, {start.Lat + dLat, start.Lon},
		{start.Lat + dLat, start.Lon + dLon}, {start.Lat, start.Lon + dLon}, {start.Lat, start.Lon},
	}
	n := int(side / 50)
	if n < 1 {
		n = 1
	}
	var pts []gpx.Point
	for e := 0; e < 4; e++ {
		for i := 0; i < n; i++ {
			f := float64(i) / float64(n)
			x := float64(e*n+i) / float64(4*n)
			pts = append(pts, gpx.Point{
				Lat: corners[e][0] + f*(corners[e+1][0]-corners[e][0]),
				Lon: corners[e][1] + f*(corners[e+1][1]-corners[e][1]),
				Ele: 100 + ascentM*(1-math.Abs(2*x-1)), HasEle: true,
			})
		}
	}
	pts = append(pts, pts[0])
	return routing.Path{Points: pts}
}

// wrSpur is a loop in name only: out along one road and straight back.
func wrSpur(start routing.LatLng, lengthM float64) routing.Path {
	const step = 50.0
	n := int(lengthM / 2 / step)
	var pts []gpx.Point
	for i := 0; i <= n; i++ {
		pts = append(pts, gpx.Point{Lat: start.Lat + float64(i)*step/111320, Lon: start.Lon, Ele: 100, HasEle: true})
	}
	for i := n - 1; i >= 0; i-- {
		pts = append(pts, gpx.Point{Lat: start.Lat + float64(i)*step/111320, Lon: start.Lon + 0.00002, Ele: 100, HasEle: true})
	}
	return routing.Path{Points: pts}
}

// The rider's start point the tests use: invented, and distinctive so a
// scan of a log or response for it cannot match by chance. It is stored at
// three decimals, so the engine is expected to see wrStartStored.
const (
	wrStartPlace = "Ghent, Belgium"
	wrStartLat   = 47.376911
	wrStartLon   = 8.541722
)

var wrStartStored = routing.LatLng{Lat: 47.377, Lon: 8.542}

func (h *wrHarness) setStart(rider string) {
	h.t.Helper()
	if err := h.starts.Set(context.Background(), rider, wrStartPlace, wrStartLat, wrStartLon); err != nil {
		h.t.Fatal(err)
	}
}

// setProfile gives a rider an FTP and weight (a zero weight leaves it unset).
func (h *wrHarness) setProfile(rider string, ftp, weightKg float64) {
	h.t.Helper()
	ctx := context.Background()
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{Rider: rider, FTPWatts: ftp}); err != nil {
		h.t.Fatal(err)
	}
	if weightKg > 0 {
		if err := h.training.SetWeight(ctx, rider, weightKg); err != nil {
			h.t.Fatal(err)
		}
	}
}

// timedStep is one time step at an optional power target.
func timedStep(name string, intensity workout.Intensity, sec, watts float64) workout.WorkoutStep {
	s := workout.WorkoutStep{Name: name, Intensity: intensity, Duration: workout.DurationTime, Seconds: sec, Target: workout.TargetOpen}
	if watts > 0 {
		s.Target, s.TargetLow, s.TargetHigh = workout.TargetPower, watts, watts
	}
	return s
}

// seedWorkout creates a cycling workout on date and returns it.
func (h *wrHarness) seedWorkout(rider, name string, zone workout.Zone, date string, steps ...workout.WorkoutStep) workout.Workout {
	h.t.Helper()
	wk, err := h.training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: rider, Sport: model.SportCycling, Name: name, Date: date, Zone: zone, Steps: steps,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return wk
}

// enduranceWorkout is a two-hour endurance ride with no power target.
func (h *wrHarness) enduranceWorkout(rider, date string) workout.Workout {
	return h.seedWorkout(rider, "Endurance ride", workout.ZoneEndurance, date,
		timedStep("Ride", workout.IntensityActive, 7200, 0))
}
