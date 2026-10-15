import test from 'node:test'
import assert from 'node:assert/strict'

import {
  QUALITY_FOLLOW,
  QUALITY_LOSSLESS,
  QUALITY_MP3,
  QUALITY_OVERRIDE_OPTIONS,
  qualityOverride,
} from './qualityOverride.js'

// 「下载音质」开关是**盖住**而不是改掉各入口自己的音质 —— 这条语义一旦漂了，
// 用户改回「跟随」就复原不回来，所以用表钉住。
test('qualityOverride：跟随时不动各入口自己的档位', () => {
  assert.equal(qualityOverride('highest', QUALITY_FOLLOW), 'highest')
  assert.equal(qualityOverride('320k', ''), '320k')
  assert.equal(qualityOverride('flac24bit', undefined), 'flac24bit')
  assert.equal(qualityOverride('flac24bit', null), 'flac24bit')
  assert.equal(qualityOverride('128k', '   '), '128k') // 空白 = 跟随
})

test('qualityOverride：覆盖时一律点名那一档', () => {
  assert.equal(qualityOverride('128k', QUALITY_LOSSLESS), 'flac')
  assert.equal(qualityOverride('flac24bit', QUALITY_LOSSLESS), 'flac')
  assert.equal(qualityOverride('highest', QUALITY_MP3), '320k')
  assert.equal(qualityOverride('flac', QUALITY_MP3), '320k')
})

test('qualityOverride：认不出的值当跟随（不钉死所有下载）', () => {
  assert.equal(qualityOverride('128k', '乱写的'), '128k')
  assert.equal(qualityOverride('highest', 'LOSSLESS'), 'highest') // 大小写不敏感？不认
  assert.equal(qualityOverride('highest', 'mp3'), 'highest')      // 不是 mp3_320 就不认
})

test('开关的三个选项齐全，且与后端常量同值', () => {
  assert.deepEqual(
    QUALITY_OVERRIDE_OPTIONS.map((o) => o.value),
    ['', 'lossless', 'mp3_320'],
  )
  for (const o of QUALITY_OVERRIDE_OPTIONS) {
    assert.ok(o.label && o.desc, `选项 ${o.value} 缺说明文字`)
  }
})

test('空 base + 跟随 = 空串（让调用方自己兜底，不偷偷挑一个档位）', () => {
  assert.equal(qualityOverride('', QUALITY_FOLLOW), '')
})
