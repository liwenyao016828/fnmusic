import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mergeTaskList, isSettled, SETTLED } from './downloadMerge.js'

/* 守的判据（用户 2026-10-15 拍板要做的一条，来自上游 miyin v0.5.2 的修复）：
 *
 * 「取消下载任务后，取链/写元数据的滞留流程不再把任务复活为运行/完成状态」
 *
 * 我们这边的形态不一样（任务状态机在前端 downloadManager.js，队列另有一份
 * 落盘到 NAS 的副本），但根因同类：**移除/停止只改了本地，服务端那份要等一次
 * PUT 才更新，而中间任何一次同步都是拿服务端整份替换本地** —— 旧状态把新状态
 * 盖回去。用户看到的就是：
 *   · 点了取消，过一会儿它自己又跑起来，最后显示「下载成功」；
 *   · 一键「清除记录」清干净了，刷新一下记录全回来；
 *   · 点「停止全部」，切个页面又变回排队中。
 *
 * 这三条以前没有任何测试看着（不是纯函数，跑不起来），所以规则被抽到
 * downloadMerge.js —— 这里逐条钉住。
 */

/** 造一个够用的任务 */
const task = (id, status, extra = {}) => ({ id, status, songKey: `k-${id}`, ...extra })

test('取消过的任务：服务端旧副本再同步一次也不复活', () => {
  // 场景：用户点取消（本地已删、墓碑已立），服务端那份还是删之前的样子
  const remote = [task('t1', 'pending'), task('t2', 'pending')]
  const locals = [task('t2', 'pending')] // t1 已被移除
  const got = mergeTaskList({ list: remote, locals, cancelled: ['t1'], fromServer: true })
  assert.deepEqual(got.map(t => t.id), ['t2'])
})

test('清空记录后：服务端旧副本也不会把已完成记录搬回来', () => {
  // clearCompleted/clearRecords 走的是同一条 dropTasks → 墓碑
  const remote = [task('t1', 'success'), task('t2', 'success')]
  const got = mergeTaskList({ list: remote, locals: [], cancelled: ['t1', 't2'], fromServer: true })
  assert.deepEqual(got, [])
})

test('墓碑按任务 id：同一首歌之后重新入队不受影响', () => {
  // 「取消」针对这一次任务；重新下载会生成新 id（addSong 里 Date.now+随机）
  const remote = [task('t1', 'pending'), task('t9', 'pending')]
  const got = mergeTaskList({ list: remote, locals: [], cancelled: ['t1'], fromServer: true })
  assert.deepEqual(got.map(t => t.id), ['t9'])
})

test('服务端说还在排队、本地已「已停止」→ 以本地为准，且带停止标记', () => {
  // 这就是「点停止全部，切个页面又变回排队中」那条路
  const remote = [task('t1', 'pending')]
  const locals = [task('t1', 'stopped', { error: '已被用户停止' })]
  const got = mergeTaskList({ list: remote, locals, fromServer: true })
  assert.equal(got.length, 1)
  assert.equal(got[0].status, 'stopped')
  assert.equal(got[0].error, '已被用户停止')
  // 关键：_aborted 必须留在 true 上，否则 executeTask 一进去就不返回，任务真的会跑
  assert.equal(got[0]._aborted, true)
})

test('本地已成功/已失败，也不被服务端的在途状态拉回待下载', () => {
  for (const settled of ['success', 'failed']) {
    const remote = [task('t1', 'downloading')]
    const locals = [task('t1', settled)]
    const got = mergeTaskList({ list: remote, locals, fromServer: true })
    assert.equal(got[0].status, settled, `${settled} 被覆盖了`)
  }
})

test('反向不覆盖：本地在跑、服务端已成功 → 认服务端（那是真的下完了）', () => {
  const remote = [task('t1', 'success', { path: '/vol1/Music/a.flac' })]
  const locals = [task('t1', 'downloading')]
  const got = mergeTaskList({ list: remote, locals, fromServer: true })
  assert.equal(got[0].status, 'success')
  assert.equal(got[0].path, '/vol1/Music/a.flac')
})

test('本地缓存（非服务端）恢复：不做终态比对，行为与改造前一致', () => {
  const local = [task('t1', 'success')]
  const got = mergeTaskList({ list: [task('t1', 'pending')], locals: local, fromServer: false })
  // 本地缓存就是最后一次落盘的结果，不需要「谁更权威」的判断
  assert.equal(got[0].status, 'pending')
})

test('刷新页面：在途任务重置为待下载重新排队，并清掉重试倒计时', () => {
  const got = mergeTaskList({
    list: [task('t1', 'downloading'), task('t2', 'resolving'), task('t3', 'success')],
    locals: [],
    fromServer: true,
  })
  assert.equal(got[0].status, 'pending')
  assert.equal(got[0].error, '页面刷新后重新排队')
  assert.equal(got[1].status, 'pending')
  // 已完成的不动，也不该被标成「刷新后重新排队」
  assert.equal(got[2].status, 'success')
  assert.equal(got[2].error, undefined)
  // 所有任务的这两个内部字段都要归零，避免带着上次的停止标记复活
  for (const t of got) {
    assert.equal(t._aborted, false)
    assert.equal(t._retryAfter, 0)
  }
})

test('脏数据：无 id 的条目跳过，非数组返回空（不能让队列整体炸掉）', () => {
  const got = mergeTaskList({
    list: [null, undefined, {}, task('t1', 'pending'), { id: '', status: 'pending' }],
    locals: [],
  })
  assert.deepEqual(got.map(t => t.id), ['t1'])
  assert.deepEqual(mergeTaskList({ list: null }), [])
  assert.deepEqual(mergeTaskList({ list: 'nope' }), [])
})

test('cancelled 接受 Set 或数组（downloadManager 传的是 Set）', () => {
  const remote = [task('t1', 'pending'), task('t2', 'pending')]
  assert.deepEqual(mergeTaskList({ list: remote, cancelled: new Set(['t1']) }).map(t => t.id), ['t2'])
  assert.deepEqual(mergeTaskList({ list: remote, cancelled: ['t2'] }).map(t => t.id), ['t1'])
  assert.deepEqual(mergeTaskList({ list: remote, cancelled: null }).map(t => t.id), ['t1', 't2'])
})

test('「本地定下来」的集合就是这四个（别偷偷加 pending/resolving/downloading）', () => {
  assert.deepEqual([...SETTLED].sort(), ['failed', 'paused', 'stopped', 'success'])
  assert.equal(isSettled('stopped'), true)
  assert.equal(isSettled('paused'), true)
  for (const s of ['pending', 'resolving', 'downloading']) {
    assert.equal(isSettled(s), false, `${s} 不该算「本地定下来」`)
  }
})

test('暂停中的任务：服务端旧副本不会把它拉回下载（比取消复活更隐蔽）', () => {
  // pauseTask 只改本地 + _aborted，服务端那份要等一次 PUT
  const remote = [task('t1', 'pending')]
  const locals = [task('t1', 'paused', { _aborted: true })]
  const got = mergeTaskList({ list: remote, locals, fromServer: true })
  assert.equal(got[0].status, 'paused')
  assert.equal(got[0]._aborted, true)
})

test('本地已暂停、服务端已成功 → 认服务端（真的下完了，别一直挂暂停）', () => {
  const remote = [task('t1', 'success', { path: '/vol1/Music/a.flac' })]
  const locals = [task('t1', 'paused', { _aborted: true })]
  const got = mergeTaskList({ list: remote, locals, fromServer: true })
  assert.equal(got[0].status, 'success')
  assert.equal(got[0]._aborted, false)
})
