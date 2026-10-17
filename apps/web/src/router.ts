import { createRouter, createWebHistory, type RouteLocationGeneric, type RouteRecordRaw } from 'vue-router'
import { redirectFor } from './routeRedirects'

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
export const routes: RouteRecordRaw[] = [
  // Task 7 replaces this with the real Today page; until then it is the plan.
  { path: '/today', component: () => import('./pages/TrainingPlanPage.vue') },
  {
    // Pathless parent: keeps TrainingPage's Plan/Fitness pills until the
    // sidebar (Task 5) replaces them.
    path: '',
    component: () => import('./pages/TrainingPage.vue'),
    children: [
      { path: '/plan', component: () => import('./pages/TrainingPlanPage.vue') },
      { path: '/fitness', component: () => import('./pages/TrainingFitnessPage.vue') },
    ],
  },
  { path: '/routes', component: () => import('./pages/LibraryPage.vue') },
  { path: '/routes/new', component: () => import('./pages/AddPage.vue') },
  { path: '/routes/build', component: () => import('./pages/BuildRoutePage.vue') },
  { path: '/crews', component: () => import('./pages/CrewsPage.vue') },
  { path: '/settings', component: () => import('./pages/SettingsPage.vue') },
  { path: '/people', component: () => import('./pages/PeoplePage.vue') },
  // Not part of "the app" the way the rest of these are — a share
  // recipient may hold no role in this deployment at all. See App.vue's
  // own isSharedRoutePage for why it renders outside the usual shell.
  { path: '/shared/:token', component: () => import('./pages/SharedRoutePage.vue') },
  // Pre-redesign paths, kept so bookmarks still land.
  { path: '/', redirect: moved },
  { path: '/add', redirect: moved },
  { path: '/build', redirect: moved },
  { path: '/training', redirect: moved },
  { path: '/training/plan', redirect: moved },
  { path: '/training/fitness', redirect: moved },
  { path: '/:pathMatch(.*)*', redirect: moved },
]

export const router = createRouter({ history: createWebHistory(), routes })
