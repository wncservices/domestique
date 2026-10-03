<script setup lang="ts">
// A private link that puts the training plan in the calendar the rider already
// reads (Google, Apple, Outlook). The link is the credential: anyone who holds
// it can read the plan's session names, so it is shown exactly once, here,
// right after it is made. The server keeps only a hash, which is also why a
// lost link cannot be shown again: regenerate, and the old one stops working.
//
// Self-contained (its own endpoints, not the profile's Save bar), and absent
// when the deployment has no public_url to build the link from.
import { computed, onMounted, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { CalendarFeedLink, CalendarFeedStatus } from '@/api/types'

const toast = useToast()

const status = ref<CalendarFeedStatus | null>(null)
// Held in memory only: it is never stored in the browser, and a reload loses it.
const link = ref<CalendarFeedLink | null>(null)
const busy = ref(false)
const confirmingRegenerate = ref(false)
const confirmingRevoke = ref(false)

const available = computed(() => !!status.value?.available)
const active = computed(() => !!status.value?.active)

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

function fail(title: string, err: unknown) {
  toast.add({ title, description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
}

async function load() {
  try {
    status.value = await api.calendarFeed()
  } catch {
    // A card that cannot load is hidden rather than shouting over the page's
    // main content.
    status.value = null
  }
}

async function generate() {
  busy.value = true
  confirmingRegenerate.value = false
  try {
    link.value = await api.createCalendarFeed()
    await load()
  } catch (err) {
    fail('Could not create the calendar link', err)
  } finally {
    busy.value = false
  }
}

async function revoke() {
  busy.value = true
  confirmingRevoke.value = false
  try {
    await api.revokeCalendarFeed()
    link.value = null
    await load()
    toast.add({ title: 'Calendar link revoked', description: 'It stopped working at once.', icon: 'i-lucide-link-2-off' })
  } catch (err) {
    fail('Could not revoke the calendar link', err)
  } finally {
    busy.value = false
  }
}

async function copy(text: string, what: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.add({ title: `${what} copied`, icon: 'i-lucide-copy', color: 'success' })
  } catch {
    toast.add({ title: 'Could not copy', description: 'Select the link and copy it by hand.', icon: 'i-lucide-triangle-alert', color: 'warning' })
  }
}

function ago(iso: string): string {
  const minutes = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 60000))
  if (minutes < 2) return 'just now'
  if (minutes < 120) return `${minutes} minutes ago`
  const hours = Math.round(minutes / 60)
  if (hours < 48) return `${hours} hours ago`
  return `${Math.round(hours / 24)} days ago`
}

const lastFetched = computed(() =>
  status.value?.lastFetchedAt ? `Last fetched ${ago(status.value.lastFetchedAt)}` : 'Not fetched yet',
)

onMounted(load)
</script>

<template>
  <UCard v-if="available" variant="outline">
    <template #header>
      <h2 class="flex items-center gap-2 text-lg font-semibold">
        <UIcon name="i-lucide-calendar-days" />
        Calendar link
      </h2>
      <p class="text-sm text-muted">Your planned sessions in the calendar you already use. Yours alone.</p>
    </template>

    <div class="flex flex-col gap-4">
      <p class="text-sm text-muted">
        Subscribe to this link in Google Calendar, Apple Calendar or Outlook and each planned session shows up on its
        day. It carries session names, durations and targets only: no health data, no routes. Google can take up to a
        day to show changes.
      </p>

      <template v-if="link">
        <UAlert
          color="warning"
          variant="subtle"
          icon="i-lucide-key-round"
          title="Copy it now"
          description="This link will not be shown again. Anyone who has it can read your plan; regenerate it if it ever gets out."
        />
        <div class="flex flex-col gap-3">
          <UFormField label="For Google Calendar (Other calendars, From URL)">
            <div class="flex gap-2">
              <UInput :model-value="link.url" readonly class="w-full" aria-label="Calendar link" />
              <UButton color="neutral" variant="soft" icon="i-lucide-copy" aria-label="Copy the link" @click="copy(link.url, 'Link')">
                Copy
              </UButton>
            </div>
          </UFormField>
          <UFormField label="For Apple Calendar (webcal)">
            <div class="flex gap-2">
              <UInput :model-value="link.webcalUrl" readonly class="w-full" aria-label="Calendar link for Apple Calendar" />
              <UButton color="neutral" variant="soft" icon="i-lucide-copy" aria-label="Copy the webcal link" @click="copy(link.webcalUrl, 'Link')">
                Copy
              </UButton>
            </div>
          </UFormField>
        </div>
      </template>

      <div v-if="!active" class="flex flex-wrap items-center gap-3">
        <UButton icon="i-lucide-link" :loading="busy" @click="generate">Create calendar link</UButton>
      </div>

      <div v-else class="flex flex-wrap items-center justify-between gap-2 border-t border-default pt-3">
        <p class="text-sm text-muted">{{ lastFetched }}</p>
        <div class="flex flex-wrap gap-2">
          <UButton color="neutral" variant="soft" icon="i-lucide-refresh-cw" :disabled="busy" @click="confirmingRegenerate = true">
            Regenerate
          </UButton>
          <UButton color="error" variant="ghost" icon="i-lucide-link-2-off" :disabled="busy" @click="confirmingRevoke = true">
            Revoke
          </UButton>
        </div>
      </div>
    </div>

    <UModal v-model:open="confirmingRegenerate" title="Make a new calendar link?">
      <template #body>
        <p class="text-sm text-toned">
          The current link stops working at once. Your calendar app will need the new one.
        </p>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" @click="confirmingRegenerate = false">Cancel</UButton>
          <UButton :loading="busy" @click="generate">Make a new link</UButton>
        </div>
      </template>
    </UModal>

    <UModal v-model:open="confirmingRevoke" title="Revoke the calendar link?">
      <template #body>
        <p class="text-sm text-toned">
          The link stops working at once and your calendar app will stop updating. You can make a new one any time.
        </p>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" @click="confirmingRevoke = false">Cancel</UButton>
          <UButton color="error" :loading="busy" @click="revoke">Revoke</UButton>
        </div>
      </template>
    </UModal>
  </UCard>
</template>
