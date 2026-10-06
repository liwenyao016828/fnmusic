import { test } from 'node:test'
import assert from 'node:assert/strict'

import {
  QUALITY_LADDER,
  buildQualityTiers,
  isHighestQuality,
  normalizeQuality,
  qualityLabel,
  guessExtFromUrl,
  isLosslessTier,
  isLossyContainer,
  urlLooksLossy,
} from './quality.js'

test('isHighestQuality: 空值与 highest 都表示自动最高音质', () => {
  assert.equal(isHighestQuality(''), true)
  assert.equal(isHighestQuality(null), true)
  assert.equal(isHighestQuality(undefined), true)
  assert.equal(isHighestQuality('highest'), true)
  assert.equal(isHighestQuality('320k'), false)
  assert.equal(isHighestQuality('flac'), false)
})

test('音质阶梯顺序为 flac24bit > flac > 320k > 192k > 128k', () => {
  assert.deepEqual(QUALITY_LADDER, ['flac24bit', 'flac', '320k', '192k', '128k'])
})

test('highest: 取各音源宣称音质的并集，并按阶梯排序', () => {
  const tiers = buildQualityTiers('highest', [
    ['128k', '320k', 'flac'],
    ['128k', 'flac'],
    ['320k'],
  ])
  assert.deepEqual(tiers, ['flac', '320k', '128k'])
})

test('highest: 并集包含 flac24bit 时排在最高档', () => {
  const tiers = buildQualityTiers('highest', [['128k', 'flac24bit'], ['320k']])
  assert.deepEqual(tiers, ['flac24bit', '320k', '128k'])
})

test('highest: 音源只声明 128k 时只尝试 128k', () => {
  assert.deepEqual(buildQualityTiers('highest', [['128k']]), ['128k'])
})

test('highest: 音源未上报 qualitys 时回退到保底阶梯', () => {
  assert.deepEqual(buildQualityTiers('highest', []), ['flac', '320k', '128k'])
  assert.deepEqual(buildQualityTiers('highest', [[]]), ['flac', '320k', '128k'])
})

test('highest: 非标准音质档位追加到标准阶梯之后兜底', () => {
  const tiers = buildQualityTiers('highest', [['128k', 'hires', 'flac']])
  assert.deepEqual(tiers, ['flac', '128k', 'hires'])
})

test('固定音质: 只尝试该档位，不受音源声明影响', () => {
  assert.deepEqual(buildQualityTiers('320k', [['128k']]), ['320k'])
  assert.deepEqual(buildQualityTiers('flac', []), ['flac'])
  assert.deepEqual(buildQualityTiers('128k', [['flac', '320k']]), ['128k'])
})

test('qualityLabel 给出可读文案', () => {
  assert.equal(qualityLabel('highest'), '最高音质')
  assert.equal(qualityLabel('flac24bit'), 'FLAC 24bit')
  assert.equal(qualityLabel('320k'), '320K')
})

test('guessExtFromUrl 按 URL / 音质推断扩展名', () => {
  assert.equal(guessExtFromUrl('https://x/y.flac', 'flac'), 'flac')
  assert.equal(guessExtFromUrl('https://x/y.flac?token=1', ''), 'flac')
  assert.equal(guessExtFromUrl('https://x/y.mp3', ''), 'mp3')
  assert.equal(guessExtFromUrl('https://x/stream', 'flac24bit'), 'flac')
  assert.equal(guessExtFromUrl('https://x/stream', '320k'), 'mp3')
})

test('normalizeQuality: 搜索接口返回的大写音质被规范为小写', () => {
  assert.equal(normalizeQuality('320K'), '320k')
  assert.equal(normalizeQuality('FLAC'), 'flac')
  assert.equal(normalizeQuality('  FLAC24BIT '), 'flac24bit')
  assert.equal(normalizeQuality(''), 'highest')
  assert.equal(normalizeQuality(null), 'highest')
})

test('固定音质传入大写时也被规范化（避免脚本无法识别）', () => {
  assert.deepEqual(buildQualityTiers('FLAC', []), ['flac'])
  assert.deepEqual(buildQualityTiers('320K', []), ['320k'])
})

test('highest: 音源声明大写音质也能并入阶梯', () => {
  assert.deepEqual(buildQualityTiers('highest', [['FLAC', '320K']]), ['flac', '320k'])
})

test('guessExtFromUrl: 大写 FLAC 也能推断出 flac 扩展名', () => {
  assert.equal(guessExtFromUrl('https://x/stream', 'FLAC'), 'flac')
  assert.equal(guessExtFromUrl('https://x/stream', '320K'), 'mp3')
})

test('qualityLabel: 大写音质也能给出正确文案', () => {
  assert.equal(qualityLabel('FLAC'), 'FLAC 无损')
  assert.equal(qualityLabel('320K'), '320K')
})

// ── 「请求无损却拿到有损」的识别 ──

test('isLosslessTier: 只有无损档位才算无损', () => {
  assert.equal(isLosslessTier('flac'), true)
  assert.equal(isLosslessTier('FLAC'), true) // 大写也要认
  assert.equal(isLosslessTier('flac24bit'), true)
  assert.equal(isLosslessTier('hires'), true)
  assert.equal(isLosslessTier('320k'), false)
  assert.equal(isLosslessTier('128k'), false)
  assert.equal(isLosslessTier('highest'), false) // 自动档不等于「要求无损」
  assert.equal(isLosslessTier(''), false)
  assert.equal(isLosslessTier(null), false)
})

test('isLossyContainer: 压缩有损才算有损，wav/ape 等无损不算', () => {
  assert.equal(isLossyContainer('mp3'), true)
  assert.equal(isLossyContainer('MP3'), true)
  assert.equal(isLossyContainer('m4a'), true)
  assert.equal(isLossyContainer('ogg'), true)
  // 这几个是无损，不能误判
  assert.equal(isLossyContainer('flac'), false)
  assert.equal(isLossyContainer('wav'), false)
  assert.equal(isLossyContainer('ape'), false)
  assert.equal(isLossyContainer('dsf'), false)
  assert.equal(isLossyContainer(''), false)
  assert.equal(isLossyContainer(null), false)
})

test('urlLooksLossy: 明确的 .mp3 / .php 判为有损', () => {
  assert.equal(urlLooksLossy('https://x/a.mp3'), true)
  assert.equal(urlLooksLossy('https://x/a.MP3?auth=1'), true)
  assert.equal(urlLooksLossy('https://x/proxy.php?id=1'), true)
  assert.equal(urlLooksLossy('https://x/s?format=mp3'), true)
})

test('urlLooksLossy: 看不出格式的 CDN 不透明直链必须放行（不误杀）', () => {
  // 这是关键取舍：要求「必须含 .flac」会把这类合法无损全部误杀
  assert.equal(urlLooksLossy('https://cdn.x/api/v1/stream?token=abc&id=1'), false)
  assert.equal(urlLooksLossy('https://x/a.flac'), false)
  assert.equal(urlLooksLossy(''), false)
  assert.equal(urlLooksLossy(null), false)
})
