// 本地列表的展示分页：只切展示窗口，不截断原始曲目队列。
// 播放全部、批量下载和订阅仍使用完整 activeSongList。
export const DISPLAY_PAGE_SIZES = [12, 24, 48, 96]

export function normalizePageSize(value, fallback = 24) {
  const n = Number(value)
  return DISPLAY_PAGE_SIZES.includes(n) ? n : fallback
}

export function clampPage(page, total, pageSize) {
  const size = normalizePageSize(pageSize)
  const pages = Math.max(1, Math.ceil(Math.max(0, Number(total) || 0) / size))
  const n = Math.floor(Number(page) || 1)
  return Math.min(pages, Math.max(1, n))
}

export function displayPage(list, page, pageSize) {
  const size = normalizePageSize(pageSize)
  const p = clampPage(page, Array.isArray(list) ? list.length : 0, size)
  return (Array.isArray(list) ? list : []).slice((p - 1) * size, p * size)
}

export function pageLabel(page, total, pageSize) {
  const size = normalizePageSize(pageSize)
  return `第 ${clampPage(page, total, size)} / ${Math.max(1, Math.ceil((Number(total) || 0) / size))} 页 · 共 ${Number(total) || 0} 项`
}

// LRC：保留同一时刻多行、[offset]、翻译，并按时间排序；元数据不当歌词展示。
export function parseDisplayLyrics(raw = '', translations = '') {
  const offsetMatch = String(raw).match(/\[offset:\s*(-?\d+)\]/i)
  const offset = offsetMatch ? Number(offsetMatch[1]) / 1000 : 0
  function lines(source) {
    const out = []
    for (const line of String(source || '').split('\n')) {
      const text = line.replace(/\[\d{1,2}:\d{2}(?:\.\d{1,3})?\]/g, '').trim()
      if (!text) continue
      const re = /\[(\d{1,2}):(\d{2})(?:\.(\d{1,3}))?\]/g
      let m
      while ((m = re.exec(line))) {
        const time = Math.max(0, Number(m[1]) * 60 + Number(m[2]) + Number((m[3] || '0').padEnd(3, '0')) / 1000 - offset)
        out.push({ time, text })
      }
    }
    return out
  }
  const trans = new Map(lines(translations).map((l) => [l.time.toFixed(1), l.text]))
  return lines(raw).sort((a, b) => a.time - b.time).map((l) => ({ ...l, trans: trans.get(l.time.toFixed(1)) || '' }))
}

export function activeLyricIndex(lines, currentTime) {
  if (!Array.isArray(lines) || !lines.length) return -1
  let low = 0
  let high = lines.length - 1
  let found = 0
  while (low <= high) {
    const mid = (low + high) >> 1
    if (lines[mid].time <= Number(currentTime || 0)) { found = mid; low = mid + 1 } else high = mid - 1
  }
  return found
}
