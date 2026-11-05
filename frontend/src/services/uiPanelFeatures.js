/** 飞牛音乐官方页面注入面板的功能设置，与后端 config.ui_panel.go 对齐。 */
export const UI_PANEL_FEATURES = Object.freeze([
  { key: 'quality', configKey: 'ui_panel_quality_enabled', label: '试听音质', desc: '在面板中切换飞牛音乐的试听音质。' },
  { key: 'assistant', configKey: 'ui_panel_assistant_enabled', label: 'AI 指令与功能开关', desc: '保留面板中的指令输入、结果卡片和快捷功能开关。' },
  { key: 'jump', configKey: 'ui_panel_jump_enabled', label: '打开曲率', desc: '保留从飞牛音乐跳转到曲率的按钮。' },
  { key: 'download', configKey: 'ui_panel_download_enabled', label: '歌曲下载入口', desc: '在飞牛音乐的歌曲菜单中显示“下载到曲率”。' },
])

/** 缺字段的旧后端 / 读取中沿用默认开启，避免升级时突然隐藏功能。 */
export function normalizeUIPanelFeatures(config = {}) {
  return UI_PANEL_FEATURES.map(feature => ({
    ...feature,
    enabled: config[feature.configKey] !== false,
  }))
}
