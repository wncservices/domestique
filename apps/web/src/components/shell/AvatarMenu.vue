<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import { useColorMode } from '@/color-mode'
import { useLibrary } from '@/composables/useLibrary'
import { describeError } from '@/utils/errorMessage'
import { menuItems, type MenuItem } from './navItems'

defineProps<{ compact?: boolean }>()

const { me, canUpload, routingConfigured } = useLibrary()
const { mode, cycle } = useColorMode()
const route = useRoute()
const toast = useToast()

// Carries the rider back to where they were before signing in. Validated
// server-side too (handleSSOLogin rejects anything that is not a same-origin
// path) — this is only what the button offers, not the actual guarantee.
const signInHref = computed(() => `/sso/login?return_to=${encodeURIComponent(route.fullPath)}`)

const signingOut = ref(false)

// authMode oidc's session belongs to this app, not a proxy, so ending it is a
// state change (POST) rather than a navigation (a link). The redirect that
// follows still has to be a real navigation, though — a fetch cannot carry
// the browser to the issuer's own end-session page the way a plain link can.
async function signOut() {
  signingOut.value = true
  try {
    const { redirectTo } = await api.logout()
    window.location.href = redirectTo || '/'
  } catch (err) {
    signingOut.value = false
    toast.add({
      title: 'Sign out failed',
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  }
}

const themeIcon = computed(() =>
  mode.value === 'system' ? 'i-lucide-monitor' : mode.value === 'dark' ? 'i-lucide-moon' : 'i-lucide-sun',
)
const themeLabel = computed(() => (mode.value === 'system' ? 'Theme: system' : `Theme: ${mode.value}`))

const icons: Record<string, string> = {
  Settings: 'i-lucide-settings',
  People: 'i-lucide-users',
  'Sign out': 'i-lucide-log-out',
  'Open access': 'i-lucide-shield-off',
}

function toDropdown(item: MenuItem) {
  if (item.notice) {
    return {
      label: item.label,
      description: item.notice,
      icon: icons[item.label],
      color: 'warning' as const,
      disabled: true,
    }
  }
  if (item.action === 'theme') {
    // Keeps the menu open so cycling light, dark, system can be seen working.
    return {
      label: themeLabel.value,
      icon: themeIcon.value,
      onSelect: (e: Event) => {
        e.preventDefault()
        cycle()
      },
    }
  }
  if (item.action === 'signOut') {
    return { label: item.label, icon: icons[item.label], disabled: signingOut.value, onSelect: signOut }
  }
  return { label: item.label, icon: icons[item.label], to: item.to, external: item.label === 'Sign out' }
}

const groups = computed(() => {
  if (!me.value) return []
  const signOutMode =
    me.value.authenticated && me.value.authMode === 'oidc'
      ? 'post'
      : me.value.authenticated && me.value.logoutUrl
        ? 'link'
        : undefined
  const all = menuItems(me.value.role, {
    authMode: me.value.authMode,
    signOut: signOutMode,
    logoutUrl: me.value.logoutUrl,
  }).map(toDropdown)
  // Add and Build lived in the old header; Routes will own them as buttons
  // later, until then the menu is where they stay reachable. Hidden rather
  // than disabled for a viewer: the page would be an empty form.
  const routes = [
    canUpload.value ? { label: 'Add route', icon: 'i-lucide-plus', to: '/routes/new' } : null,
    canUpload.value && routingConfigured.value
      ? { label: 'Build route', icon: 'i-lucide-pencil-ruler', to: '/routes/build' }
      : null,
  ].filter((i) => i !== null)
  const signOutItems = all.filter((i) => i.label === 'Sign out')
  const rest = all.filter((i) => i.label !== 'Sign out')
  return [routes, rest, signOutItems].filter((g) => g.length)
})

const displayName = computed(() => me.value?.name || me.value?.user || '')
const initial = computed(() => (displayName.value.trim()[0] ?? '?').toLocaleUpperCase())
</script>

<template>
  <!-- authMode oidc is the first mode where the SPA genuinely loads while
       signed out: mode proxy's forwardAuth stops an anonymous visitor before
       a request reaches this app at all. -->
  <UButton
    v-if="me && !me.authenticated && me.authMode === 'oidc'"
    :to="signInHref"
    external
    icon="i-lucide-log-in"
    color="primary"
    variant="subtle"
    size="sm"
  >
    <span :class="compact ? 'hidden' : 'hidden lg:inline'">Sign in</span>
  </UButton>

  <UDropdownMenu v-else-if="me" :items="groups" :content="{ align: compact ? 'end' : 'start', side: compact ? 'bottom' : 'top' }">
    <button
      type="button"
      class="flex w-full min-w-0 items-center gap-3 rounded-lg p-2 text-left hover:bg-elevated"
      :aria-label="`Account menu for ${displayName || 'this deployment'}`"
    >
      <span
        class="flex size-8 shrink-0 items-center justify-center rounded-full bg-primary/10 text-[length:var(--text-body)] font-semibold text-primary"
        aria-hidden="true"
      >
        <UIcon v-if="!me.authenticated" name="i-lucide-user" class="size-4" />
        <template v-else>{{ initial }}</template>
      </span>
      <span v-if="!compact" class="hidden min-w-0 lg:block">
        <span class="block truncate text-[length:var(--text-body)] text-highlighted">{{ displayName || 'Open access' }}</span>
        <span class="block truncate text-[length:var(--text-label)] text-muted">{{ me.authenticated ? me.role : 'no login required' }}</span>
      </span>
    </button>
  </UDropdownMenu>
</template>
