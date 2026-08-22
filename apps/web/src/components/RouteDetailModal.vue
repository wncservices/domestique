<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import type { Account, Route } from '@/api/types'
import RouteMap from './RouteMap.vue'

const props = defineProps<{
  open: boolean
  route: Route | null
  accounts: Account[]
}>()
const emit = defineEmits<{ 'update:open': [boolean] }>()

const distance = computed(() => (props.route ? `${(props.route.distanceM / 1000).toFixed(1)} km` : ''))
const ascent = computed(() => (props.route ? `${Math.round(props.route.ascentM)} m` : ''))
const gpxUrl = computed(() => (props.route ? api.gpxUrl(props.route.slug) : ''))

const INTERNAL_TAG_PREFIXES = ['komoot:', 'garmin:']
const visibleTags = computed(() =>
  (props.route?.tags ?? []).filter(
    (tag) => !INTERNAL_TAG_PREFIXES.some((prefix) => tag.startsWith(prefix)),
  ),
)

// route.targets holds crew ids; ownerCrews is the owner's own crews with
// names attached — the only place a name for one of those ids is available
// without a separate lookup. A target whose crew no longer resolves there
// falls back to the raw id, same as unknownTargets does elsewhere.
const targetNames = computed(() => {
  if (!props.route) return []
  const byId = new Map(props.route.ownerCrews.map((c) => [c.id, c.name]))
  return props.route.targets.map((id) => byId.get(id) ?? id)
})

function accountLabel(accountId: string): string {
  return props.accounts.find((a) => a.id === accountId)?.label || accountId
}

function statusVerb(status: string): string {
  if (status === 'synced') return 'synced'
  if (status === 'stale') return 'pending changes'
  return 'not pushed yet'
}

// Full breakdown, not the worst-status-wins summary SyncBadge shows on the
// card — the point of this popup is to answer "which of my crew's devices
// actually has this," which the card's own badge deliberately compresses
// away.
const syncRows = computed(() =>
  (props.route?.syncState ?? []).map((s) => ({
    label: accountLabel(s.accountId),
    verb: statusVerb(s.status),
    updatedAt: s.updatedAt,
  })),
)

// RouteMap fetches nothing itself — it only draws whatever points it's
// given — so this owns the one fetch a single-route view needs. UModal
// doesn't render its #body slot content until first opened, so this only
// ever fires once there's actually something to show, the same laziness
// TrackPreview gets from its own IntersectionObserver for the card grid.
const points = ref<[number, number][]>([])
const loadingTrack = ref(false)
// Same failure signal TrackPreview.vue already shows ("track unavailable")
// for the identical fetch on the card grid — this popup was silently
// rendering an empty map on a failed fetch instead, which looked like
// nothing happened rather than like an error.
const trackFailed = ref(false)

watch(
  () => (props.open ? props.route?.slug : null),
  async (slug) => {
    points.value = []
    trackFailed.value = false
    if (!slug) return
    loadingTrack.value = true
    try {
      const track = await api.track(slug)
      points.value = track.points
    } catch {
      points.value = []
      trackFailed.value = true
    } finally {
      loadingTrack.value = false
    }
  },
)

const mapRoutes = computed(() =>
  props.route ? [{ slug: props.route.slug, points: points.value }] : [],
)
</script>

<template>
  <UModal
    :open="open"
    :title="route?.name"
    :description="route?.slug"
    :ui="{ content: 'sm:max-w-2xl' }"
    @update:open="(v: boolean) => emit('update:open', v)"
  >
    <template #body>
      <div v-if="route" class="flex flex-col gap-4">
        <div class="h-64 overflow-hidden rounded-lg bg-elevated/50 sm:h-80">
          <USkeleton v-if="loadingTrack" class="size-full" />
          <div v-else-if="trackFailed" class="grid size-full place-items-center">
            <p class="text-sm text-muted">track unavailable</p>
          </div>
          <RouteMap v-else :routes="mapRoutes" />
        </div>

        <p v-if="route.description" class="text-sm text-toned">{{ route.description }}</p>

        <dl class="flex flex-wrap gap-5">
          <div>
            <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Distance</dt>
            <dd class="tabular-nums">{{ distance }}</dd>
          </div>
          <div>
            <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Ascent</dt>
            <dd class="tabular-nums">{{ ascent }}</dd>
          </div>
          <div>
            <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Points</dt>
            <dd class="tabular-nums">{{ route.pointCount }}</dd>
          </div>
          <div>
            <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Updated</dt>
            <dd>{{ route.updatedAt }}</dd>
          </div>
        </dl>

        <div class="flex flex-wrap gap-1.5">
          <UBadge
            :color="route.sport === 'running' ? 'warning' : 'primary'"
            variant="subtle"
            size="sm"
            :icon="route.sport === 'running' ? 'i-lucide-footprints' : 'i-lucide-bike'"
          >
            {{ route.sport }}
          </UBadge>
          <UBadge v-for="tag in visibleTags" :key="tag" color="neutral" variant="soft" size="sm">
            {{ tag }}
          </UBadge>
          <UBadge v-if="route.owner" color="neutral" variant="ghost" size="sm" icon="i-lucide-user">
            {{ route.owner }}
          </UBadge>
        </div>

        <div v-if="targetNames.length">
          <h4 class="mb-1 text-[0.7rem] uppercase tracking-wide text-dimmed">Shared to</h4>
          <div class="flex flex-wrap gap-1.5">
            <UBadge v-for="name in targetNames" :key="name" color="neutral" variant="soft" size="sm">
              {{ name }}
            </UBadge>
          </div>
        </div>

        <div v-if="syncRows.length">
          <h4 class="mb-1 text-[0.7rem] uppercase tracking-wide text-dimmed">Sync status</h4>
          <ul class="flex flex-col gap-1 text-sm text-toned">
            <li v-for="row in syncRows" :key="row.label">
              <span class="font-medium">{{ row.label }}</span>: {{ row.verb }}
              <span v-if="row.updatedAt" class="text-dimmed">(last push {{ row.updatedAt }})</span>
            </li>
          </ul>
        </div>
      </div>
    </template>
    <template #footer>
      <div class="flex w-full justify-between gap-2">
        <!-- external: see RouteCard.vue's identical button for why —
             a same-origin path like /api/gpx/... otherwise reads as an
             internal route to vue-router, which intercepts the click
             before `download` ever gets a chance to fire. -->
        <UButton
          v-if="route"
          :href="gpxUrl"
          external
          download
          icon="i-lucide-download"
          color="neutral"
          variant="subtle"
        >
          Download GPX
        </UButton>
        <UButton color="neutral" variant="ghost" @click="emit('update:open', false)">Close</UButton>
      </div>
    </template>
  </UModal>
</template>
