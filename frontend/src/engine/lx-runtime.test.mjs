/**
 * lx-runtime 的回归测试（node --test 风格，零依赖）。
 *
 * 跑法：
 *   cd frontend && node src/engine/lx-runtime.test.mjs
 *
 * 守的是一条**真实踩过的坑**：
 *   相当一部分音源脚本是**两段式**的 —— 脚本先异步拉远程配置/校验版本，
 *   拿到结果后才 `lx.on('request', ...)` 注册 handler。
 *   实测用户反馈的那个音源在 `loadScript()` 返回后约 250ms 才注册。
 *   如果取链时不等它，就会误报「音源尚未就绪」，
 *   而 `probeSource()` 会把这次误报记成**一次失败并计入熔断** ——
 *   于是一个完全正常的音源被「一键清理失效音源」误杀。
 *
 * 这类 bug 是**静默**的（只在竞态窗口里出现），没有测试守着必然回归。
 */
import assert from 'node:assert/strict'
import test from 'node:test'

import { lxRuntime, __testing } from './lx-runtime.js'

/** 造一个「两段式」音源脚本：延迟 delayMs 后才注册 handler */
function twoStageScript(delayMs, url) {
  return `
    setTimeout(function () {
      lx.on('request', function (payload) {
        return Promise.resolve('${url}')
      })
      lx.send('inited', { sources: { kw: { qualitys: ['128k'] } } })
    }, ${delayMs})
  `
}

test('两段式脚本：loadScript 返回后立刻取链，不得误报「尚未就绪」', async () => {
  const id = '__two_stage'
  const url = 'https://example.com/ok.mp3'

  const loaded = await lxRuntime.loadScript({ id, name: '两段式' }, twoStageScript(150, url))
  assert.equal(loaded, true, '脚本应当加载成功')

  // 关键：这里**不做任何等待**，模拟 App 里「刚 load 完就 probe」的时序
  assert.equal(lxRuntime.requestHandlers.has(id), false, '前提：此刻 handler 尚未注册（竞态窗口）')

  const res = await lxRuntime.probeSource(id, 'kw')
  assert.equal(res.ok, true, `应等到 handler 注册并取链成功，实际：${res.error}`)
  assert.equal(res.url, url)
})

test('两段式脚本：waitHandler 在注册后返回 true', async () => {
  const id = '__wait_ok'
  await lxRuntime.loadScript({ id, name: '等待成功' }, twoStageScript(120, 'https://example.com/a.mp3'))

  assert.equal(await lxRuntime.waitHandler(id, 2000), true)
  assert.equal(lxRuntime.hasHandler(id), true)
})

test('永不注册的脚本：waitHandler 超时返回 false，不卡死', async () => {
  const id = '__never'
  await lxRuntime.loadScript({ id, name: '死脚本' }, '/* 什么都不做 */')

  const t0 = Date.now()
  assert.equal(await lxRuntime.waitHandler(id, 300), false)
  const cost = Date.now() - t0
  assert.ok(cost >= 250 && cost < 2000, `应在超时附近返回，实际 ${cost}ms`)

  // 取链要明确报错，而不是永远挂着
  const res = await lxRuntime.probeSource(id, 'kw')
  assert.equal(res.ok, false)
  assert.match(res.error, /尚未就绪/)
})

test('可达性校验不可用时不得阻断取链（fail-open）', async () => {
  // 取链后会调后端 /api/player/check 校验地址是否真能取到音频。
  // Node 测试环境里没有后端，这个调用必然失败 —— 此时**必须放行**，
  // 不能把「校验不了」当成「地址不可用」，否则后端版本旧一点就整个播不了。
  const id = '__failopen'
  await lxRuntime.loadScript(
    { id, name: 'fail-open' },
    `lx.on('request', function () { return Promise.resolve('https://example.com/ok.mp3') })`,
  )

  const r = await lxRuntime.probeSource(id, 'kw')
  assert.equal(r.ok, true, `校验接口不可用时应放行，实际：${r.error}`)
})

