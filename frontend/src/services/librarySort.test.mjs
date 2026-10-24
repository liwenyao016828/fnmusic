import test from 'node:test'
import assert from 'node:assert/strict'

import {
  DEFAULT_SORT,
  SORT_KEYS,
  defaultDirOf,
  isSortKey,
  nextSortDir,
  parseSortPref,
  sortPrefOf,
  sortSongs,
} from './librarySort.js'

// 造一条曲目：只需要排序会用到的字段。
function song(over = {}) {
  return { path: over.path || '/vol1/Music/x.mp3', title: '', filename: '', size: 0, updated_at: 0, ...over }
}

const names = (list) => list.map(s => s.title || s.filename)

// ── 默认排序：最新 ──

test('默认按最新排序（updated_at 倒序）', () => {
  const list = [
    song({ title: '老歌', updated_at: 1000 }),
    song({ title: '最新', updated_at: 3000 }),
    song({ title: '中间', updated_at: 2000 }),
  ]
  assert.deepEqual(names(sortSongs(list, 'newest', 'desc')), ['最新', '中间', '老歌'])
  // 不传 key/dir 时也必须是「最新」——这就是用户一进页面看到的顺序
  assert.deepEqual(names(sortSongs(list)), ['最新', '中间', '老歌'])
  assert.equal(DEFAULT_SORT.key, 'newest')
  assert.equal(DEFAULT_SORT.dir, 'desc')
})

test('updated_at 缺失/为 0 的条目退回后端原顺序，并排在有时长的条目之后', () => {
  const list = [
    song({ title: 'A无时间', updated_at: 0 }),
    song({ title: 'B有时间', updated_at: 200 }),
    song({ title: 'C无时间' }),
    song({ title: 'D有时间', updated_at: 100 }),
  ]
  assert.deepEqual(names(sortSongs(list, 'newest', 'desc')), ['B有时间', 'D有时间', 'A无时间', 'C无时间'])
})

test('全都没有 updated_at 时保持后端原顺序（不能把顺序搞乱）', () => {
  const list = ['a', 'b', 'c', 'd'].map((t, i) => song({ title: t, path: `/v/${i}.mp3` }))
  assert.deepEqual(names(sortSongs(list, 'newest', 'desc')), ['a', 'b', 'c', 'd'])
})

test('时间相同的条目保持原顺序（稳定）', () => {
  const list = ['一', '二', '三'].map((t, i) => song({ title: t, updated_at: 500, path: `/v/${i}.mp3` }))
  assert.deepEqual(names(sortSongs(list, 'newest', 'desc')), ['一', '二', '三'])
})

// ── 文本列：中文友好 + 升降序 ──

test('标题按中文（拼音）比较，而不是按码位', () => {
  // 码位序会把「周」(U+5468) 排在「阿」(U+963F) 前面，拼音序是 a < chen < zhou。
  const list = [song({ title: '周杰伦 - 晴天' }), song({ title: '阿杜 - 他一定很爱你' }), song({ title: '陈奕迅 - 十年' })]
  assert.deepEqual(names(sortSongs(list, 'title', 'asc')), ['阿杜 - 他一定很爱你', '陈奕迅 - 十年', '周杰伦 - 晴天'])
  assert.deepEqual(names(sortSongs(list, 'title', 'desc')), ['周杰伦 - 晴天', '陈奕迅 - 十年', '阿杜 - 他一定很爱你'])
})

test('标题为空时退回文件名参与比较', () => {
  const list = [song({ title: '', filename: 'zz.mp3' }), song({ title: 'aa', filename: 'aa.mp3' })]
  assert.deepEqual(names(sortSongs(list, 'title', 'asc')), ['aa', 'zz.mp3'])
})

test('专辑 / 格式按文本排序', () => {
  const byAlbum = [
    song({ title: 's1', album: '叶惠美' }),
    song({ title: 's2', album: '范特西' }),
    song({ title: 's3' }), // 无专辑 = 空串，升序最前
  ]
  assert.deepEqual(sortSongs(byAlbum, 'album', 'asc').map(s => s.title), ['s3', 's2', 's1'])

  const byFormat = [song({ title: 'f1', format: 'mp3' }), song({ title: 'f2', format: 'flac' }), song({ title: 'f3', format: 'ape' })]
  assert.deepEqual(sortSongs(byFormat, 'format', 'asc').map(s => s.title), ['f3', 'f2', 'f1'])
})

