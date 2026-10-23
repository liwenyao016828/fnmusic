/**
 * 单一后端下载队列的「入队映射」与「旧本地队列迁移」规则。
 *
 * 纯函数，无 Vue / 网络 / localStorage，便于 node --test 钉住几条会丢记录的边界：
 *   · 旧 success 只作为**完成历史**导入，绝不重新下载；
 *   · 旧 failed / stopped / paused 原样保留，不因迁移自动开始；
 *   · 旧 resolving / downloading（页面关闭时中断）迁成 stopped，不偷偷重跑；
 *   · 迁移回执只按 client_id 确认；没确认的留在本地，下次启动再试。
 */

export const MIGRATION_FLAG = 'fn_backend_queue_migrated_v1'
export const MIGRATION_BATCH_SIZE = 50

export const PLATFORM_SOURCES = new Set(['wy', 'tx', 'kg', 'kw', 'mg'])

export function platformSongId(song, platform) {
  const s = song || {}
  const p = String(platform || '').toLowerCase()
  if (!PLATFORM_SOURCES.has(p)) return ''
  const source = String(s.source || '').toLowerCase()
  if (source && source !== p) return ''
  const raw = String(s.platform_id || s.songmid || s.song_id || s.hash || s.id || '').trim()
  return raw.replace(/^(wy|tx|kg|kw|mg)_/i, '')
}

function platformOf(song, sourceId) {
  const s = song || {}
  // 调用方显式指定了平台（换源）就以它为准；否则用歌曲自身来源。
  const chosen = String(sourceId || '').toLowerCase()
  if (PLATFORM_SOURCES.has(chosen)) return chosen
  const own = String(s.source || '').toLowerCase()
  return PLATFORM_SOURCES.has(own) ? own : ''
}

/**
 * 新下载：搜索/歌单里的一首歌 → 后台提交项。
 *
 * `clientId` 由调用方生成并落库，提交失败/超时后用同一个 id 重试是幂等的。
 */
export function songToBackendItem(song, { clientId, sourceId = '', quality = 'highest' } = {}) {
  const s = song || {}
  const platform = platformOf(s, sourceId)
  return {
    client_id: String(clientId || ''),
    name: s.name || s.song_name || s.title || '',
    singer: s.singer || s.artist || '',
    album: s.album || '',
    duration: Number(s.duration || s.interval || 0) || 0,
    cover: s.cover || s.pic || '',
    platform,
    platform_id: platform ? platformSongId(s, platform) : '',
    // 用户显式选的档位优先；搜索结果的 quality 只是渠道标注，不能盖掉设置。
    quality: quality || s.quality || 'highest',
  }
}

/**
 * 旧浏览器任务 → 迁移提交项。
 *
 * - success：`history:true` + 原 path/actual_quality/size，只落记录不下载；
 * - failed/stopped/paused：保留原状态与 error；
 * - pending：按排队入队（用户原本就等着它下）；
 * - resolving/downloading/handoff/未知：页面关闭时中断，迁成 stopped，绝不自动重跑。
 */
export function legacyTaskToBackendItem(task, { qualityFallback = 'highest', index = 0 } = {}) {
  const t = task || {}
  const song = t.song || {}
  const platform = String(t.platform || song.source || '').toLowerCase()
  const valid = PLATFORM_SOURCES.has(platform)
  const base = {
    client_id: String(t.id || `legacy-${index}-${song.name || 'track'}`),
    name: song.name || song.title || '',
    singer: song.singer || song.artist || '',
    album: song.album || '',
    duration: Number(song.duration || song.interval || 0) || 0,
    cover: song.cover || song.pic || '',
    platform: valid ? platform : '',
    platform_id: valid ? platformSongId(song, platform) : '',
    quality: t.quality || qualityFallback,
    added_at: Number(t.addedAt || 0) || 0,
    completed_at: Number(t.completedAt || 0) || 0,
    error: t.error || '',
  }
  if (t.status === 'success') {
    return {
      ...base,
      status: 'success',
      history: true,
      path: t.path || '',
      actual_quality: t.resolvedQuality || t.actualQuality || '',
      size: Number(t.size || 0) || 0,
    }
  }
  if (t.status === 'failed' || t.status === 'stopped' || t.status === 'paused') {
    return { ...base, status: t.status }
  }
  if (t.status === 'pending') return { ...base, status: 'pending' }
  return {
    ...base,
    status: 'stopped',
    error: t.error || '上次下载在浏览器里中断，已停止；需要时请手动重试',
  }
}

/** 迁移回执 → 哪些可以删本地、哪些必须留下。只认 client_id。 */
export function splitMigrationReceipt(receipt = null) {
  const r = receipt || {}
  const ids = (v) => (Array.isArray(v) ? v : []).map(x => (typeof x === 'string' ? x : x?.client_id)).filter(Boolean)
  const added = ids(r.added)
  const duplicate = ids(r.skipped?.filter?.(x => String(x.reason || '').toLowerCase() === 'duplicate'))
  const skipped = Array.isArray(r.skipped) ? r.skipped : []
  const skippedReasons = new Map(
    skipped
      .filter(x => String(x.reason || '').toLowerCase() !== 'duplicate')
      .map(x => [x.client_id, String(x.reason || 'skipped')]),
  )
  // 只回计数不回 id：无法证明任何一条真的入队，全部保留。
  if (r.unmapped) {
    return { confirmedIds: new Set(), duplicateIds: new Set(), skippedReasons, unmapped: true }
  }
  return {
    confirmedIds: new Set(added),
    duplicateIds: new Set(duplicate),
    skippedReasons,
    unmapped: false,
  }
}

/** 旧任务里真正需要展示/迁移的：成功历史 + 未完成记录都保留。 */
export function legacyTasksToMigrate(tasks) {
  return (Array.isArray(tasks) ? tasks : []).filter(t => t && t.id && t.status !== 'cancelled')
}
