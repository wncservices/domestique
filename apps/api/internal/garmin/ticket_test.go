package garmin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Where the ticket sits on the success page is Garmin's business and it has
// moved at least once. Every shape seen so far, plus the ones a small change
// would produce.
func TestTicketIsFoundWhereverItSits(t *testing.T) {
	const want = "ST-01234-abcDEF-cas"

	for _, tc := range []struct{ name, body string }{
		{
			// The original: an embed URL in double quotes.
			"embed url in double quotes",
			`<a href="https://sso.garmin.com/sso/embed?ticket=` + want + `">go</a>`,
		},
		{
			// What broke it: the same URL in a JavaScript string. Garmin
			// returned "Success" and this package called it a bad password.
			"javascript string in single quotes",
			`<script>var response_url = 'https://sso.garmin.com/sso/embed?ticket=` + want + `';</script>`,
		},
		{
			"a different path entirely",
			`<script>window.location = "https://connect.garmin.com/modern?ticket=` + want + `";</script>`,
		},
		{
			"not the first query parameter",
			`<a href="https://sso.garmin.com/sso/embed?service=x&ticket=` + want + `">go</a>`,
		},
		{
			"followed by another parameter",
			`<a href="https://sso.garmin.com/sso/embed?ticket=` + want + `&service=x">go</a>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := ticketPattern.FindStringSubmatch(tc.body)
			if m == nil {
				t.Fatalf("no ticket found in %s", tc.body)
			}
			if m[1] != want {
				t.Errorf("ticket = %q, want %q", m[1], want)
			}
		})
	}
}

// The sign-in form carries no ticket, and must not appear to.
func TestNoTicketOnTheSignInForm(t *testing.T) {
	body := `<form><input type="hidden" name="_csrf" value="abc"/>
	<input name="username"/><input name="password" type="password"/></form>`
	if m := ticketPattern.FindStringSubmatch(body); m != nil {
		t.Errorf("found a ticket on the sign-in form: %q", m[1])
	}
}

// The service a ticket is issued for and the login-url it is redeemed with
// have to be the same string. When they were not, the sign-in succeeded and
// the exchange returned 401 — the ticket was real and simply not valid for
// what this client claimed to be.
//
// Asserted against the request the client actually builds, not against a
// constant, so splitting them again fails here rather than in production.
func TestTicketIsRedeemedForTheServiceItWasIssuedFor(t *testing.T) {
	var loginURL string
	sso := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/oauth-service/oauth/preauthorized"):
			loginURL = r.URL.Query().Get("login-url")
			w.Write([]byte("oauth_token=t&oauth_token_secret=s"))
		case r.Method == http.MethodPost:
			w.Write([]byte(`<title>Success</title><script>var response_url =
				'` + r.Host + `/sso/embed?ticket=ST-1-abc-cas';</script>`))
		default:
			w.Write([]byte(`<input name="_csrf" value="csrf-1"/>`))
		}
	}))
	defer sso.Close()

	c := New()
	c.SSOBase, c.APIBase, c.WebBase = sso.URL+"/sso", sso.URL, sso.URL
	c.consumer = consumerKey{Key: "k", Secret: "s"}

	// The sign-in request records what service the ticket is issued for.
	service := c.signinParams().Get("service")

	_, _ = c.Login(t.Context(), "rider@example.com", "pw")

	if loginURL == "" {
		t.Fatal("the ticket was never presented for exchange")
	}
	if loginURL != service {
		t.Errorf("ticket issued for service %q but redeemed with login-url %q", service, loginURL)
	}
}
