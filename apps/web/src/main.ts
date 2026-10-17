import ui from '@nuxt/ui/vue-plugin'
import { createApp } from 'vue'
import App from './App.vue'
import { applyColorMode, initColorMode } from './color-mode'
import { createAppRouter } from './router'
import { initTelemetry } from './telemetry'
import { prefetchMapLibreModules } from './utils/maplibre'
import './styles.css'

// Before anything else: catches errors/traces from router setup and mount
// itself, not just from code that runs after this point.
initTelemetry()

// Vite needs the import path literal, hence the glob rather than a template.
const pages = import.meta.glob('./pages/*.vue')
const router = createAppRouter((page) => pages[`./pages/${page}.vue`]!)

initColorMode()

createApp(App).use(router).use(ui).mount('#app')

// Again, after mount: see applyColorMode.
applyColorMode()

// Warms maplibre-gl/pmtiles/protomaps-themes-base in the background from
// the moment this app shell loads, not only once a rider actually opens a
// map — see prefetchMapLibreModules' own doc comment. Deliberately not in
// landing.html's own entry: that bundle must stay maplibre-free.
prefetchMapLibreModules()
