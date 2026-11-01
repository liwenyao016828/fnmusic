import test from 'node:test'
import assert from 'node:assert/strict'

import {
  QUALITY_FOLLOW,
  QUALITY_TOP_ONLY,
  QUALITY_AUTO_LOWER,
  LEGACY_QUALITY_LOSSLESS,
  LEGACY_QUALITY_MP3,
  QUALITY_MODE_OPTIONS,
  normalizeQualityMode,
  qualityModeTarget,
  qualityModeAllowsLower,
} from './qualityOverride.js'

// 2026-10-30 重构：两个控件（覆盖档位 + 降级开关）合并成一个三选一。
// 这张表必须与后端 pkg/config 的 NormalizeQualityMode/QualityModeTarget/
// QualityModeAllowsLower 完全同口径 —— 钉住三件事：归一化、点名档、降级开关。

test('normalizeQualityMode：三个合法值原样通过，其它一律跟随', () => {
  assert.equal(normalizeQualityMode(QUALITY_FOLLOW), '')
  assert.equal(normalizeQualityMode(QUALITY_TOP_ONLY), 'top_only')
  assert.equal(normalizeQualityMode(QUALITY_AUTO_LOWER), 'auto_lower')
  assert.equal(normalizeQualityMode(''), '')
  assert.equal(normalizeQualityMode('   '), '')
  assert.equal(normalizeQualityMode(undefined), '')
  assert.equal(normalizeQualityMode(null), '')
  assert.equal(normalizeQualityMode('乱写的'), '')
})

test('normalizeQualityMode：旧值读到的瞬间迁到新三选一', () => {
  assert.equal(normalizeQualityMode(LEGACY_QUALITY_LOSSLESS), QUALITY_TOP_ONLY)
  assert.equal(normalizeQualityMode(LEGACY_QUALITY_MP3), QUALITY_FOLLOW)
})

test('qualityModeTarget：非跟随模式点名最高档，跟随不动各入口自己的档位', () => {
  assert.equal(qualityModeTarget('highest', QUALITY_FOLLOW), 'highest')
  assert.equal(qualityModeTarget('320k', ''), '320k')
  assert.equal(qualityModeTarget('flac24bit', undefined), 'flac24bit')
  assert.equal(qualityModeTarget('128k', '   '), '128k') // 空白 = 跟随
  // 两种非跟随模式差别不在「要什么」（都点名 flac），而在拿不到时降不降
  assert.equal(qualityModeTarget('128k', QUALITY_TOP_ONLY), 'flac')
  assert.equal(qualityModeTarget('highest', QUALITY_AUTO_LOWER), 'flac')
  assert.equal(qualityModeTarget('flac', QUALITY_TOP_ONLY), 'flac')
})

test('qualityModeTarget：认不出的值当跟随（不钉死所有下载）', () => {
  assert.equal(qualityModeTarget('128k', '乱写的'), '128k')
  assert.equal(qualityModeTarget('highest', 'top_only '), 'flac') // trim 后认
})

test('qualityModeAllowsLower：只有「仅下载最高」说不', () => {
  assert.equal(qualityModeAllowsLower(QUALITY_FOLLOW), true)
  assert.equal(qualityModeAllowsLower(QUALITY_AUTO_LOWER), true)
  assert.equal(qualityModeAllowsLower(QUALITY_TOP_ONLY), false)
  assert.equal(qualityModeAllowsLower(''), true)
  assert.equal(qualityModeAllowsLower(undefined), true)
})

test('模式的三个选项齐全，且与后端常量同值', () => {
  assert.deepEqual(
    QUALITY_MODE_OPTIONS.map((o) => o.value),
    ['', 'top_only', 'auto_lower'],
  )
  for (const o of QUALITY_MODE_OPTIONS) {
    assert.ok(o.label && o.desc, `选项 ${o.value} 缺说明文字`)
  }
})

test('空 base + 跟随 = 空串（让调用方自己兜底，不偷偷挑一个档位）', () => {
  assert.equal(qualityModeTarget('', QUALITY_FOLLOW), '')
})
