import { createRouter, createWebHistory, type RouteLocationGeneric, type RouteRecordRaw } from 'vue-router'
import { redirectFor } from './routeRedirects.ts'
import { browserReloadDeps, isChunkLoadError, reloadForNewBuild } from './utils/staleChunk.ts'

export { redirectFor }

// Old paths forward to their new home, query and hash intact. Falls back to
// /today for anything redirectFor does not know (the catch-all).
function moved(to: RouteLocationGeneric) {
  const target = redirectFor(to.path, to.query as Record<string, string>)
  return { path: target?.path ?? '/today', query: target?.query ?? to.query, hash: to.hash }
}

// Each page answers a different question: what is today's plan, what is in
// the library, how do I put something in it, who am I connected to, and
// (admin-only) who has access at all. The catch-all keeps a refresh on any
// path rendering the app rather than the API's 404 — the Go side already
// falls back to index.html.
// `load` maps a page file name to its lazy component. main.ts passes real
// dynamic imports; the route-table test passes stubs, since Node cannot load
// .vue files.
export type PageLoader = (page: string) => () => Promise<unknown>

export function buildRoutes(load: PageLoader): RouteRecordRaw[] {
  return [
    { path: '/today', component: load('TodayPage') },
    { path: '/plan', component: load('PlanPage') },
    { path: '/goals', component: load('GoalsPage') },
    { path: '/workouts', component: load('WorkoutsPage') },
    { path: '/fitness', component: load('FitnessPage') },
    { path: '/routes', component: load('RoutesPage') },
    { path: '/routes/new', component: load('AddPage') },
    { path: '/routes/build', component: load('BuildRoutePage') },
    { path: '/crews', component: load('CrewsPage') },
    { path: '/settings', component: load('SettingsPage') },
    { path: '/people', component: load('PeoplePage') },
    // Not part of "the app" the way the rest of these are — a share
    // recipient may hold no role in this deployment at all. See App.vue's
    // own isSharedRoutePage for why it renders outside the usual shell.
    { path: '/shared/:token', component: load('SharedRoutePage') },
    // Pre-redesign paths, kept so bookmarks and sent emails still land.
    { path: '/', redirect: moved },
    { path: '/add', redirect: moved },
    { path: '/build', redirect: moved },
    { path: '/training', redirect: moved },
    { path: '/training/plan', redirect: moved },
    { path: '/training/fitness', redirect: moved },
    { path: '/:pathMatch(.*)*', redirect: moved },
  ]
}

// A page whose chunk a deploy removed fails to load and vue-router stays
// put, so the tap seemed to do nothing. A full load of that page fetches the
// new build instead (utils/staleChunk.ts). Tests pass their own reload.
const reloadPage = (url: string) => {
  reloadForNewBuild(url, browserReloadDeps())
}

export function createAppRouter(load: PageLoader, history = createWebHistory(), reload = reloadPage) {
  // Pages that own a hash (Settings sections, Fitness #profile) scroll
  // themselves, so a hash navigation is left alone.
  const router = createRouter({
    history,
    routes: buildRoutes(load),
    // A new page opens at the top; a hash or a query change on the same page
    // (the Fitness/Season switch) leaves scrolling to the page itself.
    scrollBehavior: (to, from, saved) => saved ?? (to.hash || to.path === from.path ? false : { top: 0 }),
  })
  router.onError((err, to) => {
    if (isChunkLoadError(err)) reload(to.fullPath)
  })
  return router
}
