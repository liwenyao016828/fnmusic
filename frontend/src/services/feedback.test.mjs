import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  feedbackItems,
  notify,
  info,
  ok,
  warn,
  fail,
  dismiss,
  dismissAll,
  FEEDBACK_DEDUPE_MS,
} from './feedback.js'
import { logEntries, clearLocalLog } from './appLog.js'

/* 守的判据（2026-10-15 全站 UI/UX 重构 P0「统一反馈」）：
 *
 * 重构前同一件事（例：「下载目录保存失败」）在四个通道里各写一遍、措辞还不一样：
 * 页脚文案、各页自带的 InlineNotice、App.vue 的单槽位 appToast、少数 alert()。
 * 用户看到的是「提示太多、说法不一致」。
 *
 * 这里钉住新通道的**取舍**（不是钉 UI）：
 *   · 瞬时提示会自己走（不会像 InlineNotice 那样永久占位）；
 *   · 连点同一个按钮不刷屏（去重），但不同等级的话不合并；
 *   · 失败默认可一键去「日志」（老 appToast 的友好行为，现在任何组件都调得到）；
 *   · 空文案不产生一条空气泡。
 */

/** 每个用例前清干净，避免互相影响 */
function reset() {
  dismissAll()
  clearLocalLog()
}

test('ok/info 是普通提示，不挂「看日志」入口', () => {
  reset()
  ok('已保存')
  assert.equal(feedbackItems.value.length, 1)
  assert.equal(feedbackItems.value[0].level, 'ok')
  assert.equal(feedbackItems.value[0].goLogs, false)

  info('已刷新')
  assert.equal(feedbackItems.value.length, 2)
  assert.equal(feedbackItems.value[1].level, 'info')
})

test('失败默认给「点一下看日志」，但可以显式关掉', () => {
  reset()
  fail('下载目录保存失败')
  assert.equal(feedbackItems.value[0].level, 'fail')
  assert.equal(feedbackItems.value[0].goLogs, true)

  reset()
  fail('这句不需要日志入口', { goLogs: false })
  assert.equal(feedbackItems.value[0].goLogs, false)
})

test('空文案不产生气泡（调用方传了空东西不该在屏幕上留个空框）', () => {
  reset()
  assert.equal(notify(''), 0)
  assert.equal(notify('   '), 0)
  assert.equal(notify(null), 0)
  assert.equal(notify(undefined), 0)
  assert.equal(feedbackItems.value.length, 0)
})

test('同一句话连点只留一条（去重窗口内合并，重置计时）', () => {
  reset()
  const a = ok('已保存')
  const b = ok('已保存')
  assert.equal(a, b, '重复推送应返回同一个 id（不是新增一条）')
  assert.equal(feedbackItems.value.length, 1)
  assert.ok(FEEDBACK_DEDUPE_MS > 0)
})

test('不同等级不算重复（「已保存」和「保存失败」是两件事）', () => {
  reset()
  ok('保存')
  fail('保存')
  assert.equal(feedbackItems.value.length, 2)
})

test('列表有上限，长时间不清理也不会无界增长', () => {
  reset()
  for (let i = 0; i < 30; i++) info(`第 ${i} 条`)
  assert.ok(feedbackItems.value.length <= 12, `实际 ${feedbackItems.value.length} 条`)
  // 保留的是最新的那批
  assert.equal(feedbackItems.value[feedbackItems.value.length - 1].text, '第 29 条')
})

test('dismiss / dismissAll 能清掉（模板点了气泡就调它）', () => {
  reset()
  const id = ok('一')
  ok('二')
  dismiss(id)
  assert.deepEqual(feedbackItems.value.map((i) => i.text), ['二'])
  dismissAll()
  assert.equal(feedbackItems.value.length, 0)
})

test('到点自动消失；timeout: 0 表示「不自动消失」', async () => {
  reset()
  ok('会自己走', { timeout: 20 })
  ok('留着不动', { timeout: 0 })
  assert.equal(feedbackItems.value.length, 2)
  await new Promise((r) => setTimeout(r, 60))
  assert.deepEqual(feedbackItems.value.map((i) => i.text), ['留着不动'])
})

test('warn/fail 带 scope 时顺手写一条应用日志（界面一句结论，细节进日志）', () => {
  reset()
  fail('保存失败', { scope: '下载设置', detail: new Error('boom') })
  const last = logEntries().at(-1)
  assert.equal(last.level, 'error')
  assert.equal(last.scope, '下载设置')
  assert.match(last.message, /保存失败/)
  assert.match(last.message, /boom/) // detail 也带进去了

  reset()
  warn('音源慢', { scope: '取链' })
  assert.equal(logEntries().at(-1).level, 'warn')
})

test('普通提示不写日志（否则日志会被「已保存」之类刷满）', () => {
  reset()
  ok('已保存', { scope: '不该写的 scope' })
  assert.equal(logEntries().length, 0)
})