test('探针曲目必须按平台给，不能所有平台共用一首', async () => {
  const id = '__probe_tracks'
  await lxRuntime.loadScript(
    { id, name: '探针' },
    `lx.on('request', function (p) {
       return Promise.resolve('https://example.com/' + p.source + '/' + p.info.musicInfo.songmid)
     })`,
  )

  const mids = []
  for (const plat of ['wy', 'kw', 'kg', 'tx']) {
    const r = await lxRuntime.probeSource(id, plat)
    assert.equal(r.ok, true, `${plat} 探针应成功：${r.error}`)
    mids.push(r.url.split('/').pop())
  }

  // 曲目 ID 是平台私有的：拿酷我的 ID 去问网易/QQ 必然取不到，
  // 会让「导入后自动测试」把好音源误判成坏的。
  assert.equal(
    new Set(mids).size,
    mids.length,
    `各平台应使用不同的探针曲目，实际：${mids.join(', ')}`,
  )
})

test('同步注册的脚本：loadScript 返回时 handler 已就绪', async () => {
  const id = '__sync'
  const url = 'https://example.com/sync.mp3'
  await lxRuntime.loadScript(
    { id, name: '同步' },
    `lx.on('request', function () { return Promise.resolve('${url}') })`,
  )

  assert.equal(lxRuntime.hasHandler(id), true)
  const res = await lxRuntime.probeSource(id, 'kw')
  assert.equal(res.ok, true, `实际：${res.error}`)
  assert.equal(res.url, url)
})

test('waitInited：脚本 send("inited") 之前不算就绪，之后才算', async () => {
  const id = '__inited_late'
  const loaded = await lxRuntime.loadScript({ id, name: '晚初始化' }, `
    lx.on('request', function () { return Promise.resolve('https://example.com/a.mp3') })
    setTimeout(function () {
      lx.send('inited', { sources: { kw: { qualitys: ['128k'] } } })
    }, 400)
  `)
  assert.equal(loaded, true)
  assert.equal(lxRuntime.hasHandler(id), true, '这个脚本是同步注册 handler 的 —— 正是「handler 就绪但没 init 完」那种')
  assert.deepEqual(lxRuntime.getPlatforms(id), [], 'init 之前平台列表必须为空 —— 这正是「还没好」的判据')

  const ok = await lxRuntime.waitInited(id, 3000)
  assert.equal(ok, true, 'inited 之后应当返回 true')
  assert.deepEqual(lxRuntime.getPlatforms(id), ['kw'])
})

test('waitInited：永不 init 的脚本超时返回 false，不卡死', async () => {
  const id = '__never_inited'
  await lxRuntime.loadScript({ id, name: '永不初始化' }, `
    lx.on('request', function () { return Promise.resolve('https://example.com/b.mp3') })
  `)
  const t0 = Date.now()
  assert.equal(await lxRuntime.waitInited(id, 400), false)
  assert.ok(Date.now() - t0 >= 350, '应当真的等到超时，而不是立刻返回')
})

// ─────────────────────────────────────────────────────────────────────────────
// 定时器闸门
//
// 守的坑（真机活体实测，2026-10-06）：
//   有些第三方音源脚本（obfuscator.io 混淆）会在加载时注册 `setInterval(fn, 2000)`，
//   回调是混淆器的**反调试 / 自我完整性校验** —— `while(!![]){}` 死循环 + `debugger`
//   断点 + 自递归重跑字符串数组解码。
//   浏览器侧量到：longtask 每 2000ms 一条、单条 650~1112ms，
//   `long-performance-frame` 归因 `_0x2bcf9e ×11 = 8375ms invoker=TimerHandler:setInterval`
//   —— 主线程 35%~45% 的时间被占死。
//   表现是飞牛宿主里拖窗口「每 3~4 秒卡一下拖不动，然后又恢复，循环」。
//
// 下面的用例守三件事：① 周期任务默认不落地；② 放行开关有效；
// ③ **加载完必须把真实全局原样还回去** —— 这一条最要命，
//    替换忘了还原，整个应用自己的定时器就全废了（而且是静默的）。
// ─────────────────────────────────────────────────────────────────────────────

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

