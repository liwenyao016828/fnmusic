/**
 * 订阅判定的回归测试。
 *
 * 跑法：cd frontend && node --test src/services/subscribe.test.mjs
 *
 * 守的判据：**「已订阅」判定必须稳** ——
 * 判错「未订阅」会重复建任务（用户看到两个一样的订阅），
 * 判错「已订阅」会让用户以为订阅生效了其实没有。
 * 所以链接、ID、短链三种来源都要能算到同一个 key。
 */
import assert from 'node:assert/strict'
import test from 'node:test'

import {
  playlistKey, monitorKeys, taskKey, pickSubscription, setSubscriptionEnabled,
  buildSubscriptions, setCapability, subscribePlaylist, anySubscriptionRanOnce,
  pushGuideSteps, pushGuideDoneCount, SUBSCRIBE_INTERVAL_MINUTES,
} from './subscribe.js'
import { MonitorAPI, PushAPI, FnosAPI } from '../api/client.js'

test('playlistKey：网易云的各种链接形态都能认出平台与歌单 ID', () => {
  const want = 'wy:3778678'
  assert.equal(playlistKey({ link: 'https://music.163.com/playlist?id=3778678' }), want)
  assert.equal(playlistKey({ link: 'https://music.163.com/#/playlist?id=3778678' }), want)
  assert.equal(playlistKey({ link: 'https://music.163.com/m/playlist?id=3778678&userid=1' }), want)
  assert.equal(playlistKey({ link: 'https://music.163.com/playlist/3778678' }), want)
})

test('playlistKey：QQ 音乐的链接形态', () => {
  const want = 'tx:7011264340'
  assert.equal(playlistKey({ link: 'https://y.qq.com/n/ryqq/playlist/7011264340' }), want)
  assert.equal(playlistKey({ link: 'https://i.y.qq.com/n2/m/share/details/taoge.html?id=7011264340' }), want)
})

test('playlistKey：只有 ID + 平台代号时也能算出同一个 key', () => {
  assert.equal(playlistKey({ id: '3778678', source: 'wy' }), 'wy:3778678')
  assert.equal(playlistKey({ id: '7011264340', source: 'tx' }), 'tx:7011264340')
  // 精选歌单（只有 id、没有链接）与链接形态必须匹配上，否则会重复订阅
  assert.equal(
    playlistKey({ id: '3778678', source: 'wy' }),
    playlistKey({ link: 'https://music.163.com/playlist?id=3778678' }),
  )
})

test('playlistKey：认不出 ID 的分享短链退化为链接本身（仍能自比自）', () => {
  const short = 'https://163cn.tv/AbCdEf'
  const key = playlistKey({ link: short })
  assert.equal(key, '163cn.tv/AbCdEf')
  // 同一条短链两次订阅必须匹配上
  assert.equal(key, playlistKey({ link: short }))
})

test('playlistKey：无有效输入时返回空串（不参与匹配）', () => {
  assert.equal(playlistKey({}), '')
  assert.equal(playlistKey(), '')
  assert.equal(playlistKey({ link: '   ' }), '')
})

test('monitorKeys：从监控的 target.playlists 提取 key，兼容只给 ID 的配置', () => {
  const keys = monitorKeys({
    sources: ['wy'],
    target: {
      playlists: [
        { link: 'https://music.163.com/playlist?id=3778678' },
        { id: '7011264340', source: 'tx' },
        { name: '没有链接也没有 ID 的坏配置' },
      ],
    },
  })
  assert.deepEqual(keys, ['wy:3778678', 'tx:7011264340'])
})

test('taskKey：只有 source_kind=link 的推送任务才参与订阅匹配', () => {
  assert.equal(taskKey({ source_kind: 'link', source_id: 'https://music.163.com/playlist?id=3778678' }), 'wy:3778678')
  // 按账号歌单 ID 同步的任务（daily/playlist）不是「订阅某个链接」，不该被匹配
  assert.equal(taskKey({ source_kind: 'playlist', source_id: '3778678' }), '')
  assert.equal(taskKey({ source_kind: 'daily', source_id: '' }), '')
  assert.equal(taskKey(null), '')
})

