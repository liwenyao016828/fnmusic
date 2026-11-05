/**
 * 跨时区专项：模拟**浏览器在 Asia/Shanghai**（用户在杭州看 NAS 上的日志）。
 *
 * 跑法：cd frontend && TZ=Asia/Shanghai node --test src/services/logTime.tz.test.mjs
 *      （npm test 会自动带上 TZ，见 package.json）
 *
 * 为什么单独一个文件：这些断言只有在 TZ=Asia/Shanghai 下才有意义 —— 进程时区不同，
 * 期望值就不同。把它们和与时区无关的用例混在一起，会让「在 NAS 上跑测试」时
 * 得到与用户完全不同的结论。
 *
 * 守的是用户 2026-11-03 的原话：「我本地 22 点，日志显示 14 点」。
 */
import assert from 'node:assert/strict'
import test from 'node:test'

import { tsOf, tsOfServer, fmtLocalDateTime, fmtServerDateTime, entryTimeText } from './logTime.js'

const TZ = process.env.TZ || ''

test('前提：本用例组必须在 TZ=Asia/Shanghai 下运行', (t) => {
  if (TZ !== 'Asia/Shanghai') {
    t.skip(`TZ=${TZ || '(未设置)'}，跳过跨时区断言；用 TZ=Asia/Shanghai 重跑`)
    return
  }
  // 固定偏移 +08:00（中国无夏令时）
  assert.equal(-new Date('2026-11-03T00:00:00Z').getTimezoneOffset(), 480)
})

test('UTC 后端写的 14:23:43Z → 浏览器显示 22:23:43（不是 14:23:43）', (t) => {
  if (TZ !== 'Asia/Shanghai') return t.skip('需要 TZ=Asia/Shanghai')
  // 这就是 curl /api/logs 拿到的真实数据形状：
  //   {"at":"14:23:43","time":"2026-11-03T14:23:43Z",...}
  const e = { at: '14:23:43', time: '2026-11-03T14:23:43Z' }
  assert.equal(entryTimeText(e), '2026-11-03 22:23:43')
  assert.notEqual(entryTimeText(e).slice(11, 19), e.at, '绝不能直显服务器 UTC 墙钟')
})

test('落盘 qulv.log 的行首 RFC3339 也是同一换算', (t) => {
  if (TZ !== 'Asia/Shanghai') return t.skip('需要 TZ=Asia/Shanghai')
  // sink.go:196 写的是 time.RFC3339，NAS 上是 2026-11-03T14:23:43Z
  assert.equal(fmtLocalDateTime('2026-11-03T14:23:43Z'), '2026-11-03 22:23:43')
})

test('跨午夜：UTC 16:30Z 在本地已是次日 00:30，日期必须跟着变', (t) => {
  if (TZ !== 'Asia/Shanghai') return t.skip('需要 TZ=Asia/Shanghai')
  assert.equal(fmtLocalDateTime('2026-11-03T16:30:00Z'), '2026-11-04 00:30:00')
})

test('任务日志：push 的 Unix 秒 → 本地时间', (t) => {
  if (TZ !== 'Asia/Shanghai') return t.skip('需要 TZ=Asia/Shanghai')
  // push/store.go Run.StartedAt = 1793691033（秒）
  assert.equal(fmtLocalDateTime(1793691033), fmtLocalDateTime(1793691033 * 1000))
  // 与 Date 本地渲染一致
  const d = new Date(1793691033 * 1000)
  const p = (n) => String(n).padStart(2, '0')
  assert.equal(
    fmtLocalDateTime(1793691033),
    `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`,
  )
})

test('monitor 旧运行记录的 naive UTC 墙钟 → 按 UTC 解释，显示本地 22:17:44', (t) => {
  if (TZ !== 'Asia/Shanghai') return t.skip('需要 TZ=Asia/Shanghai')
  // "2026-11-03T14:17:44" 是 monitor 旧记录（nowISO()，无 offset）—— 它记的
  // 是 NAS 的 UTC 墙钟。14:17 UTC = 北京 22:17。这正是「本地 22 点显示 14 点」
  // 的根因：以前被 Date.parse 按浏览器本地解释。现在 tsOfServer 补 Z 按 UTC
  // 解析，任务日志（LogViewer loadTasks）已换用它。
  assert.equal(fmtServerDateTime('2026-11-03T14:17:44'), '2026-11-03 22:17:44')
  assert.equal(tsOfServer('2026-11-03T14:17:44'), Date.parse('2026-11-03T14:17:44Z'))
  // 带负 offset 的 RFC3339（真实 IANA 反例：美东用户）同样正确换算：
  // 14:17 UTC = 09:17 EST —— tsOfServer 不能只对正偏移有效。
  assert.equal(fmtServerDateTime('2026-11-03T09:17:44-05:00'), '2026-11-03 22:17:44')
})

test('tsOfServer 对带 offset / Unix 时间戳零影响（不重复换算）', (t) => {
  if (TZ !== 'Asia/Shanghai') return t.skip('需要 TZ=Asia/Shanghai')
  // 新 monitor 记录（nowStamp）带 Z：与旧数据混排时两者必须落在同一绝对时刻轴上
  assert.equal(tsOfServer('2026-11-03T14:17:44Z'), tsOf('2026-11-03T14:17:44Z'))
  // push 的 Unix 秒：tsOfServer 与 tsOf 结果一致（都按 UTC 解释 Unix 时间戳）
  assert.equal(tsOfServer(1793691033), tsOf(1793691033))
  // 新旧记录排序正确性：旧 naive(UTC 14:17) 与新 RFC3339(UTC 14:18) 相差 1 分钟
  assert.equal(tsOfServer('2026-11-03T14:18:00Z') - tsOfServer('2026-11-03T14:17:44'), 16_000)
})

test('前端本地留存（appLog "MM-DD HH:MM:SS"）仍按浏览器本地解释', (t) => {
  if (TZ !== 'Asia/Shanghai') return t.skip('需要 TZ=Asia/Shanghai')
  // appLog 的旧留存是浏览器本地墙钟（记在用户机器上）→ 语义与 monitor 的
  // UTC 墙钟相反，绝不能跟着 tsOfServer 一起改。tsOf（默认）保持本地解释。
  const ms = tsOf('11-03 14:17:44')
  assert.equal(new Date(ms).getHours(), 14, '本地留存的 14 点就是本地 14 点')
  // 新条目带 time（stampISO RFC3339 带本地 offset）→ entryTimeText 走它，
  // 显示也是本地墙钟，两个时代的记录在同一时间轴上不互相打架。
  assert.equal(entryTimeText({ time: '2026-11-03T14:17:44+08:00' }), '2026-11-03 14:17:44')
})
