import { encodeSlug } from '@/api/client'

/**
 * Warms the browser's HTTP cache for every route's card-preview PNG ahead of
 * it actually scrolling into view. TrackPreview.vue's own IntersectionObserver
 * still gates *rendering* (a card off-page is v-show'd away, and a card that
 * has never scrolled near the viewport has no reason to paint yet), but
 * nothing stops the underlying image request from having already completed
 * by the time it does — the endpoint's own `Cache-Control: private,
 * max-age=86400` (see handleTrackPreviewImage) is exactly what makes a
 * request fired now and one fired on scroll resolve to the same cache entry.
 *
 * Affordable in a way this would not have been for the old JSON-plus-client-
 * SVG path: these are tens-of-KB server-rendered PNGs (see RenderCardImage),
 * not the multi-MB geometry payloads TrackPreview's own intersection-
 * observer gating exists to avoid firing all at once. A small concurrency
 * cap plus fetchpriority=low still keeps a library of hundreds of routes
 * from competing with whatever the page is still doing to become
 * interactive — this is bandwidth spent opportunistically, not a barrier
 * to first paint.
 */
const CONCURRENCY = 6

export function prefetchTrackPreviewImages(slugs: string[], theme: 'light' | 'dark') {
  let index = 0

  function next() {
    if (index >= slugs.length) return
    const slug = slugs[index++]
    const img = new Image()
    img.setAttribute('fetchpriority', 'low')
    img.decoding = 'async'
    img.onload = next
    img.onerror = next
    img.src = `/api/track-preview-image/${encodeSlug(slug)}?theme=${theme}`
  }

  for (let i = 0; i < CONCURRENCY && i < slugs.length; i++) next()
}
