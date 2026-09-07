import ui from '@nuxt/ui/vue-plugin'
import { createApp } from 'vue'
import { applyColorMode, initColorMode } from '../color-mode'
import '../styles.css'
import { initTelemetry } from '../telemetry'
import Landing from './Landing.vue'

// The logged-out page is its own Vite entry rather than a route in the app:
// it is served on a different host, to people who are not signed in, and it
// has no business pulling in the router, the API client or the library state.
// Sharing styles.css is the point — same palette, same surfaces, same dark
// mode — while sharing nothing that can fail. telemetry.ts is shared for
// the same reason styles.css is: it's not app-specific, so there's nothing
// to diverge on.
initTelemetry()
initColorMode()

createApp(Landing).use(ui).mount('#landing')

// Again after mount: unhead rewrites the html class. See applyColorMode.
applyColorMode()
