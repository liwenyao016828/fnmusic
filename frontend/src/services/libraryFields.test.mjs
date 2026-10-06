import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  FIELD_DEFS,
  FIELD_KEYS,
  DEFAULT_FIELDS,
  parseFields,
  toggleFieldKey,
  platformName,
  planCells,
  summaryCells,
  summaryBar,
  cardModel,
  cardFields,
  splitCards,
  mapWithLimit,
} from './libraryFields.js'

// 字段 key 是**前后端唯一的契约**：后端 parseCompleteFields 认不出来的词会被静默忽略，
// 界面只表现为「这个字段一次都没补上」。所以这里把它钉死。
test('FIELD_KEYS 与后端 parseCompleteFields 认的词一一对应', () => {
  assert.deepEqual(FIELD_KEYS, ['lyric', 'cover', 'year', 'track', 'disc', 'genre'])
  assert.equal(FIELD_DEFS.length, FIELD_KEYS.length)
  for (const f of FIELD_DEFS) {
    assert.ok(f.name, `${f.key} 缺中文名`)
    assert.ok(f.hint, `${f.key} 缺说明 —— 风格只有 QQ 有，不写清楚用户会以为漏补了`)
  }
})

test('parseFields：非法值丢掉，全空退回默认「歌词+封面」', () => {
  assert.deepEqual(parseFields('year,genre'), ['year', 'genre'])
  assert.deepEqual(parseFields(' year , disc '), ['year', 'disc'])
  // 拼错的 key 必须被丢掉而不是照发给后端
  assert.deepEqual(parseFields('publish_date,year'), ['year'])
  assert.deepEqual(parseFields(''), DEFAULT_FIELDS)
  assert.deepEqual(parseFields(null), DEFAULT_FIELDS)
  assert.deepEqual(parseFields('封面'), DEFAULT_FIELDS)
})

test('toggleFieldKey 增删各一次', () => {
  assert.deepEqual(toggleFieldKey(['lyric'], 'year'), ['lyric', 'year'])
  assert.deepEqual(toggleFieldKey(['lyric', 'year'], 'lyric'), ['year'])
})

test('planCells 只显示真的会写的字段与值', () => {
  const cells = planCells({
    need_lyric: true,
    need_cover: false,
    fill_year: 2003,
    fill_track: 3,
    fill_disc: 0,
    fill_genre: 'Pop 流行',
  })
  assert.deepEqual(cells, [
    { k: '歌词', v: '平台版' },
    { k: '年份', v: 2003 },
    { k: '曲序', v: 3 },
    { k: '风格', v: 'Pop 流行' },
  ])

  // 平台没给值 ⇒ 一个格子都不出（宁可空着，也不显示猜的值）
  assert.deepEqual(planCells({ need_year: true, fill_year: 0, fill_genre: '' }), [])
  assert.deepEqual(planCells(null), [])
})

test('summaryCells 报全部六个字段，0 的不占位', () => {
  const out = summaryCells({
    total: 10, matched: 8, lyric_filled: 5, cover_filled: 5,
    year_filled: 3, track_filled: 2, disc_filled: 0, genre_filled: 1,
    no_match: 2, failed: 1,
  }, 4)
  const byKey = Object.fromEntries(out.map((c) => [c.k, c.v]))
  assert.equal(byKey['共'], 10)
  assert.equal(byKey['补年份'], 3)
  assert.equal(byKey['补曲序'], 2)
  assert.equal(byKey['补风格'], 1)
  assert.equal(byKey['没搜到'], 2)
  assert.equal(byKey['写入失败'], 1)
  assert.equal(byKey['AI 判断匹配'], 4)
  assert.ok(!('补光盘' in byKey), '数量为 0 的字段不该占位')
  // 「补年份」这类必须在**有数量时**出现 —— 以前只有歌词/封面，新字段补了多少用户看不见
  assert.ok(out.some((c) => c.k === '补年份'))
})

