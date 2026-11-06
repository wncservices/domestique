import assert from 'node:assert/strict'
import { test } from 'node:test'
import { cardState, shortcutHelp } from '../src/utils/appleHealthCard.ts'

test('an unlinked rider sees the off state, even with a stray token', () => {
  assert.equal(cardState({ linked: false, shortcutUrl: '' }, null), 'off')
  assert.equal(cardState({ linked: false, shortcutUrl: '' }, 'abc'), 'off')
})

test('a linked rider with a freshly issued token is just-linked', () => {
  assert.equal(cardState({ linked: true, shortcutUrl: '' }, 'abc'), 'just-linked')
})

test('a linked rider without a token in memory is linked', () => {
  assert.equal(cardState({ linked: true, shortcutUrl: '' }, null), 'linked')
  assert.equal(cardState({ linked: true, shortcutUrl: '' }, ''), 'linked')
})

test('no shortcut url falls back to the docs', () => {
  assert.deepEqual(shortcutHelp({ linked: true, shortcutUrl: '' }), { kind: 'docs' })
})

test('a configured shortcut url is linked directly', () => {
  assert.deepEqual(shortcutHelp({ linked: true, shortcutUrl: 'https://example.test/s' }), {
    kind: 'link',
    url: 'https://example.test/s',
  })
})
