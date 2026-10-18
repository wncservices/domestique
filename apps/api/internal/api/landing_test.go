package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/wncservices/domestique/apps/api/internal/auth"
)

// The apex serves the logged-out page; the app host serves the app. Getting
// this backwards would either hide the app behind a marketing page or show a
// signed-in rider the sales pitch.
func TestLandingIsServedOnlyOnTheApex(t *testing.T) {
	web := fstest.MapFS{
		"index.html":    {Data: []byte("<html>APP</html>")},
		"landing.html":  {Data: []byte("<html>LANDING</html>")},
		"assets/app.js": {Data: []byte("<html>ASSET</html>")},
	}

	for _, tc := range []struct {
		name, landingHost, host, path, want string
	}{
		{"apex gets the landing page", "domestique.dev", "domestique.dev", "/", "LANDING"},
		{"app host gets the app", "domestique.dev", "app.domestique.dev", "/", "APP"},
		{"host matching ignores case", "domestique.dev", "Domestique.Dev", "/", "LANDING"},
		{"a port on the host is ignored", "domestique.dev", "domestique.dev:8080", "/", "LANDING"},
		// Every path on the apex is the landing page, not just the root.
		// This used to serve the app, on the theory that only the front door
		// needed the logged-out page — which meant domestique.dev/settings
		// rendered the application UI on the one host that is deliberately
		// public and carries no forwardAuth. Nothing behind it was reachable,
		// since every API call arrives there without Remote-User and is
		// refused, but a logged-out page that is only logged-out at "/" is
		// not a logged-out page.
		{"a deep link on the apex is still the landing page",
			"domestique.dev", "domestique.dev", "/settings", "LANDING"},
		{"an unknown deep link too",
			"domestique.dev", "domestique.dev", "/routes/some-ride", "LANDING"},
		// Real files are still themselves: both pages load /assets/... from
		// the same build, so the apex must serve them rather than answering
		// every request with HTML.
		{"assets are served on the apex", "domestique.dev", "domestique.dev",
			"/assets/app.js", "ASSET"},
		{"assets are served on the app host", "domestique.dev", "app.domestique.dev",
			"/assets/app.js", "ASSET"},
		// A laptop configures no landing host and must still get the app.
		{"unset serves the app to everyone", "", "domestique.dev", "/", "APP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{WebFS: web, LandingHost: tc.landingHost}

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			srv.spaHandler().ServeHTTP(rec, req)

			if got := rec.Body.String(); got != "<html>"+tc.want+"</html>" {
				t.Errorf("served %q, want %s", got, tc.want)
			}
		})
	}
}

// The build now produces a real favicon.ico (browsers request it on their
// own, regardless of the <link rel="icon"> tag either page declares) — it
// must be served as itself, not swept up by the landing page or the SPA
// shell, on either host.
func TestFaviconIcoServesTheRealFavicon(t *testing.T) {
	web := fstest.MapFS{
		"index.html":   {Data: []byte("<html>APP</html>")},
		"landing.html": {Data: []byte("<html>LANDING</html>")},
		"favicon.ico":  {Data: []byte("ICOBYTES")},
	}

	for _, tc := range []struct{ name, host string }{
		{"the apex", "domestique.dev"},
		{"the app host", "app.domestique.dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{WebFS: web, LandingHost: "domestique.dev"}

			req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			srv.spaHandler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK || rec.Body.String() != "ICOBYTES" {
				t.Errorf("status %d body %q, want the real favicon.ico", rec.Code, rec.Body.String())
			}
		})
	}
}

// Real files are exempted from the anonymous mode: oidc redirect the same
// way /assets/... is — the one path that answered with a 302 rather than the
// file, which is just as unusable to a browser's favicon.ico probe.
func TestFaviconIcoSurvivesTheAnonymousOIDCRedirect(t *testing.T) {
	web := fstest.MapFS{
		"index.html":   {Data: []byte("<html>APP</html>")},
		"landing.html": {Data: []byte("<html>LANDING</html>")},
		"favicon.ico":  {Data: []byte("ICOBYTES")},
	}
	srv := &Server{WebFS: web, LandingHost: "domestique.dev", Auth: oidcAuthenticator(t)}

	req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	req.Host = "app.domestique.dev"
	rec := httptest.NewRecorder()
	srv.spaHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "ICOBYTES" {
		t.Errorf("status %d body %q, want the favicon, not the redirect", rec.Code, rec.Body.String())
	}
}

// An unbuilt or older frontend has no landing.html. Serving the app is a far
// better failure than 404-ing the front door.
func TestMissingLandingFallsBackToTheApp(t *testing.T) {
	web := fstest.MapFS{"index.html": {Data: []byte("<html>APP</html>")}}
	srv := &Server{WebFS: web, LandingHost: "domestique.dev"}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "domestique.dev"
	rec := httptest.NewRecorder()
	srv.spaHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "<html>APP</html>" {
		t.Errorf("status %d body %q, want the app", rec.Code, rec.Body.String())
	}
}

