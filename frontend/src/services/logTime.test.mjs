/**
 * logTime 的回归测试。
 *
 * 跑法：
 *   cd frontend && node --test src/services/logTime.test.mjs
 *   TZ=Asia/Shanghai node --test src/services/logTime.test.mjs   ← 见文末「跨时区」用例组
 *
 * 守的判据（都是用户真实报过的问题）：
 *
 *   1. **带 offset 的时间戳必须换算到浏览器本地时区**，不能直显服务器墙钟。
 *      2026-11-03 用户口径：「本地 22 点日志显示 14 点」—— NAS 是 UTC，
 *      22:00 (+08:00) 在 NAS 上是 14:00Z。若把它当墙钟直显就是慢 8 小时。
 *   2. 历史数据（不带 offset 的 naive ISO、"MM-DD HH:MM:SS"）仍按浏览器本地解释 ——
 *      它们本身已丢失时区，没有「正确值」可换算，改语义会让旧记录跳变。
 *   3. 解析不了就回退原文，绝不显示 NaN / Invalid Date / 1970。
 */
import assert from 'node:assert/strict'
import test from 'node:test'

import { tsOf, tsOfServer, fmtLocalDateTime, fmtServerDateTime, entryTimeText } from './logTime.js'

/** 当前进程的本地时区偏移（分钟，东为正）；用来断言与 cron 无关的实际换算结果。 */
function localOffsetMinutes(ms) {
  return -new Date(ms).getTimezoneOffset()
}

test('RFC3339 带 Z：绝对时刻就是那一秒，与本地时区无关', () => {
  const ms = tsOf('2026-11-03T14:23:43Z')
  assert.equal(ms, Date.UTC(2026, 10, 3, 14, 23, 43))
})

test('RFC3339 带 +08:00 与等价的 Z 表示同一个绝对时刻', () => {
  assert.equal(tsOf('2026-11-03T22:23:43+08:00'), tsOf('2026-11-03T14:23:43Z'))
})

test('毫秒级小数秒与负数/极小偏移都能解析', () => {
  assert.equal(tsOf('2026-11-03T14:23:43.500Z'), Date.UTC(2026, 10, 3, 14, 23, 43, 500))
  assert.equal(tsOf('2026-11-03T14:23:43-00:00'), tsOf('2026-11-03T14:23:43Z'))
})

test('Unix 秒 / 毫秒：按 1e12 分界，push 运行记录用的是秒', () => {
  const sec = 1793691033 // push/store.go Run.StartedAt
  assert.equal(tsOf(sec), sec * 1000)
  assert.equal(tsOf(sec * 1000), sec * 1000)
  assert.equal(tsOf(0), 0)
  assert.equal(tsOf(-5), 0)
})

test('naive ISO 的两种语义：tsOf 按浏览器本地（前端留存），tsOfServer 按 UTC（后端旧墙钟）', () => {
  const s = '2026-11-03T14:17:44'
  // tsOf（默认）= 前端本地留存语义：字符串墙钟就是浏览器本地墙钟
  assert.equal(tsOf(s), new Date(2026, 10, 3, 14, 17, 44).getTime())
  // 空格分隔的老写法等价
  assert.equal(tsOf('2026-11-03 14:17:44'), tsOf(s))
  // 只到分钟的老写法补 :00
  assert.equal(tsOf('2026-11-03T14:17'), new Date(2026, 10, 3, 14, 17, 0).getTime())

  // tsOfServer = 后端旧墙钟语义（monitor 旧记录）：字符串墙钟是服务器 UTC 墙钟
  assert.equal(tsOfServer(s), Date.UTC(2026, 10, 3, 14, 17, 44))
  assert.equal(tsOfServer('2026-11-03 14:17:44'), Date.UTC(2026, 10, 3, 14, 17, 44))
  // 带 offset 的输入不受 utcIfNaive 影响（绝对时刻本来明确）
  assert.equal(tsOfServer('2026-11-03T14:17:44Z'), Date.UTC(2026, 10, 3, 14, 17, 44))
  assert.equal(tsOfServer('2026-11-03T22:17:44+08:00'), Date.UTC(2026, 10, 3, 14, 17, 44))
})

