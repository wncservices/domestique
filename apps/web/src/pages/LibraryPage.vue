<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useLibrary } from '@/composables/useLibrary'
import { useColorMode } from '@/color-mode'
import LibraryDuplicatesPanel from '@/components/LibraryDuplicatesPanel.vue'
import PlanPanel from '@/components/PlanPanel.vue'
import RouteCard from '@/components/RouteCard.vue'
import RouteDetailModal from '@/components/RouteDetailModal.vue'
import RouteMap from '@/components/RouteMap.vue'
import type { Sport, UpcomingRide } from '@/api/types'
import { formatRideWhen, todayISO } from '@/utils/rideDates'
import { prefetchTrackPreviewImages } from '@/utils/prefetchPreviewImages'

// Named explicitly for App.vue's <KeepAlive include="LibraryPage">, rather
// than relying on build-tool filename inference: the whole point of that
// KeepAlive is to stop every card's preview (route line + background wash)
// from being rebuilt on every visit, and a silent inference miss would
// bring that regression back with no error to point at.
defineOptions({ name: 'LibraryPage' })

const {
  accounts,
  routes,
  problems,
  plan,
  loading,
  me,
  canPush,
  canUpload,
  refresh,
} = useLibrary()

const { resolved } = useColorMode()

// Fires as soon as the library itself is known — not gated on scrolling,
// not gated on which page is showing — so a card's own IntersectionObserver
// (TrackPreview.vue) usually finds its image already sitting in the browser
// cache by the time the rider actually reaches it, rather than starting a
// fresh request right then. Re-fires on a theme toggle too, since that
// changes every image's URL (the ?theme= query param).
watch(
  [routes, resolved],
  ([currentRoutes, theme]) => {
    if (!currentRoutes.length) return
    prefetchTrackPreviewImages(
      currentRoutes.map((r) => r.slug),
      theme === 'dark' ? 'dark' : 'light',
    )
  },
  { immediate: true },
)

const search = ref('')
const pushing = ref(false)
const failures = ref<string[]>([])

const sportFilter = ref<Sport | 'all'>('all')
const sportFilterOptions: { label: string; value: Sport | 'all'; icon: string }[] = [
  { label: 'All sports', value: 'all', icon: 'i-lucide-list' },
  { label: 'Cycling', value: 'cycling', icon: 'i-lucide-bike' },
  { label: 'Running', value: 'running', icon: 'i-lucide-footprints' },
]

const visibleRoutes = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return routes.value.filter((route) => {
    if (sportFilter.value !== 'all' && route.sport !== sportFilter.value) return false
    if (!needle) return true
    return (
      route.name.toLowerCase().includes(needle) ||
      route.slug.toLowerCase().includes(needle) ||
      route.description.toLowerCase().includes(needle) ||
      route.tags.some((tag) => tag.toLowerCase().includes(needle)) ||
      (route.owner ?? '').toLowerCase().includes(needle)
    )
  })
})

// Client-side pagination over the already-fetched array: the library is one
// GET already paid for on every page load, and every server-side consumer
// of the same List() (CLI plan/push/validate, the API's own plan/push) needs
// the whole thing anyway, so slicing here is far cheaper than threading
// limit/offset all the way through that shared method.
const pageSize = 24
const page = ref(1)

// A new search result set (or a route disappearing off the current page,
// e.g. after a delete) can leave `page` pointing past the end — reset to a
// page that actually exists rather than showing an empty grid with visible
// results left unshown above it.
watch(visibleRoutes, () => {
  const maxPage = Math.max(1, Math.ceil(visibleRoutes.value.length / pageSize))
  if (page.value > maxPage) page.value = maxPage
})
watch([search, sportFilter], () => {
  page.value = 1
})

const pagedRoutes = computed(() => {
  const start = (page.value - 1) * pageSize
  return visibleRoutes.value.slice(start, start + pageSize)
})

