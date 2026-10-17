<script setup lang="ts">
import { computed, ref } from 'vue'
import type { DropdownMenuItem } from '@nuxt/ui'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { Account, Me, Route } from '@/api/types'
import ScheduleRideModal from './ScheduleRideModal.vue'
import ShareRouteDialog from './ShareRouteDialog.vue'
import SyncBadge from './SyncBadge.vue'
import TrackPreview from './TrackPreview.vue'
import { describeError } from '@/utils/errorMessage'

const props = defineProps<{
  route: Route
  accounts: Account[]
  writable: boolean
  me?: Me | null
}>()
const emit = defineEmits<{ deleted: []; updated: []; open: [] }>()

const scheduling = ref(false)
const canSchedule = computed(
  () => props.route.sport === 'cycling' && !!props.me?.permissions.includes('training:manage'),
)

const toast = useToast()

const distance = computed(() => `${(props.route.distanceM / 1000).toLocaleString(undefined, { minimumFractionDigits: 1, maximumFractionDigits: 1 })} km`)
const ascent = computed(() => `${Math.round(props.route.ascentM).toLocaleString()} m`)
const gpxUrl = computed(() => api.gpxUrl(props.route.slug))

// Provider imports tag a route with their own id ("komoot:12345",
// "garmin:502255241") so re-imports can be detected — see komootTag/garminTag
// server-side. A route imported from a share link gets the same shape
// ("shared:original-slug" — see sharedImportTag server-side). Useful for
// dedup, meaningless as a badge: nobody reading the card needs to see the
// id, only where the route came from, which the sport badge and this
// card's other own fields already say.
const INTERNAL_TAG_PREFIXES = ['komoot:', 'garmin:', 'shared:']
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
      description: describeError(err),
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

// A real <a download> click, not an item `href`: the item's link would be
// handed to vue-router as a same-origin path (see the old button's note on
// `external`), and a router navigation never honours `download`.
function downloadGpx() {
  const a = document.createElement('a')
  a.href = gpxUrl.value
  a.download = ''
  document.body.appendChild(a)
  a.click()
  a.remove()
}

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
      description: describeError(err),
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
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    claiming.value = false
  }
}

// Every per-card action lives here, each with an icon and a label, behind one
// labelled trigger. Same conditions and role gates the separate icon buttons
// had; an action that is not offered is absent, one that is offered but
// blocked is disabled and says why in its description.
const actionItems = computed(() => {
  const groups: DropdownMenuItem[][] = [
    [{ label: 'Download GPX', icon: 'i-lucide-download', onSelect: downloadGpx }],
  ]
  const own: DropdownMenuItem[] = []
  if (canEdit.value) {
    own.push({
      label: props.route.sport === 'cycling' ? 'Switch to running' : 'Switch to cycling',
      icon: props.route.sport === 'cycling' ? 'i-lucide-footprints' : 'i-lucide-bike',
      disabled: togglingSport.value,
      onSelect: toggleSport,
    })
    if (!props.route.owner) {
      own.push({
        label: 'Claim route',
        icon: 'i-lucide-user-plus',
        description: 'No owner yet, so it cannot reach a device or a crew until claimed.',
        disabled: claiming.value,
        onSelect: claim,
      })
    }
    own.push(
      targetOptions.value.length
        ? { label: 'Choose target devices', icon: 'i-lucide-watch', onSelect: openTargets }
        : {
            label: 'Choose target devices',
            icon: 'i-lucide-watch',
            disabled: true,
            description: props.route.owner
              ? 'Join or create a crew first.'
              : 'Claim this route first.',
          },
    )
  }
  if (canSchedule.value) {
    own.push({ label: 'Schedule ride', icon: 'i-lucide-calendar-plus', onSelect: () => (scheduling.value = true) })
  }
  // oidc only: mode proxy blocks anonymous traffic before it reaches this app,
  // so a share link's recipient could never sign in; mode none has no
  // anonymous state to grant a share to. See ShareRouteDialog.vue.
  if (canEdit.value && props.me?.authMode === 'oidc') {
    own.push({ label: 'Share this route', icon: 'i-lucide-share-2', onSelect: () => (sharing.value = true) })
  }
  if (own.length) groups.push(own)
  if (canEdit.value) {
    groups.push([
      { label: 'Delete route', icon: 'i-lucide-trash-2', color: 'error', onSelect: () => (confirming.value = true) },
    ])
  }
  return groups
})

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
      description: describeError(err),
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
    <div class="flex items-start justify-between gap-2">
      <h3 class="line-clamp-2 h-[2lh] min-w-0 font-medium text-highlighted">{{ route.name }}</h3>
      <!-- Stops click and key events so using the menu does not also open
           the detail popup. -->
      <UDropdownMenu :items="actionItems" :content="{ align: 'end' }">
        <UButton
          icon="i-lucide-ellipsis"
          color="neutral"
          variant="ghost"
          size="xs"
          class="shrink-0"
          aria-label="Route actions"
          @click.stop
          @keydown.stop
        />
      </UDropdownMenu>
    </div>
    <p class="line-clamp-2 h-[2lh] text-sm text-toned">{{ route.description }}</p>

    <dl class="flex flex-wrap gap-x-5 gap-y-1 tabular-nums">
      <div class="min-w-0">
        <dt class="text-[length:var(--text-label)] text-muted">Distance</dt>
        <dd class="whitespace-nowrap">{{ distance }}</dd>
      </div>
      <div class="min-w-0">
        <dt class="text-[length:var(--text-label)] text-muted">Ascent</dt>
        <dd class="whitespace-nowrap">{{ ascent }}</dd>
      </div>
    </dl>

    <div class="flex flex-wrap gap-1.5">
      <UBadge
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

    <!-- A caption row, not tags: the sync badge is a real status signal, the
         other two are muted text with a dotted underline marking them as
         hoverable. -->
    <div class="mt-auto flex flex-wrap items-center gap-x-1.5 gap-y-1 pt-1 text-xs text-dimmed">
      <SyncBadge v-if="route.syncState.length" :statuses="route.syncState" :accounts="accounts" />
      <!-- syncState only ever shows the viewer's own devices, so an empty
           list has two meanings: the route reaches nobody, or it reaches
           crew members' devices but not this viewer's own. route.targets
           tells the two apart. -->
      <UTooltip
        v-if="!route.syncState.length && !route.targets.length"
        text="This route doesn't reach any device right now — it hasn't been shared to a crew, or the crew it's shared to has no members with a linked account yet."
      >
        <span class="cursor-help underline decoration-dotted decoration-dimmed/50 underline-offset-2">
          no targets
        </span>
      </UTooltip>
      <UTooltip
        v-if="!route.syncState.length && route.targets.length"
        text="Shared to a crew, but not reaching any device of your own — other members' devices may still get it."
      >
        <span class="cursor-help underline decoration-dotted decoration-dimmed/50 underline-offset-2">
          not on your devices
        </span>
      </UTooltip>

      <span v-if="route.owner" aria-hidden="true" class="text-dimmed/50">·</span>
      <UTooltip v-if="route.owner" :text="`Uploaded by ${route.owner}`">
        <span class="inline-flex items-center gap-1">
          <UIcon name="i-lucide-user" class="size-3.5" />
          {{ route.owner }}
        </span>
      </UTooltip>
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

    <ScheduleRideModal v-model:open="scheduling" :route="route" />

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
