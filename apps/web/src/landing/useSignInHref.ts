import { onMounted, ref } from 'vue'
import { signInURL } from './signIn'

// Shared by the landing page and the privacy page, which wear the same header.
export function useSignInHref() {
  // Where the app lives. Built in, so a self-hoster changes it in one place —
  // this page cannot discover its own deployment's domain at runtime.
  const appHost = import.meta.env.VITE_APP_URL ?? 'https://app.domestique.dev'

  // Where "Sign in" actually goes depends on the auth mode, which this page
  // cannot know at build time: the same published image runs any mode, so
  // baking a mode-specific path in here would be right for this deployment
  // and wrong for every self-hoster who just pulls the image rather than
  // building their own.
  //
  // mode: proxy wants a plain cross-origin link to the app host — Authelia
  // sits in front of it and shows its own login form. mode: oidc wants
  // /sso/login appended instead: the app verifies tokens itself, and a bare
  // visit to its root now redirects straight back to this page (see
  // spaHandler in apps/api/internal/api/server.go), so the old plain link
  // bounced. Starts at the proxy-shaped bare link — this page's original,
  // long-standing behaviour, and the safe default: worst case before the
  // fetch below resolves is one extra bounce through the app's own redirect,
  // not a broken link.
  //
  // A plain fetch, not the api/client.ts module: this page is a separate,
  // deliberately minimal bundle that does not pull in the router or the API
  // client to render three paragraphs. /api/me answers identically regardless
  // of which host asks — the mode is a deployment-wide setting, not a
  // per-host one — so this same-origin call never needs to touch the app
  // host at all.
  const signInHref = ref(appHost)
  onMounted(async () => {
    try {
      const res = await fetch('/api/me')
      if (!res.ok) return
      const me = await res.json()
      signInHref.value = signInURL(appHost, me.authMode, window.location.search)
    } catch {
      // Network hiccup, or a backend old enough to have no /api/me: keep the
      // proxy-shaped default rather than break the link over it.
    }
  })
  return signInHref
}
