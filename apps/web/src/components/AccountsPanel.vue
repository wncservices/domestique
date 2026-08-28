<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { Account, GarminConnection, GarminDevice, Me, WahooConnection } from '@/api/types'
import GarminSignIn from '@/components/GarminSignIn.vue'

const props = defineProps<{
  accounts: Account[]
  me?: Me | null
  canManage: boolean
  garmin: GarminConnection
  wahoo: WahooConnection
}>()
const emit = defineEmits<{
  changed: []
  garminChanged: [GarminConnection]
  wahooChanged: [WahooConnection]
}>()

// Garmin is linked by signing in, so its button opens a dialog. Wahoo is a
// redirect to its own consent screen — see connectWahooHref below — the
// one place either now happens: this used to also be linkable with a bare
// POST that created an account with no OAuth session behind it at all,
// duplicating (and, unlike this one, not actually authorizing) what
// Settings' own Wahoo card already did.
const signingIn = ref(false)

const route = useRoute()
const connectWahooHref = computed(
  () => `/wahoo/connect?return_to=${encodeURIComponent(route.fullPath)}`,
)

const toast = useToast()

// The head units on the connected Garmin account.
//
// Linking an account does not tell a rider whether their Edge will see a
// course; naming their devices does. Fetched separately from the account list
// because it is a live call to Garmin, and a slow or unhappy Connect must not
// hold up the rest of the panel.
const devices = ref<GarminDevice[]>([])
const devicesError = ref('')

async function loadDevices() {
  if (!props.garmin.connected) {
    devices.value = []
    return
  }
  devicesError.value = ''
  try {
    devices.value = await api.garminDevices()
  } catch (err) {
    devicesError.value = err instanceof Error ? err.message : String(err)
  }
}

onMounted(loadDevices)
watch(() => props.garmin.connected, loadDevices)

function lastSync(device: GarminDevice): string {
  if (!device.lastSync) return 'never synced'
  return `last synced ${new Date(device.lastSync).toLocaleDateString()}`
}

const unlinking = ref('')
const error = ref('')
const unlinkTarget = ref<Account | null>(null)
const togglingAutoPush = ref('')

async function toggleAutoPush(account: Account, enabled: boolean) {
  togglingAutoPush.value = account.id
  error.value = ''
  try {
    await api.setAccountAutoPush(account.id, enabled)
    emit('changed')
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    togglingAutoPush.value = ''
  }
}

// The reconcile pass now watches for this server-side (a Warn log and a
// metric an alert rule can watch), but a rider looking at Settings before
// that ever fires is still worth telling — the fields have been in the API
// response all along, just never rendered. 30 days is deliberately wider
// than the server's own 14-day warn window: a badge costs nothing to show
// a little early, where a log line every tick for a month would be noise.
const garminExpiry = computed(() => {
  if (!props.garmin.expiresAt) return null
  const days = Math.ceil((new Date(props.garmin.expiresAt).getTime() - Date.now()) / 86_400_000)
  if (props.garmin.expired || days <= 0) {
    return { label: 'sign-in expired', color: 'error' as const }
  }
  if (days <= 30) {
    return { label: `sign-in expires in ${days} day${days === 1 ? '' : 's'}`, color: 'warning' as const }
  }
  return null
})

/** One account per rider per provider, so hide what is already linked. */
const linkableGarmin = computed(
  () => !props.accounts.some((a) => a.provider === 'garmin' && isMine(a)),
)
const linkableWahoo = computed(
  () => !props.accounts.some((a) => a.provider === 'wahoo' && isMine(a)),
)

function onGarminChanged(connection: GarminConnection) {
  emit('garminChanged', connection)
  emit('changed')
}

function isMine(account: Account): boolean {
  const user = props.me?.user
  if (!user) return account.mine
  return account.rider.toLowerCase() === user.toLowerCase()
}

// accounts (the prop) is every device this rider can see — their own and a
// crew fellow's, per server.go's listableAccounts — because PlanPanel and
// RouteCard's SyncBadge both need the wider set to show push targets and
// sync status across a crew. Settings is a different question: "what have
// I linked," not "what can I see." A crew-wide view of who links what
// belongs on a crew page of its own, not folded into everyone's personal
// Settings — this just narrows what this one panel renders.
const myAccounts = computed(() => props.accounts.filter(isMine))