// 真机踩到的 bug（2026-09-21 开发环境）：同一歌单有三条历史记录，
// 只取第一条时「有一条还在跑」被误判成「已暂停」，用户以为没在追新。
test('pickSubscription：多条记录里只要有一条在跑就算已订阅', () => {
  const monitors = [{
    id: 'm1', enabled: false,
    target: { playlists: [{ link: 'https://music.163.com/#/playlist?id=3778678' }] },
  }]
  const tasks = [
    { id: 't1', source_kind: 'link', source_id: 'https://music.163.com/#/playlist?id=3778678', enabled: false },
    { id: 't2', source_kind: 'link', source_id: 'https://music.163.com/playlist?id=3778678', enabled: true },
  ]

  const sub = pickSubscription('wy:3778678', monitors, tasks)

  assert.equal(sub.active, true)
  assert.equal(sub.monitors.length, 1)
  assert.equal(sub.tasks.length, 2, '两条推送任务都要收进来，否则「停止更新」会漏掉没停的那条')
})

test('pickSubscription：全部停掉才算「已暂停」', () => {
  const monitors = [{ id: 'm1', enabled: false, target: { playlists: [{ id: '3778678', source: 'wy' }] } }]
  const tasks = [{ id: 't1', source_kind: 'link', source_id: 'https://music.163.com/playlist?id=3778678', enabled: false }]

  const sub = pickSubscription('wy:3778678', monitors, tasks)

  assert.equal(sub.active, false)
  assert.equal(sub.monitors.length + sub.tasks.length, 2)
})

test('pickSubscription：没匹配到任何记录时返回 null（界面显示「订阅更新」）', () => {
  assert.equal(pickSubscription('wy:3778678', [], []), null)
  assert.equal(pickSubscription('wy:3778678', [{ id: 'm9', enabled: true, target: { playlists: [{ id: '999' }] } }], []), null)
  assert.equal(pickSubscription('', [{ id: 'm9', enabled: true, target: { playlists: [{ id: '999' }] } }], []), null)
})

// ── 停止 / 恢复：请求体必须发对 ──────────────────────────────────────────────
//
// 守的判据：**「恢复更新」必须能把订阅救回来**。
// 监控接口在 v2.1.12 之前有个 `!=` 写反的 bug，点「停止更新」会把 auto_download 关掉，
// 而界面上没有单独改这个开关的入口（只有「新建监控」的表单里有）—— 老订阅只能靠
// 「恢复更新」重新断言一次配置来自愈。所以这里钉住两条请求体：
//   停止 → 只带 enabled（别去动用户自己的配置）
//   恢复 → enabled + auto_download + embed（把订阅该有的样子重新说一遍）

/** 临时替换两个 API 对象上的方法，记录调用参数 */
function stubApis() {
  const calls = []
  const origUpdate = MonitorAPI.update
  const origSave = PushAPI.saveTask
  MonitorAPI.update = (id, patch) => {
    calls.push({ kind: 'monitor', id, patch })
    return Promise.resolve({ code: 200 })
  }
  PushAPI.saveTask = (payload) => {
    calls.push({ kind: 'push', payload })
    return Promise.resolve({ code: 200 })
  }
  return {
    calls,
    restore() {
      MonitorAPI.update = origUpdate
      PushAPI.saveTask = origSave
    },
  }
}

test('setSubscriptionEnabled(false)：停止更新只改 enabled，不碰 auto_download / embed', async () => {
  const { calls, restore } = stubApis()
  try {
    await setSubscriptionEnabled({ monitors: [{ id: 'm1' }], tasks: [] }, false)
    assert.equal(calls.length, 1)
    assert.deepEqual(calls[0].patch, { enabled: false })
  } finally {
    restore()
  }
})

