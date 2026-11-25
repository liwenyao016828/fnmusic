import test from 'node:test'
import assert from 'node:assert/strict'

import {
  KIND_ORDER, kindLabel, audioKindLabel, healthFixHint,
  formatDuration, formatSize,
  titleFixRows, dupRows, healthRows, audioRows,
  mergeRows, filterRows, kindCounts, dupGroupsFromSelection, selectionCounts,
  selectableRows, candidateRows, parseInterval, candidateKeyword, qualityRank,
  titleMatchScore, versionLabel, versionRank,
} from './repairList.js'

// ── 基础展示 ────────────────────────────────────────────────────────

test('时长按 m:ss 展示，超过一小时给 h:mm:ss', () => {
  assert.equal(formatDuration(0), '—')
  assert.equal(formatDuration(3), '0:03')
  assert.equal(formatDuration(225), '3:45')
  assert.equal(formatDuration(3725), '1:02:05')
})

test('体积按 1024 进制展示', () => {
  assert.equal(formatSize(0), '—')
  assert.equal(formatSize(820), '820 B')
  assert.equal(formatSize(1024 * 1024 * 12.3), '12.3 MB')
  assert.equal(formatSize(1024 ** 3 * 2), '2.0 GB')
})

test('四类问题的中文名与文件名体检动作提示', () => {
  assert.equal(kindLabel('title'), '歌名')
  assert.equal(kindLabel('nope'), 'nope')
  assert.equal(audioKindLabel('ext_mismatch'), '格式不对')
  assert.equal(audioKindLabel('too_short'), '时长过短')
  assert.equal(audioKindLabel('fake_lossless'), '假无损')
  assert.equal(healthFixHint('rename'), '可自动改名')
  assert.equal(healthFixHint('manual'), '需手动处理')
})

// ── 行模型 ─────────────────────────────────────────────────────────

test('歌名修复的每一首都会成为一行，修完的带已写回文案', () => {
  const rows = titleFixRows([
    { path: '/m/a.flac', title: 'track01', artist: '某歌手', reason: '歌名像是文件名', suggested_title: '晴天', selected: true },
    { path: '/m/b.flac', title: 'x', artist: 'y', done: true, written_title: '稻香', selected: true },
  ])
  assert.equal(rows.length, 2)
  assert.equal(rows[0].name, 'a.flac')
  assert.equal(rows[0].suggest, '晴天')
  assert.equal(rows[0].tone, 'warn')
  assert.ok(rows[0].selected)
  assert.equal(rows[1].done, true)
  assert.equal(rows[1].tone, 'ok')
  assert.match(rows[1].detail, /稻香/)
  // 「建议 / 结果」列在 done 时读的是 written（实际写回值），不是 suggested（原建议）
  assert.equal(rows[1].written, '稻香')
  assert.equal(rows[0].written, '')
})

test('查重把一组摊成「保留 + 可删」，可删项默认勾上', () => {
  const rows = dupRows({
    groups: [
      { type: 'content', key: 'k1', count: 3, total_size: 30, keep: '/m/keep.flac', delete: ['/m/v1.flac', '/m/v2.flac'], items: [] },
      { type: 'name', key: 'k2', count: 2, total_size: 8, keep: '/m/k2a.mp3', delete: ['/m/k2b.mp3'], items: [] },
    ],
  })
  assert.equal(rows.length, 5)
  assert.equal(rows[0].role, 'keep')
  assert.equal(rows[0].selected, false)
  assert.match(rows[0].detail, /保留这一份/)
  assert.equal(rows[1].role, 'victim')
  assert.ok(rows[1].selected)
  assert.match(rows[1].detail, /内容相同/)
  assert.match(rows[4].detail, /同名不同版/)
})

test('文件名体检的行带上后端 ID 与动作提示，manual 的不算致命', () => {
  const rows = healthRows([
    { id: 'abc', path: '/m/坏名?.flac', name: '坏名?.flac', kind: 'illegal_char', detail: '含非法字符 ?', fix: 'rename', suggest: '坏名.flac', size: 2048 },
    { id: 'def', path: '/m/x.flac', kind: 'not_regular', detail: '不是普通文件', fix: 'manual', size: 0 },
  ])
  assert.equal(rows[0].rawId, 'abc')
  assert.equal(rows[0].fix, 'rename')
  assert.match(rows[0].detail, /可自动改名/)
  assert.match(rows[0].detail, /坏名\.flac/)
  assert.equal(rows[0].meta, '2.0 KB')
  assert.equal(rows[1].tone, 'warn')
})

test('音频问题的行带上容器 / 时长 / 体积', () => {
  const rows = audioRows([
    { path: '/m/fake.flac', title: '某歌', artist: '某人', kind: 'fake_lossless', detail: '无损容器但等效码率只有 64kbps', container: 'mp3', duration_sec: 225, size: 1024 * 1024, kbps: 64 },
  ])
  assert.equal(rows.length, 1)
  assert.match(rows[0].detail, /假无损/)
  assert.match(rows[0].detail, /等效码率/)
  assert.equal(rows[0].meta, 'MP3 · 3:45 · 1.0 MB')
  assert.equal(rows[0].subKind, 'fake_lossless')
})

