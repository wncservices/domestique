<script setup lang="ts">
/**
 * Share one route with someone outside every crew — not an anonymous
 * public link: whoever holds it still signs in through this app's own
 * auth before seeing anything (see docs behind SharedRoutePage.vue and
 * the server's own internal/routeshare package doc comment). A dialog
 * rather than a permanent panel, the same reasoning GarminSignIn.vue's
 * own comment gives: linking a head unit, and sharing a route, are both
 * things a rider does occasionally, not something worth permanent screen
 * space on every route card.
 */
import { computed, ref, watch } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { CreateRouteShareResponse, Route, RouteShare, RouteShareTTLDays } from '@/api/types'

const props = defineProps<{ route: Route }>()
const open = defineModel<boolean>('open', { default: false })
const toast = useToast()

const ttlDays = ref<RouteShareTTLDays>(30)
const ttlOptions: { label: string; value: RouteShareTTLDays }[] = [
  { label: '7 days', value: 7 },
  { label: '30 days', value: 30 },
  { label: '90 days', value: 90 },
]

const creating = ref(false)
// The raw token is only ever in this response, once — see the API's own
// createShareResponse doc comment. Cleared whenever the dialog closes, so
// reopening it never shows a stale link that's already scrolled out of a
// rider's clipboard history.
const justCreated = ref<CreateRouteShareResponse | null>(null)
const copied = ref(false)

const shares = ref<RouteShare[]>([])
const loadingShares = ref(false)
const revoking = ref('')

async function loadShares() {
  loadingShares.value = true
  try {
    shares.value = await api.routeShares(props.route.slug)
  } catch {
    shares.value = []
  } finally {
    loadingShares.value = false
  }
}

// Nothing from a previous visit survives reopening — same rule
// GarminSignIn.vue's own dialog follows for its own fields.
watch(open, (isOpen) => {
  if (!isOpen) return
  justCreated.value = null
  copied.value = false
  loadShares()
})

async function createShare() {
  creating.value = true
  try {
    justCreated.value = await api.createRouteShare(props.route.slug, ttlDays.value)
    copied.value = false
    await loadShares()
  } catch (err) {
    toast.add({
      title: 'Could not create the link',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    creating.value = false
  }
}

async function copyLink() {
  if (!justCreated.value) return
  try {
    await navigator.clipboard.writeText(justCreated.value.url)
    copied.value = true
  } catch {
    toast.add({
      title: 'Could not copy automatically',
      description: 'Select the link above and copy it by hand.',
      icon: 'i-lucide-triangle-alert',
      color: 'warning',
    })
  }
}

async function revoke(share: RouteShare) {
  revoking.value = share.id
  try {
    await api.revokeRouteShare(share.id)
    await loadShares()
  } catch (err) {
    toast.add({
      title: 'Could not revoke that link',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    revoking.value = ''
  }
}

// Derived rather than stored: a share's status is a function of its own
// fields and the current time, never something worth getting out of sync
// with either.
function statusFor(share: RouteShare): { label: string; color: 'success' | 'neutral' } {
  if (share.revokedAt) return { label: 'revoked', color: 'neutral' }
  if (new Date(share.expiresAt).getTime() < Date.now()) return { label: 'expired', color: 'neutral' }
  return { label: 'active', color: 'success' }
}

const sortedShares = computed(() =>
  [...shares.value].sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime()),
)
</script>

<template>
  <UModal v-model:open="open" :title="`Share “${route.name}”`" :ui="{ content: 'sm:max-w-lg' }">
    <template #body>
      <div class="flex flex-col gap-4">
        <p class="text-sm text-toned">
          Anyone with the link can view and download this one route once they sign in — nothing
          else in your library. Revoke it any time.
        </p>

        <div v-if="justCreated" class="flex flex-col gap-2 rounded-lg bg-elevated/60 p-3">
          <p class="text-xs font-medium text-dimmed">
            Copy this now — you won't be able to see it again, but you can revoke it below.
          </p>
          <div class="flex gap-2">
            <UInput :model-value="justCreated.url" readonly class="w-full font-mono text-xs" />
            <UButton
              :icon="copied ? 'i-lucide-check' : 'i-lucide-copy'"
              :color="copied ? 'success' : 'neutral'"
              variant="subtle"
              @click="copyLink"
            >
              {{ copied ? 'Copied' : 'Copy' }}
            </UButton>
          </div>
        </div>

        <form v-else class="flex items-end gap-2" @submit.prevent="createShare">
          <UFormField label="Expires after" class="flex-1">
            <USelect v-model="ttlDays" :items="ttlOptions" value-key="value" class="w-full" />
          </UFormField>
          <UButton type="submit" icon="i-lucide-link" :loading="creating">Create link</UButton>
        </form>

        <div v-if="sortedShares.length" class="flex flex-col gap-2">
          <p class="text-xs font-medium text-dimmed">Existing links</p>
          <div class="flex flex-col divide-y divide-default">
            <div
              v-for="share in sortedShares"
              :key="share.id"
              class="flex items-center gap-2 py-2 first:pt-0 last:pb-0"
            >
              <UBadge :color="statusFor(share).color" variant="subtle" size="sm">
                {{ statusFor(share).label }}
              </UBadge>
              <div class="min-w-0 flex-1 text-xs text-dimmed">
                <p>
                  Created {{ new Date(share.createdAt).toLocaleDateString() }} · expires
                  {{ new Date(share.expiresAt).toLocaleDateString() }}
                </p>
                <p v-if="share.redeemedBy.length">
                  Seen by {{ share.redeemedBy.map((r) => r.rider).join(', ') }}
                </p>
              </div>
              <UButton
                v-if="statusFor(share).label === 'active'"
                icon="i-lucide-x"
                color="neutral"
                variant="ghost"
                size="xs"
                :loading="revoking === share.id"
                aria-label="Revoke this link"
                @click="revoke(share)"
              />
            </div>
          </div>
        </div>
        <p v-else-if="!loadingShares" class="text-xs text-dimmed">No links created yet.</p>
      </div>
    </template>
    <template #footer>
      <div class="flex w-full justify-end">
        <UButton color="neutral" variant="ghost" @click="open = false">Close</UButton>
      </div>
    </template>
  </UModal>
</template>
