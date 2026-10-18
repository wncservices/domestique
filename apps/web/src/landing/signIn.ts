// Where the landing page's "Sign in" goes. The app sends a signed-out visitor
// here with ?return_to=<the path they asked for>; passing it on to /sso/login
// brings them back to it after signing in (a share link above all). The server
// checks return_to again, but only a plain path on the app is ever forwarded,
// so this page never relays somebody else's URL.
export function signInURL(appHost: string, authMode: string, search: string): string {
  if (authMode !== 'oidc') return appHost
  const login = `${appHost}/sso/login`
  const back = new URLSearchParams(search).get('return_to') ?? ''
  const plainPath = back.startsWith('/') && !back.startsWith('//') && !/[\\\s\p{Cc}]/u.test(back)
  return plainPath ? `${login}?return_to=${encodeURIComponent(back)}` : login
}