test('setSubscriptionEnabled(true)：恢复更新重新断言 auto_download / embed（老订阅自愈）', async () => {
  const { calls, restore } = stubApis()
  try {
    await setSubscriptionEnabled({ monitors: [{ id: 'm1' }], tasks: [] }, true)
    assert.equal(calls.length, 1)
    assert.deepEqual(calls[0].patch, { enabled: true, auto_download: true, embed: true })
  } finally {
    restore()
  }
})

test('setSubscriptionEnabled：推送任务回传完整字段（保存接口是整体替换，少传就清空）', async () => {
  const { calls, restore } = stubApis()
  try {
    await setSubscriptionEnabled({
      monitors: [],
      tasks: [{
        id: 't1', name: '某歌单（订阅）', provider: 'netease', source_kind: 'link',
        source_id: 'https://music.163.com/playlist?id=3778678', target_title: '某歌单',
        sync_cover: true, interval_minutes: 360, enabled: true,
      }],
    }, false)
    assert.equal(calls.length, 1)
    const p = calls[0].payload
    assert.equal(p.enabled, false)
    assert.equal(p.name, '某歌单（订阅）')
    assert.equal(p.provider, 'netease')
    assert.equal(p.source_kind, 'link')
    assert.equal(p.source_id, 'https://music.163.com/playlist?id=3778678')
    assert.equal(p.target_title, '某歌单')
    assert.equal(p.sync_cover, true)
    assert.equal(p.interval_minutes, 360)
  } finally {
    restore()
  }
})

test('setSubscriptionEnabled：没有 id 的记录直接跳过（不发出空请求）', async () => {
  const { calls, restore } = stubApis()
  try {
    await setSubscriptionEnabled({ monitors: [{}, { id: '' }], tasks: [{}] }, true)
    assert.equal(calls.length, 0)
  } finally {
    restore()
  }
})

// ── 合并成一个订阅列表：两条流水线归并成「一行 + 两个能力位」 ─────────────────
//
// 守的判据：**界面上一个来源只能占一行**。
// 分成两行的话，同一个歌单的「下载」和「同步飞牛」会各管一份间隔与启停，
// 用户看到的是两份设置却只有一个来源 —— 这正是「推送歌单 / 推送曲库」重复感的来源。

/** 造一条监控记录 */
function mon(over = {}) {
  return {
    id: 'm1', name: '某歌单（订阅）', enabled: true, sources: ['wy'],
    interval_minutes: 360, baseline_done: true, pendingCount: 0,
    target: { playlists: [{ link: 'https://music.163.com/playlist?id=3778678' }] },
    ...over,
  }
}

/** 造一条推送任务记录 */
function task(over = {}) {
  return {
    id: 't1', name: '某歌单（订阅）', provider: 'netease', source_kind: 'link',
    source_id: 'https://music.163.com/playlist?id=3778678', target_title: '某歌单',
    enabled: true, interval_minutes: 360, last_result: {},
    ...over,
  }
}

test('buildSubscriptions：同一来源的监控与推送任务归并成一行', () => {
  const rows = buildSubscriptions(
    [mon({ pendingCount: 3 })],
    [task({ last_result: { missing: [1, 2] } })],
  )

  assert.equal(rows.length, 1, '同一个歌单只能占一行')
  const r = rows[0]
  assert.equal(r.key, 'wy:3778678')
  assert.equal(r.name, '某歌单', '名称要去掉「（订阅）」后缀')
  assert.equal(r.provider, 'netease')
  assert.equal(r.download, true)
  assert.equal(r.fnos, true)
  assert.equal(r.hasDownload, true)
  assert.equal(r.hasFnos, true)
  assert.equal(r.pendingCount, 3)
  assert.equal(r.missingCount, 2)
  assert.equal(r.targetTitle, '某歌单')
  assert.equal(r.sourceLabel, '网易云 · 歌单链接')
  assert.equal(r.monitors.length, 1)
  assert.equal(r.tasks.length, 1)
})

