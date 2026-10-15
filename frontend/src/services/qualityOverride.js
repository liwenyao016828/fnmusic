/**
 * 「下载音质」开关的档位映射 —— 后端 pkg/config.QualityOverride 的**镜像**。
 *
 * 为什么两边各要一份：后端管「飞牛页面触发的下载」，本页管「网页端下载队列」，
 * 两边的取值表不一样（队列有 flac24bit，自动下载没有），但开关是**同一个**。
 * 改这边请同时改 pkg/config 的 QualityOverride 并跑两边的测试。
 *
 * 语义：开关是「盖住」而不是「改掉」各入口自己的音质设置 ——
 * 跟随 = 各用各的；无损 / 320k = 这一阵子统一用某一档，改回「跟随」还能复原。
 */

// 三个取值（与 config.DownloadQuality* 常量一一对应）
export const QUALITY_FOLLOW = ''
export const QUALITY_LOSSLESS = 'lossless'
export const QUALITY_MP3 = 'mp3_320'

/** 开关的三个选项（设置页直接渲染这个表） */
export const QUALITY_OVERRIDE_OPTIONS = [
  { value: QUALITY_FOLLOW, label: '跟随各入口自己的设置', desc: '下载队列与自动下载各用各的音质' },
  { value: QUALITY_LOSSLESS, label: '一律无损', desc: '点名 FLAC；拿不到就按原顺序兜底，不会变成下载失败' },
  { value: QUALITY_MP3, label: '一律 320K', desc: '点名 320K（本应用不转码，是「点名这一档」）' },
]

/**
 * 把开关套到某个入口自己的档位上。
 *
 * 认不出的覆盖值一律当「跟随」：宁可退化成各用各的，也不要因为一个错值
 * 把所有下载钉死在某一档。空白同样按跟随。
 */
export function qualityOverride(base, override) {
  switch (String(override || '').trim()) {
    case QUALITY_LOSSLESS:
      // 点名无损。两边的取值表里都有 flac 这一档。
      return 'flac'
    case QUALITY_MP3:
      return '320k'
    default:
      return String(base || '').trim()
  }
}
