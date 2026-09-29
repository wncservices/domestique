<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ApiError, api, garminMFABody } from '@/api/client'
import type { GarminConnection } from '@/api/types'

/**
 * Signing in to Garmin, in a dialog off the Head units card.
 *
 * A dialog rather than a panel because linking a head unit is a thing you do
 * once and then forget: a permanent form for it sat on the page competing for
 * attention with the library, which is what people actually come here for.
 *
 * An account with two-factor on is a second step in the same dialog, not a
 * second dialog: the server answers the password with a challenge id, and the
 * code goes back against it. See docs/superpowers/specs/2026-09-28-garmin-mfa-design.md.
 */
const props = defineProps<{ connection: GarminConnection }>()
const emit = defineEmits<{ changed: [GarminConnection] }>()

const open = defineModel<boolean>('open', { default: false })

/**
 * Release gate. Nothing has yet touched a real two-factor Garmin account, and
 * until one has the dialog keeps its old dead-end copy for two-factor accounts.
 * The maintainer's manual check on a deployed build turns the step on with
 * `?garminMfa=1` on the page URL; passing it flips this to true. Not a rider
 * setting — a rider on an unverified flow would be told it works.
 */
const MFA_RELEASED = false
function codeStepEnabled(): boolean {
  if (MFA_RELEASED) return true
  try {
    return new URLSearchParams(window.location.search).get('garminMfa') === '1'
  } catch {
    return false
  }
}

const email = ref('')
const password = ref('')
const busy = ref(false)
const error = ref('')
// Two failures that are not "try again": an MFA challenge this flow cannot
// answer, and a block that never reached Garmin. Both get their own words —
// a red "check your password" would be advice that cannot work.
const kind = ref<'error' | 'mfa' | 'blocked'>('error')

// The second step. `challenge` is the server-held sign-in in progress; the
// password is never kept, only this id and the code being typed.
const step = ref<'credentials' | 'code'>('credentials')
const challenge = ref('')
const method = ref<'email' | 'sms' | 'totp' | ''>('')
const code = ref('')
const attemptsRemaining = ref<number | null>(null)

const canSubmit = computed(() => !!email.value.trim() && !!password.value && !busy.value)
const canVerify = computed(() => !!code.value.trim() && !busy.value)

const codeInstruction = computed(() => {
  switch (method.value) {
    case 'email':
      return 'Garmin has emailed you a code. Enter it below.'
    case 'sms':
      return 'Garmin has texted a code to your phone. Enter it below.'
    case 'totp':
      return 'Enter the code from your authenticator app.'
    default:
      return 'Enter the two-factor code Garmin sent you, or the one your authenticator app shows.'
  }
})

function resetSecondStep() {
  step.value = 'credentials'
  challenge.value = ''
  method.value = ''
  code.value = ''
  attemptsRemaining.value = null
}

// Nothing from a previous attempt survives reopening the dialog — least of
// all a password sitting in a field nobody can see, or a half-finished code.
watch(open, (isOpen) => {
  if (!isOpen) {
    email.value = ''
    password.value = ''
    error.value = ''
    kind.value = 'error'
    resetSecondStep()
  }
})

function finish(connection: GarminConnection) {
  email.value = ''
  password.value = ''
  resetSecondStep()
  open.value = false
  emit('changed', connection)
}

