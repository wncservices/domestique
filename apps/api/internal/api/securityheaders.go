package api

import "net/http"

// contentSecurityPolicy is what the built SPA needs and nothing more. Each
// directive is here for a reason that can be checked against the code:
//
//   - script-src 'self': Vite emits only module scripts from /assets and no
//     inline script, so scripts are strict, with no 'unsafe-inline' or
//     'unsafe-eval'. This is the directive that limits what an XSS could do.
//   - style-src 'self' 'unsafe-inline': Nuxt UI (Tailwind, reka-ui) and
//     maplibre set inline style attributes and inject <style> at runtime. A
//     nonce cannot cover a style attribute, so inline styles are allowed; the
//     cost is that injected CSS is not blocked, which is far weaker than
//     injected script.
//   - connect-src 'self' https://protomaps.github.io + the Iconify API hosts:
//     the API, the same-origin basemap (pmtiles reads /tiles/basemap.pmtiles
//     with range requests), the map style's sprite and glyphs from Protomaps'
//     public CDN (utils/maplibre.ts styleFromTheme), and Nuxt UI's icons.
//     Outside Nuxt, UIcon fetches the lucide icons at runtime from
//     api.iconify.design, falling back to api.unisvg.com and api.simplesvg.com
//     (what @iconify/vue does by default); the build ships no icon CSS. None of
//     these requests carries route data, only an icon or font name. Bundling
//     the icons would drop the three hosts from this list.
//   - worker-src 'self' blob: / child-src blob:: maplibre-gl runs tile
//     decoding in a blob: worker.
//   - img-src 'self' data: blob:: card previews and sprite images, and the
//     canvases maplibre and the static preview draw.
//   - font-src 'self' data:: fonts are self-hosted through @fontsource.
//   - frame-ancestors 'none' (with X-Frame-Options for old browsers): nothing
//     may frame the app, which is cookie-authenticated.
//   - object-src 'none', base-uri 'self': no plugins, and a base tag cannot
//     redirect relative URLs.
//
// form-action is left out on purpose: sign-out is a form POST that answers with
// a redirect to the identity provider, and form-action also governs redirects.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self' https://protomaps.github.io " +
	"https://api.iconify.design https://api.unisvg.com https://api.simplesvg.com; " +
	"worker-src 'self' blob:; " +
	"child-src blob:; " +
	"manifest-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"frame-ancestors 'none'"

// securityHeaders sets the response headers every reply should carry. It runs
// outermost and sets before the handler does, so a handler that sets one of
// these itself (the calendar feed's no-referrer) keeps its own value.
//
// Referrer-Policy is same-origin, not the browser default: a share link's
// token lives in the page URL (/shared/<token>), and the default sends it as
// the Referer to every third party the page loads from.
//
// HSTS goes out only when this request arrived over HTTPS (the proxy's
// X-Forwarded-Proto counts, as everywhere else here). Over plain HTTP a
// browser ignores it, and a local `just demo` must not teach a browser to
// refuse http://localhost afterwards. No includeSubDomains or preload: those
// are a decision about the whole domain, not about this app.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if requestIsHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}
