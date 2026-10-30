// 「本地曲库删除歌曲」的判定逻辑（纯函数，有单测）。
//
// 为什么搬出来：用户 2026-10-29 报「本地曲库删除歌曲的时候，会出现 TypeError: s is not iterable。
// 列表删除了，实际没有删除。」—— 三件事叠在一起：
//
//  ① **后端回了 200 但一个文件都没删**。
//     `POST /api/nas/delete` 的回收站目录取的是「第一个文件所在盘的 .tidy_trash」
//     （见 backend/pkg/api/features.go 的 joinTrash：它按 `strings.HasPrefix(path, root)`
//     匹配允许根，`/home/liwenyao/...` 会先命中根 `/home`，于是把 `.tidy_trash` 放到了
//     用户没权限写的 `/home/.tidy_trash`）。挪进回收站失败后，Handler 仍然
//     `writeJSON(200, ...)`，只是 message 里多一句「失败 1 个」。
//     前端只看 `res.code === 200` → 当成成功 → 把列表项摘掉。
//     这就是「列表删除了，实际没有删除」。
//
//  ② **前端据此把行从列表里摘了**，于是界面与磁盘此后永久不一致。
//
//  ③ `TypeError: s is not iterable` 是 ① 的同源后果：失败都被当成成功以后，
//     排序 / 过滤 / 路径拆分这些「一定拿到数组」的地方会碰到意料之外的形状
//     （探针实测：扫描回来的每首歌**没有 `name` 字段**，只有 `title`；而模板、
//     spread、以及 `for (const x of ...)` 都假设它是字符串/数组）。
//     → 本模块的 `normalizeScanSongs` 让「喂进来什么形状都不会炸」。
//
// 约定：本模块**不改入参**、不读 DOM、不发请求；调用方（NasExplorer.vue）只做搬运。

/** 后端把「部分/全部文件没删掉」也报成 200 —— 这里的判据是 data 的执行结果，不是 code。 */
export function deleteOutcome(res) {
  const data = res?.data || {}
  const moved = numOf(data.moved)
  const deleted = numOf(data.deleted)
  const failed = numOf(data.failed)
  const errors = Array.isArray(data.errors) ? data.errors.filter(Boolean) : []
  const ok = failed === 0 && (moved + deleted) > 0
  return {
    ok,
    /** 后端整体拒绝（code != 200 / 请求失败）时用它的 message；部分失败要自己拼 */
    message: ok ? (res?.message || '已删除') : summaryOf(res, { moved, deleted, failed, errors }),
    moved,
    deleted,
    failed,
    errors,
    mode: data.mode || 'trash',
    /** 没能删掉的路径（用于「保留在列表里」，而不是假装删掉了） */
    kept: Array.isArray(data.paths) && failed > 0 ? data.paths : [],
  }
}

function numOf(v) {
  const n = Number(v)
  return Number.isFinite(n) && n > 0 ? n : 0
}

/** 后端那种「不带信息的成功话术」，出错时不能拿来当提示（否则界面会说「ok」） */
const VAGUE_MESSAGE = /^(ok|success|done|已删除|已挪进回收站)$/i

function summaryOf(res, { moved, deleted, failed, errors }) {
  const msg = String(res?.message || '').trim()
  if (failed === 0 && moved + deleted === 0) {
    // 200 + 全 0：后端什么都没做（例如 paths 为空），绝不能当成功
    return msg && !VAGUE_MESSAGE.test(msg) ? msg : '没有删除任何文件'
  }
  const head = `删除失败 ${failed} 个`
  if (errors.length) return `${head}：${errors[0]}`
  if (msg && !VAGUE_MESSAGE.test(msg) && msg !== '已挪进回收站，失败 0 个') return msg
  return head
}

/**
 * 扫描响应 → 歌曲数组（**任何形状都返回数组**）。
 *
 * 与 `nasView.js` 的 `scanViewOf` 分工：那边管「载荷形状」，这边管
 * 「每一条是不是能安全当对象用」—— 后端字段缺失/类型漂移时不炸，只补默认值。
 */
export function normalizeScanSongs(songs) {
  if (!Array.isArray(songs)) return []
  return songs.filter((s) => s && typeof s === 'object').map((s) => ({
    ...s,
    name: typeof s.name === 'string' && s.name ? s.name : (typeof s.title === 'string' ? s.title : ''),
    title: typeof s.title === 'string' ? s.title : '',
    path: typeof s.path === 'string' ? s.path : '',
  }))
}

/** 从列表里摘掉一个路径（返回新数组；路径不是字符串时原样返回，不炸） */
export function withoutPath(list, path) {
  if (!Array.isArray(list) || typeof path !== 'string' || !path) return Array.isArray(list) ? list : []
  return list.filter((x) => !x || x.path !== path)
}

/** 删除失败后要把行放回去：按 path 去重合并，保持原顺序（失败的那条在末尾补回） */
export function mergeBackByPath(list, song) {
  const base = Array.isArray(list) ? list : []
  if (!song || typeof song.path !== 'string' || !song.path) return base
  if (base.some((x) => x && x.path === song.path)) return base
  return [...base, song]
}