test('同值保持原顺序（稳定排序）', () => {
  const list = ['x1', 'x2', 'x3'].map((t, i) => song({ title: t, format: 'flac', size: 10, path: `/v/${i}.mp3` }))
  assert.deepEqual(sortSongs(list, 'format', 'asc').map(s => s.title), ['x1', 'x2', 'x3'])
  assert.deepEqual(sortSongs(list, 'size', 'desc').map(s => s.title), ['x1', 'x2', 'x3'])
})

// ── 大小：按字节数，不按字符串 ──

test('文件大小按字节比较（9 MB 不能排在 10 MB 前面）', () => {
  const list = [
    song({ title: '九兆', size: 9 * 1024 * 1024 }),
    song({ title: '十兆', size: 10 * 1024 * 1024 }),
    song({ title: '一兆', size: 1024 * 1024 }),
  ]
  // 默认降序：大的在前
  assert.deepEqual(sortSongs(list, 'size').map(s => s.title), ['十兆', '九兆', '一兆'])
  assert.deepEqual(sortSongs(list, 'size', 'asc').map(s => s.title), ['一兆', '九兆', '十兆'])
})

test('大小缺失/非法当 0，降序时排在最后', () => {
  const list = [song({ title: '无', size: undefined }), song({ title: '大', size: 2048 }), song({ title: '零', size: 0 })]
  assert.deepEqual(sortSongs(list, 'size', 'desc').map(s => s.title), ['大', '无', '零'])
})

// ── 非法输入静默回退 ──

test('非法排序键回退默认（最新），不抛错', () => {
  const list = [song({ title: 'a', updated_at: 1 }), song({ title: 'b', updated_at: 2 })]
  assert.deepEqual(names(sortSongs(list, 'filemtime', 'asc')), ['b', 'a'])
  assert.deepEqual(names(sortSongs(list, null, null)), ['b', 'a'])
  assert.equal(isSortKey('bogus'), false)
  assert.ok(SORT_KEYS.includes('size'))
})

test('非法方向回退该列默认方向', () => {
  const list = [song({ title: 'b' }), song({ title: 'a' })]
  // title 的默认方向是 asc
  assert.deepEqual(names(sortSongs(list, 'title', 'sideways')), ['a', 'b'])
  assert.equal(defaultDirOf('size'), 'desc')
  assert.equal(defaultDirOf('title'), 'asc')
  assert.equal(defaultDirOf('nope'), 'desc')
})

test('nextSortDir：同列取反，跨列用该列默认方向', () => {
  assert.equal(nextSortDir('title', 'asc'), 'desc')
  assert.equal(nextSortDir('title', 'desc'), 'asc')
  assert.equal(nextSortDir('size', undefined), 'desc')
  assert.equal(nextSortDir('title', undefined), 'asc')
  assert.equal(nextSortDir('title', 'bogus'), 'asc')
})

// ── 持久化偏好 ──

test('parseSortPref / sortPrefOf 互逆，非法值静默回退', () => {
  assert.deepEqual(parseSortPref('title:asc'), { key: 'title', dir: 'asc' })
  assert.deepEqual(parseSortPref('size:desc'), { key: 'size', dir: 'desc' })
  // 只有键没有方向 → 该列默认方向
  assert.deepEqual(parseSortPref('album'), { key: 'album', dir: 'asc' })
  // 空 / 垃圾 / 不存在的键 → 默认最新
  for (const bad of ['', null, undefined, 'bogus:asc', 'filemtime:desc', ':', 'title:sideways']) {
    const got = parseSortPref(bad)
    if (bad === 'title:sideways') {
      assert.deepEqual(got, { key: 'title', dir: 'asc' })
    } else {
      assert.deepEqual(got, { key: 'newest', dir: 'desc' }, `parseSortPref(${JSON.stringify(bad)})`)
    }
  }
  assert.equal(sortPrefOf({ key: 'title', dir: 'desc' }), 'title:desc')
  assert.equal(sortPrefOf(DEFAULT_SORT), 'newest:desc')
  assert.equal(sortPrefOf(null), 'newest:desc')
})

// ── 纯函数性质 ──

test('不修改入参，返回新数组', () => {
  const list = [song({ title: 'b', updated_at: 1 }), song({ title: 'a', updated_at: 2 })]
  const snapshot = list.map(s => s.title)
  const out = sortSongs(list, 'title', 'asc')
  assert.notEqual(out, list)
  assert.deepEqual(list.map(s => s.title), snapshot)
  assert.deepEqual(names(out), ['a', 'b'])
})

test('空数组/非数组返回空数组', () => {
  assert.deepEqual(sortSongs([], 'title', 'asc'), [])
  assert.deepEqual(sortSongs(null, 'title', 'asc'), [])
  assert.deepEqual(sortSongs(undefined), [])
})
