/**
 * 「这批曲目里哪些本地已经有了」的判定。
 *
 * 用户原话（2026-09-22）：「同一首，同歌手，不需要，只下载不一样的版本」。
 *
 * 后端判据（`backend/pkg/downloader` 的 `HandleCheckDownloaded`）：
 * 下载目录里存在 `歌手 - 歌名.mp3` **且体积 > 300KB**。
 * 文件名不同就是不同版本（`… (Live).mp3`），会照下 —— 这正是用户要的。
 *
 * ⚠️ 返回的 `exists` 映射里**同时有两种键**：`歌手 - 歌名` 和 平台曲目 id。
 * 两种都要认：监控侧登记的歌带 `song_id`，推送侧的 `missing` 没有。
 * 只认一种的话，总有一侧会漏判、把本地已有的歌再下一遍。
 */
export function pickAlreadyLocal(items = [], exists = {}) {
  const out = new Set()
  if (!Array.isArray(items) || !items.length || !exists) return out

  items.forEach((t, i) => {
    // 歌名和 id 都缺 → 判不了，当「没有」。别拿 `' - '` 这种空拼接去查表，
    // 万一后端返回了同名键就会误判成「已有」，白白漏掉一首。
    if (!t?.name && !t?.song_id) return

    const byPair = t.name ? exists[`${t.artist || ''} - ${t.name}`] : undefined
    const byId = t.song_id ? exists[t.song_id] : undefined
    if (byPair || byId) out.add(i)
  })

  return out
}
