/**
 * 列表显示列数（card grid 每行几张卡）：纯逻辑模块。
 *
 * 只放选项表、归一化、Tailwind 类映射这些**无副作用**的东西 —— 本项目的测试是
 * `node --test` 跑纯 JS（见 viewCache / searchPaging 同款分工），凡是 import 了
 * prefs.js 的模块（它再 import api/client，无扩展名）在 node --test 下根本加载
 * 不起来，所以「读/写偏好」这一跳由组件自己做（AppSettings.vue 已有同款写法：
 * `loadPref('nas_page_size')` / `savePref('nas_page_size', …)`）。
 *
 * 为什么要单独抽一个服务（而不是写在各页面里）：Tailwind 是**扫描源码里的字面量**
 * 生成工具类的。`'grid-cols-' + n` 这种拼接运行时才成形，扫描器看不见；
 * `grid-cols-6` 这类别处没出现过的类就不会被产出 —— 表现就是「选了 6 列，
 * 界面毫无反应」。所以下面把每档的类串**完整写成字面量**，消费者拼进 :class
 * 即可，不要再自己拼数字。
 *
 * 偏好键 `list_columns`（prefs.js 自动加 fn_ 前缀），存字符串值，口径与
 * `nas_page_size` / `chart_page_size` 一致。
 */

/** 偏好键（配合 prefs.js 的 loadPref / savePref 使用） */
export const LIST_COLUMNS_KEY = 'list_columns'

/** 没设置时的默认值：0 = 自动（沿用各网格原本的响应式列数） */
export const DEFAULT_COLUMNS = 0

/**
 * 可选档位。
 *
 * 「自动」= 不干预，各网格用自己的响应式（窄屏 2 张、宽屏最多 5~6 张）。
 * 选定具体数字后**所有宽度都按这个数字排**：用户是明确挑了「一行 4 个」，
 * 再让它在手机上偷偷变成 2 个就不是他的选择；嫌挤可以切回「自动」。
 */
export const COLUMN_OPTIONS = [
  { value: 0, label: '自动', desc: '按屏幕宽度自动：窄屏 2 列，宽屏最多 5 列' },
  { value: 2, label: '2 列', desc: '一行 2 张，卡片最大、最好点' },
  { value: 3, label: '3 列', desc: '一行 3 张' },
  { value: 4, label: '4 列', desc: '一行 4 张' },
  { value: 5, label: '5 列', desc: '一行 5 张' },
  { value: 6, label: '6 列', desc: '一行 6 张，一屏看得最多' },
]

/** 合法的固定列数 */
export const FIXED_COLUMNS = [2, 3, 4, 5, 6]

/**
 * 归一化：只认 0（自动）与 2~6 的整数，其余一律回落 fallback。
 * 偏好存的是字符串、可能被手改或被别的端写歪，读出来必须过这一层。
 *
 * @param {any} value
 * @param {number} [fallback=DEFAULT_COLUMNS]
 * @returns {number}
 */
export function normalizeColumns(value, fallback = DEFAULT_COLUMNS) {
  const n = Math.floor(Number(value))
  if (n === 0) return 0
  if (FIXED_COLUMNS.includes(n)) return n
  const fb = Math.floor(Number(fallback))
  return fb === 0 || FIXED_COLUMNS.includes(fb) ? fb : DEFAULT_COLUMNS
}

/**
 * 各档位的完整类串（**必须写成字面量**，见文件头关于 Tailwind 扫描的说明）。
 * xl 也钉上：NAS 网页端普遍是宽屏，只钉到 lg 的话超宽屏还是会被响应式带跑。
 */
const COLUMN_CLASS_BY_COUNT = {
  2: 'grid-cols-2 sm:grid-cols-2 md:grid-cols-2 lg:grid-cols-2 xl:grid-cols-2',
  3: 'grid-cols-3 sm:grid-cols-3 md:grid-cols-3 lg:grid-cols-3 xl:grid-cols-3',
  4: 'grid-cols-4 sm:grid-cols-4 md:grid-cols-4 lg:grid-cols-4 xl:grid-cols-4',
  5: 'grid-cols-5 sm:grid-cols-5 md:grid-cols-5 lg:grid-cols-5 xl:grid-cols-5',
  6: 'grid-cols-6 sm:grid-cols-6 md:grid-cols-6 lg:grid-cols-6 xl:grid-cols-6',
}

/**
 * 网格容器的列数类串。
 *
 * @param {number} columns 归一化后的列数（0 = 自动）
 * @param {string} autoClasses 「自动」时该网格原本的响应式类串（各页原来的写法）
 * @returns {string} 交给 :class 使用的类串
 */
export function listColumnClass(columns, autoClasses) {
  const n = normalizeColumns(columns, DEFAULT_COLUMNS)
  if (n === 0) return autoClasses || ''
  return COLUMN_CLASS_BY_COUNT[n]
}

/** 摘要文案（设置页折叠条上用） */
export function columnSummary(columns) {
  const n = normalizeColumns(columns, DEFAULT_COLUMNS)
  if (n === 0) return '自动（按屏幕宽度）'
  return `每行 ${n} 列`
}
