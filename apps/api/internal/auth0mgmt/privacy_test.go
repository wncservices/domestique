package auth0mgmt

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// failOn fails any request whose path contains sub, the way a dropped
// connection would, and passes the rest through to the fake tenant.
type failOn struct {
	sub  string
	next http.RoundTripper
}

func (f failOn) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, f.sub) {
		return nil, errors.New("connection reset by peer")
	}
	return f.next.RoundTrip(r)
}

// The lookup puts the address in the query string, and net/http's own error
// text for a failed request repeats the whole URL. The admin handlers log the
// error, so the address must not be in it (audit N10).
func TestFindByEmailErrorsDoNotEchoTheAddress(t *testing.T) {
	f := newFakeTenant(t)
	c := newTestClient(t, f)
	c.http.Transport = failOn{sub: "users-by-email", next: http.DefaultTransport}

	_, err := c.FindByEmail(t.Context(), "invitee@example.com")
	if err == nil {
		t.Fatal("want an error from the dropped connection")
	}
	if strings.Contains(err.Error(), "invitee") || strings.Contains(err.Error(), "example.com") {
		t.Errorf("error %q carries the address", err)
	}
}
