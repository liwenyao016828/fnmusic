import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  SCAN_TRUNCATED_TEXT,
  SCAN_TRUNCATED_TITLE,
  breadcrumbsFor,
  scanCacheKey,
  scanViewOf,
  scanViewOfCache,
  scanCachePayload,
  scanHintOf,
  isUnderVolume,
  volumeOf,
} from './nasView.js'

test('breadcrumbsFor：逐段累积，每段都能点回去', () => {
  assert.deepEqual(breadcrumbsFor('/vol1/Music/专辑'), [
    { name: 'vol1', path: '/vol1' },
    { name: 'Music', path: '/vol1/Music' },
    { name: '专辑', path: '/vol1/Music/专辑' },
  ])
  assert.deepEqual(breadcrumbsFor('/vol1'), [{ name: 'vol1', path: '/vol1' }])
  // 结尾多一个斜杠不能让最后一段变成空的（那样的面包屑点了没反应）
  assert.deepEqual(breadcrumbsFor('/vol1/Music/'), breadcrumbsFor('/vol1/Music'))
  assert.deepEqual(breadcrumbsFor('/vol1/Music/专辑/'), breadcrumbsFor('/vol1/Music/专辑'))
})

test('breadcrumbsFor：空路径用兜底值，绝不返回空数组', () => {
  assert.deepEqual(breadcrumbsFor(''), [{ name: 'vol1', path: '/vol1' }, { name: 'Music', path: '/vol1/Music' }])
  assert.deepEqual(breadcrumbsFor(null, '/vol3/1000/音乐'), [
    { name: 'vol3', path: '/vol3' },
    { name: '1000', path: '/vol3/1000' },
    { name: '音乐', path: '/vol3/1000/音乐' },
  ])
  for (const p of ['', null, undefined, '/']) {
    const list = breadcrumbsFor(p)
    assert.ok(list.length >= 1, `${p} 的面包屑是空的 —— 标题行会整条消失`)
    assert.ok(list.every((c) => c.name && c.path.startsWith('/')), `${p} 的段缺名字或路径`)
  }
})

test('scanCacheKey：按目录分开', () => {
  assert.equal(scanCacheKey('/vol1/Music'), 'nas_songs:/vol1/Music')
  assert.notEqual(scanCacheKey('/vol1/Music'), scanCacheKey('/vol1/Music/专辑'))
})

test('scanViewOf：顶层与 data 包装两套形状都认，脏值不炸', () => {
  const songs = [{ id: 1 }, { id: 2 }]
  assert.deepEqual(scanViewOf({ songs, truncated: true, warnings: ['上限'] }), {
    songs, truncated: true, warnings: ['上限'],
  })
  assert.deepEqual(scanViewOf({ code: 200, data: { songs, truncated: true, warnings: ['深度'] } }), {
    songs, truncated: true, warnings: ['深度'],
  })
  // 后端在没截断时可能不给这两个字段；warnings 也可能是 null（join 会炸的那种）
  assert.deepEqual(scanViewOf({ songs }), { songs, truncated: false, warnings: [] })
  assert.deepEqual(scanViewOf({ songs, truncated: null, warnings: null }), { songs, truncated: false, warnings: [] })
  assert.deepEqual(scanViewOf({ songs, truncated: 'yes', warnings: '不是数组' }), {
    songs, truncated: true, warnings: [],
  })
  // 空响应 / 半截响应不能变成 undefined.songs
  for (const bad of [null, undefined, {}, { data: null }, { songs: null }]) {
    assert.deepEqual(scanViewOf(bad), { songs: [], truncated: false, warnings: [] })
  }
})

test('scanCachePayload + scanViewOfCache：截断与原因一起进一起出', () => {
  const view = scanViewOf({ songs: [{ id: 7 }], truncated: true, warnings: ['音频数量达到本次上限'] })
  const payload = scanCachePayload(view)
  assert.deepEqual(payload, { songs: [{ id: 7 }], truncated: true, warnings: ['音频数量达到本次上限'] })
  // ⚠️ 这条就是当初的真机事故：只存 songs 的话，切回这个目录时提示会消失（缓存把「不完整」洗成「完整」）
  assert.deepEqual(scanViewOfCache(payload), view)
  // 兼容老缓存形状（以前就是一个 songs 数组）：没有截断信息，只能当未知
  assert.deepEqual(scanViewOfCache([{ id: 7 }]), { songs: [{ id: 7 }], truncated: false, warnings: [] })
  for (const bad of [null, undefined, {}, 'x', 3]) {
    assert.deepEqual(scanViewOfCache(bad), { songs: [], truncated: false, warnings: [] })
  }
  assert.deepEqual(scanCachePayload(null), { songs: [], truncated: false, warnings: [] })
})

