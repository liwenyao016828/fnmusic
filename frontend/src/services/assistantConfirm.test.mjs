// 回归测试：确认流程的事件契约。
// 捕获的 bug —— 用户点「确认」后看不到任何反应，因为：
//   1) 后端 ResolveAction 传了空 emit，执行结果与后续回复全丢；
//   2) confirm 接口返回 {code:200} 普通 JSON，前端不看响应；
//   3) tool 事件的 action_id 与 action 事件的 id 字段名不一致，
//      前端按 action_id 找卡片找不到 → 确认按钮根本没渲染出来。
import test from 'node:test'
import assert from 'node:assert/strict'
import { parseStream } from './assistantStream.js'

test('action 事件同时带 id 和 action_id（前端两种写法都能对上）', () => {
  const raw = 'data: {"type":"action","data":{"id":"act_1","action_id":"act_1","name":"download_song","detail":"下载歌曲：《晴天》- 周杰伦","status":"pending"}}\n\n'
  const [ev] = parseStream(raw)
  assert.equal(ev.event, 'action')
  assert.equal(ev.data.id, 'act_1')
  assert.equal(ev.data.action_id, 'act_1')
  assert.ok(ev.data.detail.includes('晴天'), '确认卡要能显示人话描述')
})

test('tool 事件等待确认时带 action_id，前端据此渲染确认按钮', () => {
  const raw = 'data: {"type":"tool","data":{"name":"download_song","status":"awaiting_confirmation","action_id":"act_9"}}\n\n'
  const [ev] = parseStream(raw)
  assert.equal(ev.event, 'tool')
  assert.equal(ev.data.status, 'awaiting_confirmation')
  assert.equal(ev.data.action_id, 'act_9')
})

test('确认后的流：executed 结果 + 助手后续正文 + done', () => {
  const raw = [
    'data: {"type":"tool","data":{"name":"download_song","status":"executing"}}',
    'data: {"type":"action","data":{"id":"act_1","action_id":"act_1","status":"executed","result":"已加入下载队列"}}',
    'data: {"type":"tool","data":{"name":"download_song","status":"done","result":"已加入下载队列"}}',
    'data: {"type":"role","data":{"role":"assistant"}}',
    'data: {"type":"text","data":{"text":"已经开始下载《晴天》了。"}}',
    'data: {"type":"done"}',
  ].join('\n\n') + '\n\n'
  const evs = parseStream(raw)
  assert.deepEqual(evs.map(e => e.event), ['tool', 'action', 'tool', 'role', 'text', 'done'])

  const executed = evs.find(e => e.event === 'action')
  assert.equal(executed.data.status, 'executed')
  assert.equal(executed.data.result, '已加入下载队列')

  const reply = evs.filter(e => e.event === 'text').map(e => e.data.text).join('')
  assert.ok(reply.includes('晴天'), '确认后助手的后续回复必须能显示')
})

test('执行失败时 action 状态是 failed，前端要显示失败而不是完成', () => {
  const raw = 'data: {"type":"action","data":{"id":"act_2","action_id":"act_2","status":"failed","result":"执行失败（HTTP 500）"}}\n\n'
  const [ev] = parseStream(raw)
  assert.equal(ev.data.status, 'failed')
  assert.ok(ev.data.result.includes('HTTP 500'))
})
