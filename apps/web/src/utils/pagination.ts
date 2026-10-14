/** The count the server reports in X-Total-Count, or null if it sent none. */
export function totalFromHeader(value: string | null): number | null {
  if (value === null) return null
  const trimmed = value.trim()
  if (!/^\d+$/.test(trimmed)) return null
  return Number(trimmed)
}

/** "Showing 200 of 640 crews." when the server truncated a list, else null. */
export function truncationNote(shown: number, total: number | null, noun: string): string | null {
  if (total === null || shown >= total) return null
  return `Showing ${shown} of ${total} ${noun}.`
}
