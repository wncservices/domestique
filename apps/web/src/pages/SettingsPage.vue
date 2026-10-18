<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type {
  AutoScheduleSetting,
  AutoSyncSetting,
  BasemapUpdate,
  GarminConnection,
  KomootConnection,
  MfaEnrollment,
  WahooConnection,
} from '@/api/types'
import { useLibrary } from '@/composables/useLibrary'
import AccountsPanel from '@/components/AccountsPanel.vue'
import BasemapSetup from '@/components/BasemapSetup.vue'
import GarminSetup from '@/components/GarminSetup.vue'
import KomootConnect from '@/components/KomootConnect.vue'
import WeatherSettings from '@/components/WeatherSettings.vue'
import MorningSummaryCard from '@/components/fitness/MorningSummaryCard.vue'
import RideStartCard from '@/components/plan/RideStartCard.vue'
import AppPage from '@/components/ui/AppPage.vue'
import DetailsToggle from '@/components/ui/DetailsToggle.vue'
import { settingsSections } from '@/utils/settingsSections'
import { describeError } from '@/utils/errorMessage'

const {
  accounts,
  me,
  config,
  canManageAccounts,
  canManageSettings,
  canImportKomoot,
  canManageTraining,
  routingConfigured,
  komootEnabled,
  refresh,
} = useLibrary()
const toast = useToast()
const route = useRoute()
const router = useRouter()

// --- sub-nav: anchors into one long page, not routes ---

const sections = computed(() => settingsSections(me.value?.role ?? 'none'))
const generalSections = computed(() => sections.value.filter((x) => !x.admin))
const adminSections = computed(() => sections.value.filter((x) => x.admin))
const current = ref('')

function goTo(id: string) {
  current.value = id
  // The hash watcher scrolls; scroll here too for the case where the hash
  // is already this one and so never fires.
  document.getElementById(id)?.scrollIntoView({ block: 'start' })
  router.replace({ hash: `#${id}` })
}

// Same shape as the Fitness page's #profile jump: the panels above a section
// load after mount and shift it, so scroll once now and again once they settle,
// unless the rider has scrolled in the meantime.
async function scrollToHash(loads: Promise<unknown>[]) {
  const id = route.hash.slice(1)
  if (!id) return
  current.value = id
  const jump = () => document.getElementById(id)?.scrollIntoView({ block: 'start' })
  await nextTick()
  jump()
  const startY = window.scrollY
  await Promise.allSettled(loads)
  await nextTick()
  if (Math.abs(window.scrollY - startY) < 2) jump()
}

// Re-clicking a link or editing the URL while already here changes only the
// hash, and the mount-time scroll never runs again.
watch(
  () => route.hash,
  (hash) => {
    const id = hash.slice(1)
    if (!id || !document.getElementById(id)) return
    current.value = id
    document.getElementById(id)?.scrollIntoView({ block: 'start' })
  },
)

const aboutAutoSync = ref(false)
const aboutAutoSchedule = ref(false)

// --- profile: name + password, both proxied through Auth0's Management API
// (see meDTO.canEditName/canChangePassword on the server) ---

const nameInput = ref('')
const savingName = ref(false)

// Keep the field in step with the signed-in identity — most relevantly
// right after a save round-trips through refresh().
watch(
  () => me.value?.name,
  (name) => {
    nameInput.value = name ?? ''
  },
  { immediate: true },
)

