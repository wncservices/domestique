// Run with: npm --workspace @domestique/web test
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { redactUrl, scrubItem } from '../src/utils/redactUrl.ts'

// 43 base64url characters: the shape of a share or calendar token.
const TOKEN = 'Ab3_-x'.repeat(8).slice(0, 43)

test('a token-shaped path segment is redacted', () => {
  assert.equal(
    redactUrl(`https://app.domestique.dev/foo/${TOKEN}/bar`),
    'https://app.domestique.dev/foo/REDACTED/bar',
  )
})

test('a .ics calendar token keeps its suffix and loses the token', () => {
  assert.equal(
    redactUrl(`https://app.domestique.dev/api/calendar/${TOKEN}.ics`),
    'https://app.domestique.dev/api/calendar/REDACTED.ics',
  )
})

test('the segment after /shared is redacted whatever it holds', () => {
  assert.equal(redactUrl('https://app.domestique.dev/shared/whatever-was-here'), 'https://app.domestique.dev/shared/REDACTED')
})

test('the segment after api/shares is redacted and the /track suffix kept', () => {
  assert.equal(
    redactUrl(`/api/shares/${TOKEN}/track`),
    '/api/shares/REDACTED/track',
  )
})

test('matching is case-insensitive, as on the server', () => {
  assert.equal(redactUrl('/API/Shares/short/track'), '/API/Shares/REDACTED/track')
})

test('a share path in return_to is redacted after decoding', () => {
  assert.equal(
    redactUrl(`https://app.domestique.dev/?return_to=%2Fshared%2F${TOKEN}`),
    'https://app.domestique.dev/?return_to=%2Fshared%2FREDACTED',
  )
})

test('a raw, unencoded return_to share path is redacted too', () => {
  assert.equal(
    redactUrl(`https://domestique.dev/sso?return_to=/shared/${TOKEN}&x=1`),
    'https://domestique.dev/sso?return_to=%2Fshared%2FREDACTED&x=1',
  )
})

test('a URL with no token is returned unchanged', () => {
  const url = 'https://app.domestique.dev/routes/my-ride?tab=map&sort=asc#top'
  assert.equal(redactUrl(url), url)
})

test('scrubItem redacts meta.page.url and a url-like span attribute', () => {
  const item = {
    type: 'traces',
    meta: { page: { url: `https://domestique.dev/?return_to=%2Fshared%2F${TOKEN}`, id: 'p1' } },
    payload: {
      resourceSpans: [
        {
          scopeSpans: [
            {
              spans: [
                {
                  name: `GET /api/shares/${TOKEN}/track`,
                  attributes: [
                    { key: 'http.url', value: { stringValue: `https://app.domestique.dev/api/shares/${TOKEN}/track` } },
                    { key: 'http.method', value: { stringValue: 'GET' } },
                  ],
                },
              ],
            },
          ],
        },
      ],
    },
  }
  const out = scrubItem(item)

  assert.equal(out.meta.page.url, 'https://domestique.dev/?return_to=%2Fshared%2FREDACTED')
  assert.equal(out.meta.page.id, 'p1')
  const span = out.payload.resourceSpans[0].scopeSpans[0].spans[0]
  assert.equal(span.name, 'GET /api/shares/REDACTED/track')
  assert.equal(span.attributes[0].value.stringValue, 'https://app.domestique.dev/api/shares/REDACTED/track')
  assert.equal(span.attributes[1].value.stringValue, 'GET')
  // The original item is not mutated.
  assert.ok(item.meta.page.url.includes(TOKEN))
})

test('scrubItem leaves a payload without spans alone', () => {
  const item = { type: 'log', meta: {}, payload: { message: 'hi' } }
  assert.deepEqual(scrubItem(item), item)
})