test('scanHintOf：只在被截断时出现，且悬停一定有原因', () => {
  assert.deepEqual(scanHintOf({ truncated: false, warnings: [] }), {
    show: false, text: SCAN_TRUNCATED_TEXT, title: SCAN_TRUNCATED_TITLE,
  })
  const one = scanHintOf({ truncated: true, warnings: ['目录层级超过上限，已跳过更深层：/x'] })
  assert.equal(one.show, true)
  assert.equal(one.text, '· 还有没列出来的（结果被截断）')
  assert.equal(one.title, '目录层级超过上限，已跳过更深层：/x')
  const many = scanHintOf({ truncated: true, warnings: ['a', '', null, 'b'] })
  assert.equal(many.title, 'a；b')
  // 被截断但后端没给原因：也要有兜底标题，不能让用户悬停出一片空白
  assert.equal(scanHintOf({ truncated: true }).title, SCAN_TRUNCATED_TITLE)
  assert.equal(scanHintOf({ truncated: true, warnings: [] }).show, true)
  assert.equal(scanHintOf(null).show, false)
})

// ── 磁盘切换（回归：v2.1.57「从 NAS 选脚本只能看到一个硬盘」） ────────────────
// 后端 /api/nas/browse 一直在返回 volumes（= AllowedRoots()），前端曾把它整个丢掉，
// 于是弹窗里只有默认打开的那一个根可看。判断高亮哪块盘时又踩了前缀比较的坑。

test('isUnderVolume: 根目录自己也算命中', () => {
  assert.equal(isUnderVolume('/vol1', '/vol1'), true)
  assert.equal(isUnderVolume('/vol1/', '/vol1'), true)
})

test('isUnderVolume: 子目录命中', () => {
  assert.equal(isUnderVolume('/vol1/Music/专辑', '/vol1'), true)
  assert.equal(isUnderVolume('/media/usb/songs', '/media'), true)
})

test('isUnderVolume: 前缀相似的盘不算命中（/vol1 不该匹配 /vol10）', () => {
  assert.equal(isUnderVolume('/vol10/Music', '/vol1'), false)
  assert.equal(isUnderVolume('/vol11', '/vol1'), false)
  assert.equal(isUnderVolume('/mnt2', '/mnt'), false)
  assert.equal(isUnderVolume('/homework', '/home'), false)
})

test('isUnderVolume: 空值一律 false，不抛异常', () => {
  assert.equal(isUnderVolume('', '/vol1'), false)
  assert.equal(isUnderVolume('/vol1', ''), false)
  assert.equal(isUnderVolume(null, '/vol1'), false)
  assert.equal(isUnderVolume(undefined, undefined), false)
})

test('volumeOf: 从 volumes 里挑出当前那块盘', () => {
  const vols = ['/vol1', '/vol2', '/media', '/mnt', '/home']
  assert.equal(volumeOf('/media', vols), '/media')
  assert.equal(volumeOf('/vol2/Music/专辑', vols), '/vol2')
  assert.equal(volumeOf('/home/liwenyao/projects', vols), '/home')
})

test('volumeOf: 落在所有根之外时返回空串（不高亮任何一块）', () => {
  assert.equal(volumeOf('/etc/passwd', ['/vol1', '/media']), '')
  assert.equal(volumeOf('', ['/vol1']), '')
  assert.equal(volumeOf('/vol1/Music', []), '')
})

test('volumeOf: 嵌套根取最长匹配，只亮最贴近的那块', () => {
  assert.equal(volumeOf('/vol1/Music/a', ['/vol1', '/vol1/Music']), '/vol1/Music')
  assert.equal(volumeOf('/vol1/Docs/a', ['/vol1', '/vol1/Music']), '/vol1')
})
