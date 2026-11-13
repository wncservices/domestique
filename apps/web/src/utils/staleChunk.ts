// A tab opened before a deploy still holds the old build's page chunks by
// name. After the deploy those files are gone, the lazy import of the page a
// rider taps fails, and vue-router leaves them where they were: the app looks
// hung (phones keep a tab for days, so it showed there most). The cure is a
// full load of the page they asked for, which fetches the new build.

// Each engine words the failure differently; the MIME one is what a server
// that answered the old name with HTML produced before it learnt to 404.
const chunkErrors = [
  'Failed to fetch dynamically imported module', // Chromium
  'error loading dynamically imported module', // Firefox
  'Importing a module script failed', // WebKit
  'is not a valid JavaScript MIME type',
  'Unable to preload CSS',
]

export function isChunkLoadError(err: unknown): boolean {
  const message = err instanceof Error ? err.message : String(err ?? '')
  return chunkErrors.some((m) => message.includes(m))
}

// One reload per window: if the fresh load fails the same way (the deploy is
// still rolling out), a second reload straight away would only loop.
export const reloadWindowMs = 10_000
const key = 'domestique:stale-chunk-reload'

export interface ReloadDeps {
  storage: Pick<Storage, 'getItem' | 'setItem'> | null
  now: () => number
  go: (url: string) => void
}

export function reloadForNewBuild(url: string, deps: ReloadDeps): boolean {
  let last = 0
  try {
    last = Number(deps.storage?.getItem(key) ?? 0)
  } catch {
    // Storage can be blocked (private mode); reload anyway, once per call.
  }
  const now = deps.now()
  if (last && now - last < reloadWindowMs) return false
  try {
    deps.storage?.setItem(key, String(now))
  } catch {
    // As above.
  }
  deps.go(url)
  return true
}

export function browserReloadDeps(): ReloadDeps {
  let storage: Storage | null = null
  try {
    storage = window.sessionStorage
  } catch {
    storage = null
  }
  return { storage, now: () => Date.now(), go: (url) => window.location.assign(url) }
}
