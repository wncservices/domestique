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

// A spinner icon in a page is a page-level loading state; use SkeletonCard.
// A button's own `:loading` prop never names the icon, so it cannot trip this.
const PAGE_SPINNER = /<UIcon\b[^>]*?:?name="[^"]*i-lucide-loader/

test('design: pages show loading as skeletons, not spinner icons', () => {
  const bad = offenders(PAGE_SPINNER, (f) => !f.rel.startsWith('pages/'))
  assert.deepEqual(bad, [])
})

test('design: the page-spinner scanner catches a spinner and ignores a loading prop and comments', () => {
  assert.equal(hitsIn('<UIcon name="i-lucide-loader-circle" class="animate-spin" />', PAGE_SPINNER).length, 1)
  assert.equal(hitsIn('<UIcon :name="busy ? \'i-lucide-loader-circle\' : \'i-lucide-x\'" />', PAGE_SPINNER).length, 1)
  assert.equal(hitsIn('<UIcon class="size-5" name="i-lucide-loader-circle" />', PAGE_SPINNER).length, 1)
  assert.equal(hitsIn('<UButton :loading="saving">Save</UButton>', PAGE_SPINNER).length, 0)
  assert.equal(hitsIn('<!-- <UIcon name="i-lucide-loader" /> -->', PAGE_SPINNER).length, 0)
})

// Font sizes come from the type tokens (`text-[length:var(--text-body)]`).
// Exact names with a boundary that also excludes `-`, so `text-left`,
// `text-muted` and `text-sm-ish` pass while `sm:text-sm` does not.
const RAW_TEXT_SIZE = /(?<![\w-])text-(?:xs|sm|base|lg|xl|2xl|3xl|4xl|5xl|6xl)(?![\w-])/

test('design: no raw Tailwind font-size classes', () => {
  assert.deepEqual(offenders(RAW_TEXT_SIZE, (f) => !f.rel.endsWith('.vue')), [])
})

test('design: the font-size scanner catches raw sizes and variants but not tokens or look-alikes', () => {
  assert.equal(hitsIn('<p class="text-sm text-muted">', RAW_TEXT_SIZE).length, 1)
  assert.equal(hitsIn('<p class="sm:text-sm">', RAW_TEXT_SIZE).length, 1)
  assert.equal(hitsIn('<p class="md:hover:text-2xl">', RAW_TEXT_SIZE).length, 1)
  assert.equal(hitsIn('<p class="text-[length:var(--text-body)]">', RAW_TEXT_SIZE).length, 0)
  assert.equal(hitsIn('<p class="sm:text-[length:var(--text-display)]">', RAW_TEXT_SIZE).length, 0)
  assert.equal(hitsIn('<p class="text-muted text-highlighted text-left text-center text-sm-ish text-xs-wide">', RAW_TEXT_SIZE).length, 0)
  assert.equal(hitsIn('<!-- text-sm -->', RAW_TEXT_SIZE).length, 0)
})
