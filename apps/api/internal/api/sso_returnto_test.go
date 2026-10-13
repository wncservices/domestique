package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// return_to is the one user-supplied value that ends up in a Location header.
// Browsers treat a backslash as a slash in a URL, so "/\evil.example" is the
// protocol-relative "//evil.example" once it leaves the server.
func TestSafeReturnTo(t *testing.T) {
	ok := []string{
		"",
		"/",
		"/settings",
		"/routes/hill-loop?tab=map",
		"/training/plan#week-3",
		"/shared/abc_DEF-123",
		"/a//b", // a double slash later in the path is a path, not an authority
	}
	bad := []string{
		"//evil.example",
		"//evil.example/path",
		"/\\evil.example",
		"/\\/evil.example",
		"/\\\\evil.example",
		"\\evil.example",
		"\\\\evil.example",
		"/ok\\evil",
		"/\tevil.example",
		"/\nevil.example",
		"/\revil.example",
		"/\x00evil",
		"/\x7fevil",
		"https://evil.example",
		"http://evil.example/steal",
		"javascript:alert(1)",
		"evil.example",
		"settings",
		"/ evil.example",
		" /settings",
		"/ evil",
	}
	for _, in := range ok {
		if got, err := safeReturnTo(in); err != nil || got != in {
			t.Errorf("safeReturnTo(%q) = %q, %v; want it accepted unchanged", in, got, err)
		}
	}
	for _, in := range bad {
		if got, err := safeReturnTo(in); err == nil {
			t.Errorf("safeReturnTo(%q) = %q; want it refused", in, got)
		}
	}
}

// What an accepted value does in a real redirect: it stays on this host.
func TestAnAcceptedReturnToRedirectsToThisHost(t *testing.T) {
	for _, in := range []string{"/settings", "/a//b"} {
		path, err := safeReturnTo(in)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		http.Redirect(rec, httptest.NewRequest(http.MethodGet, "/sso/callback", nil), path, http.StatusFound)
		loc := rec.Header().Get("Location")
		if len(loc) > 1 && (loc[1] == '/' || loc[1] == '\\') {
			t.Errorf("Location %q for %q can leave the site", loc, in)
		}
	}
}
