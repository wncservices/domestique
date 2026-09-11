package wahoo

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	c := New(Config{ClientID: "client-id", ClientSecret: "client-secret", RedirectURL: "https://app.example.test/wahoo/callback"})
	c.APIBase = server.URL
	return c
}

func TestAuthCodeURL(t *testing.T) {
	c := New(Config{ClientID: "abc", ClientSecret: "shh", RedirectURL: "https://app.example.test/wahoo/callback"})
	c.APIBase = "https://api.wahooligan.com"

	got, err := url.Parse(c.AuthCodeURL("the-state"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Scheme+"://"+got.Host+got.Path != "https://api.wahooligan.com/oauth/authorize" {
		t.Fatalf("unexpected endpoint: %s", got)
	}
	q := got.Query()
	if q.Get("client_id") != "abc" {
		t.Fatalf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != "https://app.example.test/wahoo/callback" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	if q.Get("response_type") != "code" {
		t.Fatalf("response_type = %q", q.Get("response_type"))
	}
	if q.Get("state") != "the-state" {
		t.Fatalf("state = %q", q.Get("state"))
	}
	if q.Get("scope") != "user_read routes_read routes_write workouts_read" {
		t.Fatalf("scope = %q", q.Get("scope"))
	}
	if q.Get("code_challenge") != "" || q.Get("code_challenge_method") != "" {
		t.Fatalf("PKCE params present on a confidential-client request: %s", got.RawQuery)
	}
}

func TestExchange(t *testing.T) {
	var gotForm url.Values
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		gotForm = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "refresh_token": "rt", "expires_in": 3600, "scope": "user_read",
		})
	})

	session, err := c.Exchange(t.Context(), "the-code")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if session.AccessToken != "at" || session.RefreshToken != "rt" {
		t.Fatalf("session = %+v", session)
	}
	if session.Expired() {
		t.Fatalf("a freshly issued token reports expired")
	}
	if gotForm.Get("grant_type") != "authorization_code" {
		t.Fatalf("grant_type = %q", gotForm.Get("grant_type"))
	}
	if gotForm.Get("code") != "the-code" {
		t.Fatalf("code = %q", gotForm.Get("code"))
	}
	if gotForm.Get("client_secret") != "client-secret" {
		t.Fatalf("client_secret = %q", gotForm.Get("client_secret"))
	}
	if gotForm.Get("code_verifier") != "" {
		t.Fatalf("code_verifier present on a confidential-client exchange")
	}
}

func TestRefreshKeepsExistingTokenWhenNoneIsReturned(t *testing.T) {
	var gotGrantType string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotGrantType = r.PostForm.Get("grant_type")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-at", "expires_in": 3600})
	})

	session, err := c.Refresh(t.Context(), "old-rt")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if gotGrantType != "refresh_token" {
		t.Fatalf("grant_type = %q", gotGrantType)
	}
	if session.RefreshToken != "old-rt" {
		t.Fatalf("refresh token = %q, want the original kept", session.RefreshToken)
	}
}

func TestExchangeSurfacesAnUpstreamError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "invalid_grant", "error_description": "code expired",
		})
	})

	_, err := c.Exchange(t.Context(), "stale-code")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("error = %v, want it to name invalid_grant", err)
	}
}

func TestExchangeRejectsAnUnreadableResponse(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not json</html>"))
	})

	if _, err := c.Exchange(t.Context(), "code"); err == nil {
		t.Fatal("expected an error for an unreadable token response")
	}
}

func TestMe(t *testing.T) {
	var gotAuth string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/user" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(Profile{ID: 42, Email: "rider@example.test", First: "Ada", Last: "Lovelace"})
	})

	profile, err := c.Me(t.Context(), "at")
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	if gotAuth != "Bearer at" {
		t.Fatalf("Authorization header = %q", gotAuth)
	}
	if profile.DisplayName() != "Ada Lovelace" {
		t.Fatalf("display name = %q", profile.DisplayName())
	}
}

func TestProfileDisplayNameFallsBackToEmail(t *testing.T) {
	p := Profile{Email: "rider@example.test"}
	if p.DisplayName() != "rider@example.test" {
		t.Fatalf("display name = %q, want the email", p.DisplayName())
	}
}

