import test from 'node:test'
import assert from 'node:assert/strict'
import { BackendDownloadAPI } from '../api/client.js'
import {
  normalizeQueue,
  normalizeSubmit,
  describeBackendError,
  backendDownload,
  resetBackendDownloadForTests,
} from './backendDownload.js'

const real = { ...BackendDownloadAPI }
function stub(overrides) {
  Object.assign(BackendDownloadAPI, overrides)
}
function restore() {
  Object.assign(BackendDownloadAPI, real)
  resetBackendDownloadForTests()
}

test('normalizeQueue：状态/统计/逐条字段都归一，paused/stopped 不丢', () => {
  const out = normalizeQueue({
    code: 200,
    data: {
      items: [
        { id: 'bd-1', client_id: 'ui-1', name: '晴天', singer: '周杰伦', status: 'PAUSED', added_at: 10, actual_quality: 'flac' },
        { id: 'bd-2', client_id: 'ui-2', name: '夜曲', singer: '周杰伦', status: 'stopped' },
        { id: 'bd-3', client_id: 'ui-3', name: '稻香', singer: '周杰伦', status: 'success', path: '/Music/a.flac' },
      ],
      stats: { pending: 0, downloading: 0, paused: 1, success: 1, failed: 0, stopped: 1, total: 3 },
    },
  })
  assert.equal(out.items.length, 3)
  assert.equal(out.items[0].status, 'paused')
  assert.equal(out.items[0].clientId, 'ui-1')
  assert.equal(out.items[0].actualQuality, 'flac')
  assert.equal(out.stats.paused, 1)
  assert.equal(out.stats.stopped, 1)
  assert.equal(out.stats.total, 3)
})

test('normalizeSubmit：部分 added + duplicate 都算确认；其它 skipped 不含在内', () => {
  const out = normalizeSubmit({
    code: 200,
    data: {
      added: [{ client_id: 'a', id: 'bd-a' }],
      skipped: [
        { client_id: 'b', reason: 'duplicate' },
        { client_id: 'c', reason: 'no_channel' },
      ],
      stats: { pending: 1 },
    },
  })
  assert.equal(out.unmapped, false)
  assert.deepEqual(out.added.map(x => x.client_id), ['a'])
  assert.deepEqual(out.skipped.map(x => x.client_id), ['b', 'c'])
  assert.equal(out.addedCount, 1)
})

test('normalizeSubmit：只回计数不回 client_id 时标记 unmapped，绝不假装全部成功', () => {
  const out = normalizeSubmit({ code: 200, data: { added: 2 } })
  assert.equal(out.unmapped, true)
  assert.equal(out.added.length, 0)
  assert.equal(out.addedCount, 2)
})

test('describeBackendError：404 归一成 not_ready，而不是普通失败', () => {
  const e = new Error('Request failed')
  e.response = { status: 404, data: {} }
  assert.deepEqual(describeBackendError(e), { code: 'not_ready', message: '当前后端版本没有后台下载接口' })
})

test('refreshStatus：旧后端 404 → available=false/ready=false，订阅托管也复位', async () => {
  try {
    stub({
      status: async () => {
        const e = new Error('404')
        e.response = { status: 404, data: {} }
        throw e
      },
    })
    const ok = await backendDownload.refreshStatus()
    assert.equal(ok, false)
    assert.equal(backendDownload.available.value, false)
    assert.equal(backendDownload.ready.value, false)
    assert.equal(backendDownload.subscriptionsManaged.value, false)
    assert.match(backendDownload.reason.value, /没有后台下载接口/)
  } finally {
    restore()
  }
})

test('refreshStatus：ready=false 时即使 HTTP 成功也不可用，并保留 channels/lx_host/subscriptions', async () => {
  try {
    stub({
      status: async () => ({
        code: 200,
        data: {
          ready: false,
          reason: '后台 worker 未启动',
          channels: [{ id: 'native', enabled: true, ready: true, reason: '' }],
          lx_host: 'starting',
          subscriptions_managed: false,
          subscriptions_reason: '订阅执行尚未切到后台',
        },
      }),
    })
    const ok = await backendDownload.refreshStatus()
    assert.equal(ok, false)
    assert.equal(backendDownload.ready.value, false)
    assert.equal(backendDownload.channels.value.length, 1)
    assert.equal(backendDownload.lxHost.value, 'starting')
    assert.equal(backendDownload.subscriptionsManaged.value, false)
    assert.match(backendDownload.subscriptionsReason.value, /订阅/)
  } finally {
    restore()
  }
})

test('refreshStatus：ready=true 且声明订阅托管时如实暴露给 PushManager', async () => {
  try {
    stub({
      status: async () => ({
        code: 200,
        data: {
          ready: true,
          channels: [{ id: 'native', enabled: true, ready: true }],
          lx_host: 'off',
          subscriptions_managed: true,
        },
      }),
    })
    assert.equal(await backendDownload.refreshStatus(), true)
    assert.equal(backendDownload.ready.value, true)
    assert.equal(backendDownload.subscriptionsManaged.value, true)
    // 2026-10-25：lxWeakHint 已删除（宿主状态归音源管理页，下载页不再提示）；此处改验 lxHost 原始值仍在
    assert.equal(backendDownload.lxHost.value, 'off')
  } finally {
    restore()
  }
})

test('submitItems：提交失败时抛人话错误，且不修改已加载的队列（任务不会因此消失）', async () => {
  try {
    stub({
      queue: async () => ({ code: 200, data: { items: [{ id: 'bd-1', client_id: 'ui-1', name: '晴天', status: 'failed' }], stats: { failed: 1, total: 1 } } }),
      submit: async () => {
        const e = new Error('timeout')
        e.code = 'ECONNABORTED'
        e.request = {}
        throw e
      },
    })
    await backendDownload.refreshQueue()
    assert.equal(backendDownload.items.value.length, 1)
    await assert.rejects(
      () => backendDownload.submitItems([{ client_id: 'ui-2', name: '夜曲' }]),
      (err) => /连不上后端/.test(err.userMessage) && err.code === 'network',
    )
    assert.equal(backendDownload.items.value.length, 1)
    assert.equal(backendDownload.items.value[0].clientId, 'ui-1')
  } finally {
    restore()
  }
})
