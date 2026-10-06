/**
 * 视图数据「新鲜度」登记表 —— 配合 SwipePager 的常驻页面用。
 *
 * 背景：SwipePager 改了之后，访问过的页面**不再卸载**（切页只是平移整行），
 * 于是"切回来重新挂载 → 重新请求 → 卡一下"这个毛病没了；代价是数据不会自己变新。
 * 「什么时候该刷」就由这张表回答。三种做法混着用（业界常见组合）：
 *
 *   ① TTL / stale-while-revalidate：切回来**先显示缓存**，超过 TTL 再后台静默刷一遍，
 *      刷回来原地替换 —— 不闪骨架屏。这是 SWR / React Query / TanStack Query 的默认思路。
 *   ② 事件失效：有写操作（下载完成、整理完成、扫描完成）时主动把相关视图标成过期，
 *      下次激活立刻刷 —— 相当于 React Query 的 invalidateQueries。
 *   ③ 首次进入照常加载，不看缓存。
 *
 * 纯逻辑、不碰 DOM，方便单测（本项目前端测试没有 jsdom）。
 */

/** 每个视图的 TTL（毫秒）：变化越快的越短 */
const TTL = {
  search: 60_000,     // 发现页：榜单/歌单变化很慢
  nas: 20_000,        // 本地目录：下载、整理会动文件
  library: 30_000,    // 曲库管家：监控与整理进度
  accounts: 300_000,  // 账号：基本不变
  logs: 5_000,        // 日志：本来就该新鲜
}

const DEFAULT_TTL = 30_000
const loadedAt = new Map()
const forced = new Set()

/** 该视图的 TTL */
export function ttlFor(view) {
  return TTL[view] ?? DEFAULT_TTL
}

/** 记一次"刚成功加载完" */
export function markLoaded(view, now = Date.now()) {
  if (!view) return
  loadedAt.set(view, now)
  forced.delete(view)
}

/** 上次成功加载的时间（0 = 从没加载过） */
export function lastLoadedAt(view) {
  return loadedAt.get(view) ?? 0
}

/**
 * 该刷新了吗？
 * 被 invalidate 标记过（有写操作）→ 一定过期；否则看 TTL。
 */
export function isStale(view, now = Date.now()) {
  if (!view) return false
  if (forced.has(view)) return true
  if (!loadedAt.has(view)) return true   // 用 has 而不是真假值：时间戳 0 是合法值
  return now - loadedAt.get(view) > ttlFor(view)
}

/** 主动标记过期（写操作之后调）：下次激活就会刷 */
export function invalidate(...views) {
  for (const v of views) if (v) forced.add(v)
}

/** 仅测试用：清空登记表 */
export function resetViewCache() {
  loadedAt.clear()
  forced.clear()
}
