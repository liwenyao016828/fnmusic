import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  ttlOf, cacheKey, read, age, isFresh, write, drop, dropPrefix, clear, size,
  downloadFresh, markDownloadChecked, markSubscription, dedupe, inflightSize,
} from './detailCache.js'

test('各类数据有自己的 TTL，未知类型给默认值', () => {
  assert.equal(ttlOf('songs'), 300_000)
  assert.equal(ttlOf('downloads'), 60_000)
  assert.equal(ttlOf('nope'), 60_000)
})

test('key 形状固定；缺 kind 或缺 id 时返回空串（= 不缓存）', () => {
  assert.equal(cacheKey('playlist', 'wy', 123), 'playlist:wy:123')
  assert.equal(cacheKey('link', '', 'https://x/y'), 'link::https://x/y')
  assert.equal(cacheKey('playlist', 'wy', ''), '')
  assert.equal(cacheKey('', 'wy', '1'), '')
})

test('写进去能原样读出来，同一对象引用不变', () => {
  clear()
  const songs = [{ id: 'a' }, { id: 'b' }]
  write('playlist:wy:1', { songs })
  assert.equal(read('playlist:wy:1').songs, songs)
  assert.equal(read('missing'), undefined)
  assert.equal(read(''), undefined)
})

test('TTL 内算新鲜，超过就不新鲜了', () => {
  clear()
  write('playlist:wy:1', { songs: [] }, 1000)
  assert.equal(isFresh('playlist:wy:1', 'songs', 1000 + 299_999), true)
  assert.equal(isFresh('playlist:wy:1', 'songs', 1000 + 300_001), false)
  // 不同类型 TTL 不同：同一时刻 downloads 过期、songs 还没
  assert.equal(isFresh('playlist:wy:1', 'downloads', 1000 + 61_000), false)
  assert.equal(isFresh('playlist:wy:1', 'songs', 1000 + 61_000), true)
})

test('没写过的 key 一律不新鲜（= 必须发请求）', () => {
  clear()
  assert.equal(isFresh('playlist:wy:404'), false)
  assert.equal(age('playlist:wy:404'), Infinity)
})

test('age 报告写入时间差', () => {
  clear()
  write('playlist:wy:1', { songs: [] }, 5000)
  assert.equal(age('playlist:wy:1', 5000), 0)
  assert.equal(age('playlist:wy:1', 9000), 4000)
})

test('查已下载不会刷新曲目的新鲜度（否则曲目永不过期）', () => {
  clear()
  write('playlist:wy:1', { songs: [{ id: 'a' }] }, 1000)
  markDownloadChecked('playlist:wy:1', 100_000)
  // at 仍是 1000 → 曲目 5 分钟后照样过期
  assert.equal(isFresh('playlist:wy:1', 'songs', 1000 + 300_001), false)
  // 但已下载态是刚查过的
  assert.equal(downloadFresh('playlist:wy:1', 100_000), true)
  assert.equal(downloadFresh('playlist:wy:1', 100_000 + 60_001), false)
})

test('没查过已下载时 downloadFresh 为假', () => {
  clear()
  write('playlist:wy:1', { songs: [] }, 1000)
  assert.equal(downloadFresh('playlist:wy:1', 1000), false)
  // 不存在的 key 也不能炸
  assert.equal(downloadFresh('nope', 1000), false)
  assert.equal(markDownloadChecked('nope'), undefined)
})

test('drop / dropPrefix / clear 各自的粒度', () => {
  clear()
  write('playlist:wy:1', { songs: [] }, 0)
  write('playlist:wy:2', { songs: [] }, 0)
  write('playlist:tx:1', { songs: [] }, 0)
  drop('playlist:wy:1')
  assert.equal(read('playlist:wy:1'), undefined)
  assert.equal(size(), 2)
  dropPrefix('playlist:wy:')
  assert.equal(read('playlist:wy:2'), undefined)
  assert.equal(size(), 1)
  clear()
  assert.equal(size(), 0)
})

