/**
 * LRC 歌词解析 + 取「当前正在唱的那一行」。
 *
 * 2026-10-26 用户要求播放组件在原来进度条的位置显示歌词（单行），因此底栏也需要
 * 能算出当前行。此前这份解析只在 ImmersivePlayer.vue 里内联了一份，这里抽出来共用
 * （ImmersivePlayer 暂不动，避免影响沉浸式页的逐行滚动 / 字号 / 偏移逻辑）。
 *
 * @param {string} text 原始 LRC 文本
 * @returns {{ time: number, text: string }[]} 按时间升序
 */
export function parseLyric(text) {
  if (!text) return []
  // ⚠️ offset 标签不一定在第一行：实测 LRC（如「夜曲」）把它放在第 9 行
  //    （[id:]/[ar:]/[ti:]/[by:]/[hash:]/[al:]/[sign:]/[qq:]/[total:] 之后）。
  //    只在首行找 → offset 被忽略 → 整首歌的歌词比声音早/晚 N 秒，就是「对不上」。
  //    与 ImmersivePlayer 同口径：全文搜第一处 offset。
  let offsetSec = 0
  const offsetMatch = /\[offset:\s*(-?\d+)\]/i.exec(text)
  if (offsetMatch) offsetSec = parseInt(offsetMatch[1], 10) / 1000

  const out = []
  for (const line of text.split('\n')) {
    if (/^\[offset:/i.test(line) || /^\[(ti|ar|al|by|hash):/i.test(line)) continue
    const tagRe = /\[(\d{1,2}):(\d{2})(?:\.(\d{1,3}))?\]/g
    const raw = line.replace(/\[\d{1,2}:\d{2}(?:\.\d{1,3})?\]/g, '').trim()
    if (!raw) continue
    let m
    while ((m = tagRe.exec(line)) !== null) {
      const min = parseInt(m[1], 10)
      const sec = parseInt(m[2], 10)
      const msStr = (m[3] || '0').padEnd(3, '0').slice(0, 3)
      out.push({
        time: Math.max(0, min * 60 + sec + parseInt(msStr, 10) / 1000 - offsetSec),
        text: raw,
      })
    }
  }
  return out.sort((a, b) => a.time - b.time)
}

/**
 * 当前时间点正在唱的那一行歌词。
 * 与 ImmersivePlayer 的 currentLineIndex 同口径：从最后一行往前找第一个
 * `time <= t` 的行；没有命中就返回第一行。
 * @param {string} text 原始 LRC
 * @param {number} time 播放进度（秒）
 * @returns {string} 歌词文本；没有歌词时返回 ''
 */
export function currentLyricLine(text, time) {
  const lines = parseLyric(text)
  if (!lines.length) return ''
  const t = time || 0
  for (let i = lines.length - 1; i >= 0; i--) {
    if (t >= lines[i].time) return lines[i].text
  }
  return lines[0].text
}

/**
 * 按偏移量整体平移 LRC 时间轴（不动歌词文本）。
 *
 * 2026-10-28 用户要求「调整歌词时间段」：很多人手里的 .lrc 整份早/晚几百毫秒，
 * 逐行改不现实，这里整份平移。写回文件时用平移后的结果，所以它必须和
 * parseLyric 用同一套时间格式（[mm:ss.xxx]）。
 *
 * @param {string} text 原始 LRC
 * @param {number} offsetSec 秒，正数=整体延后（歌词更晚出现），负数=提前
 * @returns {string} 平移后的 LRC；没有偏移或没有歌词时原样返回
 */
export function offsetLyric(text, offsetSec) {
  if (!text || !offsetSec) return text || ''
  const delta = Math.round(offsetSec * 1000)
  return text.replace(/\[(\d{1,2}):(\d{2})(?:\.(\d{1,3}))?\]/g, (m, mm, ss, mss) => {
    // 毫秒位补零到 3 位再转数字：`.5` 是 500ms 而不是 5ms
    const msStr = String(mss || '0').padEnd(3, '0').slice(0, 3)
    let total = parseInt(mm, 10) * 60000 + parseInt(ss, 10) * 1000 + parseInt(msStr, 10) + delta
    // 平移到 0 之前就贴 0 —— 时间戳不能为负，播放器会直接丢这一行
    if (total < 0) total = 0
    const m2 = Math.floor(total / 60000)
    const s2 = Math.floor((total % 60000) / 1000)
    const ms2 = total % 1000
    return `[${String(m2).padStart(2, '0')}:${String(s2).padStart(2, '0')}.${String(ms2).padStart(3, '0')}]`
  })
}
