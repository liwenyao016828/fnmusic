import test from 'node:test'
import assert from 'node:assert/strict'
import {
  songToBackendItem,
  legacyTaskToBackendItem,
  splitMigrationReceipt,
} from './downloadMigration.js'

test('songToBackendItem：优先 songmid，换平台不拿原平台 id 去取链', () => {
  const wx = songToBackendItem(
    { id: 'wy_1973665667', songmid: '1973665667', source: 'wy', name: '晴天', singer: '周杰伦' },
    { clientId: 'ui-1' },
  )
  assert.equal(wx.platform, 'wy')
  assert.equal(wx.platform_id, '1973665667')

  // 任务实际平台是 tx，但 song 仍是 wy 的搜索对象且没有 tx songmid —— 留空，交给后端判 no_channel
  const switched = songToBackendItem(
    { id: 'wy_1973665667', source: 'wy', name: '晴天', singer: '周杰伦' },
    { clientId: 'ui-2', sourceId: 'tx' },
  )
  assert.equal(switched.platform, 'tx')
  assert.equal(switched.platform_id, '')
})

test('songToBackendItem：手动/本地条目不带第三方平台与 id', () => {
  const it = songToBackendItem(
    { id: 'local-1', source: 'nas', name: '自己整理的一首', singer: '未知' },
    { clientId: 'ui-3' },
  )
  assert.equal(it.platform, '')
  assert.equal(it.platform_id, '')
})

test('legacyTaskToBackendItem：success 只导入完成历史，绝不重下', () => {
  const it = legacyTaskToBackendItem({
    id: 'task_1', status: 'success', quality: 'flac', path: '/vol1/Music/a.flac',
    resolvedQuality: 'flac', addedAt: 100, completedAt: 200,
    song: { name: '晴天', singer: '周杰伦', source: 'wy', songmid: '123' },
  }, { index: 0 })
  assert.equal(it.status, 'success')
  assert.equal(it.history, true)
  assert.equal(it.path, '/vol1/Music/a.flac')
  assert.equal(it.actual_quality, 'flac')
  assert.equal(it.completed_at, 200)
})

test('legacyTaskToBackendItem：failed/stopped/paused 原样保留，不自动开始', () => {
  for (const status of ['failed', 'stopped', 'paused']) {
    const it = legacyTaskToBackendItem({ id: `task_${status}`, status, error: 'x', song: { name: 'a', source: 'wy', songmid: '1' } })
    assert.equal(it.status, status)
    assert.equal(it.history, undefined)
  }
})

test('legacyTaskToBackendItem：浏览器中断的 resolving/downloading 迁成 stopped', () => {
  for (const status of ['resolving', 'downloading', 'handoff', 'weird']) {
    const it = legacyTaskToBackendItem({ id: `task_${status}`, status, song: { name: 'a', source: 'wy', songmid: '1' } })
    assert.equal(it.status, 'stopped')
    assert.match(it.error, /中断/)
  }
})

test('splitMigrationReceipt：added + duplicate 确认，其余保留；unmapped 全部保留', () => {
  const ok = splitMigrationReceipt({
    added: [{ client_id: 'a' }],
    skipped: [{ client_id: 'b', reason: 'duplicate' }, { client_id: 'c', reason: 'no_channel' }],
  })
  assert.deepEqual([...ok.confirmedIds], ['a'])
  assert.deepEqual([...ok.duplicateIds], ['b'])
  assert.equal(ok.skippedReasons.get('c'), 'no_channel')
  assert.equal(ok.unmapped, false)

  const unknown = splitMigrationReceipt({ addedCount: 2, unmapped: true })
  assert.equal(unknown.confirmedIds.size, 0)
  assert.equal(unknown.unmapped, true)
})
