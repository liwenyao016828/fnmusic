/**
 * source-error 的回归测试。
 *
 * 跑法：cd frontend && node --test src/engine/source-error.test.mjs
 *
 * 守的判据：**原始报错不能丢**。
 * 翻译只是为了让人看懂，排查时仍然需要原文 —— 所以每条映射结果都必须
 * 包含原始报错（除了那些本身已经说清楚的、我们自己的状态文案）。
 */
import assert from 'node:assert/strict'
import test from 'node:test'

import { explainSourceError, isNotReadyError } from './source-error.js'

// 实测遇到过的真实原始报错（来自音源脚本自己的 catch-all）
const REAL_RAW_ERRORS = [
  ['unknow error', /返回异常/],
  ['get music url failed', /取链失败/],
  ['block ip', /IP 被封禁/],
  ['get url failed', /取链失败/],
  ['Object trans failure: 140017.', null], // 未归类 → 原样返回
]

test('真实原始报错都会被翻译成可读说明', () => {
  for (const [raw, want] of REAL_RAW_ERRORS) {
    const out = explainSourceError(raw)
    assert.ok(out.length > 0, `${raw} 应产出非空说明`)
    if (want) {
      assert.match(out, want, `${raw} 应归类到 ${want}，实际：${out}`)
    } else {
      assert.equal(out, raw, `${raw} 未归类时应原样返回，实际：${out}`)
    }
  }
})

test('翻译结果必须带上原始报错（信息不能丢）', () => {
  const out = explainSourceError('unknow error')
  assert.ok(out.includes('unknow error'), `翻译后应保留原文，实际：${out}`)
})

test('HTTP 状态类错误能被区分开', () => {
  assert.match(explainSourceError('Request failed with status code 404'), /404/)
  assert.match(explainSourceError('502 Bad Gateway'), /502|503/)
  assert.match(explainSourceError('503 Service Unavailable'), /502|503/)
  assert.match(explainSourceError('429 Too Many Requests'), /限流/)
  assert.match(explainSourceError('Internal Server Error'), /500/)
})

test('我们自己产生的状态文案不做二次包装', () => {
  for (const own of [
    '音源尚未就绪（等待 2s 仍未注册 handler）',
    '脚本加载失败: SyntaxError',
    '未导入任何可用音源脚本，请先在「音源管理」导入第三方音源',
  ]) {
    assert.equal(explainSourceError(own), own, `${own} 应原样返回`)
  }
})

test('接受 Error 对象与空值', () => {
  assert.match(explainSourceError(new Error('unknow error')), /unknow error/)
  assert.match(explainSourceError(''), /未知错误/)
  assert.match(explainSourceError(null), /未知错误/)
  assert.match(explainSourceError(undefined), /未知错误/)
})

test('顺序判据：具体规则优先于笼统规则', () => {
  // 同时含 404 与 "取链失败" 时，应命中更具体的 404
  const out = explainSourceError('取链失败: HTTP 404 not found')
  assert.match(out, /404/, `应优先报 404，实际：${out}`)
})

// ─────────────────────────────────────────────────────────────────────────────
// isNotReadyError：这次失败该不该「立刻再试一次」
//
// 判据的来源是一次真机误判（2026-09-30）：导入后立刻自动测试，正常的音源
// 脚本回了自己的「服务初始化中，请稍后」，被记成「不可用」，
// 用户看到「测试完成：0 个可用」。这类错误值得重试，502 那种不值得。
// ─────────────────────────────────────────────────────────────────────────────

test('脚本自己的「还没初始化好」要判为可重试', () => {
  // 真机实测原文（星海音乐源）
  assert.equal(isNotReadyError('服务初始化中，请稍后'), true)
  // 我们自己在等 handler 时产生的文案
  assert.equal(isNotReadyError('音源尚未就绪（等待 2s 仍未注册 handler）'), true)
  for (const msg of ['正在初始化，请稍后再试', '插件加载中', 'Source is not ready', 'initializing', 'Please wait']) {
    assert.equal(isNotReadyError(msg), true, `${msg} 应判为可重试`)
  }
})

test('真正的失败不该重试', () => {
  for (const msg of [
    'Request failed with status code 502',
    'HTTP 404 not found',
    '获取URL失败, 不支持的源: git',
    'Error',
    'IP 已被封禁',
  ]) {
    assert.equal(isNotReadyError(msg), false, `${msg} 不该被当成「还没好」`)
  }
})

test('「初始化超时」是已经等过的结论，不能当「还没好」', () => {
  // 关键：它含「初始化」二字，但语义相反 —— 再试一次没有意义
  assert.equal(isNotReadyError('音源初始化超时（10s）'), false)
  assert.equal(isNotReadyError('初始化超时'), false)
})

test('isNotReadyError 接受 Error 对象与空值，且不抛异常', () => {
  assert.equal(isNotReadyError(new Error('服务初始化中，请稍后')), true)
  assert.equal(isNotReadyError(new Error('502 Bad Gateway')), false)
  assert.equal(isNotReadyError(''), false)
  assert.equal(isNotReadyError(null), false)
  assert.equal(isNotReadyError(undefined), false)
})

test('重复翻译不会套娃（幂等）', () => {
  // 实测 2026-09-30：lx-runtime.probeSource 的 error 已是翻译过的，
  // 调用方再翻译一次，规则会命中文案里的「HTTP 502/503」→ 套娃。
  const once = explainSourceError('Request failed with status code 502')
  assert.match(once, /API 网关错误/)
  assert.equal(explainSourceError(once), once, '已翻译的文案再翻译必须原样返回')
  assert.equal(explainSourceError(explainSourceError(once)), once)
  assert.equal((once.match(/原始报错：/g) || []).length, 1, '只应出现一次「原始报错：」')
})

test('幂等判据只认「（原始报错：」这个我们自己加的标记', () => {
  // 普通原文里出现「原始报错」四个字（不带全角括号冒号）不该被误判为已翻译
  const msg = '原始报错 get url failed'
  assert.match(explainSourceError(msg), /取链失败/)
})
