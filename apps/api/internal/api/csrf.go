package api

import (
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// csrfGuard refuses a state-changing request that a browser is making on
// another site's behalf (audit N4). SameSite=Lax on the session cookies is the
// only other control, and it does not cover a sibling subdomain, a proxy-mode
// deployment whose cookie is Authelia's, or the no-cookie local demo.
//
// Three checks, in order:
//
//  1. Fetch Metadata. Every current browser sends Sec-Fetch-Site and a page
//     cannot forge or strip it. "same-origin" is this app's own page, "none"
//     is a person typing the URL or using a bookmark; "same-site" (a sibling
//     subdomain) and "cross-site" are refused.
//  2. Without it, an Origin header (every browser sends one on a write) must
//     be this deployment's own: the Host the request arrived on, or the
//     configured public URL or OIDC redirect hosts. An Origin of "null" (a
//     sandboxed frame, a cross-origin redirect) is refused.
//  3. A request with neither header is not a browser acting for a page: the
//     Authelia forwardAuth path, curl, a monitoring script, the test suite.
//     It cannot be driven cross-site, so it passes. This is why the guard
//     cannot lock the CLI or a proxy deployment out.
//
// Then a body, if there is one, must be application/json (the upload is the
// one multipart endpoint). That is what turns a cross-site <form
// enctype="text/plain"> or no-cors fetch, which can only send simple content
// types, into something no handler would have parsed anyway.
//
// Nothing is exempt: reads (GET, HEAD, OPTIONS) are not guarded, so the
// calendar feed, the OIDC and Wahoo callbacks (all GET, state-checked; the
// issuer is configured for the default query response mode, not form_post)
// and the SPA's own pages never reach this; POST /sso/logout is a same-origin
// form post from the SPA and stays guarded.
func (s *Server) csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		site, origin := r.Header.Get("Sec-Fetch-Site"), r.Header.Get("Origin")
		refused := false
		switch {
		case site != "":
			refused = site != "same-origin" && site != "none"
		case origin != "":
			refused = !s.isOwnOrigin(r, origin)
		}
		if refused {
			s.logger().Warn("cross-site write refused",
				"method", r.Method, "path", redactPath(r.URL.Path), "sec_fetch_site", site, "origin", originHost(origin))
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site request refused"})
			return
		}

		if r.ContentLength != 0 {
			mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if mt != "application/json" && !(mt == "multipart/form-data" && isUploadRoute(r)) {
				writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "request body must be application/json"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// isUploadRoute is the one endpoint that takes a multipart body: the GPX upload.
func isUploadRoute(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/api/routes"
}

// isOwnOrigin reports whether an Origin header names this deployment: the Host
// the request came in on, or a host the operator configured (public_url, the
// OIDC redirect and preview redirect URLs). Scheme is not compared, since TLS
// ends at the proxy.
func (s *Server) isOwnOrigin(r *http.Request, origin string) bool {
	host := originHost(origin)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, r.Host) {
		return true
	}
	configured := []string{s.publicURL()}
	oidc := s.authenticator().OIDC()
	configured = append(configured, oidc.RedirectURL, oidc.PreviewRedirectURL)
	for _, c := range configured {
		if c == "" {
			continue
		}
		if u, err := url.Parse(c); err == nil && strings.EqualFold(u.Host, host) {
			return true
		}
	}
	return false
}

// originHost is the host:port of an Origin header, "" for "null" or anything
// that is not an absolute URL.
func originHost(origin string) string {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}