async function saveName() {
  const name = nameInput.value.trim()
  if (!name || name === me.value?.name) return
  savingName.value = true
  try {
    await api.updateMe(name)
    await refresh()
    toast.add({ title: 'Name updated', icon: 'i-lucide-check', color: 'success' })
  } catch (err) {
    toast.add({
      title: 'Could not update your name',
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    savingName.value = false
  }
}

const sendingPasswordReset = ref(false)

async function sendPasswordReset() {
  sendingPasswordReset.value = true
  try {
    await api.sendPasswordReset()
    toast.add({
      title: 'Password reset email sent',
      description: `Check ${me.value?.email} for a link to set a new password.`,
      icon: 'i-lucide-mail-check',
      color: 'success',
    })
  } catch (err) {
    toast.add({
      title: 'Could not send the reset email',
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    sendingPasswordReset.value = false
  }
}

// --- two-factor authentication: this app never renders a QR code itself —
// enrolling and confirming a new factor both happen on Auth0's own hosted
// Guardian page, reached through a one-time ticket URL. This app only lists
// what's already enrolled and lets the rider remove a factor. ---

const mfaEnrollments = ref<MfaEnrollment[]>([])
const loadingMfa = ref(false)
const enrolling = ref(false)
const removingFactorId = ref('')
const removeTarget = ref<MfaEnrollment | null>(null)
const removingFactor = ref(false)
// Set whenever a ticket URL might not have opened on its own — see
// startMfaEnroll. A real <a> the rider clicks themselves always works,
// unlike a second window.open attempt.
const enrollUrl = ref('')

const factorLabels: Record<string, string> = {
  totp: 'Authenticator app',
  sms: 'Text message',
  email: 'Email',
  'push-notification': 'Push notification',
  'webauthn-roaming': 'Security key',
  'webauthn-platform': 'Device passkey',
  'recovery-code': 'Recovery codes',
}

function factorLabel(type: string): string {
  return factorLabels[type] ?? type
}

async function loadMfa() {
  if (!me.value?.canEditName) return
  loadingMfa.value = true
  try {
    mfaEnrollments.value = await api.mfaEnrollments()
  } catch (err) {
    toast.add({
      title: 'Could not load two-factor authentication',
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    loadingMfa.value = false
  }
}

// Opens Auth0's own hosted enrollment page in a new tab — nothing to embed,
// and the rider is still on Settings in this tab when they're done, so a
// manual refresh (rather than guessing when they've finished) picks it up.
//
// window.open here runs after an await, which means it is no longer inside
// the click's own user-activation window by the time the response comes
// back — browsers (Safari always, Chrome/Firefox often, depending on how
// long the round trip took) silently block it instead of opening the tab.
// There is no reliable cross-browser way to detect that after the fact
// (a blocked call can return either null or a window that immediately
// closes itself), so rather than guessing, a real link stays visible until
// the rider clicks it themselves — an actual click on an <a> is always a
// trusted user gesture, so it can never be blocked the way a second
// window.open attempt could be.
async function startMfaEnroll() {
  enrolling.value = true
  enrollUrl.value = ''
  try {
    const { ticketUrl } = await api.enrollMfa()
    window.open(ticketUrl, '_blank', 'noopener')
    enrollUrl.value = ticketUrl
    toast.add({
      title: 'Enrollment ready',
      description: 'Opened in a new tab — if you didn\'t see it, use the link below.',
      icon: 'i-lucide-external-link',
      color: 'info',
    })
  } catch (err) {
    toast.add({
      title: 'Could not start enrollment',
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    enrolling.value = false
  }
}

async function removeMfaFactor() {
  const target = removeTarget.value
  if (!target) return
  removingFactor.value = true
  removingFactorId.value = target.id
  try {
    await api.removeMfaEnrollment(target.id)
    toast.add({ title: 'Removed', icon: 'i-lucide-check', color: 'success' })
    removeTarget.value = null
    await loadMfa()
  } catch (err) {
    toast.add({
      title: 'Could not remove it',
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    removingFactor.value = false
    removingFactorId.value = ''
  }
}

// --- delete my account: every trace of this rider's own data, then their
// Auth0 identity, then the session itself. Irreversible — see api.deleteMe's
// own doc comment. ---

const deleteAccountOpen = ref(false)
const deletingAccount = ref(false)

async function deleteMyAccount() {
  deletingAccount.value = true
  try {
    const { redirectTo } = await api.deleteMe()
    // Full page navigation, not a router push — the session is gone and so
    // is the account, there is nothing left in this app for the SPA to show.
    window.location.href = redirectTo
  } catch (err) {
    toast.add({
      title: 'Could not delete your account',
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
    deletingAccount.value = false
    deleteAccountOpen.value = false
  }
}

// Settings owns the Komoot connection now: this is the page where sign-ins
// live, and the Add page only consumes the result.
const connection = ref<KomootConnection>({ connected: false, shared: false, canConnect: false })
const connectionError = ref('')

const garmin = ref<GarminConnection>({ connected: false, canConnect: false })
const garminError = ref('')

const wahoo = ref<WahooConnection>({ connected: false, canConnect: false })
const wahooError = ref('')

async function loadConnection() {
  if (!komootEnabled.value || !canImportKomoot.value) return
  try {
    connection.value = await api.komootConnection()
  } catch (err) {
    connectionError.value = describeError(err)
  }
}

// --- auto-sync: a deployment-wide switch, not per-rider — every upload or
// edit pushes to devices on its own once it's on, with nobody clicking
// "Push to devices" ---

const autoSync = ref<AutoSyncSetting | null>(null)
const togglingAutoSync = ref(false)

// --- auto-schedule: same deployment-wide shape as auto-sync, for training
// instead of routes — every rider's current plan week gets scheduled into
// real workouts on its own, with nobody clicking "Schedule this week's
// workouts" ---

const autoSchedule = ref<AutoScheduleSetting | null>(null)
const togglingAutoSchedule = ref(false)

// --- basemap: the tiles component's map data, updated from a button
// instead of the pmtiles extract + kubectl cp runbook ---

const basemap = ref<BasemapUpdate | null>(null)

async function loadBasemap() {
  if (!canManageSettings.value) return
  try {
    basemap.value = await api.basemap()
  } catch (err) {
    toast.add({
      title: 'Could not read the basemap status',
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  }
}

// Self-rescheduling rather than setInterval: each tick decides for itself
// whether there is anything left to poll for, so this naturally stops the
// moment a run finishes instead of ticking forever in the background.
let basemapPollTimer: ReturnType<typeof setTimeout> | undefined

async function pollBasemapWhileRunning() {
  await loadBasemap()
  if (basemap.value?.status === 'pending' || basemap.value?.status === 'running') {
    basemapPollTimer = setTimeout(pollBasemapWhileRunning, 4000)
  }
}

onUnmounted(() => {
  if (basemapPollTimer) clearTimeout(basemapPollTimer)
})

async function loadAutoSync() {
  if (!canManageSettings.value) return
  try {
    autoSync.value = await api.autoSync()
  } catch (err) {
    toast.add({
      title: 'Could not read the auto-sync setting',
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  }
}

async function toggleAutoSync(enabled: boolean) {
  togglingAutoSync.value = true
  try {
    autoSync.value = await api.setAutoSync(enabled)
    toast.add({
      title: enabled ? 'Auto-sync turned on' : 'Auto-sync turned off',
      description: enabled
        ? 'Every upload or edit will push to devices on its own from now on.'
        : "Uploads and edits will wait for someone to click “Push to devices” again.",
      icon: 'i-lucide-check',
      color: 'success',
    })
  } catch (err) {
    toast.add({
      title: 'Could not change auto-sync',
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    togglingAutoSync.value = false
  }
}

async function loadAutoSchedule() {
  if (!canManageSettings.value) return
  try {
    autoSchedule.value = await api.autoSchedule()
  } catch (err) {
    toast.add({
      title: 'Could not read the auto-schedule setting',
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  }
}

async function toggleAutoSchedule(enabled: boolean) {
  togglingAutoSchedule.value = true
  try {
    autoSchedule.value = await api.setAutoSchedule(enabled)
    toast.add({
      title: enabled ? 'Auto-schedule turned on' : 'Auto-schedule turned off',
      description: enabled
        ? "Every rider's current plan week will be scheduled into real workouts on its own from now on."
        : 'Riders will need to click "Schedule this week\'s workouts" themselves again.',
      icon: 'i-lucide-check',
      color: 'success',
    })
  } catch (err) {
    toast.add({
      title: 'Could not change auto-schedule',
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    togglingAutoSchedule.value = false
  }
}

async function loadGarmin() {
  if (!canManageAccounts.value) return
  try {
    garmin.value = await api.garminConnection()
  } catch (err) {
    garminError.value = describeError(err)
  }
}

async function loadWahoo() {
  if (!canManageAccounts.value) return
  try {
    wahoo.value = await api.wahooConnection()
  } catch (err) {
    wahooError.value = describeError(err)
  }
}

// Connecting or disconnecting Wahoo links or unlinks the head unit, same as
// Garmin — the accounts list is stale the moment either changes.
async function wahooChanged(next: WahooConnection) {
  wahoo.value = next
  await refresh()
}

// Signing in to Garmin links the head unit, so the accounts list is stale the
// moment either of those changes.
async function garminChanged(next: GarminConnection) {
  garmin.value = next
  await refresh()
}

// Cards arrive asynchronously, so a section is only called empty once every
// load behind it has settled; otherwise the fallback would flash first.
const loaded = ref(false)
const deploymentEmpty = computed(
  () => loaded.value && !autoSync.value?.canManage && !autoSchedule.value?.canManage && !garmin.value.consumer && !config.value?.source,
)
const basemapEmpty = computed(() => loaded.value && !basemap.value?.canManage)
const notificationsEmpty = computed(() => !canManageTraining.value)

onMounted(async () => {
  // Wait for the shared state first. Whether Komoot is worth asking about
  // depends on the config and the caller's permissions, and mounting a page
  // races the shell's first fetch — without this the card renders and then
  // reports "no encryption key" because it never got to ask.
  await refresh()
  const loads = [
    loadConnection(),
    loadGarmin(),
    loadWahoo(),
    loadMfa(),
    loadAutoSync(),
    loadAutoSchedule(),
    pollBasemapWhileRunning(),
  ]
  scrollToHash(loads)
  await Promise.all(loads)
  loaded.value = true
})
</script>

<template>
  <AppPage title="Settings" subtitle="Your account, your devices and how Domestique trains and warns you.">
    <div class="flex flex-col gap-6 lg:grid lg:grid-cols-[12rem_minmax(0,1fr)] lg:items-start lg:gap-10">
      <!-- One nav for both widths: a horizontal scrolling row on a phone, a
           sticky column from lg up, so the landmark is announced once. -->
      <nav aria-label="Settings" class="flex gap-4 overflow-x-auto lg:sticky lg:top-20 lg:flex-col lg:gap-3 lg:overflow-visible">
        <ul class="flex gap-1 lg:flex-col">
          <li v-for="section in generalSections" :key="section.id" class="shrink-0">
            <a
              :href="`#${section.id}`"
              class="block rounded-md px-3 py-1.5 text-[length:var(--text-body)] text-muted hover:bg-elevated hover:text-highlighted focus-visible:outline-2 focus-visible:outline-primary aria-[current=location]:bg-primary/10 aria-[current=location]:font-medium aria-[current=location]:text-primary"
              :aria-current="current === section.id ? 'location' : undefined"
              @click.prevent="goTo(section.id)"
            >
              {{ section.label }}
            </a>
          </li>
        </ul>
        <div v-if="adminSections.length" class="flex items-center gap-1 lg:flex-col lg:items-stretch">
          <p class="shrink-0 px-3 text-[length:var(--text-label)] text-dimmed" aria-hidden="true">Admin</p>
          <ul aria-label="Admin" class="flex gap-1 lg:flex-col">
            <li v-for="section in adminSections" :key="section.id" class="shrink-0">
              <a
              :href="`#${section.id}`"
              class="block rounded-md px-3 py-1.5 text-[length:var(--text-body)] text-muted hover:bg-elevated hover:text-highlighted focus-visible:outline-2 focus-visible:outline-primary aria-[current=location]:bg-primary/10 aria-[current=location]:font-medium aria-[current=location]:text-primary"
              :aria-current="current === section.id ? 'location' : undefined"
              @click.prevent="goTo(section.id)"
            >
              {{ section.label }}
            </a>
            </li>
          </ul>
        </div>
      </nav>

      <div class="flex min-w-0 flex-col gap-10">
        <section id="profile" class="scroll-mt-20 flex flex-col gap-4" aria-labelledby="profile-title">
          <div>
            <h2 id="profile-title" class="text-[length:var(--text-title)] text-highlighted">Profile</h2>
            <p class="text-[length:var(--text-body)] text-muted">Your own name and sign-in, for this account only.</p>
          </div>

          <p class="text-[length:var(--text-body)] text-toned">
            Signed in as
            <span class="text-highlighted">
              {{ me?.authenticated ? `${me.name || me.user} (${me.role})` : 'nobody — every visitor is an admin' }}
            </span>
          </p>

          <!-- Only ever available under authMode oidc, with Auth0 Management API
               credentials configured — meDTO omits both flags (defaulting them
               false) for a proxy/none deployment or one without that client. -->
          <UCard v-if="me?.canEditName" variant="outline">
            <div class="flex flex-col gap-4">
              <UFormField label="Name">
                <div class="flex max-w-sm gap-2">
                  <UInput v-model="nameInput" class="flex-1" @keyup.enter="saveName" />
                  <UButton
                    :loading="savingName"
                    :disabled="!nameInput.trim() || nameInput.trim() === me?.name"
                    @click="saveName"
                  >
                    Save
                  </UButton>
                </div>
              </UFormField>

              <div>
                <p class="mb-1 text-[length:var(--text-body)] font-medium text-toned">Password</p>
                <UButton
                  v-if="me?.canChangePassword"
                  size="sm"
                  variant="soft"
                  icon="i-lucide-mail"
                  :loading="sendingPasswordReset"
                  @click="sendPasswordReset"
                >
                  Email me a reset link
                </UButton>
                <p v-else class="text-[length:var(--text-label)] text-dimmed">
                  You sign in with an external provider — there's no password here to change.
                </p>
              </div>

              <div>
                <div class="mb-1 flex items-center gap-2">
                  <p class="text-[length:var(--text-body)] font-medium text-toned">Two-factor authentication</p>
                  <UButton
                    size="xs"
                    color="neutral"
                    variant="ghost"
                    icon="i-lucide-refresh-cw"
                    :loading="loadingMfa"
                    aria-label="Refresh"
                    @click="loadMfa"
                  />
                </div>

                <div v-if="mfaEnrollments.length" class="flex flex-col gap-2">
                  <div
                    v-for="factor in mfaEnrollments"
                    :key="factor.id"
                    class="flex items-center gap-2 text-[length:var(--text-body)]"
                  >
                    <UIcon name="i-lucide-shield-check" class="size-4 text-dimmed" />
                    <span class="flex-1 text-toned">
                      {{ factorLabel(factor.type) }}<span v-if="factor.name"> · {{ factor.name }}</span>
                    </span>
                    <UBadge
                      :color="factor.status === 'confirmed' ? 'success' : 'warning'"
                      variant="subtle"
                      size="sm"
                    >
                      {{ factor.status === 'confirmed' ? 'Active' : 'Pending' }}
                    </UBadge>
                    <UButton
                      size="xs"
                      color="neutral"
                      variant="ghost"
                      icon="i-lucide-x"
                      :aria-label="`Remove ${factorLabel(factor.type)}`"
                      :loading="removingFactor && removingFactorId === factor.id"
                      @click="removeTarget = factor"
                    />
                  </div>
                </div>
                <p v-else class="mb-2 text-[length:var(--text-label)] text-dimmed">Nothing set up yet.</p>

                <div class="mt-2 flex flex-wrap items-center gap-2">
                  <UButton
                    size="sm"
                    variant="soft"
                    icon="i-lucide-shield-plus"
                    :loading="enrolling"
                    @click="startMfaEnroll"
                  >
                    Add a factor
                  </UButton>
                  <!-- The new tab this opens on click can be silently blocked by
                       the browser (see startMfaEnroll's own comment) — this stays
                       up as a guaranteed-to-work fallback until the rider uses it
                       or starts a fresh attempt. -->
                  <UButton
                    v-if="enrollUrl"
                    :to="enrollUrl"
                    external
                    target="_blank"
                    rel="noopener noreferrer"
                    size="sm"
                    variant="outline"
                    color="neutral"
                    icon="i-lucide-external-link"
                  >
                    Continue in Auth0
                  </UButton>
                </div>
              </div>
            </div>
          </UCard>

          <!-- Same gating as the Profile card above: only ever available under
               authMode oidc with Auth0 Management API credentials configured.
               Kept at the foot of the section, away from the forms — a
               destructive, rarely-used action doesn't need to compete for
               attention with the rest. -->
          <div v-if="me?.canEditName" class="flex justify-end">
            <UButton size="sm" color="error" variant="ghost" icon="i-lucide-trash-2" @click="deleteAccountOpen = true">
              Delete my account
            </UButton>
          </div>
        </section>

        <section id="devices" class="scroll-mt-20 flex flex-col gap-4" aria-labelledby="devices-title">
          <div>
            <h2 id="devices-title" class="text-[length:var(--text-title)] text-highlighted">Devices and sync</h2>
            <p class="text-[length:var(--text-body)] text-muted">Head units and accounts your routes are pushed to or imported from.</p>
          </div>

          <AccountsPanel
            :accounts="accounts"
            :me="me"
            :can-manage="canManageAccounts"
            :garmin="garmin"
            :wahoo="wahoo"
            @changed="refresh"
            @garmin-changed="garminChanged"
            @wahoo-changed="wahooChanged"
          />

          <UAlert
            v-if="garminError"
            color="error"
            variant="subtle"
            icon="i-lucide-triangle-alert"
            :description="garminError"
          />

          <UAlert
            v-if="wahooError"
            color="error"
            variant="subtle"
            icon="i-lucide-triangle-alert"
            :description="wahooError"
          />

          <UCard v-if="canImportKomoot && komootEnabled" variant="outline">
            <template #header>
              <h3 class="flex items-center gap-2 font-medium text-highlighted">
                <UIcon name="i-lucide-mountain-snow" />
                Komoot
              </h3>
              <p class="text-[length:var(--text-body)] text-muted">Sign in to import your own planned routes.</p>
            </template>

            <UAlert
              v-if="connectionError"
              color="error"
              variant="subtle"
              icon="i-lucide-triangle-alert"
              :description="connectionError"
              class="mb-4"
            />

            <KomootConnect :connection="connection" @changed="connection = $event" />
          </UCard>
        </section>

        <section id="training" class="scroll-mt-20 flex flex-col gap-4" aria-labelledby="training-title">
          <div>
            <h2 id="training-title" class="text-[length:var(--text-title)] text-highlighted">Training</h2>
            <p class="text-[length:var(--text-body)] text-muted">Where your rides start and how your plan gets scheduled.</p>
          </div>

          <div v-if="canManageTraining" class="flex flex-col gap-4">
            <p class="text-[length:var(--text-body)] text-muted">
              Your FTP and the rest of your training profile live on
              <RouterLink to="/fitness#profile" class="text-primary underline underline-offset-2">Fitness</RouterLink>.
            </p>

            <RideStartCard :routing-configured="routingConfigured" />

          </div>
          <p v-else class="text-[length:var(--text-body)] text-dimmed">Nothing to set here for your role.</p>
        </section>

        <section id="notifications" class="scroll-mt-20 flex flex-col gap-4" aria-labelledby="notifications-title">
          <div>
            <h2 id="notifications-title" class="text-[length:var(--text-title)] text-highlighted">Notifications</h2>
            <p class="text-[length:var(--text-body)] text-muted">What Domestique warns you about for planned rides.</p>
          </div>

          <!-- A rider's own setting. The card hides itself when the deployment
               has weather switched off. -->
          <WeatherSettings v-if="canManageTraining" />
          <!-- The morning email is a notification, so it is set here; the card
               hides itself when the deployment has no mail configured. -->
          <MorningSummaryCard v-if="canManageTraining" />
          <p v-if="notificationsEmpty" class="text-[length:var(--text-body)] text-dimmed">Nothing to set here for your role.</p>
        </section>

        <section v-if="adminSections.length" id="deployment" class="scroll-mt-20 flex flex-col gap-4" aria-labelledby="deployment-title">
          <div>
            <h2 id="deployment-title" class="text-[length:var(--text-title)] text-highlighted">Deployment</h2>
            <p class="text-[length:var(--text-body)] text-muted">Settings that apply to every rider on this deployment.</p>
          </div>

          <div class="flex flex-col gap-4">
            <!-- Deployment plumbing, and only an admin gets it: the same pattern
                 the Garmin setup / "This deployment" cards further down follow.
                 Lives under Deployment: it is a deployment-wide switch. -->
            <UCard v-if="autoSync?.canManage" variant="outline">
              <template #header>
                <h3 class="flex items-center gap-2 font-medium text-highlighted">
                  <UIcon name="i-lucide-refresh-cw" />
                  Auto-sync
                </h3>
                <p class="text-[length:var(--text-body)] text-muted">
                  Pull in new routes from riders' connected accounts and push what changed out to devices on its own.
                </p>
              </template>

              <label class="flex w-fit items-center gap-2 text-[length:var(--text-body)] text-toned">
                <USwitch
                  :model-value="autoSync.enabled"
                  :loading="togglingAutoSync"
                  @update:model-value="toggleAutoSync"
                />
                {{ autoSync.enabled ? 'On' : 'Off' }}
              </label>
              <p v-if="autoSync.updatedBy" class="mt-2 text-[length:var(--text-label)] text-dimmed">
                Last changed by {{ autoSync.updatedBy }}.
              </p>
              <DetailsToggle v-model:open="aboutAutoSync" label="Learn more" class="mt-3">
                <p class="text-[length:var(--text-body)] text-muted">
                  Whether this deployment pulls in new routes from a rider's connected Wahoo, Komoot or
                  Garmin on its own, then pushes what changed out to devices — for every rider, without
                  anyone clicking "Push to devices". Which of a rider's own devices actually get pushed
                  to is set per device, under Devices and sync.
                </p>
              </DetailsToggle>
            </UCard>

            <!-- Same deployment-wide shape as auto-sync, for training instead of
                 routes, and likewise admin only. -->
            <UCard v-if="autoSchedule?.canManage" variant="outline">
              <template #header>
                <h3 class="flex items-center gap-2 font-medium text-highlighted">
                  <UIcon name="i-lucide-calendar-clock" />
                  Auto-schedule
                </h3>
                <p class="text-[length:var(--text-body)] text-muted">
                  Sync every rider's completed sessions and schedule their current plan week on its own.
                </p>
              </template>

              <label class="flex w-fit items-center gap-2 text-[length:var(--text-body)] text-toned">
                <USwitch
                  :model-value="autoSchedule.enabled"
                  :loading="togglingAutoSchedule"
                  @update:model-value="toggleAutoSchedule"
                />
                {{ autoSchedule.enabled ? 'On' : 'Off' }}
              </label>
              <p v-if="autoSchedule.updatedBy" class="mt-2 text-[length:var(--text-label)] text-dimmed">
                Last changed by {{ autoSchedule.updatedBy }}.
              </p>
              <DetailsToggle v-model:open="aboutAutoSchedule" label="Learn more" class="mt-3">
                <p class="text-[length:var(--text-body)] text-muted">
                  Whether training runs on its own: every rider's completed Garmin/Wahoo sessions are
                  synced in the background, and their current plan week is scheduled into real workouts
                  — for every rider with a goal and a filled-in fitness profile, without anyone visiting
                  the Training page to click "Sync now" or "Schedule this week's workouts".
                </p>
              </DetailsToggle>
            </UCard>

            <!-- Deployment plumbing, and only an admin gets it: the API omits the
                 consumer entirely for everyone else, so this card does not exist for
                 a rider. Nothing here is theirs to set or worth them knowing. -->
            <UCard v-if="garmin.consumer" variant="outline">
              <template #header>
                <h3 class="flex items-center gap-2 font-medium text-highlighted">
                  <UIcon name="i-lucide-watch" />
                  Garmin setup
                </h3>
                <p class="text-[length:var(--text-body)] text-muted">
                  One pair of app keys for the whole deployment, so riders can sign in.
                </p>
              </template>

              <GarminSetup :consumer="garmin.consumer" @changed="loadGarmin" />
            </UCard>

            <!-- Deployment plumbing, and only an admin gets it: the API omits
                 Source entirely for everyone else (it names the database host and
                 port), the same pattern the Garmin setup card above follows. -->
            <UCard v-if="config?.source" variant="outline">
              <template #header>
                <h3 class="flex items-center gap-2 font-medium text-highlighted">
                  <UIcon name="i-lucide-info" />
                  This deployment
                </h3>
              </template>

              <dl class="grid gap-3 text-[length:var(--text-body)] sm:grid-cols-2">
                <div>
                  <dt class="text-dimmed">Library</dt>
                  <dd class="tabular-nums text-[length:var(--text-label)] break-all text-highlighted">{{ config.source }}</dd>
                </div>
              </dl>
            </UCard>
          </div>
          <p v-if="deploymentEmpty" class="text-[length:var(--text-body)] text-dimmed">Nothing to set here.</p>
        </section>

        <section v-if="adminSections.length" id="basemap" class="scroll-mt-20 flex flex-col gap-4" aria-labelledby="basemap-title">
          <div>
            <h2 id="basemap-title" class="text-[length:var(--text-title)] text-highlighted">Basemap</h2>
            <p class="text-[length:var(--text-body)] text-muted">
              Tile data behind a route's map — self-hosted, so a rider's coordinates never reach a third party.
            </p>
          </div>

          <!-- Deployment plumbing, and only an admin gets it — same gating as
               auto-sync above, since (unlike the Garmin/config cards) the DTO
               here is shaped to distinguish "unavailable" from "not your job,"
               not to omit itself for a rider. -->
          <div class="flex flex-col gap-4">
            <UCard v-if="basemap?.canManage" variant="outline">
              <template #header>
                <h3 class="flex items-center gap-2 font-medium text-highlighted">
                  <UIcon name="i-lucide-map" />
                  Map basemap
                </h3>
              </template>

              <BasemapSetup :basemap="basemap" @changed="pollBasemapWhileRunning" />
            </UCard>
          </div>
          <p v-if="basemapEmpty" class="text-[length:var(--text-body)] text-dimmed">The basemap status is not available.</p>
        </section>
      </div>
    </div>

    <UModal
      :open="!!removeTarget"
      title="Remove this factor?"
      @update:open="removeTarget = null"
    >
      <template #body>
        <p class="text-[length:var(--text-body)] text-toned">
          “{{ removeTarget ? factorLabel(removeTarget.type) : '' }}” will no longer be asked for at
          sign-in. Remove it only if you've lost access to it or no longer want it.
        </p>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" :disabled="removingFactor" @click="removeTarget = null">
            Cancel
          </UButton>
          <UButton color="error" :loading="removingFactor" @click="removeMfaFactor">Remove</UButton>
        </div>
      </template>
    </UModal>

    <UModal
      :open="deleteAccountOpen"
      title="Delete your account?"
      @update:open="deleteAccountOpen = $event"
    >
      <template #body>
        <p class="text-[length:var(--text-body)] text-toned">
          This permanently removes your routes, linked devices and crew membership, then your
          sign-in itself. You will be signed out immediately and cannot undo this — coming back
          means a fresh invite and starting from nothing.
        </p>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" :disabled="deletingAccount" @click="deleteAccountOpen = false">
            Cancel
          </UButton>
          <UButton color="error" :loading="deletingAccount" @click="deleteMyAccount">
            Delete my account
          </UButton>
        </div>
      </template>
    </UModal>
  </AppPage>
</template>
