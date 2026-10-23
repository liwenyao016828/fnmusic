// 下载页「按序号快速多选」的解析逻辑（纯函数，可单测）。
//
// 为什么单独抽出来：勾选上百行只能一格一格点，用户的口径是
// 「输入 100 就是 0-100，也可以输入 11-200」。序号边界怎么夹、
// 「前 N 首」和「a-b」怎么区分、输入不合法怎么回话 —— 混在组件里没法测，
// 所以规则放这里，用 rangeSelect.test.mjs 钉住。
//
// 口径（三处列表共用：下载队列 / 歌单曲目 / 自动下载记录）：
//   · 行号一律 **1 基**，就是界面上那一列 `#` 显示的数字。
//   · 单独一个数 `N` = 「前 N 首」= 1..N。
//   · `a-b` = a 到 b（含两端）。写成 `0-100` 也认：0 没有对应的行，
//     按用户的意思就是从头开始，所以夹成 1..100，不报错。
//   · 支持逗号分隔的多段：`1-10, 80, 150-160` 会取并集。
//   · 超出当前行数的部分**夹到实际行数**而不是报错 —— 队列会边下边变短，
//     严格判界会让人以为功能坏了。
//   · 全夹完一行都没有（比如列表空了，或 `500-600` 而只有 3 行）才给错误提示。

/**
 * 解析输入，返回命中的 1 基行号集合。
 *
 * @param {string} input   用户输入（`100` / `11-200` / `0-100` / `1-10,80`）
 * @param {number} total   当前列表的行数
 * @returns {{ ok: boolean, indexes: number[], error: string, from: number, to: number }}
 *          indexes 升序；from/to 是夹过界的实际生效区间（多段时取最小/最大，仅用于回话）
 */
export function parseRangeSpec(input, total) {
  const empty = { ok: false, indexes: [], error: '', from: 0, to: 0 }
  const n = Math.max(0, Number(total) || 0)
  if (!n) return { ...empty, error: '当前列表里没有可选的行' }

  const raw = String(input ?? '').trim()
  if (!raw) return { ...empty, error: '输入行号，例如 100 或 11-200' }

  // 先把区间两边的空格吸掉（`11 - 200` → `11-200`），否则下一步按空格分段
  // 会把「11 - 200」拆成三段。吸完之后空格就只剩「段与段之间」的意思了。
  const text = raw.replace(/\s*([-~到至])\s*/g, '$1')

  // 中文逗号/顿号/空格也当分隔符：手机输入法很容易打出「1，10」
  const parts = text.split(/[,，、;；\s]+/).filter(Boolean)
  const picked = new Set()
  let low = Infinity
  let high = -Infinity

  for (const part of parts) {
    // 区间：a-b（也接受 a~b / a到b）
    const rangeMatch = part.match(/^(\d+)\s*[-~到至]\s*(\d+)$/)
    if (rangeMatch) {
      const a = Number(rangeMatch[1])
      const b = Number(rangeMatch[2])
      // 反着写（200-11）按用户的意思就是这一段，不较真方向
      const from = Math.min(a, b)
      const to = Math.max(a, b)
      // 0 起算 = 从第一行开始
      const start = Math.max(1, from)
      const end = Math.min(n, to)
      if (start > end) continue // 整段都在列表外，跳过
      for (let i = start; i <= end; i++) picked.add(i)
      low = Math.min(low, start)
      high = Math.max(high, end)
      continue
    }

    // 单个行号
    if (/^\d+$/.test(part)) {
      const v = Number(part)
      if (v < 1) continue // 0 单独出现没有意义
      // 「100」= 前 100 首；已经在多段里逐个点也成立（1-10,80 → 第 80 行）
      const end = Math.min(n, v)
      for (let i = 1; i <= end; i++) picked.add(i)
      low = Math.min(low, 1)
      high = Math.max(high, end)
      continue
    }

    return { ...empty, error: `看不懂「${part}」—— 可以输入 100（前 100 首）或 11-200（第 11 到 200 首）` }
  }

  if (!picked.size) {
    return { ...empty, error: `没有命中任何行（当前共 ${n} 行）` }
  }

  const indexes = Array.from(picked).sort((a, b) => a - b)
  return { ok: true, indexes, error: '', from: low, to: high }
}

/**
 * 按行号集合挑出对应的条目。
 * @param {Array} list 当前**可见**列表（已按筛选/分页顺序）
 * @param {number[]} indexes 1 基行号
 */
export function pickByIndexes(list, indexes) {
  const arr = Array.isArray(list) ? list : []
  const out = []
  for (const i of indexes || []) {
    if (i >= 1 && i <= arr.length) out.push(arr[i - 1])
  }
  return out
}
