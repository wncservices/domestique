import type { StyleSpecification } from 'maplibre-gl'

/**
 * The maplibre-gl/pmtiles/protomaps-themes-base module loading RouteMap.vue
 * and RouteBuilderMap.vue both need — pulled out here, a real ES module
 * rather than a Vue SFC's <script setup> body, so the cache below is an
 * unambiguous module-level singleton: a route-detail map and the route
 * builder's own map, mounted on the same page, both resolve every one of
 * these from cache instantly rather than each re-importing or
 * re-registering the pmtiles protocol.
 */
let modulesReady: Promise<{
  maplibregl: typeof import('maplibre-gl')
  themes: typeof import('protomaps-themes-base')
}> | null = null

export function loadMapLibreModules() {
  if (!modulesReady) {
    modulesReady = Promise.all([
      import('maplibre-gl'),
      import('maplibre-gl/dist/maplibre-gl.css'),
      import('pmtiles'),
      import('protomaps-themes-base'),
    ]).then(([gl, , { Protocol }, themes]) => {
      gl.addProtocol('pmtiles', new Protocol().tile)
      return { maplibregl: gl, themes }
    })
  }
  return modulesReady
}

/**
 * The .pmtiles basemap is self-hosted specifically so route coordinates
 * never reach a third party (see tiles/AGENTS.md in domestique-infra) —
 * style/sprite/glyphs carry no location data, so those come from a public
 * CDN. Absolute URL, not a bare relative path: the pmtiles:// scheme wraps
 * a real fetchable URL, and building it from location.origin keeps this
 * working whether the app is reached at domestique.dev or app.domestique.dev.
 */
export function styleFromTheme(
  theme: 'light' | 'dark',
  themes: typeof import('protomaps-themes-base'),
): StyleSpecification {
  const { layers, namedTheme } = themes
  const basemapUrl = `pmtiles://${window.location.origin}/tiles/basemap.pmtiles`
  return {
    version: 8,
    glyphs: 'https://protomaps.github.io/basemaps-assets/fonts/{fontstack}/{range}.pbf',
    sprite: `https://protomaps.github.io/basemaps-assets/sprites/v4/${theme}`,
    sources: {
      protomaps: {
        type: 'vector',
        url: basemapUrl,
        attribution: '© <a href="https://openstreetmap.org">OpenStreetMap</a>',
      },
    },
    layers: layers('protomaps', namedTheme(theme), { lang: 'en' }),
  }
}

export async function buildMapStyle(theme: 'light' | 'dark'): Promise<StyleSpecification> {
  const themes = await import('protomaps-themes-base')
  return styleFromTheme(theme, themes)
}
