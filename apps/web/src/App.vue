<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import ColorModeToggle from '@/components/ColorModeToggle.vue'
import { useLibrary } from '@/composables/useLibrary'
import { roleColor } from '@/utils/role'

const {
  me,
  accounts,
  routes,
  error,
  refresh,
  totalDistance,
  totalAscent,
  canUpload,
  canManagePeople,
  canManageCrews,
} = useLibrary()
const route = useRoute()
const router = useRouter()
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
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  }
}

// accounts is every device this rider can see — their own and a crew
// fellow's (see server.go's listableAccounts) — but this tile is answering
// "how many head units do I have," not "how many can I see." Same
// own-account check AccountsPanel.vue's isMine already uses, so the two
// never quietly disagree on what "mine" means.
const myAccountCount = computed(() => {
  const user = me.value?.user?.toLowerCase()
  if (!user) return 0
  return accounts.value.filter((a) => a.rider.toLowerCase() === user).length
})

// One categorical accent per tile (docs/design-system.md) so the four
// separate at a glance instead of repeating the same primary tint four
// times — purely decorative, carries no status meaning.
const stats = computed(() => [
  { label: 'Routes', value: String(routes.value.length), icon: 'i-lucide-route', color: 'primary' },
  {
    label: 'Distance',
    value: `${totalDistance.value.toFixed(0)} km`,
    icon: 'i-lucide-ruler',
    color: 'ember',
  },
  {
    label: 'Ascent',
    value: `${Math.round(totalAscent.value).toLocaleString()} m`,
    icon: 'i-lucide-mountain',
    color: 'sky',
  },
  {
    label: 'Head units',
    value: String(myAccountCount.value),
    icon: 'i-lucide-watch',
    color: 'violet',
  },
])

// Add is hidden rather than disabled for a viewer: the page would be an empty
// form and an explanation, which is worse than not offering it.
const links = computed(() =>
  [
    { to: '/', label: 'Library', icon: 'i-lucide-route' },
    canUpload.value ? { to: '/add', label: 'Add route', icon: 'i-lucide-plus' } : null,
    canManageCrews.value ? { to: '/crews', label: 'Crews', icon: 'i-lucide-users-round' } : null,
    canManagePeople.value ? { to: '/people', label: 'People', icon: 'i-lucide-users' } : null,
    { to: '/settings', label: 'Settings', icon: 'i-lucide-settings' },
  ].filter((link) => link !== null),
)

// /sso/callback redirects here with ?notice=... whenever the issuer denied
// a login on purpose mid-provisioning (a linked identity, a brand-new
// signup's roles just granted — see sso.go's own withNotice) rather than
// rejecting it outright — a toast is the friendly landing for that; a raw
// JSON blob is what showed up here before this existed. Stripped from the
// URL right after so a refresh doesn't show it again.
//
// A watch with immediate: true, not a one-shot check in onMounted: the
// router's own initial navigation is not guaranteed to have resolved by the
// time App.vue — the root component, mounted directly rather than reached
// through a route — first runs its mounted hook, so route.query can still
// be empty at that exact moment. Watching reacts correctly whether the
// query was already there or resolves a tick later.
watch(
  () => route.query.notice,
  (notice) => {
    if (typeof notice !== 'string' || !notice) return
    toast.add({ title: notice, icon: 'i-lucide-info', color: 'info' })
    const { notice: _drop, ...query } = route.query
    router.replace({ path: route.path, query })
  },
  { immediate: true },
)

onMounted(refresh)
</script>

