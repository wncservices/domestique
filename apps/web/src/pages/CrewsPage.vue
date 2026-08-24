<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { Crew, Person, Ride, UpcomingRide } from '@/api/types'
import { useLibrary } from '@/composables/useLibrary'
import { usePagedList } from '@/composables/usePagedList'
import { formatRideWhen, rideDay, rideMonth, todayISO } from '@/utils/rideDates'
import TrackPreview from '@/components/TrackPreview.vue'

const { crews, accounts, routes, me, loading, error, refresh, can } = useLibrary()
const toast = useToast()

// The page's own list is only crews the rider is actually in — an owner's
// or a plain member's, both "approved" — everything else (browsing for a
// new one, a pending request) moved into the Search popup below. Splitting
// it this way is the whole point of this restructure: a rider in a few
// crews used to scroll past every *other* crew's roster and requests just
// to find their own.
const myCrews = computed(() => crews.value.filter((c) => c.membershipStatus === 'approved'))
const otherCrews = computed(() => crews.value.filter((c) => c.membershipStatus !== 'approved'))

const { page: crewsPage, paged: pagedCrews, pageSize: crewsPageSize } = usePagedList(myCrews, 24)

// People-page data, fetched separately (not part of useLibrary's own
// state) purely to widen knownRiders below — every other use of this page
// only needs crews/accounts/routes. Silently empty for a caller without
// people:manage rather than surfacing its own error: the picker just falls
// back to what it could already see without this.
const people = ref<Person[]>([])
onMounted(async () => {
  if (!can('people:manage')) return
  try {
    people.value = await api.people()
  } catch {
    // best-effort only — see comment above
  }
})

// Every rider identifier the app already knows about, from data already
// fetched for this page — a linked account's owner, a route's owner, a
// crew's owner, or (for an admin who can see the People page) a likely
// guess at someone who has been granted access but has not yet uploaded,
// linked, or created anything of their own. That last source is the one
// that actually matters for a just-invited rider: found live, an admin
// could see a newly signed-in person on the People page but had no way to
// find them here to add to a crew — every other source needs the person to
// have already done something besides sign in.
const knownRiders = computed(() => {
  const set = new Set<string>()
  for (const a of accounts.value) if (a.rider) set.add(a.rider)
  for (const r of routes.value) if (r.owner) set.add(r.owner)
  for (const c of crews.value) if (c.owner) set.add(c.owner)
  for (const p of people.value) if (p.likelyRider) set.add(p.likelyRider)
  return [...set].sort((a, b) => a.localeCompare(b))
})

// Riders worth suggesting for this particular crew: already-approved
// members are pointless to re-suggest (adding one is a 409), but a rider
// with a pending request stays in the list on purpose. Picking one who
// already self-requested approves them in the same step, cheaper than
// approve-from-the-pending-row-below; picking one already invited is a
// harmless no-op — still awaiting their own confirmation either way.
function suggestedRiders(crew: Crew): string[] {
  const approved = new Set(
    (crew.members ?? []).filter((m) => m.status === 'approved').map((m) => m.rider),
  )
  return knownRiders.value.filter((rider) => !approved.has(rider))
}

// The one or two letters shown in a member's little avatar circle — a
// rider is only ever a username here (see App.vue's own note on what
// "rider" means), never a display name with a first/last split to draw on,
// so the first couple of characters is the whole of what's available.
function initials(rider: string): string {
  return rider.slice(0, 2).toUpperCase()
}

// Every upcoming ride across every crew this rider is in, soonest first per
// crew (the API already sorts by date then time) — drives both the "Next
// ride" line on each row of the main list below and, on the Library page,
// the banner at the top of the page. Fetched here rather than folded into
// useLibrary's own state: this is the only page that needs it today, and
// adding it to every useLibrary consumer's own fetch would cost every other
// page a request for data it never shows.
const upcomingRides = ref<UpcomingRide[]>([])
async function loadUpcomingRides() {
  try {
    upcomingRides.value = await api.upcomingRides(todayISO())
  } catch {
    // Best-effort — the main list still works fine with no "Next ride" line.
    upcomingRides.value = []
  }
}

// upcomingRides is already sorted soonest-first, so the first match for a
// crew id is that crew's next ride.
function nextRideFor(crewId: string): UpcomingRide | null {
  return upcomingRides.value.find((r) => r.crewId === crewId) ?? null
}

onMounted(refresh)
onMounted(loadUpcomingRides)

// Every call below fires after an edit made right here, on a page already
// showing the crew list being refreshed — never on arriving at the page,
// that's onMounted(refresh) above — so none of them should blank the list
// back to its loading skeleton on the way to showing the same thing
// updated. See useLibrary.ts's own doc comment on refresh().
function backgroundRefresh() {
  loadUpcomingRides()
  return refresh({ background: true })
}

// --- create ---

const createOpen = ref(false)
const createName = ref('')
const creating = ref(false)
const createError = ref('')

async function createCrew() {
  if (!createName.value.trim()) return
  creating.value = true
  createError.value = ''
  try {
    await api.createCrew({ name: createName.value.trim() })
    toast.add({ title: `Created ${createName.value.trim()}`, icon: 'i-lucide-users-round', color: 'success' })
    createOpen.value = false
    createName.value = ''
    await backgroundRefresh()
  } catch (err) {
    createError.value = err instanceof Error ? err.message : String(err)
  } finally {
    creating.value = false
  }
}

