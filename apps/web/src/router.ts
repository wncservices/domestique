import { createRouter, createWebHistory, type RouteLocationGeneric, type RouteRecordRaw } from 'vue-router'
import { redirectFor } from './routeRedirects.ts'

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
  // Plan and Fitness each get TrainingPage as their own layout (for its
  // pills) rather than sharing one pathless parent: a parent with
  // path '' normalises to '/' and shadows the '/' redirect below.
  // Task 5 removes the pills and this wrapper.
  const training = (path: string, page: string): RouteRecordRaw => ({
    path,
    component: load('TrainingPage'),
    children: [{ path: '', component: load(page) }],
  })
  return [
    // Task 7 replaces this with the real Today page; until then it is the plan.
    { path: '/today', component: load('TrainingPlanPage') },
    training('/plan', 'TrainingPlanPage'),
    training('/fitness', 'TrainingFitnessPage'),
    { path: '/routes', component: load('LibraryPage') },
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

export function createAppRouter(load: PageLoader, history = createWebHistory()) {
  return createRouter({ history, routes: buildRoutes(load) })
}
