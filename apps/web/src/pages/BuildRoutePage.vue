<script setup lang="ts">
import { useLibrary } from '@/composables/useLibrary'
import RouteBuilderPanel from '@/components/RouteBuilderPanel.vue'

const { canUpload, routingConfigured, refresh } = useLibrary()
</script>

<template>
  <div class="flex flex-col gap-6">
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
  </div>
</template>