// Which slugs the current page shows — used to v-show cards rather than
// v-for-slice them (see the template below) so a card that leaves the
// page is hidden, not destroyed. RouteCard/TrackPreview's own fetch is
// the expensive part to redo: a dense town-centre route's preview can run
// to tens of thousands of points, and paginating away and back used to
// tear the whole card down and rebuild it — re-fetching, re-parsing and
// re-painting all of it — for data that cannot have changed underneath
// it. api.track()/trackPreview() now memoize by slug too, but that alone
// doesn't save the DOM/SVG paint cost of a full remount; v-show does,
// since the component instance and its already-rendered DOM never go away.
const pagedSlugs = computed(() => new Set(pagedRoutes.value.map((r) => r.slug)))

// Map mode wants every filtered route plotted at once, not one page of 24 —
// pagination doesn't apply to it.
const viewMode = ref<'grid' | 'map'>('grid')

// Keyed by slug rather than refetched per filter change: a search keystroke
// or sport toggle only changes which routes are *visible*, not their
// geometry, so this only grows, never refetches something it already has.
const trackCache = ref(new Map<string, [number, number][]>())
const loadingTracks = ref(false)

async function loadTracksForMap() {
  const missing = routes.value.filter((r) => !trackCache.value.has(r.slug))
  if (!missing.length) return
  loadingTracks.value = true
  try {
    const { api } = await import('@/api/client')
    await Promise.all(
      missing.map(async (r) => {
        try {
          const track = await api.track(r.slug)
          trackCache.value.set(r.slug, track.points)
        } catch {
          trackCache.value.set(r.slug, [])
        }
      }),
    )
  } finally {
    loadingTracks.value = false
  }
}

watch(viewMode, (mode) => {
  if (mode === 'map') loadTracksForMap()
})

const mapRoutes = computed(() =>
  visibleRoutes.value.map((r) => ({ slug: r.slug, points: trackCache.value.get(r.slug) ?? [] })),
)

const selectedSlug = ref<string | null>(null)
const selectedRoute = computed(() => routes.value.find((r) => r.slug === selectedSlug.value) ?? null)

// A route can vanish out from under an open detail popup — someone else
// deletes it, or a refresh just drops it — without the click that opened
// it ever firing again to notice. Left alone, the modal stayed open
// showing an empty shell (every v-if="route" in RouteDetailModal hidden,
// an undefined title) instead of just closing.
watch(selectedRoute, (route) => {
  if (selectedSlug.value && !route) selectedSlug.value = null
})

// This page is always already showing the routes/plan being refreshed —
// every call below fires after an edit made right here, not on arriving at
// the page — so none of them should blank the grid back to its loading
// skeleton on the way to showing the same data updated.
function backgroundRefresh() {
  return refresh({ background: true })
}

// Upcoming crew rides, for the banner at the top of the page — fetched here
// rather than through useLibrary: this is the only page that shows it, and
// folding it into that shared composable would cost every other page a
// request for data it never displays. Lazy-imported the same way push()
// below pulls in api/client, rather than a top-level import, to keep this
// off the critical bundle for a page most visits never need it on.
const upcomingRides = ref<UpcomingRide[]>([])
onMounted(async () => {
  try {
    const { api } = await import('@/api/client')
    upcomingRides.value = await api.upcomingRides(todayISO())
  } catch {
    // Best-effort — the page works fine with no banner.
  }
})

async function push(items: { accountId: string; slug: string }[]) {
  pushing.value = true
  failures.value = []
  try {
    const { api } = await import('@/api/client')
    const result = await api.push(items)
    failures.value = result.failures
    await backgroundRefresh()
  } catch (err) {
    failures.value = [err instanceof Error ? err.message : String(err)]
  } finally {
    pushing.value = false
  }
}
</script>