test('脚本起的 setInterval 不落地：回调一次都不许跑，但必须记账', async () => {
  const id = '__timer_gate'
  globalThis.__lxGateTicks = 0
  lxRuntime.droppedTimers.delete(id)

  const script = `
    setInterval(function () { globalThis.__lxGateTicks++ }, 20)
    lx.send('inited', { sources: { kw: { qualitys: ['128k'] } } })
  `
  assert.equal(await lxRuntime.loadScript({ id, name: '反调试音源' }, script), true)
  await sleep(150)

  assert.equal(globalThis.__lxGateTicks, 0, '被闸门挡下的 setInterval 一次都不该触发')
  assert.ok(
    (lxRuntime.droppedTimers.get(id) || 0) > 0,
    '挡下之后必须记账，否则将来查不到是哪个音源在起定时器'
  )
  delete globalThis.__lxGateTicks
})

test('全局写法的 setInterval 也要挡：脚本里 globalThis.setInterval / window.setInterval', async () => {
  const id = '__timer_gate_global'
  globalThis.__lxGlobalTicks = 0

  // ⚠️ 参数遮蔽对 `globalThis.setInterval(...)` 无效 —— 这条专测
  // swapGlobalTimers() 那段「求值期间临时换掉真实全局」有没有生效。
  const script = `
    var h = globalThis.setInterval(function () { globalThis.__lxGlobalTicks++ }, 20)
    setTimeout(function () { clearInterval(h) }, 130)
    lx.send('inited', { sources: { kw: { qualitys: ['128k'] } } })
  `
  assert.equal(await lxRuntime.loadScript({ id, name: '全局写法音源' }, script), true)
  await sleep(180)

  assert.equal(globalThis.__lxGlobalTicks, 0, '写死全局的 setInterval 同样不该落地')
  delete globalThis.__lxGlobalTicks
})

test('加载完必须把真实全局还回去（忘了还原 = 全应用的定时器静默失效）', async () => {
  const beforeInterval = globalThis.setInterval

  await lxRuntime.loadScript(
    { id: '__timer_restore', name: '还原检查' },
    `setInterval(function () {}, 20)
     globalThis.setInterval(function () {}, 20)
     lx.send('inited', { sources: { kw: { qualitys: ['128k'] } } })`
  )

  assert.equal(globalThis.setInterval, beforeInterval, 'globalThis.setInterval 必须还原成应用自己那个')
})

test('requestAnimationFrame 不许落地：音源脚本没有渲染诉求', async () => {
  const id = '__timer_raf'
  globalThis.__lxRafTicks = 0
  // node 里没有 rAF，补一个同步调用的桩 —— 不补的话「放行」分支因为
  // `realRaf` 是 undefined 也会掉进挡下的路，这条用例就测不出区别。
  const prevRaf = globalThis.requestAnimationFrame
  globalThis.requestAnimationFrame = function (fn) {
    fn(0)
    return 1
  }

  try {
    const script = `
      requestAnimationFrame(function () { globalThis.__lxRafTicks++ })
      lx.send('inited', { sources: { kw: { qualitys: ['128k'] } } })
    `
    assert.equal(await lxRuntime.loadScript({ id, name: 'rAF 音源' }, script), true)
    await sleep(80)

    assert.equal(globalThis.__lxRafTicks, 0, '脚本起的 rAF 不该跑')
    assert.ok((lxRuntime.droppedTimers.get(id) || 0) > 0, 'rAF 被挡下同样要记账')
  } finally {
    if (prevRaf === undefined) delete globalThis.requestAnimationFrame
    else globalThis.requestAnimationFrame = prevRaf
    delete globalThis.__lxRafTicks
  }
})

