package komoot

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

// net/http's error text for a failed request repeats the full URL: the query
// string, and for Login the rider's email in the path. Callers log these.
func TestTransportErrorsDoNotCarryTheRequestURL(t *testing.T) {
	const secret = "SECRETTOKEN123"
	const email = "rider-secret@example.test"

	c := New()
	c.HTTP.Transport = droppedTransport{}
	c.BaseV6 = "https://api.komoot.test/v006?token=" + secret + "&p="
	c.BaseV7 = "https://api.komoot.test/v007?token=" + secret + "&p="
	c.LoginWithToken("u1", "tok")

	calls := map[string]func(context.Context) error{
		"Login":      func(ctx context.Context) error { return c.Login(ctx, email, "pw") },
		"Tours":      func(ctx context.Context) error { _, err := c.Tours(ctx, true); return err },
		"GPX":        func(ctx context.Context) error { _, err := c.GPX(ctx, "42"); return err },
		"DeleteTour": func(ctx context.Context) error { return c.DeleteTour(ctx, "42") },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call(t.Context())
			if err == nil {
				t.Fatal("want an error")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "rider-secret") {
				t.Errorf("error %q carries the request URL", err)
			}
			if !strings.Contains(err.Error(), "connection reset") {
				t.Errorf("error %q lost its cause", err)
			}
		})
	}
}

// The next-page link comes out of a response body; a malformed one must not
// be echoed back with its query string.
func TestAnUnusableURLErrorDoesNotEchoTheQueryString(t *testing.T) {
	c := New()
	err := c.allowedHost("https://api.komoot.de/v007/%zz?cursor=SECRETTOKEN123")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "SECRETTOKEN123") {
		t.Errorf("error %q echoes the query string", err)
	}
}
