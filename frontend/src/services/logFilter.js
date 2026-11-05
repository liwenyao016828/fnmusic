/**
 * 日志筛选（纯函数，无 Vue 依赖）。
 *
 * 之前「运行日志」这个 tab 没有任何筛选手段：缓冲里最多 2000 条，靠肉眼翻找
 * 一条失败记录基本不可能。级别与来源字段后端早就返回了（applog.Entry.Level /
 * Source），缺的只是这里的判断逻辑与界面上的几个控件。
 *
 * 把规则抽成纯函数的理由与 services/downloadMerge.js 一致：筛选是最容易写出
 * 「大小写不一致」「空条件把全部过滤没了」这类小 bug 的地方，必须能直接跑测试。
 */

export const LEVEL_ALL = 'all'
export const SOURCE_ALL = 'all'

/** 级别下拉的可选项（值即后端 Entry.Level 的取值）。 */
export const LEVEL_OPTIONS = [
  { value: LEVEL_ALL, label: '全部级别' },
  { value: 'error', label: '仅错误' },
  { value: 'warn', label: '仅警告' },
  { value: 'info', label: '仅信息' },
]

/**
 * 归一化搜索词：去首尾空白、转小写。
 * @param {*} q
 * @returns {string}
 */
export function normalizeQuery(q) {
  return String(q ?? '').trim().toLocaleLowerCase()
}

/**
 * 若干段文本里是否包含搜索词（大小写不敏感）。空搜索词恒为真。
 * @param {string} query 已归一化或未归一化均可
 * @param {...*} parts 参与匹配的文本，null/undefined 会被跳过
 * @returns {boolean}
 */
export function matchesQuery(query, ...parts) {
  const q = normalizeQuery(query)
  if (!q) return true
  return parts
    .filter((p) => p !== null && p !== undefined && p !== '')
    .join(' ')
    .toLocaleLowerCase()
    .includes(q)
}

/**
 * 级别过滤。`'all'` 放行全部；未知过滤值按 `'all'` 处理（宁可显示，不要误隐藏）。
 * @param {*} level 条目级别
 * @param {string} filter
 * @returns {boolean}
 */
export function levelMatches(level, filter) {
  if (!filter || filter === LEVEL_ALL) return true
  return String(level || 'info') === filter
}

/**
 * 来源过滤。`'all'` 放行全部；条目没有 source 时按 backend 计（后端 Add 的默认值）。
 * @param {*} source
 * @param {string} filter
 * @returns {boolean}
 */
export function sourceMatches(source, filter) {
  if (!filter || filter === SOURCE_ALL) return true
  return String(source || 'backend') === filter
}

/**
 * 运行日志条目的组合筛选：级别 + 来源 + 「只看失败」+ 搜索词，条件之间是 AND。
 *
 * 「只看失败」是 task / local 两个 tab 原有的开关，与级别下拉取交集：
 * 勾了只看失败又选「仅警告」结果必然为空，这是符合直觉的严格语义，不做隐式放宽。
 *
 * @param {{level?:string, source?:string, message?:string, time?:string, at?:string}} e
 * @param {{level?:string, source?:string, onlyFailed?:boolean, query?:string}} [opts]
 * @returns {boolean}
 */
export function serverEntryMatches(e, opts = {}) {
  if (!e) return false
  const { level = LEVEL_ALL, source = SOURCE_ALL, onlyFailed = false, query = '' } = opts
  if (onlyFailed && e.level !== 'error') return false
  return levelMatches(e.level, level)
    && sourceMatches(e.source, source)
    && matchesQuery(query, e.message, e.source, e.level, e.time, e.at)
}

/**
 * 从当前已加载的条目里收集来源选项，供下拉框使用。
 * 只反映「缓冲区里真实出现过的来源」——后端还有 api / nas / library / downloader
 * 等来源（pkg/api、pkg/library、pkg/downloader 里的 AddFrom 调用点），
 * 写死选项会出现「选了一个永远不存在的来源」。
 * @param {Array<{source?:string}>} entries
 * @returns {Array<{value:string, label:string}>} 以「全部来源」开头
 */
export function collectSources(entries) {
  const seen = new Set()
  for (const e of entries || []) {
    if (e && e.source) seen.add(String(e.source))
  }
  const rest = [...seen].sort((a, b) => (a === 'backend' ? -1 : b === 'backend' ? 1 : a.localeCompare(b)))
  return [{ value: SOURCE_ALL, label: '全部来源' }, ...rest.map((s) => ({ value: s, label: sourceText(s) }))]
}

/**
 * 来源的显示名。backend / frontend 沿用界面原有的「后端 / 前端」，
 * 其余来源（api / nas / library / downloader…）直接显示原始标识，
 * 不做臆测翻译 —— 否则筛选值与导出包里的 source 列对不上。
 * @param {*} s
 * @returns {string}
 */
export function sourceText(s) {
  if (s === 'frontend') return '前端'
  if (s === 'backend') return '后端'
  return String(s || '后端')
}