test('buildSubscriptions：只有监控时 fnos 位是空的（但能再建）', () => {
  const r = buildSubscriptions([mon()], [])[0]

  assert.equal(r.download, true)
  assert.equal(r.hasDownload, true)
  assert.equal(r.fnos, false)
  assert.equal(r.hasFnos, false, '没有推送记录 → 界面该知道要「新建」而不是「切换」')
  assert.equal(r.canCreate, true, '认得出平台 + 拿得到链接，就能再建同步任务')
  assert.equal(r.blockReason, '')
})

test('buildSubscriptions：只有推送任务时 download 位是空的，来源从 provider 倒推', () => {
  const r = buildSubscriptions([], [task()])[0]

  assert.equal(r.download, false)
  assert.equal(r.hasDownload, false)
  assert.equal(r.fnos, true)
  assert.equal(r.hasFnos, true)
  // 推送任务只存 provider + 链接，没有 source 代号 —— 得能倒推出「网易云」
  assert.equal(r.source, 'wy')
  assert.equal(r.link, 'https://music.163.com/playlist?id=3778678')
})

// ⚠️ 认不出 key 的记录**也必须出现在列表里**。
// 榜单「当前列表」模式的监控没有 target.playlists，推送任务里也有 daily/playlist 类型 ——
// 过滤掉的话用户会以为「我建的任务不见了」，比显示一行难看更糟。
test('buildSubscriptions：认不出歌单 key 的记录退化成 id 行，不能消失', () => {
  const rows = buildSubscriptions(
    [mon({ id: 'm9', name: '热歌榜（订阅）', target: {} })],
    [task({ id: 't9', name: '日推同步', source_kind: 'daily', source_id: '' })],
  )

  const keys = rows.map((r) => r.key).sort()
  assert.deepEqual(keys, ['monitor:m9', 'task:t9'])

  const m9 = rows.find((r) => r.key === 'monitor:m9')
  assert.equal(m9.name, '热歌榜')
  assert.equal(m9.download, true)
  assert.equal(m9.canCreate, false)
  assert.match(m9.blockReason, /链接或 ID|平台/, '建不了另一条时要说明原因，不能静默置灰')

  const t9 = rows.find((r) => r.key === 'task:t9')
  assert.equal(t9.fnos, true)
  assert.equal(t9.canCreate, false)
})

test('buildSubscriptions：一个监控绑多个歌单时只认第一个 key（不重复出行）', () => {
  const rows = buildSubscriptions(
    [mon({
      target: {
        playlists: [
          { link: 'https://music.163.com/playlist?id=3778678' },
          { link: 'https://music.163.com/playlist?id=9999999' },
        ],
      },
    })],
    [],
  )

  assert.equal(rows.length, 1, '同一个监控出现在两行 → 点任一行的开关都像同时动了两行')
  assert.equal(rows[0].key, 'wy:3778678')
})

test('buildSubscriptions：两个能力位都是「任一记录启用」同口径', () => {
  const rows = buildSubscriptions(
    [mon({ id: 'm1', enabled: false }), mon({ id: 'm2', enabled: true })],
    [task({ id: 't1', enabled: false }), task({ id: 't2', enabled: false })],
  )

  assert.equal(rows.length, 1)
  assert.equal(rows[0].download, true, '有任一监控在跑就算开着')
  assert.equal(rows[0].fnos, false, '推送侧全停才算关着')
  assert.equal(rows[0].monitors.length, 2)
  assert.equal(rows[0].tasks.length, 2)
})

test('buildSubscriptions：来源平台认不出时 canCreate=false 并说明原因', () => {
  const r = buildSubscriptions([mon({ sources: [], target: { playlists: [{ id: '3778678' }] } })], [])[0]

  assert.equal(r.canCreate, false, '没有平台代号 → 推送任务建不出来')
  assert.match(r.blockReason, /平台/)
})

test('buildSubscriptions：按名称排序（纯函数定序，界面不因「谁先建」跳来跳去）', () => {
  const rows = buildSubscriptions(
    [
      mon({ id: 'a', name: '乙歌单（订阅）', target: { playlists: [{ link: 'https://music.163.com/playlist?id=1111111' }] } }),
      mon({ id: 'b', name: '甲歌单（订阅）', target: { playlists: [{ link: 'https://music.163.com/playlist?id=2222222' }] } }),
    ],
    [],
  )

  assert.deepEqual(rows.map((r) => r.name), ['甲歌单', '乙歌单'])
})