// http.FileServer publishes a directory index for any directory it is asked
// for, so /assets/ answered with a list of every file in the build — on the
// public host too. Nothing secret is in those names, but nobody asked this
// server to publish an index, and a directory is not a page.
func TestDirectoriesAreNotListed(t *testing.T) {
	web := fstest.MapFS{
		"index.html":    {Data: []byte("<html>APP</html>")},
		"landing.html":  {Data: []byte("<html>LANDING</html>")},
		"assets/app.js": {Data: []byte("<html>ASSET</html>")},
	}

	for _, tc := range []struct{ name, host, path, want string }{
		{"apex", "domestique.dev", "/assets/", "LANDING"},
		{"app host", "app.domestique.dev", "/assets/", "APP"},
		{"without the trailing slash", "app.domestique.dev", "/assets", "APP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{WebFS: web, LandingHost: "domestique.dev"}

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			srv.spaHandler().ServeHTTP(rec, req)

			body := rec.Body.String()
			if strings.Contains(body, "app.js") {
				t.Errorf("the directory was listed: %q", body)
			}
			if body != "<html>"+tc.want+"</html>" {
				t.Errorf("served %q, want %s", body, tc.want)
			}
		})
	}
}

func oidcAuthenticator(t *testing.T) *auth.Authenticator {
	t.Helper()
	a, err := auth.New(auth.Config{
		Mode: auth.ModeOIDC,
		OIDC: auth.OIDCConfig{
			Issuer: "https://idp.example.test/", ClientID: "x",
			RedirectURL: "https://app.example.test/sso/callback",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// mode: oidc is the one mode where the app itself decides who gets in — an
// anonymous visitor to the app host is sent to the front door instead of
// the app shell, same as mode: proxy always looked from outside (Traefik
// never let the request arrive at all).
func TestAnonymousOIDCVisitorsAreSentToTheLandingPage(t *testing.T) {
	web := fstest.MapFS{
		"index.html":    {Data: []byte("<html>APP</html>")},
		"landing.html":  {Data: []byte("<html>LANDING</html>")},
		"assets/app.js": {Data: []byte("<html>ASSET</html>")},
	}
	srv := &Server{WebFS: web, LandingHost: "domestique.dev", Auth: oidcAuthenticator(t)}

	// A deep link carries where the visitor was going, so the landing page's
	// Sign in brings them back there (a share link above all) rather than to
	// the app's front page.
	for _, tc := range []struct{ name, path, want string }{
		{"the app shell itself", "/", "https://domestique.dev/"},
		{"a deep link", "/settings", "https://domestique.dev/?return_to=%2Fsettings"},
		{"a share link", "/shared/abc123", "https://domestique.dev/?return_to=%2Fshared%2Fabc123"},
		{"a deep link with a query", "/fitness?tab=season", "https://domestique.dev/?return_to=%2Ffitness%3Ftab%3Dseason"},
		{"an unknown deep link", "/routes/some-ride", "https://domestique.dev/?return_to=%2Froutes%2Fsome-ride"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = "app.domestique.dev"
			rec := httptest.NewRecorder()
			srv.spaHandler().ServeHTTP(rec, req)

			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302", rec.Code)
			}
			if loc := rec.Header().Get("Location"); loc != tc.want {
				t.Errorf("Location = %q, want %q", loc, tc.want)
			}
		})
	}
}

// The landing and privacy pages are the apex's; on the app host they are sent
// there, signed in or not, so the app host never renders the front door.
func TestTheAppHostSendsTheApexPagesToTheApex(t *testing.T) {
	web := fstest.MapFS{
		"index.html":   {Data: []byte("<html>APP</html>")},
		"landing.html": {Data: []byte("<html>LANDING</html>")},
		"privacy.html": {Data: []byte("<html>PRIVACY</html>")},
	}
	srv := &Server{WebFS: web, LandingHost: "domestique.dev", Auth: oidcAuthenticator(t)}
	for path, want := range map[string]string{
		"/landing.html": "https://domestique.dev/",
		"/privacy.html": "https://domestique.dev/privacy.html",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "app.domestique.dev"
		rec := httptest.NewRecorder()
		srv.spaHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d %q, want 302 %q", path, rec.Code, rec.Header().Get("Location"), want)
		}
		// On the apex itself the page is served as before.
		req = httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "domestique.dev"
		rec = httptest.NewRecorder()
		srv.spaHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s on the apex: %d, want 200", path, rec.Code)
		}
	}
}

