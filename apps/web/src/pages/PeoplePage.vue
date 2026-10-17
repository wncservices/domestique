<script setup lang="ts">
import { nextTick, onMounted, ref, watch, computed } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import type { DropdownMenuItem } from '@nuxt/ui'
import { api, ApiError } from '@/api/client'
import type { AssignableRole, Person, RiderInfo, RiderMerge } from '@/api/types'
import { roleColor } from '@/utils/role'
import { usePagedList } from '@/composables/usePagedList'
import { describeError } from '@/utils/errorMessage'
import EmptyState from '@/components/ui/EmptyState.vue'
import AppPage from '@/components/ui/AppPage.vue'

const toast = useToast()

const people = ref<Person[]>([])
const { page: peoplePage, paged: pagedPeople, pageSize: peoplePageSize } = usePagedList(people, 24)
const loading = ref(true)
const error = ref('')
// null (never configured) is distinct from "" (a real, empty error) — the
// page needs to tell "no Management API credentials set" apart from any
// other failure, and only the first one hides the invite form entirely
// rather than just showing an alert above it.
const unavailable = ref(false)

// Riders that have data here but no account to sign in as them. Best effort:
// the list is a hint for the admin, so a failure to fetch it is not an error.
const orphanRiders = ref<string[]>([])
// The signed-in admin's own rider, to tell their own row from everyone else's.
const myRider = ref('')

async function load() {
  loading.value = true
  error.value = ''
  unavailable.value = false
  try {
    people.value = await api.people()
    api.orphanRiders().then((r) => { orphanRiders.value = r.riders }).catch(() => { orphanRiders.value = [] })
    api.me().then((m) => { myRider.value = m.user ?? '' }).catch(() => {})
  } catch (err) {
    if (err instanceof ApiError && err.status === 412) {
      unavailable.value = true
    } else {
      error.value = describeError(err)
    }
  } finally {
    loading.value = false
  }
}

onMounted(load)

const roleOptions: { label: string; value: AssignableRole }[] = [
  { label: 'Admin', value: 'admin' },
  { label: 'Rider', value: 'rider' },
  { label: 'Viewer', value: 'viewer' },
]

// --- invite ---

const inviting = ref(false)
const inviteOpen = ref(false)
const inviteError = ref('')
const inviteEmail = ref('')
const inviteName = ref('')
const inviteRole = ref<AssignableRole>('rider')

async function sendInvite() {
  if (!inviteEmail.value.trim()) return
  inviting.value = true
  inviteError.value = ''
  try {
    const result = await api.invitePerson({
      email: inviteEmail.value.trim(),
      name: inviteName.value.trim() || undefined,
      role: inviteRole.value,
    })
    if (result.error) {
      // The account exists and has its role — only the email failed. Worth
      // saying plainly rather than as a generic failure, since retrying the
      // whole invite would try (and fail) to create the same account again.
      toast.add({
        title: 'Account created, but the invite email failed',
        description: result.error,
        icon: 'i-lucide-triangle-alert',
        color: 'warning',
      })
    } else if (result.granted) {
      // They already had an Auth0 identity — most often a prior Google
      // sign-in — so this granted access to it directly rather than
      // creating a second account. No invite email goes out: they already
      // have a way to sign in.
      toast.add({
        title: `Granted access to ${result.person.email}`,
        description: 'They already had a sign-in for this address — no new account was created.',
        icon: 'i-lucide-shield-check',
        color: 'success',
      })
    } else {
      toast.add({
        title: `Invited ${result.person.email}`,
        icon: 'i-lucide-mail-check',
        color: 'success',
      })
    }
    inviteOpen.value = false
    inviteEmail.value = ''
    inviteName.value = ''
    inviteRole.value = 'rider'
    await load()
  } catch (err) {
    inviteError.value = describeError(err)
  } finally {
    inviting.value = false
  }
}

// --- role changes ---

const changingRole = ref('')