test('buildSubscriptions：空输入返回空列表（不抛错）', () => {
  assert.deepEqual(buildSubscriptions(), [])
  assert.deepEqual(buildSubscriptions(null, null), [])
  assert.deepEqual(buildSubscriptions([null], [undefined]), [])
})

// ── setCapability：关是两侧一起关，开只开一侧 ────────────────────────────────
//
// 判据（2026-09-22 用户反馈）：「在推送的时候点了停止怎么还在推到下载」——
// 只停一侧时，另一侧照旧在跑，而界面上完全看不出来。
// 所以「停止」= 这个订阅别跑了（两侧一起停）；「开启」才按点的那一侧来。

test('setCapability：关「下载」= 两侧一起停', async () => {
  const { calls, restore } = stubApis()
  try {
    const row = buildSubscriptions([mon()], [task()])[0]
    await setCapability(row, 'download', false)

    const kinds = calls.map((c) => c.kind).sort()
    assert.deepEqual(kinds, ['monitor', 'push'], '停止必须连另一侧一起停，否则用户以为停了其实还在跑')
    for (const c of calls) {
      if (c.kind === 'monitor') assert.deepEqual(c.patch, { enabled: false })
      else assert.equal(c.payload.enabled, false)
    }
  } finally {
    restore()
  }
})

test('setCapability：关「飞牛」同样两侧一起停', async () => {
  const { calls, restore } = stubApis()
  try {
    const row = buildSubscriptions([mon()], [task()])[0]
    await setCapability(row, 'fnos', false)

    const kinds = calls.map((c) => c.kind).sort()
    assert.deepEqual(kinds, ['monitor', 'push'])
  } finally {
    restore()
  }
})

test('setCapability：开「下载」只开监控，不碰推送', async () => {
  const { calls, restore } = stubApis()
  try {
    const row = buildSubscriptions([mon()], [task()])[0]
    await setCapability(row, 'download', true)

    assert.equal(calls.length, 1)
    assert.equal(calls[0].kind, 'monitor')
    // 开启时会一并带上 auto_download / embed（监控的「启用」语义就是「开始自动下载」）
    assert.equal(calls[0].patch.enabled, true)
    assert.equal(calls[0].patch.auto_download, true)
  } finally {
    restore()
  }
})

test('setCapability：开「同步飞牛」只发推送请求，不碰监控', async () => {
  const { calls, restore } = stubApis()
  try {
    const row = buildSubscriptions([mon()], [task()])[0]
    await setCapability(row, 'fnos', true)

    assert.equal(calls.length, 1)
    assert.equal(calls[0].kind, 'push')
    assert.equal(calls[0].payload.enabled, true)
    assert.equal(calls[0].payload.id, 't1')
    assert.equal(calls[0].payload.provider, 'netease', '整体替换接口必须回传完整字段')
  } finally {
    restore()
  }
})

// ── 按勾选建任务 ─────────────────────────────────────────────────────────────
//
// 守的判据：**勾了什么就只建什么**。
// 以前一律两条都建 —— 用户只想自动下载，却多出一条用不到的飞牛同步任务。
// 反向的坑更严重：只勾了「同步飞牛」但飞牛没连上时，**不能返回成功**
// （界面会显示「已订阅」，用户以为好了，其实什么都没建）。

