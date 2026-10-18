// Run with: npm --workspace @domestique/web test
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { menuItems, navItems } from '../src/components/shell/navItems.ts'

test('nav: six destinations in two groups', () => {
  assert.deepEqual(
    navItems.map((i) => i.label),
    ['Today', 'Plan', 'Workouts', 'Fitness', 'Routes', 'Crews'],
  )
  assert.deepEqual(
    navItems.map((i) => i.group),
    ['you', 'you', 'you', 'you', 'shared', 'shared'],
  )
  assert.deepEqual(
    navItems.map((i) => i.to),
    ['/today', '/plan', '/workouts', '/fitness', '/routes', '/crews'],
  )
})

test('menu: People is admin only', () => {
  assert.ok(!menuItems('viewer').some((i) => i.label === 'People'))
  assert.ok(!menuItems('rider').some((i) => i.label === 'People'))
  assert.ok(menuItems('admin').some((i) => i.label === 'People'))
})

test('menu: settings and theme are always there', () => {
  const labels = menuItems('viewer').map((i) => i.label)
  assert.ok(labels.includes('Settings'))
  assert.ok(labels.includes('Theme'))
})

test('menu: sign out depends on how the session ends', () => {
  assert.ok(!menuItems('rider').some((i) => i.label === 'Sign out'))
  const post = menuItems('rider', { signOut: 'post' }).find((i) => i.label === 'Sign out')
  assert.equal(post?.action, 'signOut')
  const link = menuItems('rider', { signOut: 'link', logoutUrl: '/out' }).find((i) => i.label === 'Sign out')
  assert.equal(link?.to, '/out')
})

test('menu: the open-deployment notice shows only when auth is off', () => {
  assert.ok(!menuItems('admin', { authMode: 'oidc' }).some((i) => i.notice))
  const open = menuItems('admin', { authMode: 'none' }).find((i) => i.notice)
  assert.equal(open?.notice, 'no login required')
})

test('menu: Add route and Build route are page actions on Routes, not menu items', () => {
  const labels = menuItems('admin', { authMode: 'oidc', signOut: 'post' }).map((i) => i.label)
  assert.ok(!labels.includes('Add route'))
  assert.ok(!labels.includes('Build route'))
})