func TestMeSurfacesAnUpstreamFailure(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"missing user_read scope"}`))
	})

	if _, err := c.Me(t.Context(), "at"); err == nil {
		t.Fatal("expected an error")
	}
}

func aRouteRequest() RouteRequest {
	updated, _ := time.Parse(time.RFC3339, "2026-01-02T03:04:05Z")
	return RouteRequest{
		ExternalID: "kluisbergen", Name: "Kluisbergen", Description: "A short climb",
		UpdatedAt: updated, DistanceM: 12345, AscentM: 250, StartLat: 50.85, StartLng: 4.35,
		Filename: "kluisbergen.fit", FIT: []byte("FIT-BYTES"),
	}
}

func TestCreateRoutePostsTheDocumentedFormFields(t *testing.T) {
	var gotMethod, gotPath string
	var gotForm url.Values
	var gotAuth string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		gotForm = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 5})
	})

	id, err := c.CreateRoute(t.Context(), "at", aRouteRequest())
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	if id != "5" {
		t.Fatalf("id = %q, want the response's numeric id as a string", id)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/routes" {
		t.Fatalf("%s %s, want POST /v1/routes", gotMethod, gotPath)
	}
	if gotAuth != "Bearer at" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotForm.Get("route[external_id]") != "kluisbergen" {
		t.Errorf("route[external_id] = %q", gotForm.Get("route[external_id]"))
	}
	if gotForm.Get("route[name]") != "Kluisbergen" {
		t.Errorf("route[name] = %q", gotForm.Get("route[name]"))
	}
	if gotForm.Get("route[provider_updated_at]") != "2026-01-02T03:04:05Z" {
		t.Errorf("route[provider_updated_at] = %q", gotForm.Get("route[provider_updated_at]"))
	}
	if gotForm.Get("route[workout_type_family_id]") != "0" {
		t.Errorf("route[workout_type_family_id] = %q, want 0 (BIKING)", gotForm.Get("route[workout_type_family_id]"))
	}
	if gotForm.Get("route[start_lat]") != "50.85" || gotForm.Get("route[start_lng]") != "4.35" {
		t.Errorf("start lat/lng = %q/%q", gotForm.Get("route[start_lat]"), gotForm.Get("route[start_lng]"))
	}
	if gotForm.Get("route[distance]") != "12345" || gotForm.Get("route[ascent]") != "250" {
		t.Errorf("distance/ascent = %q/%q", gotForm.Get("route[distance]"), gotForm.Get("route[ascent]"))
	}
	if gotForm.Get("route[description]") != "A short climb" {
		t.Errorf("route[description] = %q", gotForm.Get("route[description]"))
	}
	if gotForm.Get("route[filename]") != "kluisbergen.fit" {
		t.Errorf("route[filename] = %q", gotForm.Get("route[filename]"))
	}

	// route[file] is a full data URI, not bare base64 — the one detail the
	// docs would let you miss silently.
	wantFile := "data:application/vnd.fit;base64," + base64.StdEncoding.EncodeToString([]byte("FIT-BYTES"))
	if gotForm.Get("route[file]") != wantFile {
		t.Errorf("route[file] = %q, want %q", gotForm.Get("route[file]"), wantFile)
	}
}

func TestCreateRoutePostsRunningAsItsOwnWorkoutTypeFamily(t *testing.T) {
	var gotForm url.Values
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		gotForm = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 5})
	})

	req := aRouteRequest()
	req.Sport = "running"
	if _, err := c.CreateRoute(t.Context(), "at", req); err != nil {
		t.Fatalf("create route: %v", err)
	}
	if got := gotForm.Get("route[workout_type_family_id]"); got != "1" {
		t.Errorf("route[workout_type_family_id] = %q, want 1 (RUNNING)", got)
	}
}

func TestUpdateRoutePUTsToTheRouteID(t *testing.T) {
	var gotMethod, gotPath string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 5})
	})

	if _, err := c.UpdateRoute(t.Context(), "at", "5", aRouteRequest()); err != nil {
		t.Fatalf("update route: %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/v1/routes/5" {
		t.Fatalf("%s %s, want PUT /v1/routes/5", gotMethod, gotPath)
	}
}

func TestDeleteRoute(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.DeleteRoute(t.Context(), "at", "5"); err != nil {
		t.Fatalf("delete route: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/v1/routes/5" {
		t.Fatalf("%s %s, want DELETE /v1/routes/5", gotMethod, gotPath)
	}
	if gotAuth != "Bearer at" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}

func TestDeleteRouteSurfacesAnUpstreamFailure(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if err := c.DeleteRoute(t.Context(), "at", "5"); err == nil {
		t.Fatal("expected an error for a 404")
	}
}

func TestListRoutesFiltersDeletedAndParsesFields(t *testing.T) {
	var gotAuth string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/routes" {
			t.Errorf("%s %s, want GET /v1/routes", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[
			{
				"id": 5, "name": "Kemmelberg", "description": "hilly",
				"file": {"url": "https://cdn.example.test/route.fit"},
				"external_id": "kemmelberg-loop", "deleted": false,
				"start_lat": 50.79, "start_lng": 2.81,
				"distance": 5500.0, "ascent": 100.0,
				"updated_at": "2026-08-01T10:00:00Z", "created_at": "2026-07-01T10:00:00Z"
			},
			{
				"id": 6, "name": "Gone", "deleted": true,
				"file": {"url": "https://cdn.example.test/gone.fit"}
			}
		]`))
	})

	routes, err := c.ListRoutes(t.Context(), "at")
	if err != nil {
		t.Fatalf("ListRoutes: %v", err)
	}
	if gotAuth != "Bearer at" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want 1 (the deleted one filtered out)", len(routes))
	}

	got := routes[0]
	if got.ID != "5" || got.ExternalID != "kemmelberg-loop" || got.Name != "Kemmelberg" {
		t.Errorf("route = %+v", got)
	}
	if got.FileURL != "https://cdn.example.test/route.fit" {
		t.Errorf("file url = %q", got.FileURL)
	}
	if got.DistanceM != 5500 || got.AscentM != 100 {
		t.Errorf("stats = %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("updated_at not parsed")
	}
}

