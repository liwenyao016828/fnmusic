/**
 * userMsg 的回归测试。
 *
 * 跑法：cd frontend && node --test src/services/userMsg.test.mjs
 *
 * 守的判据（2026-09-18 的约定）：
 *   1. 界面上只能出现**一句人话** —— 不能漏出 HTTP 状态、接口路径、堆栈
 *   2. 后端自己给的人话要优先用（比按状态码猜更准）
 *   3. 技术细节不能丢 —— 它必须完整地进日志（describeError）
 */
import assert from 'node:assert/strict'
import test from 'node:test'

import { usableMessage, shortReason, describeError, apiError } from './userMsg.js'

/** 造一个 axios 风格的错误 */
function httpError(status, data = {}, url = '/charts/playlist/detail') {
  return { response: { status, data, config: { method: 'get', url } } }
}

test('usableMessage：只接受短、含中文、无技术痕迹的文案', () => {
  assert.equal(usableMessage('没能找到歌单 id'), true)
  assert.equal(usableMessage('账号未登录'), true)

  // 太长
  assert.equal(usableMessage('这是一句特别长的话'.repeat(10)), false)
  // 不含中文
  assert.equal(usableMessage('not found'), false)
  // 带接口路径
  assert.equal(usableMessage('GET /api/charts/detail 失败'), false)
  // 带堆栈
  assert.equal(usableMessage('TypeError: x is undefined\n    at foo (a.js:1:2)'), false)
  // 空值
  assert.equal(usableMessage(''), false)
  assert.equal(usableMessage(null), false)
})

test('后端给的人话优先于按状态码猜', () => {
  // 400 但后端说得比「参数不对」更具体
  assert.equal(shortReason(httpError(400, { message: '没能找到歌单 id' })), '没能找到歌单 id')
})

test('后端给的是技术串时，回退到按状态码判断', () => {
  const out = shortReason(httpError(400, { message: 'GET /api/x failed: EOF' }))
  assert.ok(!out.includes('/api/'), `界面文案不能漏出接口路径，实际：${out}`)
  assert.match(out, /参数不对/)
})

test('常见状态码都有一句人话', () => {
  assert.match(shortReason(httpError(401, {})), /登录/)
  assert.match(shortReason(httpError(403, {})), /权限|登录/)
  assert.match(shortReason(httpError(404, {})), /接口不存在/)
  assert.match(shortReason(httpError(429, {})), /频繁/)
  assert.match(shortReason(httpError(500, {})), /服务端/)
  assert.match(shortReason(httpError(503, {})), /服务端/)
})

test('网络类错误（连不上 / 超时）单独区分', () => {
  assert.match(shortReason({ code: 'ERR_NETWORK', message: 'Network Error' }), /连不上/)
  assert.match(shortReason({ code: 'ECONNABORTED' }), /超时/)
})

test('完全无法判断时用兜底文案', () => {
  assert.equal(shortReason(null, '操作失败'), '操作失败')
  assert.equal(shortReason({}, '操作失败'), '操作失败')
  assert.equal(shortReason(new Error('boom'), '操作失败'), '操作失败')
})

test('界面文案一律不含接口路径 / HTTP 状态 / 堆栈', () => {
  const samples = [
    httpError(400, { message: 'GET /api/charts/detail failed' }),
    httpError(500, { message: 'internal' }),
    httpError(404, {}),
    { code: 'ERR_NETWORK', message: 'Network Error' },
    { code: 'ECONNABORTED' },
    new Error('TypeError: x is undefined'),
  ]
  for (const e of samples) {
    const s = shortReason(e)
    assert.ok(!s.includes('/api/'), `不能漏接口路径：${s}`)
    assert.ok(!/\b(4|5)\d\d\b/.test(s), `不能漏 HTTP 状态码：${s}`)
    assert.ok(!/\bat .+:\d+/.test(s), `不能漏堆栈：${s}`)
    assert.ok(s.length <= 40, `必须是一句短语（<=40 字），实际 ${s.length}：${s}`)
  }
})

test('describeError：技术细节完整保留，供写进日志', () => {
  const d = describeError(httpError(500, { message: 'boom' }, '/push/tasks'))
  assert.match(d, /HTTP 500/)
  assert.match(d, /GET/)
  assert.match(d, /\/api\/push\/tasks/) // baseURL 要补上
  assert.match(d, /boom/)

  assert.match(describeError({ code: 'ECONNABORTED', config: { url: '/x' } }), /超时/)
  assert.match(describeError({ code: 'ERR_NETWORK' }), /网络不可达/)
  assert.match(describeError(new Error('boom')), /Error: boom/)
  assert.equal(describeError(null), '')
})

test('apiError：统一响应里 code!==200 才算失败', () => {
  assert.equal(apiError({ code: 200, message: 'ok' }), '')
  assert.equal(apiError(null), '操作失败')
  assert.equal(apiError({ code: 500, message: '账号未登录' }), '账号未登录')
  // 技术串 → 兜底
  assert.equal(apiError({ code: 500, message: 'POST /api/x failed' }), '操作失败')
})
