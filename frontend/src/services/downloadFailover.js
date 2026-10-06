/**
 * 下载失败的「该不该换源」判定（纯函数，便于单测）。
 *
 * 背景（2026-09-22 用户反馈）：「歌曲下载失败怎么没有自动换源下载呢」。
 *
 * 原来的分类只有「可重试 / 不可重试」两种，而 CDN 403、返回的不是音频、
 * 文件过小这几类被判成**不可重试**（`HTTP_FATAL` / `PREVIEW_CLIP`）→ 直接 failed。
 * 可它们恰恰是**换一个音源重新取链就能救**的那一类：
 *
 * - 本地问题（权限 / 只读）→ 换源无用，换哪个源都写不下去；
 * - 链路问题（403 / 坏链 / 试听片段 / 通用下载失败）→ 这个**源**的链是坏的，换源才治得了。
 *
 * 所以这里把「不可重试」拆成 LOCAL_FATAL 与 SOURCE_LEVEL 两桶。
 */

/** 本地问题：换源救不了（下载目标目录写不进去）。注意**不含 ENOSPC** —— 保持既有行为。 */
export const LOCAL_FATAL_CODES = new Set(['EACCES', 'EPERM', 'EROFS'])

/** 链路问题：这条直链坏了，换个音源重新取链可能就好了 */
export const SOURCE_LEVEL_CODES = new Set([
  'HTTP_FATAL',
  'PREVIEW_CLIP',
  'DOWNLOAD_FAILED',
  'GET_URL_FAILED',
])

/** 本地问题判据：错误码或文案指向「写不下去」，换源没有意义 */
export function isLocalFatal(code, message) {
  if (code && LOCAL_FATAL_CODES.has(code)) return true
  return /权限|permission denied|read-only|只读/.test(String(message || '').toLowerCase())
}

/**
 * 链路问题判据：值得换个音源再试一次。
 *
 * 除了错误码，还兜一层文案 —— 后端的错误串不一定被 `classifyBackendResult`
 * 归到具体码上（未知错误会落到 `DOWNLOAD_FAILED`，但文案里可能写着「不是音频」）。
 */
export function isSourceLevelFailure(code, message) {
  if (isLocalFatal(code, message)) return false
  if (code && SOURCE_LEVEL_CODES.has(code)) return true
  return /不是音频|过小|无效流|短于|http 4\d\d|403|404|410/.test(String(message || '').toLowerCase())
}

/**
 * 还没试过的音源 ID。
 *
 * `tried` 里同时要包含「已经用来取过链的源」（`_triedSources`）和
 * 「当前正在用的源」（`activeSourceId`）—— 只传其中一个会把当前源当成没试过。
 */
export function untriedSources(tried, all) {
  const seen = new Set((tried || []).filter(Boolean))
  return (all || []).filter((id) => id && !seen.has(id))
}

/**
 * 这次失败该不该重试、该不该换源。
 *
 * 语义与改造前的 `handleTaskFailure` **完全兼容**：`transientRetry` 为 false
 * 且不是链路问题时，结论与旧代码一致（不重试）；新增的只有
 * 「链路问题 + 还有没试过的音源 → 换源重试」这一条。
 *
 * @param {{code?:string, message?:string, attempts?:number, tried?:string[],
 *          all?:string[], maxAttempts?:number, autoFailover?:boolean,
 *          transientRetry?:boolean}} ctx
 *   transientRetry —— 改造前那套「可重试」判定（超时/断网/5xx/429/取链失败）的结果
 * @returns {{retry:boolean, switchSource:boolean}} switchSource 为 true 时调用方应换源
 */
export function decideFailover(ctx) {
  const {
    code, message,
    attempts = 0,
    tried = [],
    all = [],
    maxAttempts = 3,
    autoFailover = true,
    transientRetry = false,
  } = ctx || {}

  if (attempts >= maxAttempts || !autoFailover) {
    return { retry: false, switchSource: false }
  }
  // 本地问题：换源无用，重试也无用
  if (isLocalFatal(code, message)) {
    return { retry: false, switchSource: false }
  }

  const remaining = untriedSources(tried, all)
  // 链路问题：换源才有意义。没有别的源可换就交给原来的判定
  if (isSourceLevelFailure(code, message) && remaining.length > 0) {
    return { retry: true, switchSource: true }
  }
  if (transientRetry) {
    return { retry: true, switchSource: remaining.length > 0 }
  }
  return { retry: false, switchSource: false }
}
