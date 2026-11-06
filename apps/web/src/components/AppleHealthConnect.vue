<script setup lang="ts">
// Connects the rider's Apple Health shortcut. The token is the credential and
// the server keeps only its hash, so it is shown once, right after it is made,
// and held in memory only: a reload loses it, and regenerating replaces it.
// Hidden when the deployment cannot store it (412) or the role cannot link.
import { computed, onMounted, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { AppleHealthLink } from '@/api/types'
import { cardState, shortcutHelp } from '@/utils/appleHealthCard'
import { describeError } from '@/utils/errorMessage'

const DOCS_URL = 'https://github.com/wncservices/domestique/blob/main/docs/apple-health-shortcut.md'

const toast = useToast()

const link = ref<AppleHealthLink | null>(null)
const token = ref<string | null>(null)
const busy = ref(false)
const confirmingConnect = ref(false)
const confirmingRegenerate = ref(false)
const confirmingDisconnect = ref(false)

const state = computed(() => (link.value ? cardState(link.value, token.value) : null))
const help = computed(() => (link.value ? shortcutHelp(link.value) : null))

function fail(title: string, err: unknown) {
  toast.add({ title, description: describeError(err), icon: 'i-lucide-triangle-alert', color: 'error' })
}

async function load() {
  try {
    link.value = await api.appleHealth()
  } catch {
    link.value = null
  }
}

async function issue() {
  busy.value = true
  confirmingConnect.value = false
  confirmingRegenerate.value = false
  try {
    const res = await api.connectAppleHealth()
    token.value = res.token ?? null
    link.value = res
  } catch (err) {
    fail('Could not connect Apple Health', err)
  } finally {
    busy.value = false
  }
}

async function disconnect() {
  busy.value = true
  confirmingDisconnect.value = false
  try {
    await api.disconnectAppleHealth()
    token.value = null
    await load()
    toast.add({ title: 'Apple Health disconnected', icon: 'i-lucide-link-2-off' })
  } catch (err) {
    fail('Could not disconnect Apple Health', err)
  } finally {
    busy.value = false
  }
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.add({ title: 'Token copied', icon: 'i-lucide-copy', color: 'success' })
  } catch {
    toast.add({ title: 'Could not copy', description: 'Select the token and copy it by hand.', icon: 'i-lucide-triangle-alert', color: 'warning' })
  }
}

onMounted(load)
</script>

<template>
  <UCard v-if="link" variant="outline">
    <template #header>
      <h3 class="flex items-center gap-2 font-medium text-highlighted">
        <UIcon name="i-lucide-heart-pulse" />
        Apple Health
      </h3>
    </template>

    <div class="flex flex-col gap-4">
      <template v-if="state === 'off'">
        <p class="text-[length:var(--text-body)] text-muted">
          Send HRV, sleep and resting heart rate from Apple Health. This replaces Garmin's wellness for you, and the HRV
          history starts over (14 nights).
        </p>
        <div>
          <UButton icon="i-lucide-link" :loading="busy" @click="confirmingConnect = true">Connect</UButton>
        </div>
      </template>

      <template v-else-if="state === 'just-linked' && token">
        <p class="text-[length:var(--text-body)] text-muted">
          Install the shortcut, paste this when it asks, then add an automation.
        </p>
        <UAlert
          color="warning"
          variant="subtle"
          icon="i-lucide-key-round"
          title="Copy it now"
          description="This token will not be shown again. Regenerate it if it ever gets out."
        />
        <div class="flex gap-2">
          <UInput :model-value="token" readonly class="w-full" aria-label="Apple Health token" />
          <UButton color="neutral" variant="soft" icon="i-lucide-copy" aria-label="Copy the token" @click="copy(token)">
            Copy
          </UButton>
        </div>
        <div>
          <UButton
            v-if="help?.kind === 'link'"
            :to="help.url"
            target="_blank"
            color="neutral"
            variant="soft"
            icon="i-lucide-external-link"
          >
            Get the shortcut
          </UButton>
          <UButton v-else :to="DOCS_URL" target="_blank" color="neutral" variant="soft" icon="i-lucide-book-open">
            How to set it up
          </UButton>
        </div>
      </template>

      <template v-if="state === 'linked' || state === 'just-linked'">
        <div class="flex flex-wrap items-center justify-between gap-2" :class="{ 'border-t border-default pt-3': state === 'just-linked' }">
          <p class="text-[length:var(--text-body)] text-muted">
            Last received: {{ link.lastReceived || 'Nothing yet' }}
          </p>
          <div class="flex flex-wrap gap-2">
            <UButton color="neutral" variant="soft" icon="i-lucide-refresh-cw" :disabled="busy" @click="confirmingRegenerate = true">
              Regenerate
            </UButton>
            <UButton color="error" variant="ghost" icon="i-lucide-link-2-off" :disabled="busy" @click="confirmingDisconnect = true">
              Disconnect
            </UButton>
          </div>
        </div>
      </template>
    </div>

    <UModal v-model:open="confirmingConnect" title="Switch to Apple Health?">
      <template #body>
        <p class="text-[length:var(--text-body)] text-toned">
          Apple Health replaces Garmin's wellness data for you, and your HRV history starts over, so it takes 14 nights
          before it is judged against your own band.
        </p>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" @click="confirmingConnect = false">Cancel</UButton>
          <UButton :loading="busy" @click="issue">Connect</UButton>
        </div>
      </template>
    </UModal>

    <UModal v-model:open="confirmingRegenerate" title="Make a new token?">
      <template #body>
        <p class="text-[length:var(--text-body)] text-toned">
          The current token stops working at once. Your shortcut will need the new one.
        </p>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" @click="confirmingRegenerate = false">Cancel</UButton>
          <UButton :loading="busy" @click="issue">Make a new token</UButton>
        </div>
      </template>
    </UModal>

    <UModal v-model:open="confirmingDisconnect" title="Disconnect Apple Health?">
      <template #body>
        <p class="text-[length:var(--text-body)] text-toned">
          The token stops working at once and your shortcut will stop sending data.
        </p>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" @click="confirmingDisconnect = false">Cancel</UButton>
          <UButton color="error" :loading="busy" @click="disconnect">Disconnect</UButton>
        </div>
      </template>
    </UModal>
  </UCard>
</template>
