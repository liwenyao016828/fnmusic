// 搜索结果「触底自动加载更多」的分页状态机。
//
// 为什么要单独抽出来（而不是写在 SearchView 里）：停手条件有四条、
// 还有一层「已拉回未露出」的缓冲，混在组件里没法测。这里全是纯逻辑，
// 用 searchPaging.test.mjs 钉住。
//
// 三条停手条件是用户定的：**空页 / 不满一页 / 失败即停**。
// 第四条是保险：**一页里一条新歌都没有也停**。它的来历 —— 后端「全部平台」
// 分支是各平台并跑再合并（backend/pkg/search/aggregator.go 的 16 条/平台），
// 万一哪个平台的 Search 不认 page 参数、每页都返回同一批，那么「不满一页」
// 永远不会到来，用户就会一直滚、一直看到重复行。按 key 去重后没有新歌就停，
// 这个坑就翻不了车。
//
// 「每页 30 条」是**露出**口径：不管后端一页合并回来多少行（全部平台时可能
// 60-100+），界面每次只多露 30 条，多的先收在 buffer 里，避免一次塞一大坨。

/** 一次露出多少条（= 页大小，也是「不满一页」的尺子） */
export const SEARCH_PAGE_SIZE = 30

/**
 * 一首歌在分页里的身份：平台 ID 优先（带平台前缀，跨平台同曲不同源不算重复），
 * 没有 ID 才退回「歌手 - 歌名」（这时两条行长得一模一样，本来就该当重复）。
 * 与 downloadManager.js 的 getSongKey 同语义。
 */
export function searchSongKey(song) {
  const s = song || {}
  return s.id || `${s.singer || '未知'} - ${s.name || '未知'}`
}

/**
 * 建一个分页器。用法（组件侧）：
 *
 *   const pager = createSearchPager()
 *   show(pager.start(res.data?.list))            // 新搜索：露出第一页的前 30 条
 *   if (pager.beginFetch()) {                    // 触底且缓冲空了才打后端
 *     try { show(pager.finishFetch(await SearchAPI.search(kw, src, pager.page + 1, pager.pageSize))) }
 *     catch { pager.finishFetch(null) }          // 失败即停
 *   } else {
 *     show(pager.reveal())                       // 缓冲里还有就直接露，不打后端
 *   }
 *
 * `show(rows)` 把行接到列表尾部（组件自己做）。
 */
export function createSearchPager(pageSize = SEARCH_PAGE_SIZE) {
  let buffer = []      // 已拉回、还没露出的行
  let page = 0         // 已经从后端拉到第几页（0 = 还没搜过）
  let more = true      // 后端还有没有下一页（四条停手条件管它）
  let inflight = false // 有一次请求在路上
  const seen = new Set()

  const reveal = () => {
    const rows = buffer.slice(0, pageSize)
    buffer = buffer.slice(rows.length)
    return rows
  }

  // 吃进一批（新搜索或新页）：按 key 去重，返回真正的新行
  const ingest = list => {
    const fresh = []
    for (const song of list) {
      const key = searchSongKey(song)
      if (seen.has(key)) continue
      seen.add(key)
      fresh.push(song)
    }
    return fresh
  }

  return {
    get pageSize() { return pageSize },
    get page() { return page },
    get buffered() { return buffer.length },
    get inflight() { return inflight },
    /** 还有没有得看：缓冲里有货，或后端还有下一页 */
    get hasMore() { return more || buffer.length > 0 },
    /** 现在要不要打后端：还有下一页、没在拉、且缓冲已经见底 */
    get canFetch() { return more && !inflight && buffer.length === 0 },

    /** 新搜索：吃进第 1 页，返回要露出的行 */
    start(list) {
      buffer = []
      page = 0
      more = true
      inflight = false
      seen.clear()
      const rows = Array.isArray(list) ? list : []
      buffer = ingest(rows)
      page = 1
      // 不满一页 = 这已经是最后一页
      if (rows.length < pageSize) more = false
      return reveal()
    },

    /** 露出缓冲里的下一批（不打后端）。缓冲空了就返回 [] */
    reveal,

    /** 开始拉下一页；返回 false 表示现在不该拉（不要发请求） */
    beginFetch() {
      if (!more || inflight || buffer.length > 0) return false
      inflight = true
      return true
    },

    /**
     * 下一页到货（`list` 传 null/undefined = 请求失败），返回要露出的行。
     * 空页 / 不满一页 / 失败 / 整页都是重复 → 后端就此打住（more=false）。
     */
    finishFetch(list) {
      inflight = false
      const rows = Array.isArray(list) ? list : null
      if (!rows || rows.length === 0) {
        more = false // 失败即停 / 空页即停
        return []
      }
      page += 1
      const fresh = ingest(rows)
      if (rows.length < pageSize) more = false // 不满一页即停
      if (fresh.length === 0) more = false     // 一页没有新歌即停（防平台不认 page）
      buffer = buffer.concat(fresh)
      return reveal()
    },

    /** 换关键词 / 换平台 / 退出搜索：全部复位 */
    reset() {
      buffer = []
      page = 0
      more = true
      inflight = false
      seen.clear()
    },
  }
}
