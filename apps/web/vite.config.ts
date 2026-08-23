import { copyFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import ui from '@nuxt/ui/vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'
import { defineConfig, type Plugin } from 'vite'

// maplibre-gl loads its own worker at runtime via
// new URL('./maplibre-gl-worker.mjs', import.meta.url) — resolved against
// wherever its *own* chunk actually ends up being served from, not
// something Vite's asset pipeline ever sees (that URL construction lives
// inside maplibre-gl's own pre-built dist output, not source Vite parses
// for asset references). Vite hashes the chunk maplibre-gl ends up in but
// never copies the separate maplibre-gl-worker.mjs file sitting next to it
// in node_modules, so the browser requests a file the build never
// produced — a 404 that this app's own catch-all SPA routing turns into a
// 302 to the landing host, which the browser then refuses to run as a
// worker (text/html, not a script). Only a production-build problem: the
// dev server resolves that same relative URL against the real
// node_modules path, which it already serves directly.
//
// maplibre-gl-shared.mjs has to come along too: maplibre-gl-worker.mjs
// itself has a real, static `import ... from "./maplibre-gl-shared.mjs"`
// at its top. The *main-thread* copy of that same import (inside
// maplibre-gl.mjs) is fine — Vite bundles it normally, since that's an
// ordinary static import in source Vite's own build actually processes.
// But maplibre-gl-worker.mjs is a raw file we copy verbatim, never run
// through Vite at all, so when the browser loads it as a module worker,
// *the browser's own* ES module resolver goes looking for
// ./maplibre-gl-shared.mjs relative to the worker's URL — which fails
// exactly the same way the worker itself used to: no console error at
// the top level (it fails inside the worker's own module graph), just a
// map that silently never finishes initializing.
const MAPLIBRE_WORKER_FILES = ['maplibre-gl-worker.mjs', 'maplibre-gl-shared.mjs']

function copyMaplibreWorker(): Plugin {
  return {
    name: 'copy-maplibre-gl-worker',
    apply: 'build',
    closeBundle() {
      const outDir = join(dirname(fileURLToPath(import.meta.url)), 'dist/assets')
      for (const file of MAPLIBRE_WORKER_FILES) {
        // Resolved via the package's own exports map, not a relative
        // node_modules path guess — this is a workspace, so the
        // installed copy is hoisted to the repo root, not
        // apps/web/node_modules.
        const src = fileURLToPath(import.meta.resolve(`maplibre-gl/dist/${file}`))
        copyFileSync(src, join(outDir, file))
      }
    },
  }
}

// The API listens on :8080 (`domestique serve`); in dev we proxy to it so the
// frontend runs from Vite with hot reload against the real backend.
export default defineConfig({
  plugins: [vue(), ui(), copyMaplibreWorker()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.DOMESTIQUE_API ?? 'http://localhost:8080',
        changeOrigin: true,
      },
      // /sso/* (mode: oidc's login/callback/logout) is a real backend route,
      // not something Vite's SPA fallback should ever serve. Without this a
      // dev session run as `just api` + `just web` would send the Sign in
      // link and the logout fetch nowhere — both would silently hit Vite's
      // own index.html instead of the API.
      '/sso': {
        target: process.env.DOMESTIQUE_API ?? 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      // Two entries: the app, and the logged-out page served on the apex host.
      // Separate bundles on purpose — the landing page should not pull in the
      // router or the API client to render three paragraphs.
      input: {
        main: fileURLToPath(new URL('./index.html', import.meta.url)),
        landing: fileURLToPath(new URL('./landing.html', import.meta.url)),
      },
    },
  },
})
