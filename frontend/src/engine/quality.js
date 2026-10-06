/**
 * 音质阶梯（对齐觅音 miyin 的 highest 轮询降级策略）
 *
 * 取链顺序：从最高音质档逐级降级，每一档轮询全部可用音源，
 * 全部失败后再降下一档，直到拿到可用直链。
 */

// 音质从高到低（highest 模式的降级阶梯）
export const QUALITY_LADDER = ['flac24bit', 'flac', '320k', '192k', '128k']

// 展示用标签
export const QUALITY_LABELS = {
  flac24bit: 'FLAC 24bit',
  flac: 'FLAC 无损',
  hires: 'Hi-Res',
  '320k': '320K',
  '192k': '192K',
  '128k': '128K',
}

// 「最高音质」的语义：留空或显式 highest 都表示自动轮询最高
export function isHighestQuality(pref) {
  const s = String(pref == null ? '' : pref).trim().toLowerCase()
  return !s || s === 'highest'
}

/**
 * 规范化音质字符串。
 * 搜索结果中的音质可能是大写（"320K" / "FLAC"），而音源脚本约定小写，
 * 统一转小写避免传给脚本后无法识别。
 */
export function normalizeQuality(pref) {
  const s = String(pref == null ? '' : pref).trim().toLowerCase()
  if (!s || s === 'highest') return 'highest'
  return s
}

export function qualityLabel(q) {
  if (isHighestQuality(q)) return '最高音质'
  return QUALITY_LABELS[normalizeQuality(q)] || String(q || '').toUpperCase()
}

/**
 * 依据各音源「宣称支持」的音质并集，生成实际要尝试的音质档位列表。
 *
 * - 固定音质：只返回该档
 * - highest：阶梯 ∩ 并集，按阶梯顺序；并集中不在阶梯内的自定义音质追加到末尾兜底
 *
 * @param {string} preferred 期望音质，'highest' 表示自动
 * @param {string[][]} availableLists 各音源宣称支持的音质数组
 * @returns {string[]}
 */
export function buildQualityTiers(preferred, availableLists = []) {
  const pref = normalizeQuality(preferred)
  if (!isHighestQuality(pref)) return [pref]

  const union = new Set()
  for (const list of availableLists) {
    for (const q of list || []) {
      const norm = normalizeQuality(q)
      if (norm !== 'highest') union.add(norm)
    }
  }

  const tiers = QUALITY_LADDER.filter(q => union.has(q))
  // 兜底：音源宣称了非标准档位（如 hires），排在标准阶梯之后仍会被尝试
  for (const q of union) {
    if (!QUALITY_LADDER.includes(q)) tiers.push(q)
  }

  if (tiers.length) return tiers
  // 音源未上报 qualitys 时的保底阶梯
  return ['flac', '320k', '128k']
}

/**
 * 从直链 URL 粗略推断扩展名（后端落盘时还会用魔数嗅探纠正）。
 */
export function guessExtFromUrl(url, quality) {
  const u = String(url || '').toLowerCase()
  if (/\.flac(\?|$)/.test(u)) return 'flac'
  if (/\.(mp3)(\?|$)/.test(u)) return 'mp3'
  if (/\.(m4a|mp4|aac)(\?|$)/.test(u)) return 'm4a'
  if (/\.(ogg|oga)(\?|$)/.test(u)) return 'ogg'
  if (/\.(wav)(\?|$)/.test(u)) return 'wav'

  const q = normalizeQuality(quality)
  if (q.startsWith('flac')) return 'flac'
  if (q === '128k' || q === '192k' || q === '320k' || q === 'hires') return 'mp3'
  return 'mp3'
}

// ── 「请求无损却拿到有损」的识别 ──
//
// 背景：**有些音源在请求无损时会返回有损直链**。文件本身不算错
// （后端按魔数给扩展名，内容与扩展名一致，能正常播放），但用户以为拿到的是 FLAC。
// 这里提供两个方向的判定：从「请求的档位」和「后端回报的真实容器」。

/** 无损档位（用户请求这些档位时，才需要关心是否真无损） */
export const LOSSLESS_TIERS = new Set(['flac', 'flac24bit', 'hires'])

/**
 * 有损容器。
 * 后端 `actual_format` 的取值域是 flac / mp3 / ogg / ape / dsf / dff / wav / m4a。
 * 其中 wav / ape / dsf / dff 都算无损（未压缩或无损压缩），只有这几个是有损。
 */
export const LOSSY_CONTAINERS = new Set(['mp3', 'm4a', 'aac', 'ogg', 'wma'])

/** 直链里出现这些片段即可判定为有损（`.php` 是防盗链中转的典型特征） */
const LOSSY_URL_HINTS = ['.mp3', '.php', 'format=mp3', 'format=128', 'format=320']

/** 请求的音质档位是否属于无损 */
export function isLosslessTier(q) {
  return LOSSLESS_TIERS.has(normalizeQuality(q))
}

/** 后端嗅探出的容器是否属于有损 */
export function isLossyContainer(fmt) {
  return LOSSY_CONTAINERS.has(String(fmt == null ? '' : fmt).trim().toLowerCase())
}

/**
 * 直链是否**明确**看得出有损。
 *
 * 刻意只做「显式有损」的排除，**不要求显式无损**：
 * 大量音源的直链是 CDN 不透明路径（`/api/v1/xxx?token=...`），根本看不出格式。
 * 若照搬上游「必须含 `.flac`」的严格校验，会把这类合法无损全部误杀 —— 比漏判更糟。
 * 所以：明确的 `.mp3` / `.php` 判为有损（继续找别的平台）；看不出来则放行，
 * 最终由后端 `actual_format`（魔数嗅探）给出权威结论。
 */
export function urlLooksLossy(url) {
  const u = String(url == null ? '' : url).toLowerCase()
  if (!u) return false
  return LOSSY_URL_HINTS.some(h => u.includes(h))
}