async function connect() {
  busy.value = true
  error.value = ''
  kind.value = 'error'
  try {
    finish(await api.garminConnect(email.value.trim(), password.value))
  } catch (err) {
    const mfa = err instanceof ApiError && err.body.mfa === true ? garminMFABody(err) : null
    if (mfa?.challenge && codeStepEnabled()) {
      // The password has done its one job; only the challenge id carries on.
      password.value = ''
      challenge.value = mfa.challenge
      method.value = mfa.method ?? ''
      step.value = 'code'
    } else {
      if (mfa) kind.value = 'mfa'
      else if (err instanceof ApiError && err.body.blocked === true) kind.value = 'blocked'
      if (kind.value !== 'error') password.value = ''
      error.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    busy.value = false
  }
}

async function verify() {
  busy.value = true
  error.value = ''
  kind.value = 'error'
  try {
    finish(await api.garminConnectMFA(challenge.value, code.value.trim()))
  } catch (err) {
    if (!(err instanceof ApiError)) {
      error.value = err instanceof Error ? err.message : String(err)
    } else if (err.status === 422 && err.body.mfaInvalid === true) {
      // Wrong code: same challenge, field stays open.
      const body = garminMFABody(err)
      challenge.value = body.challenge ?? challenge.value
      attemptsRemaining.value = body.attemptsRemaining ?? null
      code.value = ''
      error.value = err.message
    } else if (err.status === 409 || err.status === 404) {
      // Expired, used up, or gone: a fresh sign-in is the only way forward.
      resetSecondStep()
      error.value = err.message
    } else {
      if (err.body.blocked === true) kind.value = 'blocked'
      error.value = err.message
    }
  } finally {
    busy.value = false
  }
}

function backToCredentials() {
  resetSecondStep()
  error.value = ''
  kind.value = 'error'
}
</script>

<template>
  <UModal v-model:open="open" title="Sign in to Garmin">
    <template #body>
      <div class="flex flex-col gap-4">
        <UAlert
          v-if="kind === 'mfa'"
          color="warning"
          variant="subtle"
          icon="i-lucide-shield-alert"
          title="This account uses two-factor authentication"
          description="Domestique cannot answer the code challenge, so it cannot sign in to this account. Garmin offers no other way in for an app like this one."
        />
        <UAlert
          v-else-if="kind === 'blocked'"
          color="warning"
          variant="subtle"
          icon="i-lucide-shield-x"
          title="Garmin blocked the attempt"
          :description="error"
        />
        <UAlert
          v-else-if="error"
          color="error"
          variant="subtle"
          icon="i-lucide-triangle-alert"
          :description="error"
        />

        <!-- Not offered when it cannot work: an admin has not finished
             setting Garmin up, and nothing typed here would get through. -->
        <UAlert
          v-if="!props.connection.canConnect"
          color="neutral"
          variant="subtle"
          icon="i-lucide-key-round"
          :description="
            props.connection.unavailable ||
            'An administrator has not finished setting this up.'
          "
        />

        <form
          v-else-if="step === 'code'"
          class="flex flex-col gap-3"
          @submit.prevent="verify"
        >
          <p class="text-sm text-toned">{{ codeInstruction }}</p>
          <UFormField label="Verification code">
            <UInput
              v-model="code"
              type="text"
              inputmode="numeric"
              autocomplete="one-time-code"
              placeholder="123456"
              autofocus
              class="w-full"
            />
          </UFormField>
          <p v-if="attemptsRemaining !== null" class="text-xs text-dimmed">
            {{ attemptsRemaining }}
            {{ attemptsRemaining === 1 ? 'try' : 'tries' }} left before you have to sign in again.
          </p>

          <div class="flex justify-end gap-2 pt-1">
            <UButton color="neutral" variant="ghost" :disabled="busy" @click="backToCredentials">
              Back
            </UButton>
            <UButton
              type="submit"
              icon="i-lucide-shield-check"
              :loading="busy"
              :disabled="!canVerify"
            >
              Verify
            </UButton>
          </div>
        </form>

        <form v-else class="flex flex-col gap-3" @submit.prevent="connect">
          <UFormField label="Garmin Connect email">
            <UInput
              v-model="email"
              type="email"
              autocomplete="username"
              placeholder="you@example.com"
              autofocus
              class="w-full"
            />
          </UFormField>
          <UFormField label="Password">
            <UInput
              v-model="password"
              type="password"
              autocomplete="current-password"
              class="w-full"
            />
          </UFormField>

          <p class="text-xs text-dimmed">
            Your password is not stored — it is used once to sign in, and your Edge becomes a
            place routes are sent.
          </p>

          <div class="flex justify-end gap-2 pt-1">
            <UButton color="neutral" variant="ghost" :disabled="busy" @click="open = false">
              Cancel
            </UButton>
            <UButton type="submit" icon="i-lucide-log-in" :loading="busy" :disabled="!canSubmit">
              Sign in
            </UButton>
          </div>
        </form>
      </div>
    </template>
  </UModal>
</template>