// ── 合并 / 过滤 / 勾选 ──────────────────────────────────────────────

test('四类按固定顺序合并，且一类的空结果不影响其他类', () => {
  const rows = mergeRows({
    audio: audioRows([{ path: '/m/a.flac', kind: 'too_short', detail: '只有 8 秒' }]),
    title: titleFixRows([{ path: '/m/b.flac', title: 't', selected: true }]),
    file: [],
    dup: [],
  })
  assert.deepEqual(rows.map((r) => r.kind), ['title', 'audio'])
  assert.deepEqual(KIND_ORDER, ['title', 'dup', 'file', 'audio'])
})

test('扫描结果为空时行数为 0（由视图层决定显示空态，而不是这里丢数据）', () => {
  assert.deepEqual(mergeRows({}), [])
  assert.deepEqual(titleFixRows(null), [])
})

test('按类过滤与计数', () => {
  const rows = mergeRows({
    title: titleFixRows([{ path: '/a', title: 't', selected: true }]),
    dup: dupRows({ groups: [{ type: 'content', count: 2, keep: '/b', delete: ['/c'] }] }),
    audio: audioRows([{ path: '/d', kind: 'too_short', detail: '短' }]),
  })
  assert.equal(rows.length, 4)
  assert.equal(filterRows(rows, 'all').length, 4)
  assert.equal(filterRows(rows, 'dup').length, 2)
  assert.equal(filterRows(rows, '').length, 4)
  const counts = kindCounts(rows)
  assert.deepEqual(counts, { all: 4, title: 1, dup: 2, file: 0, audio: 1 })
})

test('勾选的重复行能还原成后端要的 groups，保留项永远是原来的 keep', () => {
  const result = {
    groups: [
      { type: 'content', key: 'k1', count: 3, total_size: 30, keep: '/m/keep.flac', delete: ['/m/v1.flac', '/m/v2.flac'], items: [] },
      { type: 'name', key: 'k2', count: 2, total_size: 8, keep: '/m/k2a.mp3', delete: ['/m/k2b.mp3'], items: [] },
    ],
  }
  const rows = dupRows(result)
  // 只取消第二组的勾选
  const k2Victim = rows.find((r) => r.path === '/m/k2b.mp3')
  k2Victim.selected = false
  const groups = dupGroupsFromSelection(rows)
  assert.equal(groups.length, 1)
  assert.equal(groups[0].keep, '/m/keep.flac')
  assert.equal(groups[0].type, 'content')
  assert.deepEqual(groups[0].delete, ['/m/v1.flac', '/m/v2.flac'])

  // 全取消 → 不提交任何组（不能因为「什么都没勾」就传空 groups 触发重扫+全删）
  rows.forEach((r) => { if (r.role === 'victim') r.selected = false })
  assert.deepEqual(dupGroupsFromSelection(rows), [])
})

test('勾选汇总与全选范围排除保留行和已修完的行', () => {
  const rows = mergeRows({
    title: titleFixRows([
      { path: '/a', title: 't', selected: true },
      { path: '/b', title: 't', selected: true, done: true },
    ]),
    dup: dupRows({ groups: [{ type: 'content', count: 2, keep: '/k', delete: ['/v'] }] }),
  })
  const counts = selectionCounts(rows)
  assert.equal(counts.title, 1)
  assert.equal(counts.dup, 1)
  assert.equal(counts.all, 2)
  assert.deepEqual(selectableRows(rows, 'dup').map((r) => r.path), ['/v'])
})

// ── 候选版本 ───────────────────────────────────────────────────────

test('候选版本按音质优先、再按时长接近排序，且丢掉缺平台 id 的项', () => {
  const rows = candidateRows([
    { source: 'wy', songmid: '1', name: '晴天', singer: '周杰伦', quality: '128k', duration: 269, file_size: 1 },
    { source: 'wy', songmid: '2', name: '晴天', singer: '周杰伦', quality: 'flac', duration: 269 },
    { source: 'tx', songmid: '3', name: '晴天', singer: '周杰伦', quality: '320k', duration: 20 },
    { source: 'wy', songmid: '', name: '没有 id', quality: 'flac' },
  ], 269)
  // flac > 320k > 128k（同音质才比时长，所以 20 秒那条 320k 排在 269 秒的 128k 前面）
  assert.deepEqual(rows.map((r) => r.platformId), ['2', '3', '1'])
  assert.equal(rows[0].quality, 'flac')
})

test('同音质时更接近目标时长的排前面', () => {
  const rows = candidateRows([
    { source: 'wy', songmid: 'a', quality: '320k', duration: 20 },
    { source: 'wy', songmid: 'b', quality: '320k', duration: 268 },
  ], 269)
  assert.deepEqual(rows.map((r) => r.platformId), ['b', 'a'])
})