/** 替换订阅链路上的全部 API 方法 */
function stubSubscribeApis({ fnosAvailable = true, monitorId = 'm1', taskId = 't1' } = {}) {
  const calls = []
  const orig = {
    update: MonitorAPI.update,
    create: MonitorAPI.create,
    discover: MonitorAPI.discover,
    saveTask: PushAPI.saveTask,
    status: FnosAPI.status,
  }
  MonitorAPI.update = (id, patch) => {
    calls.push({ kind: 'monitor.update', id, patch })
    return Promise.resolve({ code: 200 })
  }
  MonitorAPI.create = (payload) => {
    calls.push({ kind: 'monitor.create', payload })
    return Promise.resolve({ code: 200, data: { id: monitorId } })
  }
  MonitorAPI.discover = (id, payload) => {
    calls.push({ kind: 'monitor.discover', id, payload })
    return Promise.resolve({ code: 200 })
  }
  PushAPI.saveTask = (payload) => {
    calls.push({ kind: 'push.saveTask', payload })
    return Promise.resolve({ code: 200, data: { id: taskId } })
  }
  FnosAPI.status = () => Promise.resolve({ code: 200, data: { music_available: fnosAvailable } })
  return {
    calls,
    restore() {
      MonitorAPI.update = orig.update
      MonitorAPI.create = orig.create
      MonitorAPI.discover = orig.discover
      PushAPI.saveTask = orig.saveTask
      FnosAPI.status = orig.status
    },
  }
}

const SUB_ARGS = {
  link: 'https://music.163.com/playlist?id=3778678',
  source: 'wy',
  name: '某歌单',
  songs: [{ name: 'a' }, { name: 'b' }],
}

test('subscribePlaylist：只勾「下载到本地曲库」时，不建推送任务', async () => {
  const { calls, restore } = stubSubscribeApis()
  try {
    const res = await subscribePlaylist({ ...SUB_ARGS, wantDownload: true, wantFnos: false })

    assert.equal(res.ok, true)
    assert.ok(res.monitorId)
    assert.equal(res.taskId, '')
    assert.match(res.message, /只下载/)

    const kinds = calls.map((c) => c.kind)
    assert.ok(kinds.includes('monitor.create'))
    assert.ok(!kinds.includes('push.saveTask'), '没勾飞牛就不该建推送任务')
    // 基线曲目要一并上报，免得后端下一轮再抓一次
    assert.deepEqual(calls.find((c) => c.kind === 'monitor.discover').payload.songs.length, 2)
  } finally {
    restore()
  }
})

test('subscribePlaylist：只勾「同步到飞牛歌单」时，不建监控', async () => {
  const { calls, restore } = stubSubscribeApis()
  try {
    const res = await subscribePlaylist({ ...SUB_ARGS, wantDownload: false, wantFnos: true })

    assert.equal(res.ok, true)
    assert.equal(res.monitorId, '')
    assert.ok(res.taskId)

    const kinds = calls.map((c) => c.kind)
    assert.ok(kinds.includes('push.saveTask'))
    assert.ok(!kinds.includes('monitor.create'), '没勾下载就不该建监控')
    assert.ok(!kinds.includes('monitor.discover'))
  } finally {
    restore()
  }
})

test('subscribePlaylist：两个都不勾 = 什么都没订阅，直接失败且不发请求', async () => {
  const { calls, restore } = stubSubscribeApis()
  try {
    const res = await subscribePlaylist({ ...SUB_ARGS, wantDownload: false, wantFnos: false })

    assert.equal(res.ok, false)
    assert.match(res.error, /至少选一项/)
    assert.equal(calls.length, 0)
  } finally {
    restore()
  }
})

// ⚠️ 这条是「假成功」的守门员：只勾飞牛 + 飞牛不可用 → 必须如实报失败。
// 返回 ok 的话界面会切到「已订阅 + 停止更新」，用户以为在同步，其实一条任务都没有。
test('subscribePlaylist：只勾飞牛但飞牛不可用时如实报失败，不返回假成功', async () => {
  const { calls, restore } = stubSubscribeApis({ fnosAvailable: false })
  try {
    const res = await subscribePlaylist({ ...SUB_ARGS, wantDownload: false, wantFnos: true })

    assert.equal(res.ok, false)
    assert.match(res.error, /飞牛音乐未连接/)
    assert.equal(calls.length, 0, '建不成就不该留下半截任务')
  } finally {
    restore()
  }
})

