/**
 * appLog 的回归测试。
 *
 * 跑法：cd frontend && node --test src/services/appLog.test.mjs
 *
 * 守的判据：
 *   1. 环形缓冲不会无限增长（前端日志不能吃光内存/配额）
 *   2. 上报通道是**注入**的，且上报失败绝不影响本地留存与调用方
 *   3. 日志模块自身永远不该抛异常 —— 它是最底层的能力，出错会连累所有调用方
 *
 * 注：node 环境没有 localStorage，hasStorage() 会返回 false，这里测的是内存态路径。
 * 真实浏览器里的持久化由浏览器验证覆盖。
 */
import assert from 'node:assert/strict'
import test from 'node:test'

import {
  logEvent,
  logError,
  logWarn,
  logInfo,
  logEntries,
  clearLocalLog,
  localLogCount,
  setLogReporter,
} from './appLog.js'

test('记一条日志，字段齐全', () => {
  clearLocalLog()
  const e = logError('歌单解析', '链接无效', 'HTTP 400 GET /api/charts/playlist/detail')
  assert.equal(e.level, 'error')
  assert.equal(e.scope, '歌单解析')
  assert.match(e.message, /链接无效/)
  assert.match(e.message, /HTTP 400/) // 技术细节要留在日志里
  assert.match(e.at, /^\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/) // 跨天也能看出日期
})

test('level 归一：只认 info / warn / error', () => {
  clearLocalLog()
  assert.equal(logEvent('weird', 'x', 'y').level, 'info')
  assert.equal(logWarn('x', 'y').level, 'warn')
  assert.equal(logInfo('x', 'y').level, 'info')
  assert.equal(logError('x', 'y').level, 'error')
})

test('环形缓冲有上限，不会无限增长', () => {
  clearLocalLog()
  for (let i = 0; i < 260; i++) logError('压力', `第 ${i} 条`)
  const all = logEntries()
  assert.equal(all.length, 200, `应封顶 200 条，实际 ${all.length}`)
  // 保留的必须是最新的那批
  assert.match(all[all.length - 1].message, /第 259 条/)
  assert.ok(!all.some((e) => /第 0 条$/.test(e.message)), '最旧的应被淘汰')
})

test('单条消息过长会截断（避免一条就撑爆配额）', () => {
  clearLocalLog()
  const e = logError('大对象', 'x'.repeat(2000))
  assert.ok(e.message.length <= 501, `应截断到 500 字，实际 ${e.message.length}`)
  assert.ok(e.message.endsWith('…'))
})

test('detail 支持 Error / 对象 / 字符串', () => {
  clearLocalLog()
  assert.match(logError('a', 'msg', new Error('boom')).message, /boom/)
  assert.match(logError('a', 'msg', { code: 1 }).message, /"code":1/)
  assert.match(logError('a', 'msg', 'plain').message, /plain/)
  assert.equal(logError('a', 'msg').message, 'msg') // 没 detail 就不加分隔符
})

test('上报通道是注入的，且带上 scope 前缀', () => {
  clearLocalLog()
  const seen = []
  setLogReporter((level, message) => seen.push({ level, message }))
  logError('推送任务保存', '保存失败', 'HTTP 500')

  assert.equal(seen.length, 1)
  assert.equal(seen[0].level, 'error')
  assert.match(seen[0].message, /^\[推送任务保存\]/)
  assert.match(seen[0].message, /保存失败/)

  setLogReporter(null)
})

test('上报失败不影响本地留存，也不抛给调用方', () => {
  clearLocalLog()
  setLogReporter(() => {
    throw new Error('上报炸了')
  })
  assert.doesNotThrow(() => logError('x', '本地要留下'))

  setLogReporter(() => Promise.reject(new Error('异步上报炸了')))
  assert.doesNotThrow(() => logError('x', '本地也要留下'))

  assert.equal(localLogCount(), 2, '两次都应留在本地')
  setLogReporter(null)
})

test('未注入上报通道时只做本地留存', () => {
  clearLocalLog()
  setLogReporter(null)
  assert.doesNotThrow(() => logError('x', 'y'))
  assert.equal(localLogCount(), 1)
})

test('logEntries 返回副本，外部改动不影响内部状态', () => {
  clearLocalLog()
  logError('x', 'y')
  const snapshot = logEntries()
  snapshot.push({ at: '99-99 99:99:99', level: 'error', scope: '', message: '伪造' })
  snapshot.length = 0
  assert.equal(localLogCount(), 1, '外部对返回值的改动不应影响内部')
})

test('clearLocalLog 清空', () => {
  clearLocalLog()
  logError('x', 'y')
  assert.equal(localLogCount(), 1)
  clearLocalLog()
  assert.equal(localLogCount(), 0)
  assert.deepEqual(logEntries(), [])
})
