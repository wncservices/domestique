// Package komoot pulls planned routes out of a Komoot account.
//
// Komoot has no public API. This speaks the same undocumented endpoints the
// website and app use, which means:
//
//   - It can break without warning, and did change hands in 2025 (Bending
//     Spoons acquired Komoot), so treat breakage as expected rather than
//     surprising. Failures here must never take the rest of the app down.
//   - It needs the account's real email and password to obtain a token. Those
//     come from the environment (KOMOOT_EMAIL / KOMOOT_PASSWORD, sourced from
//     Vault in a cluster) and are never written to disk or logged.
//
// The flow mirrors what the clients do:
//
//	GET v006/account/email/{email}/   basic auth email:password  -> user id + token
//	GET v007/users/{id}/tours/        basic auth id:token        -> paginated tours
//	GET v007/tours/{id}?...           basic auth id:token        -> coordinates
package komoot

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/telemetry"
)

const (
	baseV006 = "https://api.komoot.de/v006"
	baseV007 = "https://api.komoot.de/v007"

	// TypePlanned is a route someone plotted. TypeRecorded is a ride they did.
	// Only planned tours are worth syncing to a head unit.
	TypePlanned  = "tour_planned"
	TypeRecorded = "tour_recorded"

	defaultTimeout = 30 * time.Second
	// maxPages bounds pagination so a misbehaving API cannot loop forever.
	maxPages = 50
)

// allowedHost reports whether a URL may be requested.
//
// This matters more than it looks. Pagination follows `_links.next.href`
// straight out of Komoot's response body, and every request carries the
// account's credentials in an Authorization header. Following that link
// blindly would hand those credentials to whatever host the response names —
// an SSRF with credential disclosure, and the response body is exactly the
// part an attacker on the wire controls.
//
// So every request is checked against the hosts we were configured to talk
// to, and anything else is refused before it is sent.
func (c *Client) allowedHost(raw string) error {
	target, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("komoot: unusable URL %q: %w", raw, err)
	}

	for _, base := range []string{c.BaseV6, c.BaseV7} {
		known, err := url.Parse(base)
		if err != nil {
			continue
		}
		if target.Scheme == known.Scheme && strings.EqualFold(target.Host, known.Host) {
			return nil
		}
	}

	return fmt.Errorf("komoot: refusing to call %s://%s — not a configured Komoot host",
		target.Scheme, target.Host)
}

// newRequest builds a request, refusing any URL that is not one of the hosts
// this client was configured with. Validation happens here, before the URL is
// used to construct anything, so there is no path that reaches the network
// without passing it.
func (c *Client) newRequest(ctx context.Context, method, rawURL string) (*http.Request, error) {
	if err := c.allowedHost(rawURL); err != nil {
		return nil, err
	}
	return http.NewRequestWithContext(ctx, method, rawURL, nil)
}

// Client talks to Komoot as one account.
type Client struct {
	HTTP   *http.Client
	BaseV6 string
	BaseV7 string

	userID string
	token  string
	name   string
}

// New returns a client. Call Login before anything else.
func New() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout:   defaultTimeout,
			Transport: telemetry.OutboundTransport(http.DefaultTransport),
			// Do not follow redirects. A redirect would carry the
			// Authorization header to wherever it points.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		BaseV6: baseV006,
		BaseV7: baseV007,
	}
}

// Tour is a route in someone's Komoot account.
type Tour struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Sport     string    `json:"sport"`
	DistanceM float64   `json:"distance"`
	AscentM   float64   `json:"ascent"`
	ChangedAt time.Time `json:"changedAt"`
}

// Planned reports whether this is a plotted route rather than a recorded ride.
func (t Tour) Planned() bool { return t.Type == TypePlanned }

// Login exchanges an email and password for the account's API token.
func (c *Client) Login(ctx context.Context, email, password string) error {
	if email == "" || password == "" {
		return fmt.Errorf("komoot: email and password are both required")
	}

	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("%s/account/email/%s/", c.BaseV6, url.PathEscape(email)))
	if err != nil {
		return err
	}
	req.SetBasicAuth(email, password)

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		User     struct {
			DisplayName string `json:"displayname"`
		} `json:"user"`
	}
	if err := c.do(req, &body); err != nil {
		return fmt.Errorf("komoot login: %w", err)
	}
	if body.Username == "" || body.Password == "" {
		return fmt.Errorf("komoot login: response contained no credentials")
	}

	c.userID, c.token, c.name = body.Username, body.Password, body.User.DisplayName
	return nil
}

