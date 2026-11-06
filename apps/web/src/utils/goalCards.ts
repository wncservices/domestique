// What a goal card says about its goal, kept out of the component so it can be
// tested without a DOM.
import type { Goal } from '@/api/types'

export function goalMetaParts(g: Goal, routeName?: string): string[] {
  const parts = [g.sport, g.eventDate ?? 'rolling']
  if (g.targetDistanceM) parts.push(`${(g.targetDistanceM / 1000).toFixed(0)} km`)
  if (g.targetElevationM) parts.push(`${g.targetElevationM.toFixed(0)} m`)
  if (g.routeSlug) parts.push(routeName || g.routeSlug)
  return parts
}

export function canExplain(llmJobs: { explainPlan?: boolean } | undefined): boolean {
  return !!llmJobs?.explainPlan
}