func TestListRoutesSurfacesAnUpstreamFailure(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	if _, err := c.ListRoutes(t.Context(), "at"); err == nil {
		t.Fatal("expected an error for a 401")
	}
}

func TestDownloadRouteFromTheAPIHostCarriesAuth(t *testing.T) {
	var gotAuth string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("fit-bytes"))
	})

	got, err := c.DownloadRoute(t.Context(), "at", c.APIBase+"/v1/routes/5/file")
	if err != nil {
		t.Fatalf("DownloadRoute: %v", err)
	}
	if string(got) != "fit-bytes" {
		t.Errorf("body = %q", got)
	}
	if gotAuth != "Bearer at" {
		t.Errorf("Authorization to our own API host = %q, want Bearer at", gotAuth)
	}
}

// GET /v1/routes' file.url comes straight out of Wahoo's own response body
// (routeListItem), and a rider picks ids from that list to import — a
// compromised or malicious upstream response could point it at an internal
// address instead of the real CDN. DownloadRoute must refuse anything off
// the allowlist outright, not just withhold the bearer token from it.
func TestDownloadRouteRefusesAnUnrecognizedHost(t *testing.T) {
	var reached bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		_, _ = w.Write([]byte("fit-bytes"))
	}))
	t.Cleanup(elsewhere.Close)

	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should never reach the API host either — the URL is refused before any request is sent")
	})

	if _, err := c.DownloadRoute(t.Context(), "at", elsewhere.URL+"/route.fit"); err == nil {
		t.Fatal("expected an error for a file URL on an unrecognized host")
	}
	if reached {
		t.Error("the unrecognized host was fetched despite not being on the allowlist")
	}
}

// The two hosts this app actually expects a route file to come from: its
// own API host (carries auth) and Wahoo's own CDN (does not — see
// allowedFileHost's doc comment for why cdn.wahooligan.com specifically is
// trusted rather than "whatever host the response happens to name").
func TestAllowedFileHostAcceptsTheAPIHostAndTheCDNOnly(t *testing.T) {
	c := New(Config{ClientID: "id", ClientSecret: "secret", RedirectURL: "https://app.example.test/callback"})
	c.APIBase = "https://api.wahooligan.com"

	for _, tc := range []struct {
		url        string
		wantAPI    bool
		wantRefuse bool
	}{
		{url: "https://api.wahooligan.com/v1/routes/5/file", wantAPI: true},
		{url: "https://cdn.wahooligan.com/wahoo-cloud/uploads/route/file/x/y.fit", wantAPI: false},
		{url: "https://evil.example.test/route.fit", wantRefuse: true},
		{url: "http://cdn.wahooligan.com/route.fit", wantRefuse: true}, // right host, wrong scheme
	} {
		onAPIHost, err := c.allowedFileHost(tc.url)
		if tc.wantRefuse {
			if err == nil {
				t.Errorf("%s: expected refusal, got onAPIHost=%v", tc.url, onAPIHost)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected refusal: %v", tc.url, err)
			continue
		}
		if onAPIHost != tc.wantAPI {
			t.Errorf("%s: onAPIHost = %v, want %v", tc.url, onAPIHost, tc.wantAPI)
		}
	}
}

func TestDownloadRouteSurfacesAnUpstreamFailure(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if _, err := c.DownloadRoute(t.Context(), "at", c.APIBase+"/v1/routes/5/file"); err == nil {
		t.Fatal("expected an error for a 404")
	}
}

func TestCreateRouteRejectsAnEmptyFile(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not have reached the upstream at all")
	})
	req := aRouteRequest()
	req.FIT = nil
	if _, err := c.CreateRoute(t.Context(), "at", req); err == nil {
		t.Fatal("expected an error for an empty FIT file")
	}
}

func TestCreateRouteSurfacesAnUpstreamRejection(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":["distance can't be blank"]}`))
	})
	if _, err := c.CreateRoute(t.Context(), "at", aRouteRequest()); err == nil {
		t.Fatal("expected an error for a 422")
	}
}

func TestCreateRouteRejectsAResponseWithNoID(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "no id here"})
	})
	if _, err := c.CreateRoute(t.Context(), "at", aRouteRequest()); err == nil {
		t.Fatal("expected an error when the response carries no id")
	}
}
