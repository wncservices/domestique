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

        <!-- KeepAlive, LibraryPage only: every other page (Add, Settings,
             People, Crews) genuinely wants fresh state each visit, but
             Library's own cards each carry a background wash that's real
             work to (re)build — a route line projected from the raw track
             and, since #174, an earth/landuse/water/roads wash decoded from
             vector tiles (or, since #179, fetched from the server-side
             cache — still a network round trip either way). Without this,
             navigating away and back destroyed every TrackPreview instance
             and rebuilt all of it from scratch, even though nothing about
             the route itself had changed. LibraryPage's own route list
             already comes from useLibrary's shared state, not a
             component-local fetch, so keeping the component alive doesn't
             risk showing a stale list — a route added on another page still
             appears the moment that shared state updates. -->
        <RouterView v-slot="{ Component }">
          <KeepAlive include="LibraryPage">
            <component :is="Component" />
          </KeepAlive>
        </RouterView>

        <!-- AGPL-3.0 section 13: a modified version offered over a network has
             to offer its users the source. A link in the footer of every page is
             the simplest way to actually satisfy that, and the easiest thing to
             forget. -->
        <footer class="mt-2 border-t border-default pt-4 text-[length:var(--text-label)] text-dimmed">
          <a
            href="https://github.com/wncservices/domestique"
            target="_blank"
            rel="noopener noreferrer"
            class="hover:text-default"
          >
            Domestique — free software under the AGPL-3.0. Source.
          </a>
        </footer>
      </div>
    </component>
  </UApp>
</template>
