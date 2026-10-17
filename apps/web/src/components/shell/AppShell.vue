<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useLibrary } from '@/composables/useLibrary'
import AvatarMenu from './AvatarMenu.vue'
import { navGroupLabels, navItems, type NavItem } from './navItems'

const { canManageCrews, canManageTraining } = useLibrary()

// Same gates the old header applied: Today, Plan and Fitness are training
// pages, Crews needs crews:manage. Routes is open to every role.
const visible = computed(() =>
  navItems.filter((item) => {
    if (item.to === '/crews') return canManageCrews.value
    if (item.group === 'you') return canManageTraining.value
    return true
  }),
)
const groups = computed(() =>
  (['you', 'shared'] as NavItem['group'][])
    .map((group) => ({ group, label: navGroupLabels[group], items: visible.value.filter((i) => i.group === group) }))
    .filter((g) => g.items.length),
)

// Tooltips only earn their place on the icon rail; on the full sidebar the
// label is already beside the icon.
// Listener added on mount and removed on unmount: the shell remounts when
// the rider moves between /shared/* and the rest of the app.
const railOnly = ref(false)
const wide = window.matchMedia('(min-width: 1024px)')
const onWide = (e: MediaQueryListEvent) => (railOnly.value = !e.matches)
onMounted(() => {
  railOnly.value = !wide.matches
  wide.addEventListener('change', onWide)
})
onBeforeUnmount(() => wide.removeEventListener('change', onWide))

const itemBase =
  'flex items-center gap-3 rounded-lg px-3 py-2 text-[length:var(--text-body)] text-muted hover:bg-elevated hover:text-highlighted focus-visible:outline-2 focus-visible:outline-primary'
const itemActive = 'bg-primary/10 text-primary hover:bg-primary/10 hover:text-primary'
</script>

<template>
  <!-- Desktop: full sidebar. Tablet: the same element narrowed to an icon
       rail. Phone: hidden, replaced by the tab bar below. -->
  <aside class="app-chrome fixed inset-y-0 left-0 z-20 hidden w-16 flex-col border-r border-default sm:flex lg:w-60">
    <RouterLink to="/today" class="flex items-center gap-3 px-4 py-4">
      <span class="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary" aria-hidden="true">
        <UIcon name="i-lucide-bike" class="size-5" />
      </span>
      <span class="font-display hidden text-base font-semibold tracking-tight text-highlighted lg:inline">Domestique</span>
    </RouterLink>

    <nav aria-label="Main" class="flex flex-1 flex-col gap-4 overflow-y-auto px-2 py-2">
      <div v-for="g in groups" :key="g.group" class="flex flex-col gap-1">
        <p class="hidden px-3 pb-1 text-[length:var(--text-label)] text-dimmed lg:block">{{ g.label }}</p>
        <UTooltip v-for="item in g.items" :key="item.to" :text="item.label" :disabled="!railOnly" :content="{ side: 'right' }">
          <RouterLink :to="item.to" :class="itemBase" :active-class="itemActive" class="justify-center lg:justify-start">
            <UIcon :name="item.icon" class="size-5 shrink-0" aria-hidden="true" />
            <span class="sr-only lg:not-sr-only">{{ item.label }}</span>
          </RouterLink>
        </UTooltip>
      </div>
    </nav>

    <div class="border-t border-default p-2"><AvatarMenu /></div>
  </aside>

  <header class="app-header sticky top-0 z-20 flex w-full items-center justify-between gap-3 px-4 py-2 sm:hidden">
    <RouterLink to="/today" class="flex min-w-0 items-center gap-2">
      <UIcon name="i-lucide-bike" class="size-5 shrink-0 text-primary" aria-hidden="true" />
      <span class="font-display truncate text-base font-semibold tracking-tight text-highlighted">Domestique</span>
    </RouterLink>
    <AvatarMenu compact />
  </header>

  <div class="px-4 py-6 pb-[calc(5rem+env(safe-area-inset-bottom))] sm:pb-6 sm:pl-[calc(4rem+1.5rem)] sm:pr-6 lg:pl-[calc(15rem+2rem)] lg:pr-8">
    <slot />
  </div>

  <nav
    aria-label="Main"
    class="app-chrome fixed inset-x-0 bottom-0 z-20 border-t border-default pb-[env(safe-area-inset-bottom)] sm:hidden"
  >
    <ul class="flex">
      <li v-for="item in visible" :key="item.to" class="min-w-0 flex-1">
        <RouterLink
          :to="item.to"
          class="flex flex-col items-center gap-0.5 px-1 py-2 text-[length:var(--text-label)] text-muted focus-visible:outline-2 focus-visible:outline-primary"
          active-class="text-primary"
        >
          <UIcon :name="item.icon" class="size-5" aria-hidden="true" />
          <span class="max-w-full truncate">{{ item.label }}</span>
        </RouterLink>
      </li>
    </ul>
  </nav>
</template>
