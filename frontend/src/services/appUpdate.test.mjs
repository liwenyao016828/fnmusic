import test from 'node:test'
import assert from 'node:assert/strict'

import {
  createAppUpdate,
  formatBytes,
  normalizeCheck,
  normalizeUpdateState,
} from './appUpdate.js'

function reply(payload, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => payload,
  }
}

function ok(data) {
  return reply({ code: 200, data })
}

const wait = (ms = 0) => new Promise((resolve) => setTimeout(resolve, ms))

const CHECK_PAYLOAD = {
  current: '2.17.15',
  latest: '2.18.0',
  has_update: true,
  notes: '修复了下载队列\n新增后台下载',
  published_at: '2026-10-23T12:00:00Z',
  asset_name: 'fnmusic.fpk',
  asset_url: 'https://example.com/fnmusic.fpk',
  size: 5 * 1024 * 1024,
  html_url: 'https://example.com/release',
  checked_at: 1761220800,
  error: '',
}

test('normalizeCheck 归一化版本/大小/更新说明，并把 v 前缀去掉', () => {
  const out = normalizeCheck(CHECK_PAYLOAD)
  assert.equal(out.current, '2.17.15')
  assert.equal(out.latest, '2.18.0')
  assert.equal(out.hasUpdate, true)
  assert.equal(out.notes, '修复了下载队列\n新增后台下载')
  assert.equal(out.size, 5 * 1024 * 1024)
  assert.equal(out.checkedAt, 1761220800)
})

test('normalizeCheck 老接口没有 has_update 时用版本号兜底', () => {
  assert.equal(normalizeCheck({ current: '1.0.0', latest: '1.0.1' }).hasUpdate, true)
  assert.equal(normalizeCheck({ current: '1.0.0', latest: '1.0.0' }).hasUpdate, false)
})

test('normalizeUpdateState 防御越界 progress 与未知状态', () => {
  assert.deepEqual(normalizeUpdateState({ status: 'DOWNLOADING', progress: 250, path: '/tmp/a.fpk' }), {
    status: 'downloading',
    progress: 100,
    path: '/tmp/a.fpk',
    size: 0,
    error: '',
  })
  assert.equal(normalizeUpdateState({ status: 'wat' }).status, 'idle')
  assert.equal(normalizeUpdateState({ status: 'error', error: '没有安装权限' }).error, '没有安装权限')
})

test('formatBytes 只给下载中需要的粗粒度大小', () => {
  assert.equal(formatBytes(0), '')
  assert.equal(formatBytes(1024), '1 KB')
  assert.equal(formatBytes(5 * 1024 * 1024), '5.0 MB')
})

test('check 成功后填充卡片状态并记录检查时间', async () => {
  const calls = []
  const c = createAppUpdate({
    now: () => 42,
    fetchImpl: async (url) => {
      calls.push(url)
      return ok(CHECK_PAYLOAD)
    },
  })
  const out = await c.check()
  assert.equal(out.latest, '2.18.0')
  assert.equal(c.current.value, '2.17.15')
  assert.equal(c.hasUpdate.value, true)
  assert.equal(c.checkedAt.value, 42)
  assert.equal(c.phase.value, 'available')
  assert.equal(c.checking.value, false)
  assert.deepEqual(calls, ['/api/app/update/check'])
})

test('更新卡片可读取已下载状态，检查结果同步并可重置', async () => {
  let payload = { ...CHECK_PAYLOAD, downloaded: true, downloaded_path: '/updates/app.fpk' }
  const c = createAppUpdate({ fetchImpl: async () => ok(payload) })
  assert.equal(c.downloaded.value, false)
  assert.equal(c.downloadedPath.value, '')
  await c.check()
  assert.equal(c.downloaded.value, true)
  assert.equal(c.downloadedPath.value, '/updates/app.fpk')
  payload = CHECK_PAYLOAD
  await c.check()
  assert.equal(c.downloaded.value, false)
  assert.equal(c.downloadedPath.value, '')
  payload = { ...CHECK_PAYLOAD, downloaded: true, downloaded_path: '/updates/app.fpk' }
  await c.check()
  c.resetForTests()
  assert.equal(c.downloaded.value, false)
  assert.equal(c.downloadedPath.value, '')
})

test('静默 check 遇到 404 不抛错，只在卡片留一行可操作提示', async () => {
  const c = createAppUpdate({
    fetchImpl: async () => reply({ code: 404, message: 'not found' }, 404),
  })
  const out = await c.check({ silent: true })
  assert.equal(out, null)
  assert.equal(c.checking.value, false)
  assert.equal(c.phase.value, 'idle')
  assert.match(c.checkError.value, /当前后端版本还不支持应用内更新/)
})

test('submit 失败 message 原样显示（检查接口 500）', async () => {
  const c = createAppUpdate({
    fetchImpl: async () => reply({ code: 500, message: 'GitHub API 被限流，请稍后再试' }, 500),
  })
  await c.check()
  assert.equal(c.checkError.value, 'GitHub API 被限流，请稍后再试')
  assert.equal(c.phase.value, 'idle')
})

