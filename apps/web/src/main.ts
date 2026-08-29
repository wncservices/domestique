import ui from '@nuxt/ui/vue-plugin'
import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import { applyColorMode, initColorMode } from './color-mode'
import './styles.css'

// Each page answers a different question: what is in the library, how do I
// put something in it, who am I connected to, and (admin-only) who has
// access at all. The catch-all keeps a refresh on any path rendering the
// app rather than the API's 404 — the Go side already falls back to
// index.html.
const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: () => import('./pages/LibraryPage.vue') },
    { path: '/add', component: () => import('./pages/AddPage.vue') },
    { path: '/build', component: () => import('./pages/BuildRoutePage.vue') },
    { path: '/settings', component: () => import('./pages/SettingsPage.vue') },
    { path: '/people', component: () => import('./pages/PeoplePage.vue') },
    { path: '/crews', component: () => import('./pages/CrewsPage.vue') },
    // Not part of "the app" the way the rest of these are — a share
    // recipient may hold no role in this deployment at all. See App.vue's
    // own isSharedRoutePage for why it renders outside the usual shell.
    { path: '/shared/:token', component: () => import('./pages/SharedRoutePage.vue') },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

initColorMode()

createApp(App).use(router).use(ui).mount('#app')

// Again, after mount: see applyColorMode.
applyColorMode()
