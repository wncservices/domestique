// Run with: node --test apps/web/tests
// Enforces docs/design-system.md. Paths resolve from this file, not the cwd.
import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'
import { join, relative, sep } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'

const SRC = fileURLToPath(new URL('../src', import.meta.url))
const STYLES = fileURLToPath(new URL('../src/styles.css', import.meta.url))
const DOC = fileURLToPath(new URL('../../../docs/design-system.md', import.meta.url))

// Files that paint a map or an elevation gradient and so need literal colours.
// Each must say why in a comment. Matched on the path relative to src/, so a
// same-named file elsewhere does not inherit the exemption.
const ALLOWED_HEX_FILES = [
  'components/RouteMap.vue',
  'components/TrackPreview.vue',
  'components/RouteBuilderMap.vue',
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
  rel: relative(SRC, path).split(sep).join('/'),
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

function hitsIn(text, re) {
  return withoutComments(text).match(new RegExp(re.source, 'g')) ?? []
}

function offenders(re, skip = () => false) {
  return files
    .filter((f) => !skip(f))
    .flatMap((f) => {
      const hits = hitsIn(f.text, re)
      return hits.length ? [`${f.rel}: ${[...new Set(hits)].join(', ')}`] : []
    })
}

const HEX = /#[0-9a-fA-F]{3,8}\b/

test('design: scans a plausible number of source files', () => {
  assert.ok(files.length > 20, `only found ${files.length} files under ${SRC}`)
})

test('design: no literal hex colours outside the map-painting allowlist', () => {
  const bad = offenders(HEX, (f) => ALLOWED_HEX_FILES.includes(f.rel))
  assert.deepEqual(bad, [])
})

// The scanner must keep catching real colours and ignoring prose, or the rule
// above passes by being blind.
test('design: the hex scanner catches real hex and ignores comments', () => {
  assert.deepEqual(hitsIn("const c = '#fff'", HEX), ['#fff'])
  assert.deepEqual(hitsIn('<div style="color:#abc">x</div>', HEX), ['#abc'])
  assert.deepEqual(hitsIn('// since #174', HEX), [])
  assert.deepEqual(hitsIn('<!-- #174 -->', HEX), [])
  assert.deepEqual(hitsIn('/* see #174 */', HEX), [])
})

test('design: the uppercase scanner catches a plain uppercase class', () => {
  assert.deepEqual(hitsIn('<h3 class="text-xs uppercase tracking-wide">', /\buppercase\b/), ['uppercase'])
  assert.deepEqual(hitsIn('// no uppercase here', /\buppercase\b/), [])
})

test('design: no raw Tailwind palette classes', () => {
  const bad = offenders(
    /\b(text|bg|border|fill|stroke|ring|from|to)-(red|blue|green|orange|purple|slate|gray|amber|yellow|indigo|violet|pink|rose|emerald|teal|cyan|lime|zinc|stone|neutral|sky|ember)-\d{2,3}\b/,
  )
  assert.deepEqual(bad, [])
})

test('design: no monospace font', () => {
  assert.deepEqual(offenders(/\bfont-mono\b/), [])
})

test('design: no uppercase text', () => {
  assert.deepEqual(offenders(/\buppercase\b/), [])
})

test('design: every token in styles.css is documented', () => {
  const css = readFileSync(STYLES, 'utf8')
  const doc = readFileSync(DOC, 'utf8')
  const blocks = [...css.matchAll(/@theme\b[^{]*\{([\s\S]*?)\n\}/g)].map((m) => m[1])
  const names = blocks.flatMap((b) =>
    [...b.matchAll(/(--(?:color-zone-|color-phase-|text-|radius-)[\w-]*)\s*:/g)].map((m) => m[1]),
  )
  assert.ok(blocks.length > 0, 'no @theme block found in styles.css')
  assert.ok(names.length > 0, 'no documented-token candidates found in styles.css')
  // Whole token names: `--text-sm` must not be satisfied by `--text-small`.
  const missing = [...new Set(names)].filter((n) => !new RegExp(`${n}(?![\\w-])`).test(doc))
  assert.deepEqual(missing, [])
})

test('design: the documented allowlist matches the test', () => {
  const doc = readFileSync(DOC, 'utf8')
  const missing = ALLOWED_HEX_FILES.filter((f) => !doc.includes(f))
  assert.deepEqual(missing, [])
})
