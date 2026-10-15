import { computed, ref } from 'vue'

/**
 * Light/dark handling.
 *
 * Nuxt UI themes off a `dark` class on <html>; outside Nuxt, nothing puts it
 * there. Three states rather than two: `system` is the default and keeps
 * following the OS, and choosing light or dark pins it until the rider says
 * otherwise. Pinning has to survive a reload, or the toggle appears to forget.
 */
export type ColorMode = 'light' | 'dark' | 'system'

const STORAGE_KEY = 'domestique.color-mode'

const query = window.matchMedia('(prefers-color-scheme: dark)')

function stored(): ColorMode {
  const value = localStorage.getItem(STORAGE_KEY)
  return value === 'light' || value === 'dark' ? value : 'system'
}

const mode = ref<ColorMode>(stored())

/** What the page is actually showing, which `system` alone does not tell you. */
const resolved = computed<'light' | 'dark'>(() =>
  mode.value === 'system' ? (query.matches ? 'dark' : 'light') : mode.value,
)

/**
 * Writes the current mode to <html>.
 *
 * Exported because it has to run twice. Nuxt UI bundles unhead, which takes
 * ownership of the <html> attributes and rewrites `class` when the app mounts
 * — wiping whatever was set before. Setting it early still matters: it is what
 * stops a dark-mode reload flashing white first. So: once before mount for the
 * paint, once after for the one that survives.
 */
export function applyColorMode(): void {
  const dark = resolved.value === 'dark'
  document.documentElement.classList.toggle('dark', dark)
  // Keeps form controls and scrollbars in step with the rest.
  document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
}

function set(next: ColorMode): void {
  mode.value = next
  if (next === 'system') {
    localStorage.removeItem(STORAGE_KEY)
  } else {
    localStorage.setItem(STORAGE_KEY, next)
  }
  applyColorMode()
}

/** Cycles light → dark → system, which is what a single button can offer. */
function cycle(): void {
  set(mode.value === 'light' ? 'dark' : mode.value === 'dark' ? 'system' : 'light')
}

/**
 * Paper is white: a page printed in dark mode would come out as pale text on
 * white. Light tokens are put back for the duration of a print, and the rider's
 * mode afterwards. `beforeprint`/`afterprint` cover most browsers; the print
 * media query's own change event covers the ones (Safari) that skip them.
 */
function printLight(printing: boolean): void {
  if (printing) {
    document.documentElement.classList.remove('dark')
    document.documentElement.style.colorScheme = 'light'
  } else {
    applyColorMode()
  }
}

export function initColorMode(): void {
  applyColorMode()
  window.addEventListener('beforeprint', () => printLight(true))
  window.addEventListener('afterprint', () => printLight(false))
  window.matchMedia('print').addEventListener('change', (e: MediaQueryListEvent) => printLight(e.matches))
  // Only meaningful while following the system, but the listener stays
  // attached so switching back to `system` takes effect without a reload.
  query.addEventListener('change', () => {
    if (mode.value === 'system') applyColorMode()
  })
}

export function useColorMode() {
  return { mode, resolved, set, cycle }
}