// LoginWithToken reuses a previously obtained user id and token.
func (c *Client) LoginWithToken(userID, token string) {
	c.userID, c.token = userID, token
}

// Session returns what Login obtained, so a caller can store it and resume
// later with LoginWithToken. Komoot's login hands back a session token rather
// than expecting the password again — which is why nothing has to keep the
// password.
func (c *Client) Session() (userID, token string) { return c.userID, c.token }

// DisplayName is the account's name, when login provided one.
func (c *Client) DisplayName() string { return c.name }

// Tours lists the account's tours, newest first. Recorded rides are filtered
// out unless includeRecorded is set.
func (c *Client) Tours(ctx context.Context, includeRecorded bool) ([]Tour, error) {
	if c.userID == "" || c.token == "" {
		return nil, fmt.Errorf("komoot: not logged in")
	}

	next := fmt.Sprintf("%s/users/%s/tours/?limit=50", c.BaseV7, url.PathEscape(c.userID))
	var tours []Tour

	for page := 0; next != "" && page < maxPages; page++ {
		req, err := c.newRequest(ctx, http.MethodGet, next)
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(c.userID, c.token)

		var body struct {
			Embedded struct {
				Tours []struct {
					ID        json.Number `json:"id"`
					Name      string      `json:"name"`
					Type      string      `json:"type"`
					Sport     string      `json:"sport"`
					Distance  float64     `json:"distance"`
					Elevation float64     `json:"elevation_up"`
					ChangedAt string      `json:"changed_at"`
				} `json:"tours"`
			} `json:"_embedded"`
			Links struct {
				Next struct {
					Href string `json:"href"`
				} `json:"next"`
			} `json:"_links"`
		}
		if err := c.do(req, &body); err != nil {
			return nil, fmt.Errorf("komoot tours: %w", err)
		}

		for _, t := range body.Embedded.Tours {
			if !includeRecorded && t.Type != TypePlanned {
				continue
			}
			changed, _ := time.Parse(time.RFC3339, t.ChangedAt)
			tours = append(tours, Tour{
				ID:        t.ID.String(),
				Name:      t.Name,
				Type:      t.Type,
				Sport:     t.Sport,
				DistanceM: t.Distance,
				AscentM:   t.Elevation,
				ChangedAt: changed,
			})
		}

		next = body.Links.Next.Href
	}

	return tours, nil
}

// GPX fetches one tour and renders it as a GPX 1.1 track.
//
// Komoot returns coordinates as JSON, not GPX, so the file is built here. That
// is fine: the rest of Domestique only ever wants lat/lon/ele.
func (c *Client) GPX(ctx context.Context, tourID string) ([]byte, error) {
	if c.userID == "" || c.token == "" {
		return nil, fmt.Errorf("komoot: not logged in")
	}

	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf(
		"%s/tours/%s?_embedded=coordinates&format=coordinate_array",
		c.BaseV7, url.PathEscape(tourID)))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.userID, c.token)

	var body struct {
		Name     string `json:"name"`
		Embedded struct {
			Coordinates struct {
				Items []coordinate `json:"items"`
			} `json:"coordinates"`
		} `json:"_embedded"`
	}
	if err := c.do(req, &body); err != nil {
		return nil, fmt.Errorf("komoot tour %s: %w", tourID, err)
	}

	points := body.Embedded.Coordinates.Items
	if len(points) < 2 {
		return nil, fmt.Errorf("komoot tour %s: got %d coordinates, need at least 2",
			tourID, len(points))
	}

	return renderGPX(body.Name, points)
}

// DeleteTour removes a tour from the account. Callers are expected to only
// ever pass the id of a planned tour they themselves selected for deletion —
// this client has no opinion about what a tour is for beyond calling the
// endpoint Komoot gave it an id for.
//
// There is no public documentation for this. It is reverse engineered from
// community tooling (github.com/pnposch/komoot-cleaner, which deletes
// recorded tours the same way) against the same v007 host and the same
// userID:token basic-auth session Tours and GPX already use — not a fresh
// guess, but still unverified by Komoot itself, on an API the package doc
// already warns can break without notice.
func (c *Client) DeleteTour(ctx context.Context, tourID string) error {
	if c.userID == "" || c.token == "" {
		return fmt.Errorf("komoot: not logged in")
	}

	req, err := c.newRequest(ctx, http.MethodDelete, fmt.Sprintf("%s/tours/%s", c.BaseV7, url.PathEscape(tourID)))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.userID, c.token)

	// into is nil: a successful delete answers 200 or 204 with no JSON body
	// worth decoding, unlike every other call this client makes.
	if err := c.do(req, nil); err != nil {
		return fmt.Errorf("komoot delete tour %s: %w", tourID, err)
	}
	return nil
}

