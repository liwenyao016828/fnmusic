import test from 'node:test'
import assert from 'node:assert/strict'

import { createSearchPager, searchSongKey, SEARCH_PAGE_SIZE } from './searchPaging.js'

// 造 n 条歌，id 用 i（跨平台同曲不同源 = 不同 id，与后端的 wy_/tx_/kg_ 前缀一致）
const songs = (from, to, { idPrefix = 's' } = {}) =>
  Array.from({ length: to - from }, (_, k) => ({
    id: `${idPrefix}${from + k}`,
    name: `歌${from + k}`,
    singer: '歌手',
    source: 'wy',
  }))

const total = p => p.buffered

test('searchSongKey：平台 ID 优先，没有 ID 才退回「歌手 - 歌名」', () => {
  assert.equal(searchSongKey({ id: 'wy_1', name: '晴天', singer: '周杰伦' }), 'wy_1')
  assert.equal(searchSongKey({ name: '晴天', singer: '周杰伦' }), '周杰伦 - 晴天')
  assert.equal(searchSongKey({}), '未知 - 未知')
})

test('SEARCH_PAGE_SIZE 是 30', () => {
  assert.equal(SEARCH_PAGE_SIZE, 30)
})

test('第一页露出 30 条，多的先收进缓冲，不打后端', () => {
  const p = createSearchPager()
  const rows = p.start(songs(0, 64))
  assert.equal(rows.length, 30)
  assert.equal(total(p), 34) // 64 - 30
  assert.equal(p.page, 1)
  assert.equal(p.hasMore, true)
  assert.equal(p.canFetch, false) // 缓冲里还有货，先别打后端
})

test('缓冲里的货按 30 条一批往外露，露完才允许拉下一页', () => {
  const p = createSearchPager()
  p.start(songs(0, 64))
  assert.equal(p.reveal().length, 30)
  assert.equal(p.canFetch, false) // 还剩 4 条
  assert.equal(p.reveal().length, 4)
  assert.equal(total(p), 0)
  assert.equal(p.canFetch, true) // 缓冲见底、后端还有 → 该拉下一页了
})

test('beginFetch 期间不许并发第二拉；finishFetch 后 page 前进', () => {
  const p = createSearchPager()
  p.start(songs(0, 64))
  p.reveal()
  p.reveal()
  assert.equal(p.beginFetch(), true)
  assert.equal(p.beginFetch(), false) // 已经在路上
  assert.equal(p.canFetch, false)
  const rows = p.finishFetch(songs(64, 100))
  assert.equal(rows.length, 30)
  assert.equal(p.page, 2)
})

test('不满一页即停：30 条整页之后来 12 条，就没有下一页了', () => {
  const p = createSearchPager()
  p.start(songs(0, 30))
  assert.equal(p.hasMore, true)
  p.beginFetch()
  p.finishFetch(songs(30, 42)) // 12 < 30
  assert.equal(p.hasMore, false)
  assert.equal(p.canFetch, false)
})

test('空页即停', () => {
  const p = createSearchPager()
  p.start(songs(0, 30))
  p.beginFetch()
  assert.deepEqual(p.finishFetch([]), [])
  assert.equal(p.hasMore, false)
})

test('失败即停：finishFetch(null) 后不再拉', () => {
  const p = createSearchPager()
  p.start(songs(0, 30))
  p.beginFetch()
  assert.deepEqual(p.finishFetch(null), [])
  assert.equal(p.hasMore, false)
  assert.equal(p.canFetch, false)
  assert.equal(p.beginFetch(), false)
})

test('第一页本来就不满一页 = 直接到底（不足一屏也不会去拉第 2 页）', () => {
  const p = createSearchPager()
  const rows = p.start(songs(0, 8))
  assert.equal(rows.length, 8)
  assert.equal(p.hasMore, false)
  assert.equal(p.canFetch, false)
})

test('去重：整页都是重复行时收手（防平台不认 page 一直返回同一批）', () => {
  const p = createSearchPager()
  p.start(songs(0, 30))
  p.beginFetch()
  // 第 2 页回来的还是前 30 条 —— 一条新歌都没有
  assert.deepEqual(p.finishFetch(songs(0, 30)), [])
  assert.equal(p.hasMore, false)
})

test('去重：部分重复只露新歌，page 照常前进', () => {
  const p = createSearchPager()
  p.start(songs(0, 30))
  p.beginFetch()
  // 第 2 页：前 10 条是重复、后 20 条是新歌
  const rows = p.finishFetch([...songs(20, 30), ...songs(100, 120)])
  assert.equal(rows.length, 20)
  assert.equal(rows[0].id, 's100')
  assert.equal(p.page, 2)
})

test('跨平台同曲不同 ID 不算重复（平台前缀 ID 各算各的）', () => {
  const p = createSearchPager()
  const wy = { id: 'wy_1', name: '晴天', singer: '周杰伦' }
  const tx = { id: 'tx_0039MnYb0qxYhV', name: '晴天', singer: '周杰伦' }
  const rows = p.start([wy, tx])
  assert.equal(rows.length, 2)
})

test('reset 之后一切从头来', () => {
  const p = createSearchPager()
  p.start(songs(0, 64))
  p.beginFetch?.()
  p.reset()
  assert.equal(p.page, 0)
  assert.equal(total(p), 0)
  assert.equal(p.hasMore, true)
  assert.equal(p.canFetch, true) // 复位后允许重新搜
  const rows = p.start(songs(0, 30))
  assert.equal(rows.length, 30) // 同一批歌重新露出（新搜索，不去重）
})

test('自定义页大小也遵守同一套规则', () => {
  const p = createSearchPager(10)
  const rows = p.start(songs(0, 25))
  assert.equal(rows.length, 10)
  assert.equal(total(p), 15)
  assert.equal(p.reveal().length, 10)
  assert.equal(p.reveal().length, 5)
  assert.equal(p.canFetch, true)
})
