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
  let offsetSec = 0
  const firstLine = (text.split('\n')[0] || '')
  const offsetMatch = /^\[offset:(-?\d+)\]/i.exec(firstLine)
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