// The preview host is neither LandingHost nor the production app host, so
// without isPreviewHost every anonymous visit there — which is all of them,
// since its session cookie is scoped to a different host — was bounced
// straight to LandingHost before a login could ever start. This is the
// live bug the redirect exemption exists to fix.
func TestAnonymousOIDCVisitorsOnThePreviewHostStillGetTheApp(t *testing.T) {
	web := fstest.MapFS{
		"index.html":   {Data: []byte("<html>APP</html>")},
		"landing.html": {Data: []byte("<html>LANDING</html>")},
	}
	a, err := auth.New(auth.Config{
		Mode: auth.ModeOIDC,
		OIDC: auth.OIDCConfig{
			Issuer: "https://idp.example.test/", ClientID: "x",
			RedirectURL:        "https://app.example.test/sso/callback",
			PreviewRedirectURL: "https://preview.example.test/sso/callback",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{WebFS: web, LandingHost: "domestique.dev", Auth: a}

	for _, tc := range []struct{ name, path string }{
		{"the app shell itself", "/"},
		{"a deep link", "/settings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = "preview.example.test"
			rec := httptest.NewRecorder()
			srv.spaHandler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK || rec.Body.String() != "<html>APP</html>" {
				t.Errorf("status %d body %q, want the app, not a redirect to LandingHost", rec.Code, rec.Body.String())
			}
		})
	}
}

// A host that merely contains the preview host as a substring — the same
// CodeQL bug class already fixed once in apps/e2e/tests/login.spec.ts — must
// not match. isPreviewHost uses an exact host comparison, never .Contains.
func TestPreviewHostMatchIsExactNotSubstring(t *testing.T) {
	web := fstest.MapFS{
		"index.html":   {Data: []byte("<html>APP</html>")},
		"landing.html": {Data: []byte("<html>LANDING</html>")},
	}
	a, err := auth.New(auth.Config{
		Mode: auth.ModeOIDC,
		OIDC: auth.OIDCConfig{
			Issuer: "https://idp.example.test/", ClientID: "x",
			RedirectURL:        "https://app.example.test/sso/callback",
			PreviewRedirectURL: "https://preview.example.test/sso/callback",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{WebFS: web, LandingHost: "domestique.dev", Auth: a}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "notpreview.example.test"
	rec := httptest.NewRecorder()
	srv.spaHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (should fall through to the LandingHost redirect, not be treated as the preview host)", rec.Code)
	}
}

// The redirect must not swallow the app's own assets — the landing page it
// redirects to needs them loadable too.
func TestAnonymousOIDCVisitorsStillGetTheirAssets(t *testing.T) {
	web := fstest.MapFS{
		"index.html":    {Data: []byte("<html>APP</html>")},
		"landing.html":  {Data: []byte("<html>LANDING</html>")},
		"assets/app.js": {Data: []byte("<html>ASSET</html>")},
	}
	srv := &Server{WebFS: web, LandingHost: "domestique.dev", Auth: oidcAuthenticator(t)}

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	req.Host = "app.domestique.dev"
	rec := httptest.NewRecorder()
	srv.spaHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "<html>ASSET</html>" {
		t.Errorf("status %d body %q, want the asset", rec.Code, rec.Body.String())
	}
}

// A signed-in rider still reaches the app — the redirect is only for nobody
// being signed in at all, not for mode: oidc itself.
func TestAuthenticatedOIDCVisitorsStillGetTheApp(t *testing.T) {
	web := fstest.MapFS{
		"index.html":   {Data: []byte("<html>APP</html>")},
		"landing.html": {Data: []byte("<html>LANDING</html>")},
	}
	srv := &Server{WebFS: web, LandingHost: "domestique.dev", Auth: oidcAuthenticator(t)}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "app.domestique.dev"
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{User: "wilant"}))
	rec := httptest.NewRecorder()
	srv.spaHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "<html>APP</html>" {
		t.Errorf("status %d body %q, want the app", rec.Code, rec.Body.String())
	}
}

// The rest of this file never configures Auth (authenticator() falls back to
// mode: none) and still expects the app host to serve the app — proof the
// redirect is gated on mode: oidc specifically, not on being anonymous alone.
func TestModeNoneNeverRedirectsTheAppHost(t *testing.T) {
	web := fstest.MapFS{
		"index.html":   {Data: []byte("<html>APP</html>")},
		"landing.html": {Data: []byte("<html>LANDING</html>")},
	}
	srv := &Server{WebFS: web, LandingHost: "domestique.dev"}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "app.domestique.dev"
	rec := httptest.NewRecorder()
	srv.spaHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "<html>APP</html>" {
		t.Errorf("status %d body %q, want the app", rec.Code, rec.Body.String())
	}
}
