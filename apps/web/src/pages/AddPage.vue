<script setup lang="ts">
import AppPage from '@/components/ui/AppPage.vue'
import { useLibrary } from '@/composables/useLibrary'
import GarminCoursesPanel from '@/components/GarminCoursesPanel.vue'
import GarminDuplicatesPanel from '@/components/GarminDuplicatesPanel.vue'
import KomootDuplicatesPanel from '@/components/KomootDuplicatesPanel.vue'
import KomootPanel from '@/components/KomootPanel.vue'
import UploadPanel from '@/components/UploadPanel.vue'
import WahooRoutesPanel from '@/components/WahooRoutesPanel.vue'
import WahooDuplicatesPanel from '@/components/WahooDuplicatesPanel.vue'

const { config, canUpload, canImportKomoot, canSyncGarmin, canSyncWahoo, komootEnabled, refresh } =
  useLibrary()
</script>

<template>
  <AppPage title="Add a route" subtitle="Upload a GPX file, or bring routes in from a provider.">
    <template #actions>
      <UButton to="/routes" icon="i-lucide-arrow-left" color="neutral" variant="ghost">Back to routes</UButton>
    </template>
    <UploadPanel v-if="canUpload" @uploaded="refresh" />

    <UAlert
      v-else
      color="neutral"
      variant="subtle"
      icon="i-lucide-lock"
      title="You cannot add routes"
      description="Your role is read-only. An admin can change that in Authelia."
    />

    <KomootPanel
      v-if="canImportKomoot && komootEnabled && config"
      :state="config.komoot === 'disabled' ? 'unconfigured' : config.komoot"
      @imported="refresh"
    />
    <KomootDuplicatesPanel v-if="canImportKomoot && komootEnabled" />

    <GarminCoursesPanel v-if="canSyncGarmin" @imported="refresh" />
    <GarminDuplicatesPanel v-if="canSyncGarmin" />

    <WahooRoutesPanel v-if="canSyncWahoo" @imported="refresh" />
    <WahooDuplicatesPanel v-if="canSyncWahoo" />
  </AppPage>
</template>