test('platformName：显示值来自哪个平台，未知 id 原样回显', () => {
  assert.equal(platformName('tx'), 'QQ 音乐')
  assert.equal(platformName('wy'), '网易云')
  assert.equal(platformName('kg'), '酷狗')
  assert.equal(platformName('kw'), '酷我')
  assert.equal(platformName('xx'), 'xx')
  assert.equal(platformName(''), '—')
})

// ── 补全结果卡片墙 ──

test('summaryBar：六个字段都在，没勾的标出来', () => {
  const bar = summaryBar(['lyric', 'year'], {
    lyric_filled: 3, cover_filled: 0, year_filled: 2, track_filled: 9, disc_filled: 0, genre_filled: 1,
  })
  const byKey = Object.fromEntries(bar.map((c) => [c.key, c]))
  assert.equal(bar.length, 6)
  assert.deepEqual(byKey.lyric, { key: 'lyric', name: '歌词', selected: true, count: 3 })
  assert.equal(byKey.year.count, 2)
  // 没勾却填了的（历史批次/后端兜底）也要看得见数量，但标成未选中
  assert.equal(byKey.track.selected, false)
  assert.equal(byKey.track.count, 9)
  assert.equal(byKey.disc.count, 0)
})

test('cardModel：显示文件里真实的值，写入角标只认 *_filled', () => {
  const card = cardModel(
    { path: '/m/a.mp3', title: '夜曲', artist: '周杰伦', album: '十一月的萧邦', genre: 'Pop 流行', year: 2005, track: 3, disc: 0, has_lyric: true, has_cover: true },
    { path: '/m/a.mp3', title: '夜曲', lyric_filled: true, year_filled: true, matched_by: 'tx' },
  )
  assert.equal(card.year, 2005)
  assert.equal(card.genre, 'Pop 流行')
  assert.equal(card.hasCover, true)
  assert.deepEqual(card.wrote, {
    lyric: true, cover: false, year: true, track: false, disc: false, genre: false,
  })
  // 回读失败时不能凭空造一个标题出来
  assert.equal(cardModel(null, { path: '/m/b.mp3', title: 'X' }).title, 'X')
  assert.equal(cardModel(null, { path: '/m/b.mp3' }).year, 0)
})

test('cardFields：为 0 / 空的字段不显示，歌词封面恒显示有/无', () => {
  const fields = cardFields(cardModel(
    { title: 't', year: 0, track: 5, disc: 0, genre: '' },
    { track_filled: true },
  ))
  assert.deepEqual(fields, [
    { k: '曲序', v: 5, wrote: true },
    { k: '歌词', v: '无', wrote: false },
    { k: '封面', v: '无', wrote: false },
  ])
})

test('splitCards：没匹配上或写失败的绝不进卡片墙', () => {
  const { done, skipped } = splitCards([
    { path: 'a', matched: true },
    { path: 'b', matched: false, error: '没有搜到匹配的曲目' },
    { path: 'c', matched: true, error: '写入标签失败' },
  ])
  assert.deepEqual(done.map((c) => c.path), ['a'])
  assert.deepEqual(skipped.map((c) => c.path), ['b', 'c'])
})

test('mapWithLimit：保序、限并发、单项失败不拖垮整批', async () => {
  let running = 0
  let peak = 0
  const items = [1, 2, 3, 4, 5, 6, 7]
  const out = await mapWithLimit(items, 3, async (n) => {
    running++
    peak = Math.max(peak, running)
    await new Promise((r) => setTimeout(r, 5))
    running--
    if (n === 4) throw new Error('炸了')
    return n * 10
  })
  assert.equal(peak, 3, '并发不该超过上限')
  assert.deepEqual(out.map((r) => r.value), [10, 20, 30, undefined, 50, 60, 70])
  assert.equal(out[3].ok, false)
  assert.match(out[3].error.message, /炸了/)
  assert.equal(out[0].ok, true)
})