<template>
  <div class="flex flex-col gap-6">
    <UAlert
      v-if="upcomingRides.length"
      color="primary"
      variant="subtle"
      icon="i-lucide-calendar-clock"
      :title="upcomingRides.length === 1 ? 'A crew ride is scheduled' : `${upcomingRides.length} crew rides are scheduled`"
    >
      <template #description>
        <ul class="flex flex-col gap-0.5">
          <li v-for="ride in upcomingRides" :key="ride.id">
            {{ ride.routeName }} with {{ ride.crewName }} — {{ formatRideWhen(ride) }}
          </li>
        </ul>
      </template>
    </UAlert>

    <UAlert
      v-for="problem in problems"
      :key="problem"
      color="warning"
      variant="subtle"
      icon="i-lucide-file-warning"
      :description="problem"
    />

    <PlanPanel
      :plan="plan"
      :accounts="accounts"
      :pushing="pushing"
      :failures="failures"
      :can-push="canPush"
      @push="push"
      @refresh="backgroundRefresh"
    />

    <!-- Cross-rider by nature (the same route can turn up uploaded by two
         different identities) — the same reason the endpoint behind this
         is admin-scoped rather than "my own routes". -->
    <LibraryDuplicatesPanel v-if="me?.role === 'admin'" @changed="backgroundRefresh" />

    <section>
      <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
        <h2 class="font-medium text-highlighted">
          {{ routes.length }} route{{ routes.length === 1 ? '' : 's' }}
        </h2>
        <div class="flex flex-wrap items-center gap-2">
          <UButtonGroup>
            <UButton
              icon="i-lucide-layout-grid"
              :color="viewMode === 'grid' ? 'primary' : 'neutral'"
              :variant="viewMode === 'grid' ? 'subtle' : 'outline'"
              aria-label="Grid view"
              @click="viewMode = 'grid'"
            />
            <UButton
              icon="i-lucide-map"
              :color="viewMode === 'map' ? 'primary' : 'neutral'"
              :variant="viewMode === 'map' ? 'subtle' : 'outline'"
              aria-label="Map view"
              @click="viewMode = 'map'"
            />
          </UButtonGroup>
          <USelect
            v-model="sportFilter"
            :items="sportFilterOptions"
            icon="i-lucide-filter"
            aria-label="Filter by sport"
            class="w-40"
          />
          <UInput
            v-model="search"
            icon="i-lucide-search"
            placeholder="Filter by name, slug, tag, description or uploader"
            class="w-full sm:w-72"
          />
        </div>
      </div>

      <div v-if="loading" class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <USkeleton v-for="n in 3" :key="n" class="h-72" />
      </div>

      <UEmpty
        v-else-if="!routes.length"
        icon="i-lucide-route"
        title="No routes yet"
        description="Add one from the Add route page, or import from Komoot."
      >
        <template #actions>
          <UButton to="/add" icon="i-lucide-plus">Add a route</UButton>
        </template>
      </UEmpty>

      <UEmpty
        v-else-if="!visibleRoutes.length"
        icon="i-lucide-search-x"
        title="Nothing matches"
        :description="
          search
            ? `No route matches “${search}”.`
            : `No ${sportFilter} routes.`
        "
      />

      <template v-else-if="viewMode === 'map'">
        <div class="relative h-[600px] overflow-hidden rounded-lg border border-default">
          <RouteMap
            :routes="mapRoutes"
            :selected-slug="selectedSlug"
            @select="selectedSlug = $event"
          />
          <div
            v-if="loadingTracks"
            class="absolute inset-x-0 top-0 flex justify-center bg-elevated/80 py-1 text-xs text-muted"
          >
            Loading route tracks…
          </div>
        </div>
      </template>

      <template v-else>
        <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <RouteCard
            v-for="route in visibleRoutes"
            v-show="pagedSlugs.has(route.slug)"
            :key="route.slug"
            :route="route"
            :accounts="accounts"
            :writable="canUpload"
            :me="me"
            @deleted="backgroundRefresh"
            @updated="backgroundRefresh"
            @open="selectedSlug = route.slug"
          />
        </div>

        <UPagination
          v-if="visibleRoutes.length > pageSize"
          v-model:page="page"
          :total="visibleRoutes.length"
          :items-per-page="pageSize"
          class="mt-6 justify-center"
        />
      </template>
    </section>

    <RouteDetailModal
      :open="selectedSlug !== null"
      :route="selectedRoute"
      :accounts="accounts"
      :writable="canUpload"
      :me="me"
      @update:open="(v: boolean) => { if (!v) selectedSlug = null }"
      @updated="backgroundRefresh"
    />
  </div>
</template>
