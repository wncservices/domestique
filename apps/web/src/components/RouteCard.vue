<script setup lang="ts">
import { computed, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { Account, Me, Route } from '@/api/types'
import ShareRouteDialog from './ShareRouteDialog.vue'
import SyncBadge from './SyncBadge.vue'
import TrackPreview from './TrackPreview.vue'

const props = defineProps<{
  route: Route
  accounts: Account[]
  writable: boolean
  me?: Me | null
}>()
const emit = defineEmits<{ deleted: []; updated: []; open: [] }>()

const toast = useToast()

const distance = computed(() => `${(props.route.distanceM / 1000).toFixed(1)} km`)
const ascent = computed(() => `${Math.round(props.route.ascentM)} m`)
const gpxUrl = computed(() => api.gpxUrl(props.route.slug))

// Provider imports tag a route with their own id ("komoot:12345",
// "garmin:502255241") so re-imports can be detected — see komootTag/garminTag
// server-side. Useful for dedup, meaningless as a badge: nobody reading the
// card needs to see the id, only that the route came from Komoot or Garmin,
// which the plain "komoot"/"garmin" tag alongside it already says.
const INTERNAL_TAG_PREFIXES = ['komoot:', 'garmin:']
const visibleTags = computed(() =>
  props.route.tags.filter((tag) => !INTERNAL_TAG_PREFIXES.some((prefix) => tag.startsWith(prefix))),
)

const confirming = ref(false)
const deleting = ref(false)
const sharing = ref(false)

// Cycling/running is a two-way toggle, not a picker — one click flips it,
// the same reasoning a checkbox gets over a dropdown for a binary choice.
const togglingSport = ref(false)

async function toggleSport() {
  const next = props.route.sport === 'cycling' ? 'running' : 'cycling'
  togglingSport.value = true
  try {
    await api.updateSport(props.route.slug, next)
    emit('updated')
  } catch (err) {
    toast.add({
      title: 'Could not change sport',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    togglingSport.value = false
  }
}

/**
 * Mirrors the server's rule: riders may only edit what they uploaded, admins
 * anything. Delete and target editing share the same rule server-side
 * (`mayEdit`) — this just avoids offering buttons that would come back 403.
 */
const canEdit = computed(() => {
  if (!props.writable) return false
  const me = props.me
  if (!me) return false
  if (me.permissions.includes('routes:edit-any')) return true
  if (!me.permissions.includes('routes:edit-own')) return false
  return !props.route.owner || props.route.owner.toLowerCase() === (me.user ?? '').toLowerCase()
})

const editingTargets = ref(false)
const draftTargets = ref<string[]>([])
const savingTargets = ref(false)

// Only crews the route's *owner* currently belongs to are legal targets —
// own devices are implicit and never need naming, and the server rejects
// anything else at write time. Not every account in the deployment: that
// was the picker before crews existed, and it is exactly what let a rider
// push straight onto a stranger's device with no consent.
const targetOptions = computed(() =>
  props.route.ownerCrews.map((c) => ({ label: c.name, value: c.id })),
)

function openTargets() {
  draftTargets.value = [...props.route.targets]
  editingTargets.value = true
}

async function saveTargets() {
  savingTargets.value = true
  try {
    await api.updateTargets(props.route.slug, draftTargets.value)
    toast.add({
      title: 'Targets updated',
      description: draftTargets.value.length
        ? `“${props.route.name}” will sync to ${draftTargets.value.length} account${draftTargets.value.length === 1 ? '' : 's'}.`
        : `“${props.route.name}” will not sync anywhere until targets are set again.`,
      icon: 'i-lucide-check',
      color: 'success',
    })
    editingTargets.value = false
    emit('updated')
  } catch (err) {
    toast.add({
      title: 'Could not update targets',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    savingTargets.value = false
  }
}

// An ownerless route (an import with no --owner, or an unclaimed Garmin
// sync-back) can never be shared to a crew — validateCrewTargets checks the
// owner's own crew membership, and there is no owner to check. Claiming is
// first-come: the server only refuses if someone else claimed it first
// between this card loading and the click.
const claiming = ref(false)

async function claim() {
  claiming.value = true
  try {
    await api.claimRoute(props.route.slug)
    toast.add({
      title: 'Route claimed',
      description: `“${props.route.name}” is now yours — you can share it to your crews.`,
      icon: 'i-lucide-check',
      color: 'success',
    })
    emit('updated')
  } catch (err) {
    toast.add({
      title: 'Could not claim this route',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    claiming.value = false
  }
}

async function remove() {
  deleting.value = true
  try {
    await api.remove(props.route.slug)
    confirming.value = false
    toast.add({
      title: 'Route deleted',
      description: `“${props.route.name}” will be removed from the devices on the next push.`,
      icon: 'i-lucide-trash-2',
      color: 'success',
    })
    emit('deleted')
  } catch (err) {
    toast.add({
      title: 'Could not delete the route',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <UCard
    variant="outline"
    class="app-card-interactive flex cursor-pointer flex-col focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary"
    :ui="{ body: 'flex-1 flex flex-col gap-3' }"
    tabindex="0"
    role="button"
    :aria-label="`View details for ${route.name}`"
    @click="emit('open')"
    @keydown.enter="emit('open')"
    @keydown.space.prevent="emit('open')"
  >
    <!-- Everything below that's already its own control (badges, buttons)
         stops propagation so clicking it doesn't also open the detail
         popup — only the card's otherwise-inert space does that. A card
         acting as role="button" while containing real buttons is not
         textbook ARIA (interactive controls nested inside one another),
         but it's the same pragmatic shape plenty of production card UIs
         use, and the alternative — a separate, always-visible "View
         details" affordance — changes the design this was built around
         more than the accessibility gap it closes justifies. -->
    <TrackPreview :slug="route.slug" />

    <!-- Every text block below is clamped to a fixed number of lines *and*
         given that many lines' worth of height (h-[Nlh], not just
         line-clamp alone — line-clamp only caps overflow, it doesn't pad
         shorter text back up), so distance/ascent and the tags below
         always start at the same y no matter how long the name or
         description happen to be — a one-line name no longer sits closer
         to the stats than a three-line one. The description is no longer
         conditionally rendered for the same reason: a route with no
         description needs to reserve the same space as one that has a
         short one, not collapse it away. The slug isn't shown here at all
         — nobody reading this grid needs the permanent URL id, only the
         name and (when there is one) the description right under it. -->
    <div>
      <h3 class="line-clamp-2 h-[2lh] font-medium text-highlighted">{{ route.name }}</h3>
      <p class="line-clamp-2 h-[2lh] text-sm text-toned">{{ route.description }}</p>
    </div>

    <dl class="flex gap-5">
      <div>
        <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Distance</dt>
        <dd class="font-mono tabular-nums">{{ distance }}</dd>
      </div>
      <div>
        <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Ascent</dt>
        <dd class="font-mono tabular-nums">{{ ascent }}</dd>
      </div>
    </dl>

    <div class="flex flex-wrap gap-1.5">
      <UTooltip v-if="canEdit" text="Click to switch between cycling and running">
        <UBadge
          as="button"
          type="button"
          :color="route.sport === 'running' ? 'neutral' : 'primary'"
          variant="subtle"
          size="sm"
          :icon="route.sport === 'running' ? 'i-lucide-footprints' : 'i-lucide-bike'"
          class="cursor-pointer"
          :class="{ 'opacity-50': togglingSport }"
          :disabled="togglingSport"
          @click.stop="toggleSport"
        >
          {{ route.sport }}
        </UBadge>
      </UTooltip>
      <UBadge
        v-else
        :color="route.sport === 'running' ? 'neutral' : 'primary'"
        variant="subtle"
        size="sm"
        :icon="route.sport === 'running' ? 'i-lucide-footprints' : 'i-lucide-bike'"
      >
        {{ route.sport }}
      </UBadge>
      <UBadge v-for="tag in visibleTags" :key="tag" color="neutral" variant="soft" size="sm">
        {{ tag }}
      </UBadge>
    </div>

    <UAlert
      v-if="route.unknownTargets.length"
      color="warning"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      :title="`Unknown target${route.unknownTargets.length === 1 ? '' : 's'}`"
      :description="`${route.unknownTargets.join(', ')} — this route will never sync there.`"
      :ui="{ title: 'text-sm', description: 'text-xs' }"
    />

    <!-- flex-col, not one wrapping row: the badge below and the
         owner/action-icon group used to share a single flex-wrap row split
         by a flex-1 spacer, which worked while everything fit on one line
         but broke the moment it didn't — "not on your devices" is long
         enough to force a wrap on an ordinary card width, and a bare
         flex-1 spacer has nothing to push once its own line is empty, so
         the icons landed flush-left on their own line instead of flush-
         right, and the card's footer grew a line taller than its
         neighbours'. Two explicit rows makes the height predictable
         regardless of which badge is showing. -->
    <div class="mt-auto flex flex-col gap-1.5 pt-1">
      <div class="flex flex-wrap items-center gap-1.5">
        <SyncBadge v-if="route.syncState.length" :statuses="route.syncState" :accounts="accounts" />
        <!-- syncState only ever shows the viewer's own devices now, so an
             empty list has two genuinely different meanings: the route truly
             reaches nobody, or it reaches real crew members' devices, just
             not this viewer's own. route.targets (the crew names it was
             shared to, not the resolved reach) is what tells the two apart —
             claiming "hasn't been shared to a crew" when it plainly has
             would be flatly wrong. -->
        <UTooltip
          v-if="!route.syncState.length && !route.targets.length"
          text="This route doesn't reach any device right now — it hasn't been shared to a crew, or the crew it's shared to has no members with a linked account yet."
        >
          <UBadge color="neutral" variant="outline" size="sm" class="cursor-help">
            no targets
          </UBadge>
        </UTooltip>
        <UTooltip
          v-if="!route.syncState.length && route.targets.length"
          text="Shared to a crew, but not reaching any device of your own — other members' devices may still get it."
        >
          <UBadge color="neutral" variant="outline" size="sm" class="cursor-help">
            not on your devices
          </UBadge>
        </UTooltip>
      </div>

      <div class="flex flex-wrap items-center justify-end gap-1.5">
        <UTooltip v-if="route.owner" :text="`Uploaded by ${route.owner}`">
          <UBadge color="neutral" variant="ghost" size="sm" icon="i-lucide-user">
            {{ route.owner }}
          </UBadge>
        </UTooltip>
        <!-- An import with no --owner, or an unclaimed Garmin sync-back:
             nothing else on this card works until someone claims it, since
             target-picking and crew-sharing both key off the owner's own
             crew membership. First-come, so any edit-own rider gets the
             button, not only an admin. -->
        <UTooltip
          v-else-if="canEdit"
          text="This route has no owner yet, so it can't reach any device or be shared to a crew. Claim it to fix that."
        >
          <UButton
            size="xs"
            color="warning"
            variant="outline"
            icon="i-lucide-user-plus"
            :loading="claiming"
            @click.stop="claim"
          >
            Claim
          </UButton>
        </UTooltip>

        <!-- external: without it, UButton's Link treats a same-origin
             path like /api/gpx/... as an internal route and hands the click
             to vue-router (Nuxt UI's own isExternal check is hasProtocol,
             which a bare path never satisfies) — vue-router then does a
             client-side navigate instead of a real browser request, so
             `download` below never gets a chance to fire at all. -->
        <UButton
          :href="gpxUrl"
          external
          download
          icon="i-lucide-download"
          color="neutral"
          variant="ghost"
          size="xs"
          aria-label="Download GPX"
          @click.stop
        />
        <UTooltip
          v-if="canEdit && !targetOptions.length"
          :text="
            route.owner
              ? 'Join or create a crew first to share this route beyond your own devices'
              : 'Claim this route above first — targets are chosen from the owner\'s own crews'
          "
        >
          <UButton
            icon="i-lucide-watch"
            color="neutral"
            variant="ghost"
            size="xs"
            disabled
            aria-label="Choose target devices"
          />
        </UTooltip>
        <UButton
          v-else-if="canEdit"
          icon="i-lucide-watch"
          color="neutral"
          variant="ghost"
          size="xs"
          aria-label="Choose target devices"
          @click.stop="openTargets"
        />
        <!-- oidc only — mode: proxy blocks anonymous traffic before it ever
             reaches this app at all, so a share link's recipient (someone
             this deployment has never heard of) could never sign in to use
             one; mode: none has no anonymous state to grant a share to in
             the first place. See ShareRouteDialog.vue's own doc comment. -->
        <UButton
          v-if="canEdit && me?.authMode === 'oidc'"
          icon="i-lucide-share-2"
          color="neutral"
          variant="ghost"
          size="xs"
          aria-label="Share this route"
          @click.stop="sharing = true"
        />
        <UButton
          v-if="canEdit"
          icon="i-lucide-trash-2"
          color="error"
          variant="ghost"
          size="xs"
          aria-label="Delete route"
          @click.stop="confirming = true"
        />
      </div>
    </div>

    <ShareRouteDialog v-if="canEdit" v-model:open="sharing" :route="route" />

    <UModal v-model:open="editingTargets" title="Choose target devices">
      <template #body>
        <div class="flex flex-col gap-3">
          <p class="text-sm text-toned">
            Which head units should “{{ route.name }}” sync to?
          </p>
          <!-- Defensive, not the normal path: the trigger button is already
               disabled once targetOptions is empty, so this only fires if a
               crew is removed out from under an already-open modal. -->
          <p v-if="!targetOptions.length" class="text-xs text-dimmed">
            No crews left to share with — close this and join or create one first.
          </p>
          <UCheckboxGroup v-else v-model="draftTargets" :items="targetOptions" />
          <p v-if="targetOptions.length && !draftTargets.length" class="text-xs text-dimmed">
            Nothing selected — this route will not sync to any account until targets are set
            again.
          </p>
        </div>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton
            color="neutral"
            variant="ghost"
            :disabled="savingTargets"
            @click="editingTargets = false"
          >
            Cancel
          </UButton>
          <UButton :loading="savingTargets" @click="saveTargets">Save</UButton>
        </div>
      </template>
    </UModal>

    <UModal v-model:open="confirming" title="Delete this route?">
      <template #body>
        <p class="text-sm text-toned">
          “{{ route.name }}” will be removed from the library, and queued for removal from
          every device that currently holds it.
        </p>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" :disabled="deleting" @click="confirming = false">
            Cancel
          </UButton>
          <UButton color="error" :loading="deleting" @click="remove">Delete</UButton>
        </div>
      </template>
    </UModal>
  </UCard>
</template>
