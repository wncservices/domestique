<script setup lang="ts">
/**
 * A single route, shared outside the crew — see docs behind
 * ShareRouteDialog.vue for the feature's own shape.
 *
 * Deliberately not part of the authenticated shell App.vue otherwise
 * renders (see its own isSharedRoutePage): a share recipient may hold no
 * role in this deployment at all, so this page fetches its own /api/me and
 * its own route data through /api/shares/{token}, never useLibrary.
 */
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ApiError, api } from '@/api/client'
import type { Me, SharedRoute } from '@/api/types'
import RouteMap from '@/components/RouteMap.vue'

const route = useRoute()
const token = computed(() => String(route.params.token ?? ''))

const me = ref<Me | null>(null)
const loadingMe = ref(true)

const sharedRoute = ref<SharedRoute | null>(null)
const points = ref<[number, number][]>([])
const loadingRoute = ref(false)
// Three different situations, three different messages — a link that never
// existed or was revoked reads differently from one that simply ran out,
// and both are worth telling apart from a genuine failure.
const errorKind = ref<'' | 'not-found' | 'expired' | 'other'>('')
const errorMessage = ref('')

// Carries the recipient back here after signing in — the same return_to
// pattern App.vue's own sign-in button uses, validated server-side by
// safeReturnTo.
const signInHref = computed(() => `/sso/login?return_to=${encodeURIComponent(route.fullPath)}`)

async function loadSharedRoute() {
  loadingRoute.value = true
  errorKind.value = ''
  try {
    const [summary, track] = await Promise.all([api.sharedRoute(token.value), api.sharedRouteTrack(token.value)])
    sharedRoute.value = summary
    points.value = track.points
  } catch (err) {
    sharedRoute.value = null
    points.value = []
    if (err instanceof ApiError && err.status === 404) {
      errorKind.value = 'not-found'
    } else if (err instanceof ApiError && err.status === 410) {
      errorKind.value = 'expired'
    } else {
      errorKind.value = 'other'
      errorMessage.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    loadingRoute.value = false
  }
}

onMounted(async () => {
  try {
    me.value = await api.me()
  } catch {
    me.value = null
  } finally {
    loadingMe.value = false
  }
  if (me.value?.authenticated) await loadSharedRoute()
})

const distance = computed(() => (sharedRoute.value ? `${(sharedRoute.value.distanceM / 1000).toFixed(1)} km` : ''))
const ascent = computed(() => (sharedRoute.value ? `${Math.round(sharedRoute.value.ascentM)} m` : ''))
const gpxUrl = computed(() => api.sharedRouteGpxUrl(token.value))
const mapRoutes = computed(() =>
  sharedRoute.value ? [{ slug: sharedRoute.value.slug, points: points.value }] : [],
)
</script>

<template>
  <div class="flex flex-col gap-5">
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
        <p class="truncate text-xs text-muted">A route shared with you.</p>
      </div>
    </RouterLink>

    <USkeleton v-if="loadingMe" class="h-72 w-full" />

    <div
      v-else-if="!me?.authenticated"
      class="app-card flex flex-col items-center gap-3 px-6 py-12 text-center"
    >
      <UIcon name="i-lucide-link" class="size-8 text-dimmed" />
      <p class="max-w-xs text-sm text-toned">
        Whoever sent you this link wants to share one route with you. Sign in to see it.
      </p>
      <UButton :to="signInHref" external icon="i-lucide-log-in" color="primary">Sign in</UButton>
    </div>

    <USkeleton v-else-if="loadingRoute" class="h-72 w-full" />

    <UAlert
      v-else-if="errorKind === 'not-found'"
      color="error"
      variant="subtle"
      icon="i-lucide-link-2-off"
      title="This link doesn't work"
      description="It may have been revoked, or it never existed."
    />
    <UAlert
      v-else-if="errorKind === 'expired'"
      color="warning"
      variant="subtle"
      icon="i-lucide-clock-alert"
      title="This link has expired"
      description="Ask whoever shared it with you for a new one."
    />
    <UAlert
      v-else-if="errorKind === 'other'"
      color="error"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      title="Could not load this route"
      :description="errorMessage"
    />

    <div v-else-if="sharedRoute" class="flex flex-col gap-4">
      <div>
        <h2 class="font-display text-xl font-semibold text-highlighted">{{ sharedRoute.name }}</h2>
        <p class="text-xs text-dimmed">Shared with you — you can view and download it, nothing more.</p>
      </div>

      <div class="h-64 overflow-hidden rounded-lg bg-elevated/50 sm:h-80">
        <RouteMap :routes="mapRoutes" />
      </div>

      <dl class="flex flex-wrap gap-5">
        <div>
          <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Distance</dt>
          <dd class="font-mono tabular-nums">{{ distance }}</dd>
        </div>
        <div>
          <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Ascent</dt>
          <dd class="font-mono tabular-nums">{{ ascent }}</dd>
        </div>
      </dl>

      <UBadge
        :color="sharedRoute.sport === 'running' ? 'neutral' : 'primary'"
        variant="subtle"
        size="sm"
        class="w-fit"
        :icon="sharedRoute.sport === 'running' ? 'i-lucide-footprints' : 'i-lucide-bike'"
      >
        {{ sharedRoute.sport }}
      </UBadge>

      <!-- external: a same-origin path otherwise reads as an internal
           route to vue-router, which intercepts the click before
           `download` gets a chance to fire — same reasoning as every
           other GPX download button in this app. -->
      <UButton
        :href="gpxUrl"
        external
        download
        icon="i-lucide-download"
        color="neutral"
        variant="subtle"
        class="w-fit"
      >
        Download GPX
      </UButton>
    </div>
  </div>
</template>