test('interval 字符串能兜底成秒，搜索关键词只留歌名（不带歌手）', () => {
  assert.equal(parseInterval('03:45'), 225)
  assert.equal(parseInterval('1:02:05'), 3725)
  assert.equal(parseInterval(''), 0)
  // 2026-11-25 用户口径：搜索框只填歌名。歌手拼进去会把结果带偏
  // （真机上「你瞒我瞒 陈柏宇」搜出 107 个版本，反而更难挑）。
  assert.equal(candidateKeyword('01 晴天', '周杰伦/某人'), '晴天')
  assert.equal(candidateKeyword('稻香', ''), '稻香')
  // 文件名里的「 - 歌手」「_来源」尾巴要切掉
  assert.equal(candidateKeyword('你瞒我瞒 - ls 陈柏宇', '陈柏宇'), '你瞒我瞒')
  assert.equal(candidateKeyword('夜曲_ls_周杰伦', ''), '夜曲')
  // 括号里的版本说明（Live / 伴奏 / 翻唱）对「找同一首歌」没帮助
  assert.equal(candidateKeyword('夜曲 (Live)', ''), '夜曲')
  assert.equal(candidateKeyword('晴天【伴奏】', ''), '晴天')
})

test('音质档位排序：flac 高于 320k，未知档位垫底', () => {
  assert.ok(qualityRank('flac') > qualityRank('320k'))
  assert.ok(qualityRank('flac24bit') > qualityRank('flac'))
  assert.equal(qualityRank(''), -1)
})

// ── 匹配度与原唱/翻唱（2026-11-25 用户四条需求里的两条） ──

test('titleMatchScore：同名最高，其次开头一致，其次包含，不沾边为 0', () => {
  // 括号里的版本说明被剥掉 —— 「夜曲 (Live)」和「夜曲」是同一首歌，同档匹配度
  assert.equal(titleMatchScore('夜曲 (Live)', '夜曲'), titleMatchScore('夜曲', '夜曲'))
  // 加了后缀（remix 版）只能算「开头一致」
  assert.ok(titleMatchScore('夜曲', '夜曲') > titleMatchScore('夜曲remix版', '夜曲'))
  // 包含关系比不沾边高
  assert.ok(titleMatchScore('夜曲remix版', '夜曲') > titleMatchScore('七里香', '夜曲'))
  assert.equal(titleMatchScore('七里香', '夜曲'), 0)
  // 标点/空格/大小写不影响判断
  assert.equal(titleMatchScore('Hello, World!', 'hello world'), 100)
})

test('versionLabel：歌名对得上才打标签，歌手与原文件一致算原唱', () => {
  // 同名 + 歌手一致 → 原唱
  assert.equal(versionLabel('夜曲', '周杰伦', '夜曲', '周杰伦'), '原唱')
  // 同名 + 歌手不同 → 翻唱
  assert.equal(versionLabel('夜曲', '某翻唱歌手', '夜曲', '周杰伦'), '翻唱')
  // 歌手拼接（「周杰伦/某某」）也算同一个人
  assert.equal(versionLabel('夜曲', '周杰伦/某某', '夜曲', '周杰伦'), '原唱')
  // 歌名都不沾边 → 不打标签（不能把别的歌标成翻唱）
  assert.equal(versionLabel('七里香', '周杰伦', '夜曲', '周杰伦'), '')
  // 没有原歌手信息时只能靠歌名里自带的「翻唱」字样
  assert.equal(versionLabel('夜曲（翻唱版）', '某人', '夜曲', ''), '翻唱')
  assert.equal(versionLabel('夜曲', '某人', '夜曲', ''), '')
})

test('candidateRows：匹配度 → 原唱 → 音质 → 时长，四层依次生效', () => {
  const rows = candidateRows([
    // 别的歌（不沾边），音质最高也要垫底
    { source: 'wy', songmid: 'other', name: '七里香', singer: '周杰伦', quality: 'flac24bit', duration: 299 },
    // 同名但翻唱
    { source: 'kg', songmid: 'cover', name: '夜曲', singer: '某翻唱歌手', quality: 'flac', duration: 226 },
    // 同名 + 原唱 + 低音质
    { source: 'tx', songmid: 'orig', name: '夜曲', singer: '周杰伦', quality: '128k', duration: 226 },
    // 同名 + 原唱 + 高音质（Live 版）
    { source: 'wy', songmid: 'live', name: '夜曲 (Live)', singer: '周杰伦', quality: 'flac', duration: 228 },
  ], 226, { keyword: '夜曲', artist: '周杰伦' })
  // 同一首歌的三条都在「别的歌」前面；同为原唱时音质高的在前
  assert.deepEqual(rows.map((r) => r.platformId), ['live', 'orig', 'cover', 'other'])
  assert.equal(rows[0].version, '原唱')
  assert.equal(rows[2].version, '翻唱')
  assert.equal(rows[3].version, '') // 别的歌不打标签
})

test('candidateRows：不传 keyword 时退回老口径（音质 → 时长）', () => {
  const rows = candidateRows([
    { source: 'wy', songmid: 'a', name: 'x', quality: '128k', duration: 20 },
    { source: 'wy', songmid: 'b', name: 'x', quality: 'flac', duration: 269 },
  ], 269)
  assert.deepEqual(rows.map((r) => r.platformId), ['b', 'a'])
  assert.equal(rows[0].version, '')
})