// ErrSessionRefused is what a call wraps when Komoot answers 401 or 403: the
// stored sign-in no longer works, and the rider has to connect again.
var ErrSessionRefused = errors.New("komoot: rejected")

func (c *Client) do(req *http.Request, into any) error {
	// Backstop. newRequest already refused anything off-host; this catches a
	// future caller that builds a request by hand.
	if err := c.allowedHost(req.URL.String()); err != nil {
		return err
	}

	// hal+json first, and it is not optional: the v007 endpoints are HAL —
	// that is where the `_links.next.href` pagination comes from — and they
	// answer 406 Not Acceptable to a bare application/json. Verified against
	// the live API: json alone gives 406, hal+json reaches authentication.
	// application/json stays as a fallback for the v006 endpoints.
	req.Header.Set("Accept", "application/hal+json, application/json")
	// Komoot's edge rejects requests without a browser-ish agent.
	req.Header.Set("User-Agent", "domestique/1.0 (+https://github.com/wncservices/domestique)")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	// Cap the read: this is an untrusted third party.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w (%s) — check the Komoot credentials", ErrSessionRefused, resp.Status)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("not found (%s)", resp.Status)
	case resp.StatusCode >= 300:
		return fmt.Errorf("unexpected status %s: %s", resp.Status, snippet(raw))
	}

	if into == nil {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		// An HTML body here usually means the undocumented API moved.
		return fmt.Errorf("could not decode response (%w): %s", err, snippet(raw))
	}
	return nil
}

// gpx mirrors the subset of GPX 1.1 we emit.
type gpxDoc struct {
	XMLName xml.Name `xml:"gpx"`
	Version string   `xml:"version,attr"`
	Creator string   `xml:"creator,attr"`
	NS      string   `xml:"xmlns,attr"`
	Trk     struct {
		Name string `xml:"name"`
		Seg  struct {
			Points []gpxPoint `xml:"trkpt"`
		} `xml:"trkseg"`
	} `xml:"trk"`
}

type gpxPoint struct {
	Lat float64  `xml:"lat,attr"`
	Lon float64  `xml:"lon,attr"`
	Ele *float64 `xml:"ele,omitempty"`
}

// coordinate is one track point.
//
// Komoot sends these two ways and `format=coordinate_array` — an undocumented
// query parameter — decides which. It is not always honoured, and a client
// that understands only one shape fails every tour with a decode error while
// the account and the credentials are perfectly fine. So accept both:
//
//	[50.79, 2.81, 60.5, 0]              a coordinate array
//	{"lat":50.79,"lng":2.81,"alt":60.5} an object
type coordinate struct {
	Lat float64
	Lng float64
	Alt *float64
}

func (c *coordinate) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return errors.New("komoot: empty coordinate")
	}

	if trimmed[0] == '[' {
		var tuple []float64
		if err := json.Unmarshal(trimmed, &tuple); err != nil {
			return err
		}
		if len(tuple) < 2 {
			return fmt.Errorf("komoot: coordinate has %d values, need at least 2", len(tuple))
		}
		c.Lat, c.Lng = tuple[0], tuple[1]
		if len(tuple) >= 3 {
			alt := tuple[2]
			c.Alt = &alt
		}
		return nil
	}

	var object struct {
		Lat float64  `json:"lat"`
		Lng float64  `json:"lng"`
		Alt *float64 `json:"alt"`
	}
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return err
	}
	c.Lat, c.Lng, c.Alt = object.Lat, object.Lng, object.Alt
	return nil
}

func renderGPX(name string, coords []coordinate) ([]byte, error) {
	var doc gpxDoc
	doc.Version = "1.1"
	doc.Creator = "Domestique"
	doc.NS = "http://www.topografix.com/GPX/1/1"
	doc.Trk.Name = name

	for _, c := range coords {
		// 0,0 is in the Atlantic; it is what a missing field decodes to, not
		// a place anybody rode.
		if c.Lat == 0 && c.Lng == 0 {
			continue
		}
		point := gpxPoint{Lat: c.Lat, Lon: c.Lng}
		if c.Alt != nil {
			ele := *c.Alt
			point.Ele = &ele
		}
		doc.Trk.Seg.Points = append(doc.Trk.Seg.Points, point)
	}

	if len(doc.Trk.Seg.Points) < 2 {
		return nil, fmt.Errorf("komoot: tour had no usable coordinates")
	}

	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), out...), nil
}

func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