test('下载 → ready 自动安装，安装中进度保持 100', async () => {
  const calls = []
  const c = createAppUpdate({
    pollMs: 5,
    fetchImpl: async (url, opts = {}) => {
      calls.push(`${opts.method || 'GET'} ${url}`)
      if (url === '/api/app/update/check') return ok(CHECK_PAYLOAD)
      if (url === '/api/app/update/download') return ok({ status: 'downloading', progress: 10, size: 1024 })
      if (url === '/api/app/update/state') return ok({ status: 'ready', progress: 100, path: '/tmp/a.fpk' })
      if (url === '/api/app/update/install') return ok({ started: true, log: 'spawned' })
      throw new Error(`unexpected ${url}`)
    },
  })
  await c.check()
  await c.startUpdate()
  assert.equal(c.phase.value, 'downloading')
  assert.equal(c.progress.value, 10)

  // 停掉真实定时器，手动推一轮，避免测试里等 1s。
  c.stopPolling()
  await c.pollOnce()
  await wait(10)

  assert.equal(c.phase.value, 'installed')
  assert.equal(c.installed.value, true)
  assert.equal(c.progress.value, 100)
  assert.ok(calls.includes('POST /api/app/update/install'))
})

test('安装失败时后端中文提示原样保留，停止轮询并允许重试', async () => {
  const c = createAppUpdate({
    pollMs: 5,
    fetchImpl: async (url) => {
      if (url === '/api/app/update/check') return ok(CHECK_PAYLOAD)
      if (url === '/api/app/update/download') return ok({ status: 'downloading', progress: 50 })
      if (url === '/api/app/update/state') return ok({ status: 'ready', progress: 100 })
      // 契约定的是 code!=200；HTTP 仍可能是 200，必须按 body.code 判失败。
      if (url === '/api/app/update/install') return reply({ code: 500, message: '没有安装权限，请到飞牛应用中心手动安装新版 fpk' })
      throw new Error(`unexpected ${url}`)
    },
  })
  await c.check()
  await c.startUpdate()
  c.stopPolling()
  await c.pollOnce()
  await wait(10)

  assert.equal(c.phase.value, 'error')
  assert.equal(c.error.value, '没有安装权限，请到飞牛应用中心手动安装新版 fpk')
  assert.equal(c.installed.value, false)
})

test('下载接口失败后进入 error，不留下半截 downloading', async () => {
  const c = createAppUpdate({
    fetchImpl: async (url) => {
      if (url === '/api/app/update/check') return ok(CHECK_PAYLOAD)
      if (url === '/api/app/update/download') return reply({ code: 500, message: '磁盘空间不足，无法下载更新包' }, 500)
      throw new Error(`unexpected ${url}`)
    },
  })
  await c.check()
  await c.startUpdate()
  assert.equal(c.phase.value, 'error')
  assert.equal(c.error.value, '磁盘空间不足，无法下载更新包')
  assert.equal(c.downloading.value, false)
})

test('重复点「立即更新」只发一次 download', async () => {
  let downloads = 0
  let release
  const gate = new Promise((resolve) => { release = resolve })
  const c = createAppUpdate({
    fetchImpl: async (url) => {
      if (url === '/api/app/update/check') return ok(CHECK_PAYLOAD)
      if (url === '/api/app/update/download') {
        downloads += 1
        await gate
        return ok({ status: 'downloading', progress: 1 })
      }
      throw new Error(`unexpected ${url}`)
    },
  })
  await c.check()
  const first = c.startUpdate()
  const second = c.startUpdate()
  // async 函数即使内部 return null 也会包成 Promise，必须 await 后比较。
  assert.equal(await second, null)
  release()
  await first
  assert.equal(downloads, 1)
  c.stopPolling()
})

test('切页卸载后 resumeIfActive 接着轮询，不把进度停在半路', async () => {
  let stateCalls = 0
  const c = createAppUpdate({
    pollMs: 5,
    fetchImpl: async (url) => {
      if (url === '/api/app/update/check') return ok(CHECK_PAYLOAD)
      if (url === '/api/app/update/download') return ok({ status: 'downloading', progress: 10 })
      if (url === '/api/app/update/state') {
        stateCalls += 1
        return ok({ status: 'downloading', progress: 40 })
      }
      throw new Error(`unexpected ${url}`)
    },
  })
  await c.check()
  await c.startUpdate()
  c.stopPolling() // 模拟组件卸载
  await c.resumeIfActive()
  assert.equal(stateCalls, 1)
  assert.equal(c.phase.value, 'downloading')
  assert.equal(c.progress.value, 40)
  c.stopPolling()
})

