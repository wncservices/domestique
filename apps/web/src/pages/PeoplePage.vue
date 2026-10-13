<script setup lang="ts">
import { onMounted, ref, watch, computed } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api, ApiError } from '@/api/client'
import type { AssignableRole, Person, RiderInfo } from '@/api/types'
import { roleColor } from '@/utils/role'
import { usePagedList } from '@/composables/usePagedList'

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
      error.value = err instanceof Error ? err.message : String(err)
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
    inviteError.value = err instanceof Error ? err.message : String(err)
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
      description: err instanceof Error ? err.message : String(err),
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
      description: err instanceof Error ? err.message : String(err),
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
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    deletingPerson.value = ''
  }
}
</script>

<template>
  <div class="flex flex-col gap-6">
    <UCard variant="outline">
      <template #header>
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 class="flex items-center gap-2 font-medium text-highlighted">
              <UIcon name="i-lucide-users" />
              People
            </h2>
            <p class="text-sm text-muted">
              <template v-if="loading">Loading…</template>
              <template v-else-if="unavailable">Not available on this deployment</template>
              <template v-else>{{ people.length }} with access</template>
            </p>
          </div>
          <div class="flex gap-2">
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

      <UAlert
        v-if="orphanRiders.length"
        color="warning"
        variant="subtle"
        icon="i-lucide-user-x"
        title="Riders with data but no account"
        :description="`${orphanRiders.join(', ')} — nobody can sign in as ${orphanRiders.length === 1 ? 'this rider' : 'these riders'}. Bind an account to one from its row, or remove the data.`"
        class="mb-4"
      />

      <UEmpty
        v-if="unavailable"
        icon="i-lucide-key-round"
        title="Auth0 Management API access is not configured"
        description="An administrator can set DOMESTIQUE_AUTH0_MGMT_CLIENT_ID and DOMESTIQUE_AUTH0_MGMT_CLIENT_SECRET to enable this page."
      />

      <template v-else-if="people.length">
        <div class="flex flex-col divide-y divide-default">
          <div
            v-for="person in pagedPeople"
            :key="person.id"
            class="flex flex-wrap items-center gap-3 py-2 first:pt-0 last:pb-0"
          >
            <div class="min-w-0 flex-1">
              <p class="truncate text-sm text-highlighted">{{ person.name || person.email }}</p>
              <p class="truncate text-xs text-dimmed">{{ person.email }} · {{ lastSeen(person) }}</p>
            </div>
            <UBadge v-if="person.bindingStatus === 'bound'" color="neutral" variant="subtle" size="sm" icon="i-lucide-link">
              {{ person.boundRider }}
            </UBadge>
            <UBadge
              v-else-if="person.bindingStatus === 'needs-attention'"
              color="error"
              variant="subtle"
              size="sm"
              icon="i-lucide-triangle-alert"
            >
              needs attention: {{ person.likelyRider }}
            </UBadge>
            <UBadge
              v-else-if="person.bindingStatus === 'conflict'"
              color="warning"
              variant="subtle"
              size="sm"
              icon="i-lucide-triangle-alert"
            >
              conflict: {{ person.likelyRider }}
            </UBadge>
            <UBadge v-else-if="person.bindingStatus === 'unbound'" color="neutral" variant="outline" size="sm">
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
            <UTooltip
              v-if="person.bindingStatus"
              :text="isOwn(person) ? 'Your own sign-in: only onto a rider nobody is bound to (lockout recovery)' : 'Change which rider this is'"
            >
              <UButton
                size="sm"
                color="neutral"
                variant="ghost"
                icon="i-lucide-link"
                aria-label="Change which rider this is"
                :loading="binding === person.id"
                @click="openBind(person)"
              />
            </UTooltip>
            <UTooltip :text="person.blocked ? 'Unblock' : 'Block'">
              <UButton
                size="sm"
                color="neutral"
                variant="ghost"
                :icon="person.blocked ? 'i-lucide-shield-check' : 'i-lucide-shield-off'"
                :aria-label="person.blocked ? 'Unblock' : 'Block'"
                :loading="togglingBlocked === person.id"
                @click="blockTarget = person"
              />
            </UTooltip>
            <UTooltip text="Delete">
              <UButton
                size="sm"
                color="error"
                variant="ghost"
                icon="i-lucide-trash-2"
                aria-label="Delete"
                :loading="deletingPerson === person.id"
                @click="openDeleteConfirm(person)"
              />
            </UTooltip>
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

      <UEmpty
        v-else-if="!loading"
        icon="i-lucide-user-x"
        title="Nobody has access yet"
        description="Invite the first rider above."
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
  </div>
</template>
