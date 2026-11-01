/**
 * trimHost 的回归测试 —— 只钉「能不能用宿主选择器」这一个判据。
 *
 * 跑法：cd frontend && node --test src/services/trimHost.test.mjs
 *
 * 为什么值得单测：2026-10-31 真机上「授权目录」按钮点了没反应，根因就是判据错了 ——
 * 当时用 `getHostCapabilities().runtime !== 'unknown'`，而飞牛门户父窗口注入给
 * iframe 的 config 里根本没有 `host` 字段，于是 runtime 恒为 unknown、判据恒为
 * false、按钮永不渲染。后来改成看桥接方法表（`osConnector.methods.pickSharedFile`
 * 是不是函数）。这个判据判错一次，整条授权链路就哑了，所以钉死它。
 */
import assert from 'node:assert/strict'
import test from 'node:test'

import { bridgeSupportsPicker, hasTrimPicker, mobileBridgeReady, trimBridgeReady, trimProbed } from './trimHost.js'

test('桥接里有 pickSharedFile 才算可用', () => {
  assert.equal(bridgeSupportsPicker({ pickSharedFile: () => {} }), true)
})

test('桥接里没有 pickSharedFile 就不能算可用（真机上就是这个判据救了场）', () => {
  assert.equal(bridgeSupportsPicker({}), false)
  assert.equal(bridgeSupportsPicker({ pickSharedFile: 'pick-shared-file' }), false)
  assert.equal(bridgeSupportsPicker({ pickUserFile: () => {} }), false)
})

test('桥接整体缺失时是 false，不能抛异常', () => {
  assert.equal(bridgeSupportsPicker(null), false)
  assert.equal(bridgeSupportsPicker(undefined), false)
})

test('方法名可指定（排障时能直接问别的方法在不在）', () => {
  const methods = { openAppSetting: () => {} }
  assert.equal(bridgeSupportsPicker(methods, 'openAppSetting'), true)
  assert.equal(bridgeSupportsPicker(methods, 'pickSharedFile'), false)
})

test('hasTrimPicker 要同时满足「探过了」和「桥接可用」', () => {
  const probed = trimProbed.value
  const ready = trimBridgeReady.value
  try {
    trimProbed.value = false
    trimBridgeReady.value = true
    assert.equal(hasTrimPicker(), false, '还没探完不能算可用')

    trimProbed.value = true
    trimBridgeReady.value = false
    assert.equal(hasTrimPicker(), false, '探完但没有桥接不能算可用')

    trimProbed.value = true
    trimBridgeReady.value = true
    assert.equal(hasTrimPicker(), true)
  } finally {
    trimProbed.value = probed
    trimBridgeReady.value = ready
  }
})
/*
 * 手机端（飞牛 App / Flutter WebView）为什么要单独一条判据：
 * 用户 2026-10-31 追问「我在飞牛 app 里打开应用，为什么就没出现飞牛选择器」。
 * 手机端 SDK 不建 osConnector（getHostCapabilities() 直接返回 {runtime:'mobile'}），
 * 能力挂在 Flutter 注入的 flutter_inappwebview 上；只看 osConnector.methods 就会
 * 把手机端误判成「没有宿主」，按钮点了只会说「不在飞牛桌面里」。
 */
test('App/WebView 里靠 flutterInAppWebView 句柄判定可用', () => {
  assert.equal(mobileBridgeReady({ flutterInAppWebView: { callHandler: () => {} } }), true)
})

test('没拿到 WebView 句柄（纯浏览器 / 桌面 iframe）不算 App 通道', () => {
  assert.equal(mobileBridgeReady({ flutterInAppWebView: null }), false)
  assert.equal(mobileBridgeReady({}), false)
  assert.equal(mobileBridgeReady(null), false)
})
