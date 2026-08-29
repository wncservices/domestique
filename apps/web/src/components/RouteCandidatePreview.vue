<script setup lang="ts">
import { computed } from 'vue'
import {
  PREVIEW_HEIGHT,
  PREVIEW_WIDTH,
  projectRoute,
  routePath,
  routeStart,
} from '@/utils/routeProjection'

const props = defineProps<{ points: [number, number][] }>()

// Shared with TrackPreview.vue — see utils/routeProjection's own doc
// comment for why. That component is built entirely around fetching a
// *saved* route's data by slug (its own points, its own server-rendered
// background wash and PNG), none of which exists yet for a candidate
// nothing has saved, so this stays its own small component; only the
// projection math itself is shared.
const projection = computed(() => projectRoute(props.points))

const path = computed(() => {
  const proj = projection.value
  return proj ? routePath(props.points, proj) : ''
})

const start = computed(() => routeStart(path.value))
</script>

<template>
  <div class="aspect-[2/1] grid place-items-center overflow-hidden rounded-lg bg-elevated/50">
    <svg
      v-if="path"
      :viewBox="`0 0 ${PREVIEW_WIDTH} ${PREVIEW_HEIGHT}`"
      class="size-full"
      role="img"
      aria-label="Suggested route shape"
    >
      <path
        :d="path"
        class="track-line"
        fill="none"
        stroke-width="2.5"
        stroke-linecap="round"
        stroke-linejoin="round"
      />
      <circle v-if="start" :cx="start.x" :cy="start.y" r="4" class="fill-primary" />
    </svg>
    <p v-else class="text-sm text-muted">no track</p>
  </div>
</template>
