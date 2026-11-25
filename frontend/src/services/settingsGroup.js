import { ref } from 'vue'
import { loadPref, savePref } from './prefs'

/**
 * 设置分类（2026-11-25 用户口径：参照 magpie 的样式，从左下角弹出一个框选分类，
 * 「页面只负责显示对应的设置」）。
 *
 * 分类清单放在这里而不是组件里，是因为**两处**都要用：
 *   · `App.vue` 的侧栏左下角弹框（列出分类供选择）
 *   · `AppSettings.vue`（只渲染当前分类下的设置块）
 * 用一个模块级 ref 串起来，省掉事件/注入那一层。
 *
 * 「日志」不是设置块，它是 system 页的另一段（历史上有 navigateTo('logs') 的通知会跳过来），
 * 所以这里只登记它的名字与图标，点它走 navigateTo('logs')，不参与 inGroup 过滤。
 */
export const SETTINGS_GROUPS = [
  { id: 'general', name: '常规' },
  { id: 'download', name: '下载' },
  { id: 'files', name: '命名与文件' },
  { id: 'storage', name: '存储' },
  { id: 'integration', name: '集成' },
  { id: 'logs', name: '日志' },
  { id: 'about', name: '关于' },
]

const PREF_KEY = 'settings_group'

function isGroup(id) {
  return SETTINGS_GROUPS.some((g) => g.id === id)
}

const saved = loadPref(PREF_KEY, '')

/** 当前设置分类（只认登记过的 id，偏好丢了就回到第一类）。 */
export const settingsGroup = ref(isGroup(saved) ? saved : SETTINGS_GROUPS[0].id)

export function setSettingsGroup(id) {
  if (!isGroup(id)) return
  settingsGroup.value = id
  savePref(PREF_KEY, id)
}