<script setup lang="ts">
import AppPage from '@/components/ui/AppPage.vue'
import { useLibrary } from '@/composables/useLibrary'
import RouteBuilderPanel from '@/components/RouteBuilderPanel.vue'

const { canUpload, routingConfigured, refresh } = useLibrary()
</script>

<template>
  <AppPage title="Build a route" subtitle="Draw a route on the map and save it to the library.">
    <template #actions>
      <UButton to="/routes" icon="i-lucide-arrow-left" color="neutral" variant="ghost">Back to routes</UButton>
    </template>
    <RouteBuilderPanel v-if="canUpload && routingConfigured" @built="refresh" />

    <UAlert
      v-else-if="!canUpload"
      color="neutral"
      variant="subtle"
      icon="i-lucide-lock"
      title="You cannot build routes"
      description="Your role is read-only. An admin can change that in Authelia."
    />

    <!-- Reachable only by a direct link once someone has it bookmarked — the
         nav link itself is hidden whenever routingConfigured is false, same
         as Komoot's own unconfigured state elsewhere on the Add page. -->
    <UAlert
      v-else
      color="neutral"
      variant="subtle"
      icon="i-lucide-signpost"
      title="Route builder not available"
      description="This deployment has no routing engine configured."
    />
  </AppPage>
</template>
