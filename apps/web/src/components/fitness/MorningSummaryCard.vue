<script setup lang="ts">
// Opt in to a short email each morning: today's session, whether to ride it,
// a warning if the weather is bad. Off by default; the toggle is the
// unsubscribe.
//
// The address is never typed here. It is the one on the account, and only one
// the sign-in provider vouches for, so the card shows it and cannot change it.
// Self-contained (its own endpoints, not the profile's Save bar). Hidden when
// the summary is unavailable, except that an admin is told when the deployment
// has no mail server set up, since that is theirs to fix.
import { computed, onMounted, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api, ApiError } from '@/api/client'
import type { MorningSummaryStatus } from '@/api/types'
import { useLibrary } from '@/composables/useLibrary'

const toast = useToast()
const { canManageSettings } = useLibrary()

const status = ref<MorningSummaryStatus | null>(null)
const saving = ref(false)
const sending = ref(false)

const showExplanation = computed(
  () => status.value?.available === false && status.value.reason === 'not_configured' && canManageSettings.value,
)

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

async function load() {
  try {
    status.value = await api.morningSummary()
  } catch {
    // A card that cannot load is hidden rather than shouting over the page.
    status.value = null
  }
}

async function toggle(enabled: boolean) {
  saving.value = true
  try {
    status.value = await api.setMorningSummary(enabled)
    toast.add({
      title: enabled ? 'Morning summary is on' : 'Morning summary is off',
      icon: 'i-lucide-mail',
      color: enabled ? 'success' : 'neutral',
    })
  } catch (err) {
    toast.add({ title: 'Could not change the morning summary', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    await load()
  } finally {
    saving.value = false
  }
}

async function sendTest() {
  sending.value = true
  try {
    await api.testMorningSummary()
    toast.add({ title: 'Test email sent', description: 'It should arrive in a minute.', icon: 'i-lucide-mail-check', color: 'success' })
  } catch (err) {
    const tooMany = err instanceof ApiError && err.status === 429
    toast.add({
      title: tooMany ? 'Too many test emails' : 'Could not send the test email',
      description: errorMessage(err),
      icon: 'i-lucide-triangle-alert',
      color: tooMany ? 'warning' : 'error',
    })
  } finally {
    sending.value = false
  }
}

onMounted(load)
</script>

<template>
  <UCard v-if="status?.available" variant="outline">
    <template #header>
      <h2 class="flex items-center gap-2 text-lg font-semibold">
        <UIcon name="i-lucide-mail" />
        Morning summary
      </h2>
      <p class="text-sm text-muted">One short email after your morning sync. Yours alone.</p>
    </template>

    <div class="flex flex-col gap-4">
      <USwitch
        :model-value="status.enabled"
        :disabled="saving"
        label="Email me a summary each morning"
        @update:model-value="(value: boolean) => toggle(value)"
      />

      <p class="text-sm text-muted">
        It goes to <span class="font-medium text-highlighted">{{ status.email }}</span>, the address on your account.
        It says what today's session is, whether to ride it as planned, and a warning if the weather looks bad. It
        leaves out your sleep, heart rate and fitness numbers: the app has those, mail is not the place for them.
      </p>

      <div class="flex flex-wrap items-center gap-3 border-t border-default pt-3">
        <UButton color="neutral" variant="soft" icon="i-lucide-send" :loading="sending" @click="sendTest">
          Send me a test
        </UButton>
        <p class="text-xs text-muted">Proves the mail setup works. Sent to the address above only.</p>
      </div>
    </div>
  </UCard>

  <UAlert
    v-else-if="showExplanation"
    color="neutral"
    variant="subtle"
    icon="i-lucide-mail-x"
    title="Morning summary is not set up"
    description="Riders can get a daily email once this deployment has a mail server: set notifications.smtp and public_url in the config, and DOMESTIQUE_SMTP_PASSWORD in the environment."
  />
</template>
