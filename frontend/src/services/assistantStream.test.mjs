// 流式帧解析的回归测试。
//
// 曾经的线上 bug：后端每个 SSE 帧只写 `data: {...}`，事件名放在 JSON 的
// type 字段里（没有 `event:` 行）。解析器把这种帧的 event 默认成 'message'，
// 界面 switch(event) 匹配不到任何分支 —— 于是「助手的回答收到了却一个字都不显示」。
import test from 'node:test'
import assert from 'node:assert/strict'
import { splitFrames, parseFrame, parseStream } from './assistantStream.js'

test('没有 event: 行时，事件名取自 data.type', () => {
  const ev = parseFrame('data: {"type":"text","data":{"text":"你好"}}')
  assert.equal(ev.event, 'text')
  assert.equal(ev.data.text, '你好')
})

test('JSON 里有 type 时以它为准（后端的实际格式）', () => {
  const ev = parseFrame('event: ping\ndata: {"type":"text","data":{"text":"x"}}')
  assert.equal(ev.event, 'text')
})

test('JSON 里没有 type 时退回 event: 行', () => {
  const ev = parseFrame('event: ping\ndata: {"data":{"text":"x"}}')
  assert.equal(ev.event, 'ping')
})

test('多行 data 会被拼接成一条 JSON', () => {
  const ev = parseFrame('data: {"type":"text",\ndata: "data":{"text":"a"}}')
  assert.equal(ev.event, 'text')
  assert.equal(ev.data.text, 'a')
})

test('splitFrames 保留半截帧到 rest，不丢字符', () => {
  const { frames, rest } = splitFrames('data: {"type":"a"}\n\ndata: {"type":"b",')
  assert.deepEqual(frames, ['data: {"type":"a"}'])
  assert.equal(rest, 'data: {"type":"b",')
})

test('[DONE] 与空帧不产生事件', () => {
  assert.equal(parseFrame('data: [DONE]'), null)
  assert.equal(parseFrame(''), null)
})

test('坏 JSON 不抛异常（流不能因为一帧坏掉就整个断）', () => {
  assert.equal(parseFrame('data: {oops'), null)
})

test('meta 事件带回 session_id，界面据此把回复落到同一个会话', () => {
  const evs = parseStream('data: {"type":"meta","data":{"session_id":"s1"}}\n\n')
  assert.equal(evs.length, 1)
  assert.equal(evs[0].data.session_id, 's1')
})