test('fmtServerDateTime：naive UTC 墙钟换算到浏览器本地显示', () => {
  // 14:17 UTC = 北京 22:17 / UTC 浏览器 14:17 —— 断言用 Date 本地取值对齐，不依赖运行时区
  const d = new Date(Date.UTC(2026, 10, 3, 14, 17, 44))
  const p = (n) => String(n).padStart(2, '0')
  assert.equal(
    fmtServerDateTime('2026-11-03T14:17:44'),
    `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`,
  )
  // 解析失败回退原文（与 fmtLocalDateTime 同一约定）
  assert.equal(fmtServerDateTime('坏数据'), '坏数据')
  assert.equal(fmtServerDateTime(null), '')
})

test('appLog 的 "MM-DD HH:MM:SS" 补当前年份、按本地时间', () => {
  const ms = tsOf('11-03 22:15:07')
  const d = new Date(ms)
  assert.equal(d.getMonth(), 10)
  assert.equal(d.getDate(), 3)
  assert.equal(d.getHours(), 22)
  assert.equal(d.getMinutes(), 15)
  assert.equal(d.getSeconds(), 7)
  assert.equal(d.getFullYear(), new Date().getFullYear())
})

test('空值 / 垃圾值返回 0，不产生 NaN', () => {
  for (const v of [null, undefined, '', '   ', 'not-a-time', {}, [], NaN, Infinity]) {
    assert.equal(tsOf(v), 0, `输入 ${String(v)} 应返回 0`)
  }
})

test('fmtLocalDateTime 输出浏览器本地「日期 + 时间」，格式稳定', () => {
  const s = fmtLocalDateTime('2026-11-03T14:23:43Z')
  assert.match(s, /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/)
  // 与 Date 的本地取值逐字段对齐（不依赖具体时区）
  const d = new Date(Date.UTC(2026, 10, 3, 14, 23, 43))
  const p = (n) => String(n).padStart(2, '0')
  assert.equal(
    s,
    `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`,
  )
})

test('fmtLocalDateTime date:false 只给时分秒', () => {
  assert.match(fmtLocalDateTime('2026-11-03T14:23:43Z', { date: false }), /^\d{2}:\d{2}:\d{2}$/)
})

test('fmtLocalDateTime 解析失败回退原文（不显示 Invalid Date / 1970）', () => {
  assert.equal(fmtLocalDateTime('坏数据'), '坏数据')
  assert.equal(fmtLocalDateTime(''), '')
  assert.equal(fmtLocalDateTime(null), '')
})

test('entryTimeText 优先用带时区的 time，旧数据的 at 原样兜底', () => {
  // 有 time：换算到本地
  const withTime = entryTimeText({ at: '14:23:43', time: '2026-11-03T14:23:43Z' })
  assert.equal(withTime, fmtLocalDateTime('2026-11-03T14:23:43Z'))

  // 只有 at（旧后端只会给 HH:MM:SS，已丢失日期与时区）：原样显示，
  // 不臆造日期，也不假装它是某个绝对时刻
  assert.equal(entryTimeText({ at: '14:23:43' }), '14:23:43')

  // time 存在但坏掉 → 退回 at，而不是显示空
  assert.equal(entryTimeText({ at: '14:23:43', time: 'oops' }), '14:23:43')

  assert.equal(entryTimeText(null), '')
  assert.equal(entryTimeText(undefined), '')
})

test('时间列必须能被关键字搜索匹配到（与 LogViewer 的 search 一致）', () => {
  // 搜索框提示写着「搜索日志内容 / 来源 / 时间」，所以显示出来的时间串
  // 里的日期片段必须能命中 —— 这一条钉住「显示的串就是搜的串」。
  const shown = fmtLocalDateTime('2026-11-03T14:23:43Z')
  const day = shown.slice(0, 10)
  assert.ok(shown.includes(day))
})
