<script setup lang="ts">
// The training area's own shell — sub-tabs plus a RouterView, the same
// pattern App.vue's own top-level nav uses one level up. Split into
// TrainingFitnessPage.vue (fitness history, sync, the rider's own profile)
// and TrainingPlanPage.vue (goals, periodization plans, scheduling, the
// manual workout builder) once the single page covering all of Phases
// A–E (see docs/training-plan.md) grew too long to scroll through for
// "just let me update my FTP." Each sub-page fetches its own data
// independently — no shared store, the same no-state-management-library
// choice AGENTS.md already makes for the rest of the app — so either half
// works correctly on its own regardless of which one a rider lands on
// first.
const subTabs = [
  { to: '/training/fitness', label: 'Fitness', icon: 'i-lucide-heart-pulse' },
  { to: '/training/plan', label: 'Plan', icon: 'i-lucide-calendar-range' },
]
</script>

<template>
  <div class="flex flex-col gap-6">
    <nav class="flex gap-1" aria-label="Training sections">
      <UButton
        v-for="tab in subTabs"
        :key="tab.to"
        :to="tab.to"
        :icon="tab.icon"
        :color="$route.path === tab.to ? 'primary' : 'neutral'"
        :variant="$route.path === tab.to ? 'subtle' : 'ghost'"
        size="sm"
      >
        {{ tab.label }}
      </UButton>
    </nav>

    <RouterView />
  </div>
</template>
