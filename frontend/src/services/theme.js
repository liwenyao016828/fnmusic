/**
 * 主题色（强调色）管理
 *
 * 2026-10-29 新增。四套：蓝（默认）/ 青玉绿 / 樱粉 / 石墨灰。
 *
 * 为什么单独一个文件：
 *   主题不是「某个组件的状态」，而是**整站的强调色**。按钮、图标、描边、
 *   进度条、选中行底全都引用 --ys-accent-* 变量，所以这里只需要做一件事：
 *   把主题 id 写到 html 的 data-theme 上，剩下的交给 CSS 变量。
 *
 * 持久化复用 services/prefs.js：
 *   loadPref/savePref 会先写内存 + localStorage，再防抖 POST /api/ui-prefs，
 *   服务端文件优先 → 多端共享。所以切一次主题，别的设备刷新也能拿到。
 *
 * 口径：主题 id 只有这四个，别的值一律回落到 'blue'（防止手改 localStorage
 * 写出一个 html[data-theme=xxx] 不存在的属性，导致强调色全丢）。
 */
import { loadPref, savePref } from './prefs.js'

/** 主题清单：id / 显示名 / 强调色 hex（只用于设置页画色块，不参与计算） */
export const THEMES = [
  { id: 'blue', label: '钴蓝', hint: '默认', color: '#0066FF' },
  { id: 'green', label: '青玉', hint: '改版前的老品牌色', color: '#0B8A66' },
  { id: 'pink', label: '樱粉', hint: '', color: '#E8387B' },
  { id: 'gray', label: '石墨灰', hint: '中性，不抢眼', color: '#737373' },
]

export const DEFAULT_THEME = 'blue'

const PREF_KEY = 'theme'

/** 合法化：不在清单里的一律回落到默认主题 */
export function normalizeTheme(id) {
  const key = String(id || '').trim().toLowerCase()
  return THEMES.some((t) => t.id === key) ? key : DEFAULT_THEME
}

/**
 * 把主题应用到 <html>。
 * 只设 data-theme —— 强调色、提示条底、聚焦环、深色面上的高亮全部由 CSS 变量派生。
 */
export function applyTheme(id) {
  const theme = normalizeTheme(id)
  const root = document.documentElement
  root.setAttribute('data-theme', theme)
  // 主题是暗色选择器在某些浏览器下的渲染依据（粉色/灰色会改变原生控件观感）
  root.style.colorScheme = 'light'
  return theme
}

/** 读取已保存的主题（同步：prefs.js 在模块加载时就已 seedFromLocal） */
export function loadTheme() {
  try {
    return normalizeTheme(loadPref(PREF_KEY, DEFAULT_THEME))
  } catch {
    return DEFAULT_THEME
  }
}

/** 保存并立即应用 */
export function setTheme(id) {
  const theme = normalizeTheme(id)
  applyTheme(theme)
  try {
    savePref(PREF_KEY, theme)
  } catch { /* 持久化失败不影响本次切换 */ }
  return theme
}
