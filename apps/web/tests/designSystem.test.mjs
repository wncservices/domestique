// Run with: node --test apps/web/tests
// Enforces docs/design-system.md. Paths resolve from this file, not the cwd.
import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'

const SRC = fileURLToPath(new URL('../src', import.meta.url))
const STYLES = fileURLToPath(new URL('../src/styles.css', import.meta.url))
const DOC = fileURLToPath(new URL('../../../docs/design-system.md', import.meta.url))

// Files that paint a map or an elevation gradient and so need literal colours.
// Each must say why in a comment. Task 3 removes ElevationProfile.vue.
const ALLOWED_HEX_FILES = [
  'RouteMap.vue',
  'TrackPreview.vue',
  'RouteBuilderMap.vue',
  'ElevationProfile.vue',
]

function walk(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = join(dir, e.name)
    if (e.isDirectory()) return walk(p)
    return /\.(vue|ts)$/.test(e.name) ? [p] : []
  })
}

const files = walk(SRC).map((path) => ({
  path,
  name: basename(path),
  rel: path.slice(SRC.length + 1),
  text: readFileSync(path, 'utf8'),
}))

// Comments are prose: "since #174" is an issue number, not a colour. The `//`
// form needs leading whitespace so a URL's `://` is not mistaken for one.
function withoutComments(text) {
  return text
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|\s)\/\/.*$/gm, '$1')
}

function offenders(re, skip = () => false) {
  return files
    .filter((f) => !skip(f))
    .flatMap((f) => {
      const hits = withoutComments(f.text).match(new RegExp(re.source, 'g'))
      return hits ? [`${f.rel}: ${[...new Set(hits)].join(', ')}`] : []
    })
}

test('design: scans a plausible number of source files', () => {
  assert.ok(files.length > 20, `only found ${files.length} files under ${SRC}`)
})

test('design: no literal hex colours outside the map-painting allowlist', () => {
  const bad = offenders(/#[0-9a-fA-F]{3,8}\b/, (f) => ALLOWED_HEX_FILES.includes(f.name))
  assert.deepEqual(bad, [])
})

test('design: no raw Tailwind palette classes', () => {
  const bad = offenders(
    /\b(text|bg|border|fill|stroke|ring|from|to)-(red|blue|green|orange|purple|slate|gray|amber|yellow|indigo|violet|pink|rose|emerald|teal|cyan|lime|zinc|stone|neutral|sky|ember)-\d{2,3}\b/,
  )
  assert.deepEqual(bad, [])
})

test('design: no monospace font', { todo: true }, () => {
  assert.deepEqual(offenders(/\bfont-mono\b/), [])
})

test('design: no tiny uppercase eyebrows', { todo: true }, () => {
  assert.deepEqual(offenders(/text-\[0\.7rem\][^"]*uppercase/), [])
})

test('design: every token in styles.css is documented', () => {
  const css = readFileSync(STYLES, 'utf8')
  const doc = readFileSync(DOC, 'utf8')
  const blocks = [...css.matchAll(/@theme\b[^{]*\{([\s\S]*?)\n\}/g)].map((m) => m[1])
  const names = blocks.flatMap((b) =>
    [...b.matchAll(/(--(?:color-zone-|color-phase-|text-|radius-)[\w-]*)\s*:/g)].map((m) => m[1]),
  )
  const missing = [...new Set(names)].filter((n) => !doc.includes(n))
  assert.deepEqual(missing, [])
})

test('design: the documented allowlist matches the test', () => {
  const doc = readFileSync(DOC, 'utf8')
  const missing = ALLOWED_HEX_FILES.filter((f) => !doc.includes(f))
  assert.deepEqual(missing, [])
})
