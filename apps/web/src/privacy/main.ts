import ui from '@nuxt/ui/vue-plugin'
import { createApp } from 'vue'
import { applyColorMode, initColorMode } from '../color-mode'
import '../styles.css'
import Privacy from './Privacy.vue'

// Its own entry, like the landing page, and for the same reason: it is public,
// served on the apex to people who are not signed in, and must not pull in the
// router or the API client. It shares styles.css and the landing header so the
// two read as one site, light and dark.
initColorMode()

createApp(Privacy).use(ui).mount('#privacy')

// Again after mount: unhead rewrites the html class. See applyColorMode.
applyColorMode()
