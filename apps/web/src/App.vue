<script setup lang="ts">
import { computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useToast } from '@nuxt/ui/composables'
import AppShell from '@/components/shell/AppShell.vue'
import { useLibrary } from '@/composables/useLibrary'

const { error, refresh } = useLibrary()
const route = useRoute()
const router = useRouter()
const toast = useToast()

// /sso/callback redirects here with ?notice=... whenever the issuer denied
// a login on purpose mid-provisioning (a linked identity, a brand-new
// signup's roles just granted — see sso.go's own withNotice) rather than
// rejecting it outright — a toast is the friendly landing for that; a raw
// JSON blob is what showed up here before this existed. Stripped from the
// URL right after so a refresh doesn't show it again.
//
// A watch with immediate: true, not a one-shot check in onMounted: the
// router's own initial navigation is not guaranteed to have resolved by the
// time App.vue — the root component, mounted directly rather than reached
// through a route — first runs its mounted hook, so route.query can still
// be empty at that exact moment. Watching reacts correctly whether the
// query was already there or resolves a tick later.
watch(
  () => route.query.notice,
  (notice) => {
    if (typeof notice !== 'string' || !notice) return
    toast.add({ title: notice, icon: 'i-lucide-info', color: 'info' })
    const { notice: _drop, ...query } = route.query
    router.replace({ path: route.path, query })
  },
  { immediate: true },
)

// A share recipient is, by design, someone this deployment's library has
// never heard of — useLibrary's own fetches (routes, accounts, crews...)
// 403 for them the same way they would for any role-less identity, and
// the ordinary shell (navigation, a "your role does not allow
// routes:read" retry banner) would be actively wrong to show someone who
// isn't looking at the library at all. SharedRoutePage.vue does its own
// sign-in check and its own fetch through /api/shares/{token}, entirely
// independent of useLibrary, so this skips the whole library-page shell
// for that one route rather than trying to make it degrade gracefully.
const isSharedRoutePage = computed(() => route.path.startsWith('/shared/'))

onMounted(() => {
  if (!isSharedRoutePage.value) refresh()
})
</script>

<template>
  <UApp>
    <!-- The shared-route page keeps no shell: see isSharedRoutePage above. -->
    <component :is="isSharedRoutePage ? 'div' : AppShell">
      <div :class="['flex flex-col gap-6', isSharedRoutePage ? 'mx-auto max-w-5xl px-4 py-6' : 'mx-auto w-full max-w-6xl']">
        <UAlert
          v-if="!isSharedRoutePage && error"
          color="error"
          variant="subtle"
          icon="i-lucide-plug-zap"
          orientation="horizontal"
          title="Could not reach the API"
          :description="error"
          :actions="[{ label: 'Retry', color: 'error', variant: 'subtle', onClick: () => refresh() }]"
        />


      </div>
    </component>
  </UApp>
</template>
