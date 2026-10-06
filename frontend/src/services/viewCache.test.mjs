import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ttlFor, markLoaded, lastLoadedAt, isStale, invalidate, resetViewCache } from './viewCache.js'

test('每个视图有自己的 TTL，未知视图给默认值', () => {
  assert.equal(ttlFor('logs'), 5000)
  assert.equal(ttlFor('accounts'), 300000)
  assert.equal(ttlFor('unknown-view'), 30000)
})

test('没加载过就算过期', () => {
  resetViewCache()
  assert.equal(isStale('nas'), true)
})

test('刚标记过就不算过期；超过 TTL 才算', () => {
  resetViewCache()
  markLoaded('logs', 1000)
  assert.equal(isStale('logs', 1000 + 4999), false)
  assert.equal(isStale('logs', 1000 + 5001), true)
})

test('TTL 按视图区分：同一时刻 logs 过期而 accounts 没过期', () => {
  resetViewCache()
  markLoaded('logs', 0)
  markLoaded('accounts', 0)
  const now = 60_000
  assert.equal(isStale('logs', now), true)
  assert.equal(isStale('accounts', now), false)
})

test('invalidate 强制过期，即使刚加载过', () => {
  resetViewCache()
  markLoaded('nas', 1000)
  assert.equal(isStale('nas', 1001), false)
  invalidate('nas')
  assert.equal(isStale('nas', 1001), true)
})

test('invalidate 支持一次标多个，也让"从没加载过"的视图保持过期', () => {
  resetViewCache()
  markLoaded('nas', 0)
  markLoaded('library', 0)
  invalidate('nas', 'library', 'search')
  assert.equal(isStale('nas', 1), true)
  assert.equal(isStale('library', 1), true)
  assert.equal(isStale('search', 1), true)
})

test('markLoaded 会清掉 invalidate 的标记', () => {
  resetViewCache()
  invalidate('library')
  assert.equal(isStale('library'), true)
  markLoaded('library', 5000)
  assert.equal(isStale('library', 5001), false)
})

test('lastLoadedAt 未加载时返回 0', () => {
  resetViewCache()
  assert.equal(lastLoadedAt('nas'), 0)
  markLoaded('nas', 777)
  assert.equal(lastLoadedAt('nas'), 777)
})

test('传空值不炸', () => {
  resetViewCache()
  markLoaded('')
  assert.equal(isStale(''), false)
  invalidate(null, undefined, 'logs')
  assert.equal(isStale('logs'), true)
})