test('subscribePlaylist：勾了两项但飞牛不可用 → 保住下载主线，并说明没同步', async () => {
  const { calls, restore } = stubSubscribeApis({ fnosAvailable: false })
  try {
    const res = await subscribePlaylist({ ...SUB_ARGS, wantDownload: true, wantFnos: true })

    assert.equal(res.ok, true)
    assert.equal(res.fnosReady, false)
    assert.match(res.message, /暂不同步/)
    assert.ok(calls.some((c) => c.kind === 'monitor.create'))
    assert.ok(!calls.some((c) => c.kind === 'push.saveTask'))
  } finally {
    restore()
  }
})

test('subscribePlaylist：两项都勾且飞牛可用 → 两条任务都建', async () => {
  const { calls, restore } = stubSubscribeApis()
  try {
    const res = await subscribePlaylist({ ...SUB_ARGS, wantDownload: true, wantFnos: true })

    assert.equal(res.ok, true)
    assert.ok(res.monitorId)
    assert.ok(res.taskId)

    const push = calls.find((c) => c.kind === 'push.saveTask').payload
    assert.equal(push.provider, 'netease')
    assert.equal(push.source_kind, 'link')
    assert.equal(push.source_id, 'https://music.163.com/playlist?id=3778678')
    assert.equal(push.enabled, true)
  } finally {
    restore()
  }
})

test('subscribePlaylist：只有 ID 没链接时，推送任务用合成链接', async () => {
  const { calls, restore } = stubSubscribeApis()
  try {
    const res = await subscribePlaylist({
      id: '3778678', source: 'wy', name: '精选歌单', wantDownload: true, wantFnos: true,
    })

    assert.equal(res.ok, true)
    const push = calls.find((c) => c.kind === 'push.saveTask').payload
    assert.equal(push.source_id, 'https://music.163.com/playlist?id=3778678')
  } finally {
    restore()
  }
})

// 推送页的使用引导要不要收起，靠这个判据。
// 判错成「跑过」= 新用户一进来就看不到说明；判错成「没跑过」= 老用户被一直打扰。
test('anySubscriptionRanOnce：只有跑过一轮才算，刚建完订阅不算', () => {
  // 新用户：建了订阅但还没跑过 → 引导必须留着
  assert.equal(anySubscriptionRanOnce([
    { baselineDone: false, lastRunAt: '', lastStatus: '' },
  ]), false)
  // 监控首轮基线建好 = 发现通道确认能工作
  assert.equal(anySubscriptionRanOnce([{ baselineDone: true }]), true)
  // 推送任务跑过（有执行时间或有状态）
  assert.equal(anySubscriptionRanOnce([{ lastRunAt: '2026-09-23T10:00:00+08:00' }]), true)
  assert.equal(anySubscriptionRanOnce([{ lastStatus: 'ok' }]), true)
  // 多条订阅里只要有一条跑通就够
  assert.equal(anySubscriptionRanOnce([
    { baselineDone: false, lastRunAt: '', lastStatus: '' }, { lastStatus: 'ok' },
  ]), true)
  // 空/脏数据不能炸（接口返回 null 时界面要照常渲染）
  assert.equal(anySubscriptionRanOnce([]), false)
  assert.equal(anySubscriptionRanOnce(null), false)
  assert.equal(anySubscriptionRanOnce([null, undefined]), false)
})

