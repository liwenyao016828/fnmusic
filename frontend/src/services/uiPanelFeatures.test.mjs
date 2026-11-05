import test from 'node:test'
import assert from 'node:assert/strict'
import { UI_PANEL_FEATURES, normalizeUIPanelFeatures } from './uiPanelFeatures.js'
import { readFileSync } from 'node:fs'

const settingsSource = readFileSync(new URL('../components/AppSettings.vue', import.meta.url), 'utf8')

test('注入面板设置只包含真实面板功能，旧配置默认全开', () => {
  const rows = normalizeUIPanelFeatures()
  assert.deepEqual(rows.map(x => x.key), ['quality', 'assistant', 'jump', 'download'])
  assert.ok(rows.every(x => x.enabled))
  assert.equal(rows.some(x => /执行模式|能力开关/.test(x.label)), false)
})

test('只关闭明确配置为 false 的功能，并保留其他设置', () => {
  const rows = normalizeUIPanelFeatures({ ui_panel_assistant_enabled: false, ui_panel_quality_enabled: true })
  assert.deepEqual(rows.map(x => x.enabled), [true, false, true, true])
  assert.equal(rows.find(x => x.key === 'assistant').configKey, 'ui_panel_assistant_enabled')
  assert.equal(UI_PANEL_FEATURES.length, 4)
})

test('系统页连的是飞牛注入面板，而不是曲率自己的助手设置', () => {
  assert.ok(settingsSource.includes('title="飞牛音乐 AI 面板"'))
  assert.ok(settingsSource.includes('setUIInject(!injectedPanelEnabled.value)'))
  assert.ok(!settingsSource.includes('AssistantAPI'))
  assert.ok(!settingsSource.includes('助手执行模式'))
  assert.ok(settingsSource.includes('chartInject.error.value'), '传给提示组件的 ref 必须显式取 value，否则会变成 [object Object]')
})