async function changeRole(person: Person, role: AssignableRole) {
  if (role === person.role) return
  changingRole.value = person.id
  try {
    await api.setPersonRole(person.id, role)
    person.role = role
    toast.add({
      title: `${person.email} is now ${role}`,
      icon: 'i-lucide-shield-check',
      color: 'success',
    })
  } catch (err) {
    toast.add({
      title: `Could not change ${person.email}'s role`,
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    changingRole.value = ''
  }
}

function lastSeen(person: Person): string {
  if (!person.lastLogin) return 'never signed in'
  return new Date(person.lastLogin).toLocaleDateString()
}

// --- block / unblock ---
//
// Blocking stops two things: this identity signing in again (Auth0's own
// blocked flag) and a fresh signup with the same email getting back in
// (this app's own local blocklist, checked at every OIDC callback) — see
// api.setPersonBlocked's own doc comment. Confirmed either direction,
// since un-blocking someone is also a real access decision worth a second
// look, not just blocking.

const blockTarget = ref<Person | null>(null)
const blockReason = ref('')
const togglingBlocked = ref('')

async function confirmToggleBlocked() {
  const target = blockTarget.value
  if (!target) return
  const blocked = !target.blocked
  togglingBlocked.value = target.id
  try {
    const result = await api.setPersonBlocked(target.id, blocked, target.email, blocked ? blockReason.value.trim() : undefined)
    target.blocked = blocked
    blockTarget.value = null
    blockReason.value = ''
    if (result.error) {
      toast.add({
        title: blocked ? `${target.email} blocked, but only partially` : `${target.email} unblocked, but only partially`,
        description: result.error,
        icon: 'i-lucide-triangle-alert',
        color: 'warning',
      })
    } else {
      toast.add({
        title: blocked ? `${target.email} is now blocked` : `${target.email} is no longer blocked`,
        icon: blocked ? 'i-lucide-shield-off' : 'i-lucide-shield-check',
        color: 'success',
      })
    }
  } catch (err) {
    toast.add({
      title: `Could not ${blocked ? 'block' : 'unblock'} ${target.email}`,
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    togglingBlocked.value = ''
  }
}

// --- which rider an account is ---
//
// An account signs in as the rider it is bound to. Nothing the person can edit
// (their name at Google, say) chooses that. An admin settles what the startup
// bootstrap left alone: a conflict between two accounts claiming one rider, or
// an account that ended up on a suffixed rider. Never for one's own account:
// the server refuses, and says to use the CLI.

const bindTarget = ref<Person | null>(null)
const bindRider = ref('')
const binding = ref('')
// What binding to the typed rider would hand over, so the admin sees it first.
const bindInfo = ref<RiderInfo | null>(null)
const bindConfirmed = ref(false)

// A person's own row: their rider is the signed-in admin's. Moving your own
// sign-in is only for the way out of a lockout, onto a rider nobody is bound to.
function isOwn(person: Person): boolean {
  return !!myRider.value && person.boundRider === myRider.value
}

// Why the startup bootstrap held an account back, as a badge. The sentence
// behind it (flagText) comes from the server, which knows the rider.
function flagLabel(reason: NonNullable<Person['flagReason']>): string {
  switch (reason) {
    case 'newer-than-data':
      return 'joined after the data'
    case 'conflict':
      return 'accounts disagree'
    case 'admin-bound-elsewhere':
      return 'not on your own rider'
    default:
      return 'unverified claimant'
  }
}

// The one-click way back from a suffixed fallback ("wilant-2") to the rider
// with the data ("wilant"). Your own row goes straight through: the server lets
// an account with no one else on the target take it. Someone else's goes via
// the bind dialog, which asks for the confirmation that handing over data needs.
async function linkSuggested(person: Person) {
  const rider = person.suggestRider
  if (!rider) return
  if (!isOwn(person)) {
    openBind(person)
    bindRider.value = rider
    return
  }
  binding.value = person.id
  try {
    const result = await api.setPersonBinding(person.id, rider)
    toast.add({
      title: `You now sign in as ${result.boundRider}`,
      description: 'Your current sign-ins were ended; sign in again to see your data.',
      icon: 'i-lucide-link',
      color: 'success',
    })
    await load()
  } catch (err) {
    toast.add({
      title: `Could not link to ${rider}`,
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    binding.value = ''
  }
}

function openBind(person: Person) {
  bindTarget.value = person
  // Prefilled with the account's own rider, except for a conflict: there the
  // likely rider is exactly the name two accounts are fighting over, and
  // pre-typing it invites binding the wrong one.
  bindRider.value = person.bindingStatus === 'conflict' ? '' : (person.boundRider ?? person.likelyRider ?? '')
  bindInfo.value = null
  bindConfirmed.value = false
}

let infoTimer: ReturnType<typeof setTimeout> | undefined
watch(bindRider, (rider) => {
  bindConfirmed.value = false
  bindInfo.value = null
  clearTimeout(infoTimer)
  const wanted = rider.trim().toLowerCase()
  if (!wanted || !bindTarget.value) return
  infoTimer = setTimeout(async () => {
    try {
      const info = await api.riderInfo(wanted)
      if (wanted === bindRider.value.trim().toLowerCase()) bindInfo.value = info
    } catch {
      bindInfo.value = null
    }
  }, 250)
})

// Binding to a rider that has data or other accounts needs an explicit yes.
const needsConfirm = computed(() => !!bindInfo.value && (bindInfo.value.hasData || bindInfo.value.bindings > 0))
// Own account onto a rider someone is bound to is refused by the server; say so
// before the click rather than after.
const ownBlocked = computed(
  () => !!bindTarget.value && isOwn(bindTarget.value) && !!bindInfo.value && bindInfo.value.bindings > 0,
)
const canBind = computed(
  () => !!bindRider.value.trim() && !!bindInfo.value && !ownBlocked.value && (!needsConfirm.value || bindConfirmed.value),
)

async function confirmBind() {
  const target = bindTarget.value
  const rider = bindRider.value.trim().toLowerCase()
  if (!target || !rider || !canBind.value) return
  binding.value = target.id
  try {
    const result = await api.setPersonBinding(target.id, rider)
    target.boundRider = result.boundRider
    target.bindingStatus = 'bound'
    bindTarget.value = null
    toast.add({
      title: `${target.email} now signs in as ${result.boundRider}`,
      description: 'Their current sign-ins were ended; they sign in again.',
      icon: 'i-lucide-link',
      color: 'success',
    })
    // Another account may have stopped being a conflict.
    await load()
  } catch (err) {
    toast.add({
      title: `Could not change which rider ${target.email} is`,
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    binding.value = ''
  }
}

// --- merge a rider into another ---
//
// A person who ended up as two riders (an old name-derived one, and the one
// their account is bound to) has their data split across both. Merging moves
// everything from the orphan onto the other rider; where both have a row on
// the same key the target's is kept. It cannot be undone, so the dialog shows a
// dry run first (the server does the real work and rolls it back) and asks.

const mergeFrom = ref('')
const mergeInto = ref('')
const mergePreview = ref<RiderMerge | null>(null)
const mergeTarget = ref<RiderInfo | null>(null)
const mergeError = ref('')
const mergePreviewing = ref(false)
const merging = ref(false)
const mergeConfirmed = ref(false)

function openMerge(rider: string) {
  mergeFrom.value = rider
  mergeConfirmed.value = false
  // "wilant.nackaerts" is almost always a split of "wilant".
  const base = rider.split(/[.@|]/)[0] ?? ''
  mergeInto.value = base && base !== rider ? base : ''
  // Prefilled with the likely target: look straight away. Typed: on blur/Enter.
  if (mergeInto.value) void nextTick(runMergePreview)
}

function closeMerge() {
  mergeFrom.value = ''
  mergeInto.value = ''
  mergePreview.value = null
  mergeTarget.value = null
  mergeError.value = ''
}

// The dry run is a real round trip that does the whole merge and rolls it back,
// so it runs when the admin finishes typing (blur or Enter) or asks for it, not
// on every keystroke. Changing the name clears the old preview, so a stale one
// can never be confirmed against a different target.
watch([mergeFrom, mergeInto], () => {
  mergeConfirmed.value = false
  mergePreview.value = null
  mergeTarget.value = null
  mergeError.value = ''
})

async function runMergePreview() {
  const from = mergeFrom.value
  const into = mergeInto.value.trim().toLowerCase()
  if (!from || !into || into === from || mergePreviewing.value) return
  mergePreviewing.value = true
  mergeError.value = ''
  try {
    const [preview, info] = await Promise.all([api.mergeRider(from, into, true), api.riderInfo(into)])
    if (from === mergeFrom.value && into === mergeInto.value.trim().toLowerCase()) {
      mergePreview.value = preview
      mergeTarget.value = info
    }
  } catch (err) {
    if (from === mergeFrom.value && into === mergeInto.value.trim().toLowerCase()) {
      mergeError.value = describeError(err)
    }
  } finally {
    mergePreviewing.value = false
  }
}

const canMerge = computed(() => !!mergePreview.value && !mergeError.value && mergeConfirmed.value && !merging.value)

async function confirmMerge() {
  const from = mergeFrom.value
  const into = mergeInto.value.trim().toLowerCase()
  if (!from || !into || !canMerge.value) return
  merging.value = true
  try {
    const done = await api.mergeRider(from, into, false)
    toast.add({
      title: `Merged ${from} into ${done.into}`,
      description:
        `${done.moved} moved, ${done.merged} memberships merged, ${done.droppedSource} dropped where ${done.into} already had one.` +
        (done.warnings.length ? ` ${done.warnings.join(' ')}` : " Both riders' sign-ins ended; they sign in again."),
      icon: 'i-lucide-git-merge',
      color: done.warnings.length ? 'warning' : 'success',
    })
    closeMerge()
    await load()
  } catch (err) {
    toast.add({
      title: `Could not merge ${from} into ${into}`,
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    merging.value = false
  }
}

// --- delete ---
//
// likelyRider is only a guess at this person's local rider identity (see
// Person.likelyRider's own doc comment) — surfaced here, editable, so an
// admin can catch a wrong guess before the purge fires rather than after.

const deleteTarget = ref<Person | null>(null)
const deleteRider = ref('')
const deletingPerson = ref('')

// How many OTHER accounts are bound to the rider whose data a delete would
// purge: the purge removes the rider, and with it their bindings.
const deleteInfo = ref<RiderInfo | null>(null)
const otherAccounts = computed(() => {
  const info = deleteInfo.value
  const target = deleteTarget.value
  if (!info || !target) return 0
  const mine = target.boundRider && target.boundRider === info.rider ? 1 : 0
  return Math.max(0, info.bindings - mine)
})

let deleteTimer: ReturnType<typeof setTimeout> | undefined
function refreshDeleteInfo() {
  clearTimeout(deleteTimer)
  deleteInfo.value = null
  const wanted = deleteRider.value.trim().toLowerCase()
  if (!wanted) return
  deleteTimer = setTimeout(async () => {
    try {
      const info = await api.riderInfo(wanted)
      if (wanted === deleteRider.value.trim().toLowerCase()) deleteInfo.value = info
    } catch {
      deleteInfo.value = null
    }
  }, 250)
}
watch(deleteRider, refreshDeleteInfo)

function openDeleteConfirm(person: Person) {
  deleteTarget.value = person
  // The binding is the rider this account is; the profile-name guess is only
  // for someone who has no binding yet.
  deleteRider.value = person.boundRider ?? person.likelyRider ?? ''
  refreshDeleteInfo()
}

async function confirmDeletePerson() {
  const target = deleteTarget.value
  if (!target) return
  deletingPerson.value = target.id
  try {
    await api.deletePerson(target.id, deleteRider.value.trim() || undefined)
    people.value = people.value.filter((p) => p.id !== target.id)
    deleteTarget.value = null
    toast.add({
      title: `${target.email} deleted`,
      icon: 'i-lucide-check',
      color: 'success',
    })
  } catch (err) {
    toast.add({
      title: `Could not delete ${target.email}`,
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    deletingPerson.value = ''
  }
}

const subtitle = computed(() => {
  if (loading.value) return 'Loading…'
  if (unavailable.value) return 'Not available on this deployment'
  return `${people.value.length} with access`
})

// Binding is the only non-destructive secondary action; block and delete go
// last, in their own group. All three open the confirmation they always did.
function personActions(person: Person): DropdownMenuItem[][] {
  const groups: DropdownMenuItem[][] = []
  if (person.bindingStatus) {
    groups.push([
      {
        label: 'Change which rider this is',
        icon: 'i-lucide-link',
        description: isOwn(person)
          ? 'Your own sign-in: only onto a rider nobody is bound to (lockout recovery)'
          : undefined,
        disabled: binding.value === person.id,
        onSelect: () => openBind(person),
      },
    ])
  }
  groups.push([
    {
      label: person.blocked ? 'Unblock' : 'Block',
      icon: person.blocked ? 'i-lucide-shield-check' : 'i-lucide-shield-off',
      color: person.blocked ? undefined : 'error',
      disabled: togglingBlocked.value === person.id,
      onSelect: () => { blockTarget.value = person },
    },
    {
      label: 'Delete',
      icon: 'i-lucide-trash-2',
      color: 'error',
      disabled: deletingPerson.value === person.id,
      onSelect: () => openDeleteConfirm(person),
    },
  ])
  return groups
}
</script>

<template>
  <AppPage title="People" :subtitle="subtitle">
    <template #actions>
      <UButton icon="i-lucide-refresh-cw" color="neutral" variant="ghost" :loading="loading" @click="load">
        Refresh
      </UButton>
      <UButton
        v-if="!unavailable"
        icon="i-lucide-user-plus"
        :disabled="loading"
        @click="inviteOpen = true"
      >
        Invite
      </UButton>
    </template>

    <UCard variant="outline">
      <UAlert
        v-if="error"
        color="error"
        variant="subtle"
        icon="i-lucide-triangle-alert"
        :description="error"
        :actions="[{ label: 'Retry', color: 'error', variant: 'subtle', onClick: () => load() }]"
        class="mb-4"
      />

      <UAlert
        v-if="orphanRiders.length"
        color="warning"
        variant="subtle"
        icon="i-lucide-user-x"
        title="Riders with data but no account"
        class="mb-4"
      >
        <template #description>
          <p>
            Nobody can sign in as {{ orphanRiders.length === 1 ? 'this rider' : 'these riders' }}. Bind an
            account to one from its row, merge it into another rider, or remove the data.
          </p>
          <ul class="mt-2 flex flex-col gap-2">
            <li v-for="rider in orphanRiders" :key="rider" class="flex flex-wrap items-center gap-2">
              <code class="text-highlighted">{{ rider }}</code>
              <UButton size="xs" color="neutral" variant="outline" icon="i-lucide-git-merge" @click="openMerge(rider)">
                Merge into…
              </UButton>
            </li>
          </ul>
        </template>
      </UAlert>

      <EmptyState
        v-if="unavailable"
        bare
        :heading-level="3"
        icon="i-lucide-key-round"
        title="Auth0 Management API access is not configured"
        text="An administrator can set DOMESTIQUE_AUTH0_MGMT_CLIENT_ID and DOMESTIQUE_AUTH0_MGMT_CLIENT_SECRET to enable this page."
      />

      <template v-else-if="people.length">
        <div class="flex flex-col divide-y divide-default">
          <div
            v-for="person in pagedPeople"
            :key="person.id"
            class="flex flex-wrap items-center gap-3 py-2 first:pt-0 last:pb-0"
          >
            <div class="min-w-0 flex-1 basis-full sm:basis-0">
              <p class="truncate text-sm text-highlighted">{{ person.name || person.email }}</p>
              <p class="truncate text-xs text-dimmed">{{ person.email }} · {{ lastSeen(person) }}</p>
            </div>
            <UBadge v-if="person.bindingStatus === 'bound'" color="neutral" variant="subtle" size="sm" icon="i-lucide-link">
              {{ person.boundRider }}
            </UBadge>
            <UBadge
              v-if="person.flagReason"
              color="warning"
              variant="subtle"
              size="sm"
              icon="i-lucide-triangle-alert"
            >
              {{ flagLabel(person.flagReason) }}
            </UBadge>
            <UBadge
              v-if="person.bindingStatus === 'needs-attention' && !person.flagReason"
              color="error"
              variant="subtle"
              size="sm"
              icon="i-lucide-triangle-alert"
            >
              needs attention: {{ person.likelyRider }}
            </UBadge>
            <UBadge
              v-if="person.bindingStatus === 'conflict' && !person.flagReason"
              color="warning"
              variant="subtle"
              size="sm"
              icon="i-lucide-triangle-alert"
            >
              conflict: {{ person.likelyRider }}
            </UBadge>
            <UBadge v-if="person.bindingStatus === 'unbound'" color="neutral" variant="outline" size="sm">
              not bound
            </UBadge>
            <UBadge v-if="person.blocked" color="error" variant="subtle" size="sm">blocked</UBadge>
            <UBadge :color="roleColor(person.role)" variant="subtle" size="sm">{{ person.role }}</UBadge>
            <USelect
              :model-value="person.role"
              :items="roleOptions"
              :loading="changingRole === person.id"
              :disabled="changingRole === person.id"
              size="sm"
              class="w-28"
              aria-label="Change role"
              @update:model-value="(role: AssignableRole) => changeRole(person, role)"
            />
            <UDropdownMenu :items="personActions(person)" :content="{ align: 'end' }">
              <UButton
                size="sm"
                color="neutral"
                variant="ghost"
                icon="i-lucide-ellipsis"
                :aria-label="`Actions for ${person.name || person.email || person.id}`"
                :loading="binding === person.id || togglingBlocked === person.id || deletingPerson === person.id"
              />
            </UDropdownMenu>
            <div v-if="person.flagText || person.suggestRider" class="flex basis-full flex-col items-start gap-2">
              <p v-if="person.flagText" class="text-xs text-warning">{{ person.flagText }}</p>
              <UButton
                v-if="person.suggestRider"
                size="xs"
                icon="i-lucide-link"
                :loading="binding === person.id"
                @click="linkSuggested(person)"
              >
                {{ isOwn(person) ? 'This is me: link to' : 'Link to' }} {{ person.suggestRider }}
              </UButton>
              <p v-if="person.suggestRider" class="text-xs text-dimmed">
                Signed in as {{ person.boundRider }}, but {{ person.suggestRider }} has data and nobody is bound to it.
              </p>
            </div>
          </div>
        </div>

        <UPagination
          v-if="people.length > peoplePageSize"
          v-model:page="peoplePage"
          :total="people.length"
          :items-per-page="peoplePageSize"
          class="mt-4 justify-center"
        />
      </template>

      <EmptyState
        v-else-if="!loading"
        bare
        :heading-level="3"
        icon="i-lucide-user-x"
        title="Nobody has access yet"
        text="Invite the first rider above."
      />
    </UCard>

    <UModal v-model:open="inviteOpen" title="Invite someone">
      <template #body>
        <UAlert
          v-if="inviteError"
          color="error"
          variant="subtle"
          icon="i-lucide-triangle-alert"
          :description="inviteError"
          class="mb-4"
        />
        <form class="flex flex-col gap-3" @submit.prevent="sendInvite">
          <UFormField label="Email">
            <UInput v-model="inviteEmail" type="email" placeholder="rider@example.com" class="w-full" />
          </UFormField>
          <UFormField label="Name" hint="optional">
            <UInput v-model="inviteName" class="w-full" />
          </UFormField>
          <UFormField label="Role">
            <USelect v-model="inviteRole" :items="roleOptions" class="w-full" />
          </UFormField>
          <p class="text-xs text-dimmed">
            They'll get an email to set their own password. Nothing is shared over chat or in this app.
          </p>
          <div class="flex justify-end gap-2 pt-2">
            <UButton color="neutral" variant="ghost" @click="inviteOpen = false">Cancel</UButton>
            <UButton
              type="submit"
              icon="i-lucide-mail-plus"
              :loading="inviting"
              :disabled="!inviteEmail.trim()"
            >
              Send invite
            </UButton>
          </div>
        </form>
      </template>
    </UModal>

    <UModal
      :open="!!blockTarget"
      :title="blockTarget?.blocked ? 'Unblock this person?' : 'Block this person?'"
      @update:open="blockTarget = null"
    >
      <template #body>
        <p class="text-sm text-toned">
          <template v-if="blockTarget?.blocked">
            {{ blockTarget?.email }} will be able to sign in again, and a fresh signup with this
            email will no longer be refused.
          </template>
          <template v-else>
            {{ blockTarget?.email }} will be signed out and refused at sign-in — including a fresh
            signup with this same email. Their existing routes and data are not touched, and this
            can be undone later.
          </template>
        </p>
        <UFormField v-if="!blockTarget?.blocked" label="Reason" hint="optional, for your own records" class="mt-3">
          <UInput v-model="blockReason" class="w-full" />
        </UFormField>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" :disabled="!!togglingBlocked" @click="blockTarget = null">
            Cancel
          </UButton>
          <UButton
            :color="blockTarget?.blocked ? 'primary' : 'error'"
            :loading="!!togglingBlocked"
            @click="confirmToggleBlocked"
          >
            {{ blockTarget?.blocked ? 'Unblock' : 'Block' }}
          </UButton>
        </div>
      </template>
    </UModal>

    <UModal :open="!!bindTarget" title="Which rider is this?" @update:open="bindTarget = null">
      <template #body>
        <p class="text-sm text-toned">
          {{ bindTarget?.email }} signs in as the rider it is bound to, whatever its name says.
          <template v-if="bindTarget?.bindingStatus === 'conflict'">
            Accounts claiming
            <span class="text-highlighted">{{ bindTarget?.likelyRider }}</span> disagree, or the only one
            joined after that rider's data existed, so none was bound automatically. Bind only the one you
            know is the real rider; the name is left empty on purpose.
          </template>
        </p>
        <UFormField label="Rider" hint="their routes, devices and training follow this name" class="mt-3">
          <UInput v-model="bindRider" class="w-full" @keydown.enter.prevent="confirmBind" />
        </UFormField>
        <UAlert
          v-if="bindInfo"
          class="mt-3"
          :color="ownBlocked ? 'error' : needsConfirm ? 'warning' : 'neutral'"
          variant="subtle"
          :icon="needsConfirm ? 'i-lucide-triangle-alert' : 'i-lucide-info'"
          :description="`${bindInfo.rider}: ${bindInfo.hasData ? 'has data here' : 'has no data'}, ${bindInfo.bindings} account${bindInfo.bindings === 1 ? '' : 's'} bound.`"
        />
        <p v-if="ownBlocked" class="mt-2 text-sm text-error">
          Your own sign-in can only go onto a rider nobody is bound to. Ask another admin, or run
          <code>domestique bind-identity</code> on the server.
        </p>
        <UCheckbox
          v-else-if="needsConfirm"
          v-model="bindConfirmed"
          class="mt-3"
          label="I know this account belongs to that rider, and give it their data"
        />
        <p class="mt-3 text-xs text-dimmed">
          Their current sign-ins end, and they sign in again as this rider.
        </p>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" :disabled="!!binding" @click="bindTarget = null">Cancel</UButton>
          <UButton icon="i-lucide-link" :loading="!!binding" :disabled="!canBind" @click="confirmBind">
            Bind
          </UButton>
        </div>
      </template>
    </UModal>

    <UModal :open="!!mergeFrom" :title="`Merge ${mergeFrom} into another rider`" @update:open="closeMerge">
      <template #body>
        <p class="text-sm text-toned">
          Moves everything <span class="text-highlighted">{{ mergeFrom }}</span> owns (routes, devices, training,
          crews, sign-in bindings) onto the rider below. Where both have a row on the same key the target's is
          kept and {{ mergeFrom }}'s is dropped. Training goals and plans are added together, not merged. This cannot
          be undone.
        </p>
        <UFormField label="Merge into" hint="the rider that keeps the data" class="mt-3">
          <UInput
            v-model="mergeInto"
            placeholder="wilant"
            class="w-full"
            @blur="runMergePreview"
            @keydown.enter.prevent="runMergePreview"
          />
        </UFormField>
        <UButton
          v-if="!mergePreview && mergeInto.trim()"
          class="mt-2"
          size="xs"
          color="neutral"
          variant="outline"
          icon="i-lucide-search"
          :loading="mergePreviewing"
          @click="runMergePreview"
        >
          Check what would happen
        </UButton>
        <p v-if="mergePreviewing" class="mt-3 text-sm text-muted">Checking what would happen…</p>
        <UAlert v-if="mergeError" class="mt-3" color="error" variant="subtle" icon="i-lucide-triangle-alert" :description="mergeError" />
        <template v-if="mergePreview">
          <UAlert
            v-if="mergeTarget && !mergeTarget.hasData"
            class="mt-3"
            color="warning"
            variant="subtle"
            icon="i-lucide-triangle-alert"
            :description="`${mergeTarget.rider} has no data yet. Check the name: this would move everything onto a rider that does not exist.`"
          />
          <UAlert
            v-for="w in mergePreview.warnings"
            :key="w"
            class="mt-3"
            color="warning"
            variant="solid"
            icon="i-lucide-triangle-alert"
            title="Read this before merging"
            :description="w"
          />
          <div v-if="mergePreview.dropped.length" class="mt-3 rounded-md bg-warning/10 p-3 ring ring-warning/40">
            <p class="text-sm font-medium text-highlighted">These are dropped, and the merge cannot bring them back:</p>
            <ul class="mt-1 flex list-disc flex-col gap-1 pl-5 text-xs text-toned">
              <li v-for="(d, i) in mergePreview.dropped" :key="i">
                <span class="tabular-nums text-highlighted">{{ d.table }}<template v-if="d.what"> ({{ d.what }})</template></span>:
                {{ d.consequence }}
              </li>
            </ul>
          </div>
          <p class="mt-3 text-sm text-highlighted">
            {{ mergePreview.moved }} moved · {{ mergePreview.keptTarget }} kept from {{ mergePreview.into }} ·
            {{ mergePreview.droppedSource }} dropped from {{ mergePreview.from }} ·
            {{ mergePreview.merged }} merged
          </p>
          <div class="mt-2 max-h-56 overflow-auto rounded-md ring ring-default">
            <table class="w-full text-left text-xs">
              <thead class="text-dimmed">
                <tr>
                  <th class="p-2 font-normal">Table</th>
                  <th class="p-2 text-right font-normal">Moved</th>
                  <th class="p-2 text-right font-normal">Kept</th>
                  <th class="p-2 text-right font-normal">Dropped</th>
                  <th class="p-2 text-right font-normal">Merged</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-default">
                <tr v-for="t in mergePreview.tables" :key="t.table">
                  <td class="p-2 tabular-nums">{{ t.table }}</td>
                  <td class="p-2 text-right">{{ t.moved }}</td>
                  <td class="p-2 text-right">{{ t.keptTarget }}</td>
                  <td class="p-2 text-right" :class="t.droppedSource ? 'text-warning' : ''">{{ t.droppedSource }}</td>
                  <td class="p-2 text-right">{{ t.merged }}</td>
                </tr>
                <tr v-if="!mergePreview.tables.length">
                  <td class="p-2 text-muted" colspan="5">Nothing to move.</td>
                </tr>
              </tbody>
            </table>
          </div>
          <UCheckbox
            v-model="mergeConfirmed"
            class="mt-3"
            :label="`Merge ${mergePreview.from} into ${mergePreview.into}, and sign both riders out`"
          />
        </template>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" :disabled="merging" @click="closeMerge">Cancel</UButton>
          <UButton icon="i-lucide-git-merge" color="error" :loading="merging" :disabled="!canMerge" @click="confirmMerge">
            Merge
          </UButton>
        </div>
      </template>
    </UModal>

    <UModal :open="!!deleteTarget" title="Delete this person?" @update:open="deleteTarget = null">
      <template #body>
        <p class="text-sm text-toned">
          Permanently deletes {{ deleteTarget?.email }}'s sign-in. There is no undo.
        </p>
        <UFormField
          label="Local rider identity to also remove"
          hint="their routes, linked devices and crew membership — the rider they are bound to, else a guess: check or clear it"
          class="mt-3"
        >
          <UInput v-model="deleteRider" placeholder="(leave empty to skip local data)" class="w-full" />
        </UFormField>
        <UAlert
          v-if="otherAccounts > 0"
          class="mt-3"
          color="warning"
          variant="subtle"
          icon="i-lucide-triangle-alert"
          :description="`${otherAccounts} other account${otherAccounts === 1 ? ' is' : 's are'} also bound to ${deleteInfo?.rider}. Their data goes too and they lose the rider. Clear the field to keep it.`"
        />
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" :disabled="!!deletingPerson" @click="deleteTarget = null">
            Cancel
          </UButton>
          <UButton color="error" :loading="!!deletingPerson" @click="confirmDeletePerson">Delete</UButton>
        </div>
      </template>
    </UModal>
  </AppPage>
</template>