test('轮询接口失败即停止，不再定时打后端', async () => {
  let stateCalls = 0
  const c = createAppUpdate({
    pollMs: 5,
    fetchImpl: async (url) => {
      if (url === '/api/app/update/check') return ok(CHECK_PAYLOAD)
      if (url === '/api/app/update/download') return ok({ status: 'downloading', progress: 10 })
      if (url === '/api/app/update/state') {
        stateCalls += 1
        return reply({ code: 500, message: 'NAS 暂时连不上，请稍后重试' }, 500)
      }
      throw new Error(`unexpected ${url}`)
    },
  })
  await c.check()
  await c.startUpdate()
  c.stopPolling()
  await c.pollOnce()
  assert.equal(c.phase.value, 'error')
  assert.equal(c.error.value, 'NAS 暂时连不上，请稍后重试')
  await wait(20)
  assert.equal(stateCalls, 1)
})

// 「更新完成」提示的取值必须走**同一个**实例。
//
// 这个用例是为一个真 bug 加的（2026-10-25）：线上页面刷新后弹窗永远不出现，
// 手工在控制台 import('/src/services/appUpdate.js') 调 ensureNotice() 却正常。
// 根因是 App.vue 的 onMounted 开着头，后面还有一串 await（音源脚本、窗口记忆…），
// vite 在开发态第二次请求同一个模块时带了不同的 `?t=`，模块被实例化了两份——
// App.vue 用的是「带头的那份」，控制台拿到的是「不带头的另一份」。
// 所以入口处必须自己把提示取好并写进响应式 ref，不能依赖某个异步阶段去取。
test('ensureNotice 命中时写入 notice，并只请求一次', async () => {
  let noticeCalls = 0
  const c = createAppUpdate({
    fetchImpl: async (url) => {
      if (url === '/api/app/update/notice') {
        noticeCalls += 1
        return ok({ show: true, version: '2.21.1', from_version: '2.20.0', direction: 'up' })
      }
      throw new Error(`unexpected ${url}`)
    },
  })
  const n = await c.ensureNotice()
  assert.equal(n.version, '2.21.1')
  assert.equal(n.direction, 'up')
  assert.equal(c.notice.value.version, '2.21.1')
  // 同一会话第二次调用直接返回 null，不再打后端（否则切页就反复弹）。
  assert.equal(await c.ensureNotice(), null)
  assert.equal(noticeCalls, 1)
})

// ── 2026-11-02 更新弹窗的六项改动 ─────────────────────────────────────

test('检测到已下载的更新包：normalizeCheck 带出 downloaded/downloaded_path', () => {
  const out = normalizeCheck({
    ...CHECK_PAYLOAD,
    downloaded: true,
    downloaded_path: '/vol1/@appdata/yinshu-ai/data/updates/yinshu-ai-2.18.0.fpk',
  })
  assert.equal(out.downloaded, true)
  assert.equal(out.downloadedPath, '/vol1/@appdata/yinshu-ai/data/updates/yinshu-ai-2.18.0.fpk')
  // 老后端没有这两个字段时不能变成 undefined（模板里会当成 falsy，但测试要钉住形状）
  const old = normalizeCheck(CHECK_PAYLOAD)
  assert.equal(old.downloaded, false)
  assert.equal(old.downloadedPath, '')
})

test('检查结果里的代理测速结论被归一并写进状态', async () => {
  const c = createAppUpdate({
    fetchImpl: async () => ok({
      ...CHECK_PAYLOAD,
      proxy: {
        configured: true,
        url: 'http://192.168.1.3:7890',
        ok: true,
        latency_ms: 128,
        preferred_proxy: true,
      },
    }),
  })
  await c.check()
  assert.equal(c.proxyProbe.value.configured, true)
  assert.equal(c.proxyProbe.value.ok, true)
  assert.equal(c.proxyProbe.value.latencyMs, 128)
  assert.equal(c.proxyProbe.value.preferredProxy, true)
})

test('没配代理时 proxyProbe 保持 null，界面不会显示测速结论', async () => {
  const c = createAppUpdate({ fetchImpl: async () => ok(CHECK_PAYLOAD) })
  await c.check()
  assert.equal(c.proxyProbe.value, null)
})

test('probeProxy 走 /api/app/update/proxy/probe 并归一返回', async () => {
  const calls = []
  const c = createAppUpdate({
    fetchImpl: async (url) => {
      calls.push(url)
      return ok({ configured: true, url: 'http://p:7890', ok: false, error: 'connection refused' })
    },
  })
  const res = await c.probeProxy()
  assert.deepEqual(calls, ['/api/app/update/proxy/probe'])
  assert.equal(res.configured, true)
  assert.equal(res.ok, false)
  assert.equal(res.error, 'connection refused')
  assert.equal(c.proxyProbe.value.ok, false)
})

test('安装日志倒序：新日志在顶部（第 2 项）', async () => {
  const c = createAppUpdate({
    fetchImpl: async () => ok({
      ok: true,
      path: '/tmp/yinshu-update/install.log',
      tail: '第一行\n第二行\n第三行',
    }),
  })
  await c.fetchInstallLog()
  assert.equal(c.installLog.value, '第三行\n第二行\n第一行')
  assert.equal(c.installLogPath.value, '/tmp/yinshu-update/install.log')
})