<template>
  <UApp>
    <div class="app-header sticky top-0 z-20">
      <UContainer class="max-w-5xl">
        <div class="flex items-center justify-between gap-4 py-3">
          <RouterLink to="/" class="flex min-w-0 items-center gap-3">
            <span
              class="flex size-9 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary"
              aria-hidden="true"
            >
              <UIcon name="i-lucide-bike" class="size-5" />
            </span>
            <div class="min-w-0">
              <h1 class="font-display truncate text-base font-semibold tracking-tight text-highlighted">
                Domestique
              </h1>
              <p class="truncate text-xs text-muted">
                Shared route library, carried to every head unit.
              </p>
            </div>
          </RouterLink>

          <div class="flex shrink-0 items-center gap-2">
            <UBadge
              v-if="me?.authenticated"
              :color="roleColor(me.role)"
              variant="subtle"
              icon="i-lucide-user"
              class="hidden sm:inline-flex"
            >
              {{ me.name || me.user }} · {{ me.role }}
            </UBadge>

            <!-- authMode oidc is the first mode where the SPA genuinely
                 loads while signed out: mode proxy's forwardAuth stops an
                 anonymous visitor before a request reaches this app at all,
                 so there is no equivalent case to handle for it here. -->
            <UButton
              v-else-if="me?.authMode === 'oidc'"
              :to="signInHref"
              external
              icon="i-lucide-log-in"
              color="primary"
              variant="subtle"
              size="sm"
            >
              Sign in
            </UButton>

            <UTooltip
              v-else-if="me"
              text="Anyone who can reach this page has full access. Put it behind Authelia before exposing it."
            >
              <UBadge color="warning" variant="subtle" icon="i-lucide-shield-off">
                <span class="hidden sm:inline">no login required</span>
                <span class="sm:hidden">open</span>
              </UBadge>
            </UTooltip>

            <!-- authMode oidc: the app holds this session and ends it
                 itself, so signing out is a fetch, not a navigation to
                 somewhere else. authMode proxy: a plain link, since the
                 identity provider owns that session and this app cannot end
                 it — an XHR could not carry the browser to the provider's own
                 logout page even if it tried. -->
            <UButton
              v-if="me?.authenticated && me.authMode === 'oidc'"
              icon="i-lucide-log-out"
              color="neutral"
              variant="ghost"
              size="sm"
              :loading="signingOut"
              aria-label="Sign out"
              @click="signOut"
            >
              <span class="hidden sm:inline">Sign out</span>
            </UButton>
            <UButton
              v-else-if="me?.authenticated && me.logoutUrl"
              :to="me.logoutUrl"
              external
              icon="i-lucide-log-out"
              color="neutral"
              variant="ghost"
              size="sm"
              aria-label="Sign out"
            >
              <span class="hidden sm:inline">Sign out</span>
            </UButton>

            <ColorModeToggle />
          </div>
        </div>

        <nav class="flex gap-1 overflow-x-auto pb-1" aria-label="Sections">
          <UButton
            v-for="link in links"
            :key="link.to"
            :to="link.to"
            :icon="link.icon"
            :color="$route.path === link.to ? 'primary' : 'neutral'"
            :variant="$route.path === link.to ? 'subtle' : 'ghost'"
            size="sm"
            :aria-label="link.label"
            :class="['shrink-0', link.to === '/settings' ? 'sm:ml-auto' : '']"
          >
            <span class="hidden sm:inline">{{ link.label }}</span>
          </UButton>
        </nav>
      </UContainer>
    </div>

    <UContainer class="flex max-w-5xl flex-col gap-6 py-6">
      <section class="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <div v-for="stat in stats" :key="stat.label" class="app-card px-4 py-3">
          <div class="flex items-center gap-1.5 text-[0.7rem] uppercase tracking-wide text-dimmed">
            <span
              class="flex size-4 items-center justify-center rounded"
              :style="{ color: `var(--app-accent-${stat.color})` }"
            >
              <UIcon :name="stat.icon" class="size-3.5" />
            </span>
            {{ stat.label }}
          </div>
          <div class="font-mono mt-1 truncate text-2xl tabular-nums text-highlighted">
            {{ stat.value }}
          </div>
        </div>
      </section>

      <UAlert
        v-if="error"
        color="error"
        variant="subtle"
        icon="i-lucide-plug-zap"
        orientation="horizontal"
        title="Could not reach the API"
        :description="error"
        :actions="[{ label: 'Retry', color: 'error', variant: 'subtle', onClick: () => refresh() }]"
      />

      <!-- KeepAlive, LibraryPage only: every other page (Add, Settings,
           People, Crews) genuinely wants fresh state each visit, but
           Library's own cards each carry a background wash that's real
           work to (re)build — a route line projected from the raw track
           and, since #174, an earth/landuse/water/roads wash decoded from
           vector tiles (or, since #179, fetched from the server-side
           cache — still a network round trip either way). Without this,
           navigating away and back destroyed every TrackPreview instance
           and rebuilt all of it from scratch, even though nothing about
           the route itself had changed. LibraryPage's own route list
           already comes from useLibrary's shared state, not a
           component-local fetch, so keeping the component alive doesn't
           risk showing a stale list — a route added on another page still
           appears the moment that shared state updates. -->
      <RouterView v-slot="{ Component }">
        <KeepAlive include="LibraryPage">
          <component :is="Component" />
        </KeepAlive>
      </RouterView>

      <!-- AGPL-3.0 section 13: a modified version offered over a network has
           to offer its users the source. A link in the footer of every page is
           the simplest way to actually satisfy that, and the easiest thing to
           forget. -->
      <footer class="mt-2 border-t border-default pt-4 text-xs text-dimmed">
        <a
          href="https://github.com/wncservices/domestique"
          target="_blank"
          rel="noopener noreferrer"
          class="hover:text-default"
        >
          Domestique — free software under the AGPL-3.0. Source.
        </a>
      </footer>
    </UContainer>
  </UApp>
</template>
