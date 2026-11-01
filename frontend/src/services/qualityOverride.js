/**
 * 「下载音质」模式（三选一）—— 后端 pkg/config 的**镜像**。
 *
 * 为什么两边各要一份：后端管「飞牛页面触发的下载」，本页管「网页端下载队列」，
 * 但模式是**同一个**（config.download_quality_override）。
 * 改这边请同时改 pkg/config 的 QualityModeTarget/NormalizeQualityMode 并跑
 * 两边的测试。
 *
 * 2026-10-30 重构：从「跟随/无损/320k + 降级开关」两个控件合并成**一个**
 * 三选一 —— 用户的原话：「下载音质中，只需要三个选择，一个是跟随页面设置，
 * 一个是仅下载最高，一个是自动降档」。
 */

// 三个取值（与 config.DownloadQuality* 常量一一对应）
export const QUALITY_FOLLOW = ''
export const QUALITY_TOP_ONLY = 'top_only'
export const QUALITY_AUTO_LOWER = 'auto_lower'

/** 旧配置值（读到的瞬间迁到新三选一，见 normalizeQualityMode） */
export const LEGACY_QUALITY_LOSSLESS = 'lossless'
export const LEGACY_QUALITY_MP3 = 'mp3_320'

/** 模式的三个选项（设置页直接渲染这个表） */
export const QUALITY_MODE_OPTIONS = [
  { value: QUALITY_FOLLOW, label: '跟随页面设置', desc: '下载时页面选哪档就要哪档；平台没有那一档时自动降级' },
  { value: QUALITY_TOP_ONLY, label: '仅下载最高', desc: '只要最高档；平台给不了就报缺档，不降级' },
  { value: QUALITY_AUTO_LOWER, label: '自动降档', desc: '一律先要最高档，拿不到就用平台实际有的那一档' },
]

/**
 * 归一化模式值：认不出的、空白的一律当「跟随」。
 * 旧值 lossless → 仅下载最高（意图就是只要最高档）；mp3_320 → 跟随。
 */
export function normalizeQualityMode(value) {
  switch (String(value ?? '').trim()) {
    case QUALITY_TOP_ONLY:
      return QUALITY_TOP_ONLY
    case QUALITY_AUTO_LOWER:
      return QUALITY_AUTO_LOWER
    case LEGACY_QUALITY_LOSSLESS:
      return QUALITY_TOP_ONLY
    case LEGACY_QUALITY_MP3:
      return QUALITY_FOLLOW
    default:
      return QUALITY_FOLLOW
  }
}

/**
 * 把模式套到某个入口自己的档位上，得到这次要**点名**的档位。
 *
 * 两种非跟随模式都点名最高档（flac）—— 差别不在要什么，而在拿不到时
 * 降不降（后端 fetch.go 的 fallbackLower / config.QualityModeAllowsLower）。
 */
export function qualityModeTarget(base, mode) {
  switch (normalizeQualityMode(mode)) {
    case QUALITY_TOP_ONLY:
    case QUALITY_AUTO_LOWER:
      return 'flac'
    default:
      return String(base || '').trim()
  }
}

/**
 * 该模式下「平台没有点名的档」时能不能降级去用平台实际有的。
 * 只有「仅下载最高」说不。
 */
export function qualityModeAllowsLower(mode) {
  return normalizeQualityMode(mode) !== QUALITY_TOP_ONLY
}
