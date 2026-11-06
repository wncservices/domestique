<script setup lang="ts">
// Every goal at a glance, with the add, edit and explain actions that used to
// sit in a collapsed toggle at the bottom of Plan. State and handlers are
// usePlanGoals', shared with Plan; this page only lays them out.
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { Me } from '@/api/types'
import GoalSlideover from '@/components/plan/GoalSlideover.vue'
import PlanEmptyState from '@/components/plan/PlanEmptyState.vue'
import { pickFallbackGoal } from '@/components/plan/goalOrdering'
import AppPage from '@/components/ui/AppPage.vue'
import SkeletonCard from '@/components/ui/SkeletonCard.vue'
import { usePlanGoals } from '@/composables/usePlanGoals'
import { describeError } from '@/utils/errorMessage'
import { canExplain, goalMetaParts } from '@/utils/goalCards'

const toast = useToast()
const route = useRoute()
const router = useRouter()

const me = ref<Me | null>(null)

const {
  goals,
  loadingGoals,
  loadGoals,
  goalsError,
  goalsSettled,
  goalModalOpen,
  editingGoalId,
  goalForm,
  routeOptions,
  loadRouteOptions,
  openCreateGoal,
  openEditGoal,
  goalNote,
  proposingGoal,
  goalExplanation,
  proposeGoal,
  startingGeneralPlan,
  startGeneralPlan,
  savingGoal,
  saveGoal,
  deletingGoal,
  explainingGoal,
  explanations,
  explainPlan,
  dismissExplanation,
  deleteGoal,
} = usePlanGoals({
  toast,
  errorMessage: describeError,
  // This page shows no week or workouts, so there is nothing to refresh.
  loadWeek: async () => {},
  loadWorkouts: async () => {},
  routeQuery: route.query,
  routePath: route.path,
  router,
})

const explainEnabled = computed(() => canExplain(me.value?.llmJobs))
const routeNames = computed(() => new Map(routeOptions.value.map((o) => [o.value, o.label])))

// "Focus" is the goal the plan is built toward: the server's own ordering
// (pickFallbackGoal mirrors weekFocus). Choosing a different one to look at is
// Plan's header dropdown, a per-visit view, so this page links there instead
// of offering a choice it could not keep.
const focusId = computed(() => pickFallbackGoal(goals.value)?.id)

function explain(id: string) {
  const g = goals.value.find((x) => x.id === id)
  if (g) void explainPlan(g)
}

onMounted(async () => {
  api.me().then((m) => { me.value = m }).catch(() => {})
  void loadRouteOptions()
  await loadGoals()
})
</script>

<template>
  <AppPage title="Goals" subtitle="What you are training for. The plan is built toward the focus goal.">
    <template v-if="goals.length" #actions>
      <UButton icon="i-lucide-plus" @click="openCreateGoal()">New goal</UButton>
    </template>

    <SkeletonCard v-if="!goalsSettled || (loadingGoals && !goals.length)" :lines="3" label="Loading your goals…" />

    <UAlert
      v-else-if="goalsError"
      color="error"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      title="Could not load goals"
      :description="goalsError"
      :actions="[{ label: 'Retry', color: 'neutral', variant: 'outline', onClick: () => loadGoals() }]"
    />

    <PlanEmptyState
      v-else-if="goals.length === 0"
      :propose-goal-enabled="!!me?.llmJobs.proposeGoal"
      :starting="startingGeneralPlan"
      @goal="openCreateGoal"
      @from-route="router.push('/routes')"
      @general="startGeneralPlan"
    />

    <div v-else class="grid gap-4 md:grid-cols-2">
      <UCard v-for="g in goals" :key="g.id" variant="outline" data-testid="goal-card">
        <div class="flex flex-col gap-3">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <span class="min-w-0 truncate text-[length:var(--text-lead)] font-semibold text-highlighted">{{ g.name }}</span>
                <UBadge color="neutral" variant="subtle" size="sm" :aria-label="`Priority ${g.priority}`">{{ g.priority }}</UBadge>
                <UBadge v-if="g.id === focusId" color="primary" variant="subtle" size="sm">Focus</UBadge>
                <UButton color="neutral" variant="ghost" size="xs" trailing-icon="i-lucide-arrow-right" :to="{ path: '/plan', query: { goal: g.id } }">View plan</UButton>
              </div>
              <p class="mt-1 text-[length:var(--text-body)] text-muted">
                <template v-for="(part, i) in goalMetaParts(g, routeNames.get(g.routeSlug ?? ''))" :key="i">
                  <span v-if="i > 0" aria-hidden="true"> · </span>
                  <span class="whitespace-nowrap">{{ part }}</span>
                </template>
              </p>
            </div>
            <div class="flex shrink-0 items-center gap-1">
              <UButton
                icon="i-lucide-pencil"
                color="neutral"
                variant="ghost"
                size="sm"
                :aria-label="`Edit ${g.name}`"
                @click="openEditGoal(g)"
              />
              <UButton
                icon="i-lucide-trash-2"
                color="neutral"
                variant="ghost"
                size="sm"
                :aria-label="`Delete ${g.name}`"
                :loading="deletingGoal === g.id"
                @click="deleteGoal(g)"
              />
            </div>
          </div>

          <div v-if="explainEnabled && !explanations[g.id]">
            <UButton
              color="neutral"
              variant="soft"
              icon="i-lucide-sparkles"
              :loading="explainingGoal === g.id"
              @click="explain(g.id)"
            >
              Explain
            </UButton>
          </div>
          <p v-if="explainingGoal === g.id" class="text-[length:var(--text-label)] text-muted">This can take up to a minute.</p>
          <UAlert
            v-if="explanations[g.id]"
            color="neutral"
            variant="subtle"
            icon="i-lucide-sparkles"
            :description="explanations[g.id]"
            close
            @update:open="() => dismissExplanation(g.id)"
          />
        </div>
      </UCard>
    </div>

    <GoalSlideover
      v-model:open="goalModalOpen"
      v-model:note="goalNote"
      :editing="!!editingGoalId"
      :form="goalForm"
      :propose-goal-enabled="!!me?.llmJobs.proposeGoal"
      :saving="savingGoal"
      :proposing="proposingGoal"
      :explanation="goalExplanation"
      :route-options="routeOptions"
      @update:form="(f) => (goalForm = f)"
      @propose="proposeGoal"
      @save="saveGoal"
    />
  </AppPage>
</template>
