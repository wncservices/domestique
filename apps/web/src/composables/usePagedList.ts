import { computed, ref, watch, type Ref } from 'vue'

/**
 * Client-side pagination over an already-fetched list — the same pattern
 * RoutesPage.vue built for itself before this existed, extracted so a page
 * with nothing to filter (CrewsPage, PeoplePage) doesn't have to reinvent
 * it. Not used by RoutesPage itself: its own page-reset-on-search-or-filter
 * behavior is a different, slightly stricter semantic (reset to page 1 on
 * any filter change, not just when the current page becomes invalid) that
 * this was not worth complicating the shared API to also cover.
 */
export function usePagedList<T>(source: Ref<T[]>, pageSize = 24) {
  const page = ref(1)

  // A source list that shrinks (a crew deleted, a rider's access removed)
  // can leave `page` pointing past the end — reset to the last page that
  // actually exists rather than showing an empty page with real results
  // still sitting unshown before it.
  watch(source, () => {
    const maxPage = Math.max(1, Math.ceil(source.value.length / pageSize))
    if (page.value > maxPage) page.value = maxPage
  })

  const paged = computed(() => {
    const start = (page.value - 1) * pageSize
    return source.value.slice(start, start + pageSize)
  })

  return { page, paged, pageSize }
}
