import ui from '@nuxt/ui/vue-plugin'
import { createApp } from 'vue'
import App from './App.vue'
import { applyColorMode, initColorMode } from './color-mode'
import { router } from './router'
import { initTelemetry } from './telemetry'
import { prefetchMapLibreModules } from './utils/maplibre'
import './styles.css'

// Before anything else: catches errors/traces from router setup and mount
// itself, not just from code that runs after this point.
initTelemetry()

initColorMode()

createApp(App).use(router).use(ui).mount('#app')

// Again, after mount: see applyColorMode.
applyColorMode()

// Warms maplibre-gl/pmtiles/protomaps-themes-base in the background from
// the moment this app shell loads, not only once a rider actually opens a
// map — see prefetchMapLibreModules' own doc comment. Deliberately not in
// landing.html's own entry: that bundle must stay maplibre-free.
prefetchMapLibreModules()