async function unlink(account: Account) {
  unlinkTarget.value = null
  unlinking.value = account.id
  error.value = ''
  try {
    // Unlinking a Garmin or Wahoo account has to forget the sign-in behind
    // it too, or it comes back on the next sign-in attached to a session
    // the rider thought they had removed. Each provider's own disconnect
    // endpoint does both in one step.
    if (account.provider === 'garmin' && isMine(account)) {
      emit('garminChanged', await api.garminDisconnect())
      toast.add({ title: `${account.label} unlinked`, icon: 'i-lucide-unlink', color: 'success' })
      emit('changed')
      return
    }
    if (account.provider === 'wahoo' && isMine(account)) {
      emit('wahooChanged', await api.wahooDisconnect())
      toast.add({ title: `${account.label} unlinked`, icon: 'i-lucide-unlink', color: 'success' })
      emit('changed')
      return
    }
    await api.unlinkAccount(account.id)
    toast.add({ title: `${account.label} unlinked`, icon: 'i-lucide-unlink', color: 'success' })
    emit('changed')
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    unlinking.value = ''
  }
}
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 class="font-medium text-highlighted">Head units</h2>
          <p class="text-sm text-muted">
            {{ myAccounts.length }} linked · routes are pushed to these
          </p>
        </div>
        <div v-if="canManage" class="flex gap-2">
          <UButton
            v-if="linkableGarmin"
            icon="i-lucide-watch"
            color="neutral"
            variant="subtle"
            @click="signingIn = true"
          >
            Link Garmin
          </UButton>
          <UTooltip
            v-if="linkableWahoo && !wahoo.canConnect"
            :text="
              wahoo.unavailable ||
              'This deployment has no encryption key, so a connection could not be kept safely.'
            "
          >
            <UButton icon="i-lucide-gauge" color="neutral" variant="subtle" disabled>
              Link Wahoo
            </UButton>
          </UTooltip>
          <UButton
            v-else-if="linkableWahoo"
            :to="connectWahooHref"
            external
            icon="i-lucide-gauge"
            color="neutral"
            variant="subtle"
          >
            Link Wahoo
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

    <!-- Garmin declining to list devices is not a broken connection: courses
         still sync. Said quietly, so it does not read as a failure. -->
    <UAlert
      v-if="devicesError"
      color="warning"
      variant="subtle"
      icon="i-lucide-info"
      :description="devicesError"
      class="mb-4"
    />

    <div v-if="myAccounts.length" class="flex flex-col divide-y divide-default">
      <div
        v-for="account in myAccounts"
        :key="account.id"
        class="flex flex-wrap items-center gap-3 py-2 first:pt-0 last:pb-0"
      >
        <UIcon
          :name="account.provider === 'garmin' ? 'i-lucide-watch' : 'i-lucide-gauge'"
          class="text-dimmed"
        />
        <div class="min-w-0 flex-1">
          <p class="truncate text-sm text-highlighted">{{ account.label }}</p>
          <p class="font-mono text-xs text-dimmed">{{ account.id }}</p>
        </div>

        <UBadge v-if="!account.implemented" color="warning" variant="subtle" size="sm">
          adapter not wired up
        </UBadge>

        <UTooltip
          v-if="account.possibleDuplicateOf?.length"
          :text="`Same ${account.provider} account as ${account.possibleDuplicateOf.join(', ')} — probably worth unlinking one`"
        >
          <UBadge color="warning" variant="subtle" size="sm" icon="i-lucide-triangle-alert">
            possible duplicate
          </UBadge>
        </UTooltip>

        <UTooltip
          v-if="account.provider === 'garmin' && isMine(account) && garminExpiry"
          text="Garmin's sign-in lasts about a year, then pushes to this account fail until you reconnect it above."
        >
          <UBadge :color="garminExpiry!.color" variant="subtle" size="sm" icon="i-lucide-clock-alert">
            {{ garminExpiry!.label }}
          </UBadge>
        </UTooltip>

        <UTooltip
          v-if="account.mine"
          text="Whether auto-sync's unattended push includes this device — only takes effect once auto-sync itself is on in Settings. A manual push always reaches it either way."
        >
          <label class="flex items-center gap-1.5 text-xs text-dimmed">
            <USwitch
              size="sm"
              :model-value="account.autoPush"
              :loading="togglingAutoPush === account.id"
              @update:model-value="(v: boolean) => toggleAutoPush(account, v)"
            />
            Auto-push
          </label>
        </UTooltip>

        <UButton
          v-if="account.mine"
          icon="i-lucide-unlink"
          color="neutral"
          variant="ghost"
          size="xs"
          :loading="unlinking === account.id"
          aria-label="Unlink"
          @click="unlinkTarget = account"
        />

        <!-- The units Connect will sync a pushed course to. Shown under the
             account they belong to rather than as separate rows: they are not
             things to link or unlink, they are who is listening. -->
        <div
          v-if="account.provider === 'garmin' && isMine(account) && devices.length"
          class="w-full pl-7"
        >
          <div class="flex flex-wrap gap-1.5">
            <UTooltip
              v-for="device in devices"
              :key="device.id"
              text="Garmin Connect's own record of when this device last synced with it — not whether Domestique has pushed a route to it. A course reaches the device the next time it syncs with Connect, the same way it always has."
            >
              <UBadge color="neutral" variant="subtle" size="sm" icon="i-lucide-watch">
                {{ device.name }}
                <span class="ml-1 text-dimmed">· {{ lastSync(device) }}</span>
              </UBadge>
            </UTooltip>
          </div>
        </div>
      </div>
    </div>

    <UEmpty
      v-else
      icon="i-lucide-watch"
      title="No head units linked"
      :description="
        canManage
          ? 'Link a Garmin or Wahoo account above. Until then there is nowhere to push routes.'
          : 'Nothing is linked yet, so there is nowhere to push routes.'
      "
    />

    <GarminSignIn v-model:open="signingIn" :connection="props.garmin" @changed="onGarminChanged" />

    <UModal :open="!!unlinkTarget" title="Unlink this account?" @update:open="unlinkTarget = null">
      <template #body>
        <p class="text-sm text-toned">
          “{{ unlinkTarget?.label }}” will stop syncing routes once unlinked.
        </p>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" @click="unlinkTarget = null">Cancel</UButton>
          <UButton
            color="error"
            :loading="!!unlinkTarget && unlinking === unlinkTarget.id"
            @click="unlinkTarget && unlink(unlinkTarget)"
          >
            Unlink
          </UButton>
        </div>
      </template>
    </UModal>
  </UCard>
</template>