// 推送页顶部的三步引导（形态照 fnmusic-flow 的 onboarding）。
// 三步的 done 决定卡片打不打勾、标题是「三步即可开始」还是「一切就绪」，
// 所以判错方向的代价很实在：判成完成 = 把卡住他的人的入口藏起来。
test('pushGuideSteps：三步各自什么时候算完成', () => {
  const flags = (rows, state) =>
    Object.fromEntries(pushGuideSteps(rows, state).map((s) => [s.id, s.done]))

  // 新用户：三步都没做
  assert.deepEqual(flags([], {}), { source: false, subscribe: false, run: false })
  // 「接入音源」只数服务型音源，跟订阅无关
  assert.equal(flags([], { apiSourceCount: 2 }).source, true)
  assert.equal(flags([], { apiSourceCount: 0 }).source, false)
  assert.equal(flags([], { apiSourceCount: null }).source, false)
  // 建了订阅但两个能力位都没勾 → 第二步不算完成（这种人正是不知道下一步的）
  assert.deepEqual(flags([{ download: false, fnos: false }], {}), {
    source: false, subscribe: false, run: false,
  })
  // 勾任一能力位即完成
  assert.equal(flags([{ download: true, fnos: false }], {}).subscribe, true)
  assert.equal(flags([{ download: false, fnos: true }], {}).subscribe, true)
  // 第三步沿用 anySubscriptionRanOnce
  assert.equal(flags([{ download: true, fnos: true, lastStatus: 'ok' }], {}).run, true)
  assert.equal(flags([{ download: true, fnos: true, baselineDone: false, lastRunAt: '' }], {}).run, false)
  // 三步全做满 → 标题换成「一切就绪」、按钮变成「收起」
  assert.ok(pushGuideDoneCount(
    pushGuideSteps([{ download: true, fnos: true, baselineDone: true }], { apiSourceCount: 1 }),
  ) === 3)
})

test('pushGuideSteps：完成态文案要带真实数字，不许是同一句话', () => {
  const out = pushGuideSteps(
    [
      { download: true, fnos: true, baselineDone: true },
      { download: true, fnos: false, lastRunAt: '2026-09-23T10:00:00+08:00' },
      { download: false, fnos: false },
    ],
    { apiSourceCount: 3 },
  )
  const byId = Object.fromEntries(out.map((s) => [s.id, s]))
  assert.ok(byId.source.doneHint.includes('3'), '音源数要写进文案')
  assert.ok(byId.subscribe.doneHint.includes('3 条'), '订阅条数要写进文案')
  assert.ok(byId.subscribe.doneHint.includes('2 条在下载'), '分能力位的条数要写进文案')
  assert.ok(byId.subscribe.doneHint.includes('1 条在同步飞牛'))
  // 未完成时走 hint，且第三位的文案跟着扫描间隔配置走（不许硬编码）
  const fresh = Object.fromEntries(pushGuideSteps([], {}).map((s) => [s.id, s]))
  assert.equal(fresh.subscribe.done, false)
  assert.ok(fresh.run.hint.includes(String(SUBSCRIBE_INTERVAL_MINUTES)))
  assert.ok(pushGuideSteps([], { scanIntervalMinutes: 120 })[2].hint.includes('120 分钟'))
})

test('pushGuideSteps：每步都必须给一个能跳过去的按钮（没入口的引导等于没写）', () => {
  // create = 打开本页的新建订阅弹窗，不需要目的地名字；view/tab 必须指名去哪儿
  const named = { view: 1, tab: 1 }
  for (const s of pushGuideSteps([], {})) {
    assert.ok(s.action && typeof s.action.label === 'string' && s.action.label.length >= 2, `${s.id} 缺按钮文案`)
    const kind = s.action?.to?.kind
    assert.ok(named[kind] || kind === 'create', `${s.id} 的跳转目标种类不认识：${kind}`)
    if (named[kind]) assert.ok(s.action.to.name, `${s.id} 缺跳转目的地`)
  }
  // 勾过了按钮也不许消失（flow 是换成「管理音源」这种二级入口）
  const done = pushGuideSteps([{ download: true, fnos: true, baselineDone: true }], { apiSourceCount: 1 })
  for (const s of done) assert.ok(s.action.label, `${s.id} 完成后按钮文案丢了`)
})

test('pushGuideSteps / pushGuideDoneCount：脏数据不能炸界面', () => {
  const out = pushGuideSteps([null, undefined, {}], null)
  assert.equal(out.length, 3)
  assert.deepEqual(out.map((s) => s.done), [false, false, false])
  assert.equal(pushGuideDoneCount(null), 0)
  assert.equal(pushGuideDoneCount([{ done: true }, null, { done: false }]), 1)
})
