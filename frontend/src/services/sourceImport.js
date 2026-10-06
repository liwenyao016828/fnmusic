// 导入音源时「挑文件」的那点逻辑 —— 点选 / 拖拽 / 从 NAS 选，三条入口共用一份。
//
// 为什么单独搬出来：拖拽是**最容易静默失败**的交互。
// 拖进来一个 .txt、一张截图、或者整个文件夹，如果只是「什么也没发生」，
// 用户会以为拖成功了，然后抱怨「导入没反应」。所以「收哪些、拒哪些、拒了怎么说话、
// 上限多少」必须是可测的纯函数，组件只管渲染。
//
// 上限与后端 `pkg/sources/batch.go` 的 `maxBatchItems`（50）保持一致 ——
// 前端先拦一道，别等提交后才拿到 400。

/** 认得的脚本扩展名（后端不校验扩展名，这里只用来挡明显的误拖） */
export const SCRIPT_EXT_RE = /\.(js|mjs|cjs)$/i

/** 单次导入条数上限，与后端 `maxBatchItems` 对齐 */
export const MAX_IMPORT_ITEMS = 50

/** 文件名看起来是不是音源脚本 */
export function isScriptFile(name) {
  return typeof name === 'string' && SCRIPT_EXT_RE.test(name.trim())
}

/**
 * 把一批文件名分成「要的」和「不要的」，保持原顺序。
 * 空名字（拖文件夹时常见）算拒绝项，但**不重复计数**，避免一次拖 3 个文件夹刷出 3 条噪音。
 */
export function splitScriptNames(names = []) {
  const scripts = []
  const rejected = []
  const seen = new Set()
  for (const raw of names) {
    const name = typeof raw === 'string' ? raw.trim() : ''
    if (!name) {
      if (!seen.has('')) {
        seen.add('')
        rejected.push('（文件夹或空文件）')
      }
      continue
    }
    if (isScriptFile(name)) scripts.push(name)
    else if (!seen.has(name)) {
      seen.add(name)
      rejected.push(name)
    }
  }
  return { scripts, rejected }
}

/** 被拒文件的人话说明；没有拒绝项就返回空串（模板里直接 v-if） */
export function rejectedText(rejected = [], max = 3) {
  if (!rejected.length) return ''
  const head = rejected.slice(0, max).join('、')
  const more = rejected.length > max ? ` 等 ${rejected.length} 个` : ''
  return `已忽略 ${rejected.length} 个非脚本文件：${head}${more}`
}

/** 超出上限时的提示；没超返回空串 */
export function overflowText(total = 0) {
  if (total <= MAX_IMPORT_ITEMS) return ''
  return `一次最多导入 ${MAX_IMPORT_ITEMS} 条，当前已选 ${total} 条，请先移除一些`
}

/**
 * 从拖进来的文本里捞脚本直链 —— 从浏览器标签页拖一条链接过来是很自然的动作。
 * 只认 http(s)，去掉句尾常见的标点（中文标点也去），并按出现顺序去重。
 */
export function urlsFromText(text = '') {
  if (typeof text !== 'string' || !text) return []
  const out = []
  const seen = new Set()
  // 只吃 URL 合法字符集：中文是「非空白」但不是 URL 字符，
  // 用 `[^\s]+` 会把「https://a.com/x.js。还有」整段吞进去。
  const re = /https?:\/\/[A-Za-z0-9\-._~:/?#[\]@!&$'()*+,;=%]+/gi
  let m
  while ((m = re.exec(text)) !== null) {
    const url = m[0].replace(/[),.;:，。；：、）】》]+$/u, '')
    if (!url || seen.has(url)) continue
    seen.add(url)
    out.push(url)
  }
  return out
}

/**
 * 合并已选文件：**同名以新来的为准**（重选同一个文件就是想覆盖它），
 * 已有项保持原位置，新项追加在后面。`existing` / `incoming` 里的项至少要有 `filename`。
 */
export function mergePicked(existing = [], incoming = []) {
  const map = new Map()
  for (const it of Array.isArray(existing) ? existing : []) {
    if (it && it.filename) map.set(it.filename, it)
  }
  for (const it of Array.isArray(incoming) ? incoming : []) {
    if (it && it.filename) map.set(it.filename, it)
  }
  return [...map.values()]
}
