package wahoo

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type droppedTransport struct{}

func (droppedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection reset by peer")
}

// net/http's error text for a failed request repeats the full URL, query
// string included, and callers log these errors. Every call this client makes
// has to drop that URL, whatever token rode along in it, and keep the cause.
func TestTransportErrorsDoNotCarryTheRequestURL(t *testing.T) {
	const secret = "SECRETTOKEN123"

	c := New(Config{ClientID: "id", ClientSecret: "shh", RedirectURL: "https://app.example.test/cb"})
	c.HTTP.Transport = droppedTransport{}
	// The token sits in the query string so every URL this client builds, which
	// appends a path to APIBase, carries it.
	c.APIBase = "https://api.wahoo.test?access_token=" + secret + "&p="
	signed := "https://" + wahooCDNHost + "/f.fit?X-Signature=" + secret

	calls := map[string]func(context.Context) error{
		"Exchange":      func(ctx context.Context) error { _, err := c.Exchange(ctx, "code"); return err },
		"Refresh":       func(ctx context.Context) error { _, err := c.Refresh(ctx, "rt"); return err },
		"Me":            func(ctx context.Context) error { _, err := c.Me(ctx, "at"); return err },
		"CreateRoute":   func(ctx context.Context) error { _, err := c.CreateRoute(ctx, "at", aRouteRequest()); return err },
		"UpdateRoute":   func(ctx context.Context) error { _, err := c.UpdateRoute(ctx, "at", "5", aRouteRequest()); return err },
		"DeleteRoute":   func(ctx context.Context) error { return c.DeleteRoute(ctx, "at", "5") },
		"ListRoutes":    func(ctx context.Context) error { _, err := c.ListRoutes(ctx, "at"); return err },
		"ListWorkouts":  func(ctx context.Context) error { _, err := c.ListWorkouts(ctx, "at", 1, 30); return err },
		"DownloadRoute": func(ctx context.Context) error { _, err := c.DownloadRoute(ctx, "at", signed); return err },
		"WorkoutFIT":    func(ctx context.Context) error { _, err := c.WorkoutFIT(ctx, signed); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call(t.Context())
			if err == nil {
				t.Fatal("want an error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error %q carries the token from the URL", err)
			}
			if !strings.Contains(err.Error(), "connection reset") {
				t.Errorf("error %q lost its cause", err)
			}
		})
	}
}