test('setTimeout 照常放行：一刀切会把正常音源（含两段式注册）也掐掉', async () => {
  globalThis.__lxTimeoutRan = false

  await lxRuntime.loadScript(
    { id: '__timer_timeout_ok', name: '一次性定时器' },
    `setTimeout(function () { globalThis.__lxTimeoutRan = true }, 10)
     lx.send('inited', { sources: { kw: { qualitys: ['128k'] } } })`
  )
  await sleep(60)

  assert.equal(globalThis.__lxTimeoutRan, true, 'setTimeout 必须照常工作')
  delete globalThis.__lxTimeoutRan
})

test('放行开关：__LX_ALLOW_SCRIPT_TIMERS__ = true 后脚本的 setInterval 才真的跑', async () => {
  const id = '__timer_allowed'
  globalThis.__lxAllowTicks = 0
  globalThis.__LX_ALLOW_SCRIPT_TIMERS__ = true
  try {
    const script = `
      var h = setInterval(function () { globalThis.__lxAllowTicks++ }, 20)
      setTimeout(function () { clearInterval(h) }, 120)
      lx.send('inited', { sources: { kw: { qualitys: ['128k'] } } })
    `
    assert.equal(await lxRuntime.loadScript({ id, name: '确实要定时器的音源' }, script), true)
    await sleep(160)

    assert.ok(globalThis.__lxAllowTicks > 0, '放行开关打开后，脚本的 setInterval 应当真的跑起来')
  } finally {
    delete globalThis.__LX_ALLOW_SCRIPT_TIMERS__
    delete globalThis.__lxAllowTicks
  }
})

test('脚本劫持 console 必须被抢回：否则所有日志（含它自己的报错）静默消失', async () => {
  // 实测踩到的坑：某条混淆音源在加载时把 console.log / console.warn 整个换掉，
  // 之后所有日志静默消失（`Loaded OK` / `Load error` 一起没了），
  // 排查时看起来像「卡死在第三条音源上」，白白浪费一整轮定位。
  const nativeLog = console.log
  const nativeWarn = console.warn
  const nativeError = console.error

  const script = `
    console.log = function () {}
    console.warn = function () {}
    console.error = function () {}
    lx.send('inited', { sources: { kw: { qualitys: ['128k'] } } })
  `
  // 必须在 finally 里还原之前就**快照**下来 —— 否则 finally 自己把 console 还原了，
  // 断言恒真，这条用例就成了摆设（实测：把 reclaimConsole 变异成空实现它照样通过）。
  let after
  try {
    assert.equal(
      await lxRuntime.loadScript({ id: '__console_hijack', name: '劫持控制台' }, script),
      true
    )
    after = { log: console.log, warn: console.warn, error: console.error }
  } finally {
    console.log = nativeLog
    console.warn = nativeWarn
    console.error = nativeError
  }

  assert.equal(after.log, nativeLog, 'console.log 必须被抢回')
  assert.equal(after.warn, nativeWarn, 'console.warn 必须被抢回')
  assert.equal(after.error, nativeError, 'console.error 必须被抢回')
})

test('reclaimConsole：被换掉才抢回，没被动过就不动它', () => {
  const { reclaimConsole } = __testing
  const nativeLog = console.log
  const nativeInfo = console.info

  try {
    // 只换一个 —— 抢回列表必须精确到「被换掉的那个」，不能顺手全量重写
    console.log = function () {}
    assert.deepEqual(reclaimConsole('__probe'), ['log'], '只应报告被换掉的 log')
    assert.equal(console.log, nativeLog, 'console.log 要还原')
    assert.equal(console.info, nativeInfo, '没被换掉的 console.info 不该被碰')

    // 幂等：抢回之后不该再报告任何东西
    assert.deepEqual(reclaimConsole('__probe'), [], '没被动过时必须返回空列表')
  } finally {
    console.log = nativeLog
    console.info = nativeInfo
  }
})
