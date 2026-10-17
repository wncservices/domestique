// Kept free of any import so `node --test` can load it: router.ts pulls in
// .vue files (lazily, but still), which Node cannot parse.
export type RedirectTarget = { path: string; query: Record<string, string> }

// Where each pre-redesign path lives now. Old bookmarks keep working; the
// query travels with them (the fitness page's ?tab=season, for one).
const MOVED: Record<string, string> = {
  '/': '/today',
  '/add': '/routes/new',
  '/build': '/routes/build',
  '/training': '/plan',
  '/training/plan': '/plan',
  '/training/fitness': '/fitness',
}

export function redirectFor(path: string, query: Record<string, string>): RedirectTarget | null {
  const to = MOVED[path]
  return to ? { path: to, query } : null
}
