import { test } from 'node:test'
import assert from 'node:assert/strict'
import { deleteOutcome, normalizeScanSongs, withoutPath, mergeBackByPath } from './nasDelete.js'

test('deleteOutcome：后端 200 但 failed>0 不算成功（用户报的「列表删了、文件还在」）', () => {
  // 探针实测的真实响应：移到 /home/.tidy_trash 没权限，failed=1，但 code 仍是 200
  const res = {
    code: 200,
    message: '已挪进回收站，失败 1 个（可在「补全整理 → 数据快照」里恢复）',
    data: {
      mode: 'trash',
      moved: 0, deleted: 0, failed: 1,
      errors: ['/vol1/Music/a.mp3: mkdir /home/.tidy_trash: permission denied'],
      paths: ['/vol1/Music/a.mp3'],
    },
  }
  const out = deleteOutcome(res)
  assert.equal(out.ok, false)
  assert.equal(out.failed, 1)
  assert.match(out.message, /permission denied/)
  assert.deepEqual(out.kept, ['/vol1/Music/a.mp3'])
})

test('deleteOutcome：真的删掉了才算成功', () => {
  const ok = deleteOutcome({ code: 200, message: '已挪进回收站', data: { moved: 1, deleted: 0, failed: 0 } })
  assert.equal(ok.ok, true)
  const del = deleteOutcome({ code: 200, data: { mode: 'delete', moved: 0, deleted: 1, failed: 0 } })
  assert.equal(del.ok, true)
  assert.equal(del.mode, 'delete')
})

test('deleteOutcome：200 且全 0 也不能算成功（后端什么都没做）', () => {
  const out = deleteOutcome({ code: 200, message: 'ok', data: { moved: 0, deleted: 0, failed: 0 } })
  assert.equal(out.ok, false)
  assert.match(out.message, /没有删除任何文件/)
})

test('deleteOutcome：请求直接失败（不是 200 / 无 data）按失败处理', () => {
  const out = deleteOutcome({ code: 403, message: '路径不在允许访问的目录范围内，已拒绝访问' })
  assert.equal(out.ok, false)
  assert.equal(out.failed, 0)
  assert.match(out.message, /拒绝访问/)
  // data 缺失也不能炸
  assert.equal(deleteOutcome(null).ok, false)
  assert.equal(deleteOutcome(undefined).ok, false)
})

test('normalizeScanSongs：缺 name 的扫描结果补上 name，且永远返回数组', () => {
  // 探针实测：后端 Song 的 json 里根本没有 name 字段
  const songs = normalizeScanSongs([
    { id: 'nas_1', path: '/m/a.mp3', filename: 'a.mp3', title: 'a', artist: '本地歌手' },
    { id: 'nas_2', path: '/m/b.mp3', title: 'b' },
    null,
    'garbage',
  ])
  assert.equal(songs.length, 2)
  assert.equal(songs[0].name, 'a')
  assert.equal(songs[0].artist, '本地歌手')
  assert.equal(songs[1].name, 'b')
  for (const s of songs) assert.equal(typeof s.name, 'string')
})

test('normalizeScanSongs：非数组输入（字符串/undefined/对象）返回空数组而不是炸', () => {
  assert.deepEqual(normalizeScanSongs(undefined), [])
  assert.deepEqual(normalizeScanSongs('a,b'), [])
  assert.deepEqual(normalizeScanSongs({ songs: [] }), [])
  assert.deepEqual(normalizeScanSongs([]), [])
})

test('withoutPath：摘掉一条后原数组不被改动；坏输入原样返回', () => {
  const list = [{ path: '/a' }, { path: '/b' }]
  const next = withoutPath(list, '/a')
  assert.deepEqual(next, [{ path: '/b' }])
  assert.equal(list.length, 2)
  assert.deepEqual(withoutPath(list, ''), list)
  assert.deepEqual(withoutPath(null, '/a'), [])
})

test('mergeBackByPath：失败的行能放回去，且不重复', () => {
  const list = [{ path: '/a' }]
  assert.deepEqual(mergeBackByPath(list, { path: '/b' }), [{ path: '/a' }, { path: '/b' }])
  assert.deepEqual(mergeBackByPath(list, { path: '/a' }), [{ path: '/a' }])
  assert.deepEqual(mergeBackByPath(list, null), [{ path: '/a' }])
})
