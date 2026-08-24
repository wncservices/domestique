<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { Account, Me, Route } from '@/api/types'
import RouteMap from './RouteMap.vue'

const props = defineProps<{
  open: boolean
  route: Route | null
  accounts: Account[]
  writable: boolean
  me?: Me | null
}>()
const emit = defineEmits<{ 'update:open': [boolean]; updated: [] }>()
const toast = useToast()

// What the modal's own #description slot shows, right under the title —
// the route's description itself, not its slug: a rider has no use for the
// permanent URL id up there, and a route with none yet still says so
// rather than leaving the header blank.
const headerDescription = computed(() => {
  if (!props.route) return ''
  return props.route.description || 'No description yet.'
})

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

// Mirrors RouteCard.vue's own canEdit exactly — same server-side rule
// (`mayEdit`), just checked here too so this popup doesn't offer an Edit
// button that would come back 403.
const canEdit = computed(() => {
  if (!props.writable || !props.route) return false
  const me = props.me
  if (!me) return false
  if (me.permissions.includes('routes:edit-any')) return true
  if (!me.permissions.includes('routes:edit-own')) return false
  return !props.route.owner || props.route.owner.toLowerCase() === (me.user ?? '').toLowerCase()
})

const editingInfo = ref(false)
const draftName = ref('')
const draftDescription = ref('')
const savingInfo = ref(false)

function openEditInfo() {
  if (!props.route) return
  draftName.value = props.route.name
  draftDescription.value = props.route.description
  editingInfo.value = true
}

async function saveInfo() {
  if (!props.route || !draftName.value.trim()) return
  savingInfo.value = true
  try {
    await api.updateInfo(props.route.slug, draftName.value.trim(), draftDescription.value)
    editingInfo.value = false
    emit('updated')
  } catch (err) {
    toast.add({
      title: 'Could not update the route',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    savingInfo.value = false
  }
}

// Re-runs elevation backfill against the route's own already-stored GPX —
// see api.recalculateElevation's own doc comment. Client-side canEdit
// gating is the same UX nicety it always is here (the server is the real
// authority); whether this deployment has elevation lookup configured at
// all is not something the client knows ahead of time, so a deployment
// without it just sees the server's own 412 surfaced as a toast rather
// than the button being hidden.
const recalculating = ref(false)

async function recalculateElevation() {
  if (!props.route) return
  const before = props.route.ascentM
  recalculating.value = true
  try {
    const updated = await api.recalculateElevation(props.route.slug)
    toast.add(
      Math.round(updated.ascentM) === Math.round(before)
        ? { title: 'Ascent unchanged', description: 'This route already had real elevation.', icon: 'i-lucide-mountain', color: 'neutral' }
        : { title: 'Ascent recalculated', description: `${Math.round(before)} m → ${Math.round(updated.ascentM)} m`, icon: 'i-lucide-mountain', color: 'success' },
    )
    emit('updated')
  } catch (err) {
    toast.add({
      title: 'Could not recalculate elevation',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    recalculating.value = false
  }
}

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
    editingInfo.value = false
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
    :description="headerDescription"
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

        <div v-if="!editingInfo && canEdit" class="flex justify-end">
          <UButton
            icon="i-lucide-pencil"
            color="neutral"
            variant="ghost"
            size="xs"
            aria-label="Edit name and description"
            @click="openEditInfo"
          >
            Edit
          </UButton>
        </div>

        <form v-else-if="editingInfo" class="flex flex-col gap-3 rounded-lg bg-elevated/60 p-3" @submit.prevent="saveInfo">
          <UFormField label="Name">
            <UInput v-model="draftName" class="w-full" />
          </UFormField>
          <UFormField label="Description">
            <UTextarea v-model="draftDescription" class="w-full" :rows="3" />
          </UFormField>
          <div class="flex justify-end gap-2">
            <UButton color="neutral" variant="ghost" :disabled="savingInfo" @click="editingInfo = false">
              Cancel
            </UButton>
            <UButton type="submit" :loading="savingInfo" :disabled="!draftName.trim()">Save</UButton>
          </div>
        </form>

        <dl class="flex flex-wrap gap-5">
          <div>
            <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Distance</dt>
            <dd class="tabular-nums">{{ distance }}</dd>
          </div>
          <div>
            <dt class="flex items-center gap-1 text-[0.7rem] uppercase tracking-wide text-dimmed">
              Ascent
              <UTooltip v-if="canEdit" text="Recalculate from terrain data — fixes a route uploaded with no usable elevation of its own">
                <UButton
                  icon="i-lucide-refresh-cw"
                  color="neutral"
                  variant="ghost"
                  size="xs"
                  :ui="{ base: 'p-0.5' }"
                  :loading="recalculating"
                  aria-label="Recalculate elevation"
                  @click="recalculateElevation"
                />
              </UTooltip>
            </dt>
            <dd class="tabular-nums">{{ ascent }}</dd>
          </div>
          <div>
            <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Points</dt>
            <dd class="tabular-nums">{{ route.pointCount }}</dd>
          </div>
          <div>
            <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Updated</dt>
            <dd>{{ new Date(route.updatedAt).toLocaleDateString() }}</dd>
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
              <span v-if="row.updatedAt" class="text-dimmed">(last push {{ new Date(row.updatedAt).toLocaleDateString() }})</span>
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