// --- search / discover ---

const searchOpen = ref(false)
const searchQuery = ref('')
const visibleOtherCrews = computed(() => {
  const needle = searchQuery.value.trim().toLowerCase()
  if (!needle) return otherCrews.value
  return otherCrews.value.filter((c) => c.name.toLowerCase().includes(needle))
})

function membershipLabel(crew: Crew): string {
  switch (crew.membershipStatus) {
    case 'approved':
      return 'Member'
    case 'pending':
      return 'Pending'
    default:
      return ''
  }
}

// --- join / leave / accept / decline ---

const joining = ref('')

async function join(crew: Crew) {
  joining.value = crew.id
  try {
    await api.joinCrew(crew.id)
    toast.add({ title: `Requested to join ${crew.name}`, icon: 'i-lucide-hand', color: 'success' })
    await backgroundRefresh()
  } catch (err) {
    toast.add({
      title: 'Could not request to join',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    joining.value = ''
  }
}

const removing = ref('')

async function removeMember(crew: Crew, rider: string) {
  removing.value = `${crew.id}:${rider}`
  try {
    await api.removeCrewMember(crew.id, rider)
    await backgroundRefresh()
  } catch (err) {
    toast.add({
      title: `Could not remove ${rider}`,
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    removing.value = ''
  }
}

// --- add member directly (the owner's other way in, no request needed) ---

const addMemberInput = ref<Record<string, string>>({})
const addingMember = ref('')

async function addMember(crew: Crew) {
  const rider = (addMemberInput.value[crew.id] ?? '').trim()
  if (!rider) return
  addingMember.value = crew.id
  try {
    const updated = await api.addCrewMember(crew.id, rider)
    // A fresh invite lands pending — the rider still has to confirm it. A
    // rider who already had a request in gets approved in the same step.
    // Distinguish the two so the toast doesn't claim membership that isn't
    // there yet.
    const status = updated.members?.find((m) => m.rider.toLowerCase() === rider.toLowerCase())?.status
    toast.add(
      status === 'pending'
        ? { title: `Invited ${rider} to ${crew.name}`, description: 'Waiting for them to confirm.', icon: 'i-lucide-user-plus', color: 'success' }
        : { title: `${rider} added to ${crew.name}`, icon: 'i-lucide-user-plus', color: 'success' },
    )
    addMemberInput.value[crew.id] = ''
    await backgroundRefresh()
  } catch (err) {
    toast.add({
      title: `Could not add ${rider}`,
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    addingMember.value = ''
  }
}

const approving = ref('')

async function approveMember(crew: Crew, rider: string) {
  approving.value = `${crew.id}:${rider}`
  try {
    await api.approveCrewMember(crew.id, rider)
    toast.add({ title: `${rider} approved`, icon: 'i-lucide-check', color: 'success' })
    await backgroundRefresh()
  } catch (err) {
    toast.add({
      title: `Could not approve ${rider}`,
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    approving.value = ''
  }
}

// --- delete ---

const deleteTarget = ref<Crew | null>(null)
const deleting = ref(false)

// Same reasoning as openShare: closes the detail popup rather than
// stacking a second UModal on top of it.
function openDelete(crew: Crew) {
  deleteTarget.value = crew
  detailTarget.value = null
}

async function deleteCrew() {
  if (!deleteTarget.value) return
  deleting.value = true
  try {
    await api.deleteCrew(deleteTarget.value.id)
    toast.add({ title: `Deleted ${deleteTarget.value.name}`, icon: 'i-lucide-trash-2', color: 'success' })
    deleteTarget.value = null
    detailTarget.value = null
    await backgroundRefresh()
  } catch (err) {
    toast.add({
      title: 'Could not delete the crew',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    deleting.value = false
  }
}

const pendingFor = (crew: Crew) => crew.members?.filter((m) => m.status === 'pending') ?? []
const approvedFor = (crew: Crew) => crew.members?.filter((m) => m.status === 'approved') ?? []

// --- crew detail popup: roster (read-only for a plain member, full
// management for the owner/admin) plus scheduled rides. Opened by clicking
// any of the rider's own crews — everything that used to be an
// always-expanded block per crew on the main list now lives behind one
// popup per crew instead. ---

const detailTarget = ref<Crew | null>(null)
// Keeps the modal showing the crew it opened for even after `crews` is
// replaced wholesale by the next refresh() — matching by id, not identity.
const detailCrew = computed(() => crews.value.find((c) => c.id === detailTarget.value?.id) ?? null)

const rides = ref<Ride[]>([])
const ridesLoading = ref(false)

async function loadRides(crewId: string) {
  ridesLoading.value = true
  try {
    rides.value = await api.crewRides(crewId)
  } catch (err) {
    toast.add({
      title: 'Could not load scheduled rides',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
    rides.value = []
  } finally {
    ridesLoading.value = false
  }
}

function openDetail(crew: Crew) {
  detailTarget.value = crew
  rides.value = []
  loadRides(crew.id)
}

// --- auto-share (owner/admin only: changes what the crew does for every
// member's future uploads, not just the caller's own membership) ---

const togglingAutoShare = ref('')

async function toggleAutoShare(crew: Crew, autoShare: boolean) {
  togglingAutoShare.value = crew.id
  try {
    await api.setCrewAutoShare(crew.id, autoShare)
    toast.add({
      title: autoShare ? `New uploads will default to ${crew.name}` : `Auto-share turned off for ${crew.name}`,
      icon: 'i-lucide-share-2',
      color: 'success',
    })
    await backgroundRefresh()
  } catch (err) {
    toast.add({
      title: 'Could not change auto-share',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    togglingAutoShare.value = ''
  }
}

// --- per-member scheduling grant (owner/admin only) ---

const togglingCanSchedule = ref('')

async function toggleCanSchedule(crew: Crew, rider: string, value: boolean) {
  togglingCanSchedule.value = `${crew.id}:${rider}`
  try {
    await api.setCanScheduleCrewMember(crew.id, rider, value)
    await backgroundRefresh()
  } catch (err) {
    toast.add({
      title: `Could not change ${rider}'s scheduling permission`,
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    togglingCanSchedule.value = ''
  }
}

// --- scheduled rides ---

// todayISO/rideMonth/rideDay: see utils/rideDates.ts — shared with
// LibraryPage.vue and with the "from" cutoff loadUpcomingRides sends above,
// rather than three near-identical copies of the same date-only string
// slicing drifting apart one at a time.
const scheduleDate = ref(todayISO())
// Empty by default — a ride names a day at minimum, same as before this
// field existed; a native <input type="time"> already produces exactly the
// HH:MM the API expects, so nothing here needs to format or parse it.
const scheduleTime = ref('')
const timePresets = [
  { label: 'Morning · 09:00', value: '09:00' },
  { label: 'Afternoon · 14:00', value: '14:00' },
  { label: 'Evening · 18:00', value: '18:00' },
]

const scheduleSlug = ref('')
const scheduling = ref(false)

// Offered routes: whatever is already shared to this crew (works for any
// scheduling-authorized member) plus the caller's own other routes (will
// be auto-shared to the crew the moment they're scheduled — see the
// server's own handleCreateRide doc comment; that auto-share only succeeds
// because the caller already owns these). A member holding only the
// schedule grant, not route-edit rights, would just get a 400 from the
// second group — deduped out here isn't worth the complexity, since myRoutes
// is empty for most crew members anyway and the server's own error is clear.
const myRoutes = computed(() =>
  routes.value.filter(
    (r) =>
      !!r.owner &&
      (can('routes:edit-any') ||
        (can('routes:edit-own') && r.owner.toLowerCase() === (me.value?.user ?? '').toLowerCase())),
  ),
)
const scheduleRouteOptions = computed(() => {
  const crewId = detailCrew.value?.id
  const alreadyShared = routes.value.filter((r) => crewId && r.targets.includes(crewId))
  const seen = new Set(alreadyShared.map((r) => r.slug))
  const own = myRoutes.value.filter((r) => !seen.has(r.slug))
  return [
    ...alreadyShared.map((r) => ({ label: r.name, value: r.slug })),
    ...own.map((r) => ({ label: `${r.name} (not shared here yet)`, value: r.slug })),
  ]
})

// The picked route's own record — same routes.value the options above were
// built from, so this is a lookup, not a second fetch. Drives the preview
// (TrackPreview, already used on the Library page) and the stats line
// right under the picker, so scheduling a ride for the wrong route is
// something a glance catches before Schedule is even clicked.
const scheduleSelectedRoute = computed(() => routes.value.find((r) => r.slug === scheduleSlug.value) ?? null)

async function scheduleRide() {
  const crew = detailCrew.value
  if (!crew || !scheduleSlug.value) return
  scheduling.value = true
  try {
    await api.scheduleRide(crew.id, {
      slug: scheduleSlug.value,
      date: scheduleDate.value,
      time: scheduleTime.value || undefined,
    })
    toast.add({ title: 'Ride scheduled', icon: 'i-lucide-calendar-plus', color: 'success' })
    scheduleSlug.value = ''
    scheduleTime.value = ''
    await Promise.all([loadRides(crew.id), backgroundRefresh()])
  } catch (err) {
    toast.add({
      title: 'Could not schedule that ride',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    scheduling.value = false
  }
}

const deletingRide = ref('')

async function deleteRide(ride: Ride) {
  const crew = detailCrew.value
  if (!crew) return
  deletingRide.value = ride.id
  try {
    await api.deleteRide(crew.id, ride.id)
    await Promise.all([loadRides(crew.id), loadUpcomingRides()])
  } catch (err) {
    toast.add({
      title: 'Could not remove that ride',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    deletingRide.value = ''
  }
}

// "Sync now": pushes a scheduled ride's route to every current approved
// member's linked accounts right away, rather than waiting on the next
// automatic push. Its own dedicated endpoint, not api.push — general
// push can no longer reach anyone but the caller's own accounts (a
// deliberate security fix: crew membership is consent to see a fellow
// member's shared routes and sync one to your own device yourself, not
// consent for literally anyone else's push to silently write to your
// device on your behalf). Sync now is the one place that still crosses
// riders, and only because a rider is knowingly asking for exactly this
// one ride, right now — see the API's own config.PushTargetsFor doc
// comment for the fuller reasoning.
const syncingRide = ref('')

async function syncRide(ride: Ride) {
  const crew = detailCrew.value
  if (!crew) return
  // Purely a friendlier message than "synced" for a crew that plainly has
  // nowhere to sync to yet — the actual push (and its own, real
  // authorization/membership check) happens server-side either way.
  const roster = new Set((crew.roster ?? []).map((r) => r.toLowerCase()))
  if (!accounts.value.some((a) => roster.has(a.rider.toLowerCase()))) {
    toast.add({
      title: 'Nobody in this crew has a linked device yet',
      icon: 'i-lucide-info',
      color: 'warning',
    })
    return
  }
  syncingRide.value = ride.id
  try {
    const result = await api.syncRide(crew.id, ride.id)
    if (result.failures.length) {
      toast.add({
        title: `Synced with ${result.failures.length} failure${result.failures.length === 1 ? '' : 's'}`,
        description: result.failures.join(', '),
        icon: 'i-lucide-triangle-alert',
        color: 'warning',
      })
    } else {
      toast.add({ title: `Synced ${ride.routeName} to the crew's devices`, icon: 'i-lucide-refresh-cw', color: 'success' })
    }
  } catch (err) {
    toast.add({
      title: 'Sync failed',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    syncingRide.value = ''
  }
}

// --- share your own routes to a crew you belong to (existing routes;
// auto-share above only ever affects uploads made after it's turned on) ---

const shareTarget = ref<Crew | null>(null)
const shareSelections = ref<string[]>([])
const sharing = ref(false)

const shareOptions = computed(() => myRoutes.value.map((r) => ({ label: r.name, value: r.slug })))

// Ownerless routes this rider could edit (and so could claim) but that
// myRoutes excludes on purpose — surfaced here so "why isn't my route in
// this list" has an answer instead of a silent gap.
const claimableOrphanCount = computed(
  () => routes.value.filter((r) => !r.owner && (can('routes:edit-any') || can('routes:edit-own'))).length,
)

// True once every offered route is selected — drives the "Select all" /
// "Select none" toggle below without a separate ref to keep in sync.
const allRoutesSelected = computed(
  () => shareOptions.value.length > 0 && shareSelections.value.length === shareOptions.value.length,
)

function toggleSelectAllRoutes() {
  shareSelections.value = allRoutesSelected.value ? [] : shareOptions.value.map((o) => o.value)
}

function openShare(crew: Crew) {
  shareSelections.value = myRoutes.value.filter((r) => r.targets.includes(crew.id)).map((r) => r.slug)
  shareTarget.value = crew
  // Closes the detail popup this was opened from rather than stacking a
  // second UModal on top of it — found live: Nuxt UI's modals don't layer
  // predictably when two are open at once, and the share dialog rendered
  // invisibly behind the still-open detail one.
  detailTarget.value = null
}

async function saveShare() {
  const crew = shareTarget.value
  if (!crew) return
  sharing.value = true

  const changed = myRoutes.value.filter((r) => r.targets.includes(crew.id) !== shareSelections.value.includes(r.slug))
  const failures: string[] = []
  await Promise.all(
    changed.map(async (route) => {
      const wanted = shareSelections.value.includes(route.slug)
      const nextTargets = wanted
        ? [...route.targets, crew.id]
        : route.targets.filter((t) => t !== crew.id)
      try {
        await api.updateTargets(route.slug, nextTargets)
      } catch {
        failures.push(route.name)
      }
    }),
  )

  sharing.value = false
  if (failures.length) {
    toast.add({
      title: `Could not update ${failures.length} route${failures.length === 1 ? '' : 's'}`,
      description: failures.join(', '),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } else if (changed.length) {
    toast.add({
      title: `Updated ${changed.length} route${changed.length === 1 ? '' : 's'}`,
      icon: 'i-lucide-check',
      color: 'success',
    })
  }
  shareTarget.value = null
  await backgroundRefresh()
}
</script>

<template>
  <div class="flex flex-col gap-6">
    <UCard variant="outline">
      <template #header>
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 class="flex items-center gap-2 font-medium text-highlighted">
              <UIcon name="i-lucide-users-round" />
              Crews
            </h2>
            <p class="text-sm text-muted">
              Share routes with riders you trust — a route reaches only its owner's own devices
              until it's shared to a crew.
            </p>
          </div>
          <div class="flex items-center gap-2">
            <UButton icon="i-lucide-search" color="neutral" variant="outline" @click="searchOpen = true">
              Search crews
            </UButton>
            <UButton icon="i-lucide-plus" @click="createOpen = true">Create crew</UButton>
          </div>
        </div>
      </template>

      <UAlert
        v-if="error"
        color="error"
        variant="subtle"
        icon="i-lucide-triangle-alert"
        :description="error"
        class="mb-4"
      />

      <template v-if="myCrews.length">
        <div class="flex flex-col divide-y divide-default">
          <button
            v-for="crew in pagedCrews"
            :key="crew.id"
            type="button"
            class="flex flex-wrap items-center gap-3 py-3 text-left first:pt-0 last:pb-0 hover:bg-elevated/50"
            @click="openDetail(crew)"
          >
            <div class="min-w-0 flex-1">
              <p class="truncate text-sm text-highlighted">{{ crew.name }}</p>
              <p v-if="nextRideFor(crew.id)" class="truncate text-xs text-dimmed">
                Next ride: {{ nextRideFor(crew.id)!.routeName }} · {{ formatRideWhen(nextRideFor(crew.id)!) }}
              </p>
              <p v-else class="truncate text-xs text-dimmed">No rides scheduled yet</p>
            </div>

            <UBadge color="neutral" variant="subtle" size="sm">
              {{ crew.memberCount }} member{{ crew.memberCount === 1 ? '' : 's' }}
            </UBadge>
            <UBadge v-if="crew.mine" color="primary" variant="subtle" size="sm">Owner</UBadge>
            <UTooltip v-if="crew.autoShare" text="New route uploads default to sharing here">
              <UBadge color="primary" variant="subtle" size="sm" icon="i-lucide-share-2">Auto-share</UBadge>
            </UTooltip>
            <UBadge v-if="crew.mine && pendingFor(crew).length" color="warning" variant="subtle" size="sm">
              {{ pendingFor(crew).length }} waiting
            </UBadge>
            <UIcon name="i-lucide-chevron-right" class="size-4 text-dimmed" />
          </button>
        </div>

        <UPagination
          v-if="myCrews.length > crewsPageSize"
          v-model:page="crewsPage"
          :total="myCrews.length"
          :items-per-page="crewsPageSize"
          class="mt-4 justify-center"
        />
      </template>

      <UEmpty
        v-else-if="!loading"
        icon="i-lucide-users-round"
        title="No crews yet"
        description="Create one, or search for one to join."
      />
    </UCard>

    <UModal v-model:open="createOpen" title="Create a crew">
      <template #body>
        <UAlert
          v-if="createError"
          color="error"
          variant="subtle"
          icon="i-lucide-triangle-alert"
          :description="createError"
          class="mb-4"
        />
        <form class="flex flex-col gap-3" @submit.prevent="createCrew">
          <UFormField label="Name">
            <UInput v-model="createName" placeholder="Sunday club" class="w-full" />
          </UFormField>
          <p class="text-xs text-dimmed">
            You'll be its owner and first member — you can approve who else joins from here.
          </p>
          <div class="flex justify-end gap-2 pt-2">
            <UButton color="neutral" variant="ghost" @click="createOpen = false">Cancel</UButton>
            <UButton type="submit" icon="i-lucide-plus" :loading="creating" :disabled="!createName.trim()">
              Create
            </UButton>
          </div>
        </form>
      </template>
    </UModal>

    <!-- Search popup: every crew the rider is not (yet) approved in —
         browsing, requesting to join, or acting on an invite still waiting
         on them. Nothing here duplicates the main list above, since that
         list is approved-only. -->
    <UModal v-model:open="searchOpen" title="Search crews">
      <template #body>
        <div class="flex flex-col gap-3">
          <UInput
            v-model="searchQuery"
            icon="i-lucide-search"
            placeholder="Filter by name"
            class="w-full"
          />
          <div v-if="visibleOtherCrews.length" class="flex max-h-96 flex-col divide-y divide-default overflow-y-auto">
            <div
              v-for="crew in visibleOtherCrews"
              :key="crew.id"
              class="flex flex-wrap items-center gap-3 py-3 first:pt-0 last:pb-0"
            >
              <div class="min-w-0 flex-1">
                <p class="truncate text-sm text-highlighted">{{ crew.name }}</p>
                <p class="truncate font-mono text-xs text-dimmed">{{ crew.id }} · owner {{ crew.owner }}</p>
              </div>
              <UBadge color="neutral" variant="subtle" size="sm">
                {{ crew.memberCount }} member{{ crew.memberCount === 1 ? '' : 's' }}
              </UBadge>

              <UButton
                v-if="crew.membershipStatus === 'none'"
                size="sm"
                icon="i-lucide-hand"
                :loading="joining === crew.id"
                @click="join(crew)"
              >
                Request to join
              </UButton>
              <template v-else-if="crew.membershipStatus === 'pending' && crew.membershipOrigin === 'invite'">
                <span class="text-xs text-dimmed">{{ crew.owner }} invited you</span>
                <UButton
                  size="sm"
                  color="success"
                  variant="soft"
                  icon="i-lucide-check"
                  :loading="approving === `${crew.id}:${me?.user}`"
                  @click="approveMember(crew, me?.user ?? '')"
                >
                  Accept
                </UButton>
                <UButton
                  size="sm"
                  color="neutral"
                  variant="ghost"
                  icon="i-lucide-x"
                  :loading="removing === `${crew.id}:${me?.user}`"
                  @click="removeMember(crew, me?.user ?? '')"
                >
                  Decline
                </UButton>
              </template>
              <UBadge v-else color="warning" variant="subtle" size="sm">
                {{ membershipLabel(crew) }}
              </UBadge>
            </div>
          </div>
          <p v-else class="text-sm text-dimmed">
            {{ searchQuery ? `No crew matches "${searchQuery}".` : "You're already in every crew that exists." }}
          </p>
        </div>
      </template>
      <template #footer>
        <div class="flex justify-end">
          <UButton color="neutral" variant="ghost" @click="searchOpen = false">Close</UButton>
        </div>
      </template>
    </UModal>

    <UModal :open="!!deleteTarget" title="Delete this crew?" @update:open="deleteTarget = null">
      <template #body>
        <p class="text-sm text-toned">
          “{{ deleteTarget?.name }}” will be removed, along with its membership. Routes shared to
          it will stop reaching its members on the next push — nothing about the routes
          themselves changes.
        </p>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" :disabled="deleting" @click="deleteTarget = null">
            Cancel
          </UButton>
          <UButton color="error" :loading="deleting" @click="deleteCrew">Delete</UButton>
        </div>
      </template>
    </UModal>

    <UModal
      :open="!!shareTarget"
      :title="`Share routes to ${shareTarget?.name ?? ''}`"
      @update:open="shareTarget = null"
    >
      <template #body>
        <div class="flex flex-col gap-3">
          <div class="flex items-center justify-between gap-3">
            <p class="text-sm text-toned">
              Which of your own routes should reach {{ shareTarget?.name }}?
            </p>
            <UButton
              v-if="shareOptions.length"
              size="xs"
              color="neutral"
              variant="ghost"
              @click="toggleSelectAllRoutes"
            >
              {{ allRoutesSelected ? 'Select none' : 'Select all' }}
            </UButton>
          </div>
          <UCheckboxGroup
            v-if="shareOptions.length"
            v-model="shareSelections"
            :items="shareOptions"
            class="max-h-72 overflow-y-auto"
          />
          <p v-else class="text-xs text-dimmed">
            You don't have any routes of your own to share yet.
          </p>
          <p v-if="claimableOrphanCount" class="text-xs text-dimmed">
            {{ claimableOrphanCount }} route{{ claimableOrphanCount === 1 ? '' : 's' }} with no owner
            {{ claimableOrphanCount === 1 ? "isn't" : "aren't" }} listed here — claim
            {{ claimableOrphanCount === 1 ? 'it' : 'them' }} from the Library page first.
          </p>
        </div>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" :disabled="sharing" @click="shareTarget = null">
            Cancel
          </UButton>
          <UButton :loading="sharing" :disabled="!shareOptions.length" @click="saveShare">Save</UButton>
        </div>
      </template>
    </UModal>

    <!-- Crew detail popup: who's in it, and (owner/admin only) the roster
         controls, plus scheduled rides for everyone approved. Each section
         is its own app-card (the same "elevated tile" language the stat
         row at the top of the page and the route cards on the Library page
         already use) rather than a flat list divided by a rule — a crew
         with any real membership and rides otherwise reads as one long
         wall of text with nothing to anchor the eye. -->
    <UModal
      :open="!!detailTarget"
      :title="detailCrew?.name ?? ''"
      :ui="{ content: 'sm:max-w-xl' }"
      @update:open="(v: boolean) => { if (!v) detailTarget = null }"
    >
      <template #body>
        <div v-if="detailCrew" class="flex flex-col gap-4">
          <!-- Scheduled rides -->
          <section class="app-card flex flex-col gap-3 p-4">
            <h3 class="flex items-center gap-2 text-sm font-semibold text-highlighted">
              <UIcon name="i-lucide-calendar-days" class="size-4 text-primary" />
              Scheduled rides
            </h3>

            <USkeleton v-if="ridesLoading" class="h-16 w-full" />
            <div v-else-if="rides.length" class="flex flex-col gap-2">
              <div
                v-for="ride in rides"
                :key="ride.id"
                class="flex items-center gap-3 rounded-lg px-2 py-2 hover:bg-elevated/60"
              >
                <div class="flex w-11 shrink-0 flex-col items-center rounded-lg bg-elevated py-1 leading-none">
                  <span class="text-[0.6rem] font-medium uppercase tracking-wide text-dimmed">
                    {{ rideMonth(ride.date) }}
                  </span>
                  <span class="text-base font-semibold text-highlighted">{{ rideDay(ride.date) }}</span>
                </div>
                <div class="min-w-0 flex-1">
                  <p class="truncate text-sm font-medium text-highlighted">{{ ride.routeName }}</p>
                  <p class="truncate text-xs text-dimmed">
                    <template v-if="ride.time">{{ ride.time }} · </template>Scheduled by {{ ride.createdBy }}
                  </p>
                </div>
                <UTooltip text="Sync to the crew's devices now">
                  <UButton
                    size="sm"
                    color="neutral"
                    variant="ghost"
                    icon="i-lucide-refresh-cw"
                    :loading="syncingRide === ride.id"
                    @click="syncRide(ride)"
                  />
                </UTooltip>
                <UTooltip
                  v-if="detailCrew.mine || ride.createdBy.toLowerCase() === (me?.user ?? '').toLowerCase()"
                  text="Remove this ride"
                >
                  <UButton
                    size="sm"
                    color="neutral"
                    variant="ghost"
                    icon="i-lucide-trash-2"
                    :loading="deletingRide === ride.id"
                    @click="deleteRide(ride)"
                  />
                </UTooltip>
              </div>
            </div>
            <div v-else class="flex flex-col items-center gap-1.5 rounded-lg border border-dashed border-default py-6">
              <UIcon name="i-lucide-calendar-x" class="size-5 text-dimmed" />
              <p class="text-xs text-dimmed">No rides scheduled yet.</p>
            </div>

            <form
              v-if="detailCrew.canSchedule"
              class="flex flex-col gap-2 rounded-lg bg-elevated/60 p-3"
              @submit.prevent="scheduleRide"
            >
              <p class="text-xs font-medium text-dimmed">Schedule a ride</p>
              <UFormField label="Route">
                <USelectMenu
                  v-model="scheduleSlug"
                  :items="scheduleRouteOptions"
                  value-key="value"
                  placeholder="Pick a route"
                  size="sm"
                  class="w-full"
                />
              </UFormField>

              <!-- What's actually about to be scheduled, before Schedule is
                   clicked — the same TrackPreview the Library page's own
                   cards use, so a route reads the same way everywhere in
                   the app rather than this form inventing its own map. -->
              <div v-if="scheduleSelectedRoute" class="flex gap-3 rounded-lg bg-elevated p-2">
                <TrackPreview :slug="scheduleSelectedRoute.slug" class="w-28 shrink-0" />
                <div class="flex min-w-0 flex-1 flex-col justify-center gap-1">
                  <p class="truncate text-sm font-medium text-highlighted">{{ scheduleSelectedRoute.name }}</p>
                  <div class="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-dimmed">
                    <span class="flex items-center gap-1">
                      <UIcon name="i-lucide-ruler" class="size-3.5" />
                      {{ (scheduleSelectedRoute.distanceM / 1000).toFixed(1) }} km
                    </span>
                    <span class="flex items-center gap-1">
                      <UIcon name="i-lucide-mountain" class="size-3.5" />
                      {{ Math.round(scheduleSelectedRoute.ascentM) }} m
                    </span>
                    <span class="flex items-center gap-1 capitalize">
                      <UIcon
                        :name="scheduleSelectedRoute.sport === 'running' ? 'i-lucide-footprints' : 'i-lucide-bike'"
                        class="size-3.5"
                      />
                      {{ scheduleSelectedRoute.sport }}
                    </span>
                  </div>
                </div>
              </div>

              <div class="flex items-end gap-2">
                <!-- No icon prop: a native date/time input already draws its
                     own picker icon, so adding one of ours just doubled up
                     and pushed the field wider than it needed to be. -->
                <UFormField label="Date" class="flex-1">
                  <UInput v-model="scheduleDate" type="date" size="sm" class="w-full" />
                </UFormField>
                <UFormField label="Time (optional)" class="flex-1">
                  <UInput
                    v-model="scheduleTime"
                    type="time"
                    step="900"
                    size="sm"
                    class="w-full"
                  />
                </UFormField>
                <UButton
                  type="submit"
                  size="sm"
                  icon="i-lucide-calendar-plus"
                  :loading="scheduling"
                  :disabled="!scheduleSlug"
                >
                  Schedule
                </UButton>
              </div>

              <!-- One click for the common cases, rather than everyone
                   dragging the native time spinner from 00:00 every time —
                   a second click on the already-picked preset clears it
                   back to "no specific time" instead of just re-setting the
                   same value. -->
              <div class="flex flex-wrap items-center gap-1.5">
                <span class="text-xs text-dimmed">Quick pick:</span>
                <UButton
                  v-for="preset in timePresets"
                  :key="preset.value"
                  size="xs"
                  :color="scheduleTime === preset.value ? 'primary' : 'neutral'"
                  :variant="scheduleTime === preset.value ? 'solid' : 'soft'"
                  @click="scheduleTime = scheduleTime === preset.value ? '' : preset.value"
                >
                  {{ preset.label }}
                </UButton>
              </div>
            </form>
            <p v-if="detailCrew.canSchedule && !scheduleRouteOptions.length" class="text-xs text-dimmed">
              No routes to schedule yet — share one, or upload one of your own.
            </p>
          </section>

          <!-- Roster -->
          <section class="app-card flex flex-col gap-4 p-4">
            <div class="flex items-center justify-between gap-3">
              <h3 class="flex items-center gap-2 text-sm font-semibold text-highlighted">
                <UIcon name="i-lucide-users" class="size-4 text-primary" />
                Roster
              </h3>
              <UButton size="sm" variant="outline" icon="i-lucide-route" @click="openShare(detailCrew)">
                Share your routes
              </UButton>
            </div>

            <UTooltip
              v-if="detailCrew.mine"
              text="When on, any new route a member uploads with no explicit sharing choice of their own is shared here automatically. Existing routes are never touched by this — turning it on doesn't reach back and share anything already uploaded."
            >
              <label
                class="flex w-fit items-center gap-2 rounded-lg bg-elevated/60 px-3 py-2 text-sm text-toned"
              >
                <USwitch
                  :model-value="detailCrew.autoShare"
                  :loading="togglingAutoShare === detailCrew.id"
                  @update:model-value="(v: boolean) => toggleAutoShare(detailCrew!, v)"
                />
                Auto-share new uploads
              </label>
            </UTooltip>

            <!-- Owner/admin: full management. Everyone else: a read-only
                 list of who's currently in, plus their own way out. -->
            <template v-if="detailCrew.mine">
              <form class="flex items-center gap-2" @submit.prevent="addMember(detailCrew)">
                <USelectMenu
                  v-model="addMemberInput[detailCrew.id]"
                  :items="suggestedRiders(detailCrew)"
                  create-item="always"
                  placeholder="Search or add a rider by username"
                  icon="i-lucide-search"
                  size="sm"
                  class="flex-1"
                  @create="(rider: string) => (addMemberInput[detailCrew!.id] = rider)"
                />
                <UButton
                  type="submit"
                  size="sm"
                  icon="i-lucide-user-plus"
                  :loading="addingMember === detailCrew.id"
                  :disabled="!addMemberInput[detailCrew.id]?.trim()"
                >
                  Add
                </UButton>
              </form>

              <div class="flex flex-col gap-1">
                <div
                  v-for="member in pendingFor(detailCrew)"
                  :key="`pending-${member.rider}`"
                  class="flex items-center gap-3 rounded-lg px-2 py-1.5 hover:bg-elevated/60"
                >
                  <div
                    class="flex size-8 shrink-0 items-center justify-center rounded-full bg-warning/10 text-warning"
                  >
                    <UIcon name="i-lucide-clock" class="size-4" />
                  </div>
                  <span class="min-w-0 flex-1 truncate text-sm text-toned">
                    <span class="font-medium text-highlighted">{{ member.rider }}</span>
                    {{ member.origin === 'invite' ? 'invited — awaiting their confirmation' : 'wants to join' }}
                  </span>
                  <UButton
                    v-if="member.origin !== 'invite'"
                    size="xs"
                    icon="i-lucide-check"
                    :loading="approving === `${detailCrew.id}:${member.rider}`"
                    @click="approveMember(detailCrew!, member.rider)"
                  >
                    Approve
                  </UButton>
                  <UButton
                    size="xs"
                    color="neutral"
                    variant="ghost"
                    icon="i-lucide-x"
                    :loading="removing === `${detailCrew.id}:${member.rider}`"
                    @click="removeMember(detailCrew!, member.rider)"
                  >
                    {{ member.origin === 'invite' ? 'Cancel invite' : 'Deny' }}
                  </UButton>
                </div>

                <div
                  v-for="member in approvedFor(detailCrew)"
                  :key="`approved-${member.rider}`"
                  class="flex items-center gap-3 rounded-lg px-2 py-1.5 hover:bg-elevated/60"
                >
                  <div
                    class="flex size-8 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary"
                  >
                    {{ initials(member.rider) }}
                  </div>
                  <span class="min-w-0 flex-1 truncate text-sm font-medium text-highlighted">
                    {{ member.rider }}
                  </span>
                  <UBadge v-if="member.rider.toLowerCase() === detailCrew.owner.toLowerCase()" color="primary" variant="subtle" size="sm">
                    Owner
                  </UBadge>
                  <template v-else>
                    <UTooltip text="May schedule a crew ride, the same as the owner">
                      <label class="flex items-center gap-1.5 text-xs text-dimmed">
                        <USwitch
                          :model-value="member.canSchedule"
                          size="sm"
                          :loading="togglingCanSchedule === `${detailCrew.id}:${member.rider}`"
                          @update:model-value="(v: boolean) => toggleCanSchedule(detailCrew!, member.rider, v)"
                        />
                        <span class="hidden sm:inline">Can schedule</span>
                      </label>
                    </UTooltip>
                    <UButton
                      size="xs"
                      color="neutral"
                      variant="ghost"
                      icon="i-lucide-x"
                      :loading="removing === `${detailCrew.id}:${member.rider}`"
                      @click="removeMember(detailCrew!, member.rider)"
                    />
                  </template>
                </div>

                <p v-if="!pendingFor(detailCrew).length && !approvedFor(detailCrew).length" class="px-2 text-xs text-dimmed">
                  Nobody else has joined yet.
                </p>
              </div>
            </template>
            <template v-else>
              <div class="flex flex-col gap-1">
                <div
                  v-for="rider in detailCrew.roster ?? []"
                  :key="rider"
                  class="flex items-center gap-3 rounded-lg px-2 py-1.5"
                >
                  <div
                    class="flex size-8 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary"
                  >
                    {{ initials(rider) }}
                  </div>
                  <span class="min-w-0 flex-1 truncate text-sm font-medium text-highlighted">{{ rider }}</span>
                  <UBadge v-if="rider.toLowerCase() === detailCrew.owner.toLowerCase()" color="primary" variant="subtle" size="sm">
                    Owner
                  </UBadge>
                </div>
              </div>
              <UButton
                size="sm"
                color="neutral"
                variant="ghost"
                icon="i-lucide-log-out"
                class="w-fit"
                :loading="removing === `${detailCrew.id}:${me?.user}`"
                @click="removeMember(detailCrew, me?.user ?? '')"
              >
                Leave crew
              </UButton>
            </template>
          </section>
        </div>
      </template>
      <template #footer>
        <div class="flex w-full items-center justify-between">
          <UButton
            v-if="detailCrew?.mine"
            size="sm"
            color="error"
            variant="ghost"
            icon="i-lucide-trash-2"
            @click="openDelete(detailCrew)"
          >
            Delete crew
          </UButton>
          <span v-else />
          <UButton color="neutral" variant="ghost" @click="detailTarget = null">Close</UButton>
        </div>
      </template>
    </UModal>
  </div>
</template>