test('容量上限按 LRU 淘汰最久未用的那条', () => {
  clear()
  for (let i = 0; i < 45; i++) write(`playlist:wy:${i}`, { songs: [] }, 0)
  assert.equal(size(), 40)
  // 0..4 被淘汰（最早写入且从未命中）
  assert.equal(read('playlist:wy:0'), undefined)
  assert.equal(read('playlist:wy:44')?.songs.length, 0)
})

test('读一次就算用过：命中过的旧条目不会被淘汰', () => {
  clear()
  for (let i = 0; i < 40; i++) write(`playlist:wy:${i}`, { songs: [] }, 0)
  read('playlist:wy:0')            // 命中，挪到队尾
  write('playlist:wy:new', { songs: [] }, 0) // 挤掉现在最旧的（第 1 条）
  assert.equal(read('playlist:wy:0')?.songs.length, 0)
  assert.equal(read('playlist:wy:1'), undefined)
})

// ── dedupe：同一份数据的并发请求只发一次 ──
// 背景：发现页同时挂两份 SearchView（发现 / 榜单两个 segment），两边 onMounted
// 背靠背请求同一批数据，memGet 挡不住并发，实测一次进页面发两遍。

test('dedupe：并发同一 key 只真正执行一次，且共享同一个结果', async () => {
  let calls = 0
  let release
  const gate = new Promise((r) => { release = r })
  const fn = async () => { calls++; await gate; return { n: calls } }

  const p1 = dedupe('charts:toplists:wy', fn)
  const p2 = dedupe('charts:toplists:wy', fn)
  assert.equal(p1, p2, '在途时后到的调用应复用同一个 Promise')
  assert.equal(calls, 0, '工厂函数是异步起的，此刻还没执行')
  release()
  const [a, b] = await Promise.all([p1, p2])
  assert.equal(calls, 1)
  assert.deepEqual(a, { n: 1 })
  assert.deepEqual(b, { n: 1 })
  assert.equal(inflightSize(), 0, '完成后要从在途表里清掉，否则后续请求永远拿到旧结果')
})

test('dedupe：不同 key 互不影响，完成后可以再发', async () => {
  let calls = 0
  const fn = async () => ++calls
  assert.equal(await dedupe('a', fn), 1)
  assert.equal(await dedupe('b', fn), 2)
  // 同一个 key 再来一次 = 新请求（前一个已结束）
  assert.equal(await dedupe('a', fn), 3)
  assert.equal(inflightSize(), 0)
})

test('dedupe：失败也清场，不会把坏 Promise 永久留在在途表', async () => {
  let calls = 0
  const boom = async () => { calls++; throw new Error('500') }
  await assert.rejects(() => dedupe('boom', boom), /500/)
  assert.equal(inflightSize(), 0)
  await assert.rejects(() => dedupe('boom', boom), /500/)
  assert.equal(calls, 2, '第二次应真的重试，而不是复用上次的失败')
})

test('dedupe：空 key 不参与合并（每次都真发）', async () => {
  let calls = 0
  const fn = async () => ++calls
  await Promise.all([dedupe('', fn), dedupe('', fn)])
  assert.equal(calls, 2)
  assert.equal(inflightSize(), 0)
})

test('markSubscription：只改订阅字段，不动曲目的新鲜度', () => {
  clear()
  const t0 = 1_000_000
  write('playlist:wy:1', { songs: [{ id: 'a' }], subscription: null }, t0)
  markSubscription('playlist:wy:1', { monitorId: 9 })
  assert.deepEqual(read('playlist:wy:1').subscription, { monitorId: 9 })
  // 4:59 后仍然算新鲜（未被订阅写回提前刷成新数据）
  assert.equal(isFresh('playlist:wy:1', 'songs', t0 + 299_000), true)
  assert.equal(isFresh('playlist:wy:1', 'songs', t0 + 301_000), false)
  // 传空值 = 取消订阅，落成 null 而不是 undefined
  markSubscription('playlist:wy:1', null)
  assert.equal(read('playlist:wy:1').subscription, null)
  clear()
})

test('markSubscription：条目或 key 不存在时安静返回', () => {
  clear()
  markSubscription('nope:wy:1', { a: 1 })
  write('x', null)
  markSubscription('x', { a: 1 })   // data 为 null，不应炸
  assert.equal(read('nope:wy:1'), undefined)
  clear()
})
