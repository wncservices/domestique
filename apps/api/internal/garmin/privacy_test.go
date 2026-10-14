package garmin

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

type dropped struct{}

func (dropped) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection reset by peer")
}

// The single-use SSO ticket rides in the query string of the exchange URL, and
// net/http's own error text repeats the URL. Callers log the error (audit N2/N3).
func TestTransportErrorsDoNotCarryTheTicketURL(t *testing.T) {
	c := New()
	c.HTTP.Transport = dropped{}

	_, _, err := c.do(t.Context(), http.MethodGet, c.SSOBase+"/embed?ticket=ST-1234567-secretTicket&service=x", nil, "")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "secretTicket") || strings.Contains(err.Error(), "ticket=") {
		t.Errorf("error %q carries the ticket URL", err)
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("error %q lost its cause", err)
	}
}
