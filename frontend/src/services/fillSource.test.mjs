import test from 'node:test'
import assert from 'node:assert/strict'
import {
  KNOWN_PLATFORMS,
  normTitle,
  normArtist,
  fillPlatformOrder,
  pickSearchHit,
  nameOnlyFallbackAllowed,
} from './fillSource.js'

test('normTitle：括号补充、空格、标点都归一，同一首不同写法能对上', () => {
  assert.equal(normTitle('All Too Well (10 Minute Version)'), normTitle('All Too Well'))
  assert.equal(normTitle('晨钟暮鼓（Live）'), normTitle('晨钟暮鼓'))
  assert.equal(normTitle('Lemon - 米津玄師'), normTitle('Lemon米津玄師'))
  assert.equal(normTitle(''), '')
  assert.equal(normTitle(null), '')
})

test('normArtist：多歌手标注只留第一个', () => {
  assert.equal(normArtist('周杰伦/杨瑞代'), '周杰伦')
  assert.equal(normArtist('Stake, TwoP'), 'stake')
  assert.equal(normArtist(''), '')
})

test('fillPlatformOrder：曲目自己的平台优先，其次这一行的平台，不重复', () => {
  assert.deepEqual(fillPlatformOrder({ source: 'tx' }, 'wy'), ['tx', 'wy'])
  assert.deepEqual(fillPlatformOrder({ source: 'wy' }, 'wy'), ['wy'])
  assert.deepEqual(fillPlatformOrder({}, 'kw'), ['kw'])
})

test('fillPlatformOrder：两个平台都不认识时返回空（调用方改走聚合搜）', () => {
  assert.deepEqual(fillPlatformOrder({ source: 'lx:全豆要' }, ''), [])
  assert.deepEqual(fillPlatformOrder({}, 'netease'), [])
  assert.deepEqual(fillPlatformOrder(null, null), [])
})

test('pickSearchHit：歌名不同的候选一律不要（以前会拿 list[0] 下错歌）', () => {
  const list = [
    { name: '晨钟暮鼓（另一首）', singer: '别人', source: 'tx', duration: 240 },
    { name: '晨钟暮鼓', singer: '一鱼', source: 'tx', duration: 238 },
  ]
  const hit = pickSearchHit(list, { name: '晨钟暮鼓', artist: '一鱼', duration: 240 }, 'tx')
  assert.equal(hit.singer, '一鱼')
})

test('pickSearchHit：一个都不匹配时返回 null，绝不退回 list[0]', () => {
  const list = [{ name: '完全别的歌', singer: '别人', source: 'tx', duration: 200 }]
  assert.equal(pickSearchHit(list, { name: '晨钟暮鼓', artist: '一鱼' }, 'tx'), null)
  assert.equal(pickSearchHit([], { name: '晨钟暮鼓' }), null)
  assert.equal(pickSearchHit(null, { name: '晨钟暮鼓' }), null)
})

test('pickSearchHit：时长差太多（不是同一版录音）要拒掉', () => {
  const list = [{ name: 'Lemon', singer: '米津玄師', source: 'wy', duration: 300 }]
  // 原曲 4:15（255s），候选 5:00（300s）差 45s > max(30, 25.5)
  assert.equal(pickSearchHit(list, { name: 'Lemon', artist: '米津玄師', duration: 255 }), null)
})

test('pickSearchHit：优先认指定平台，其次曲目自己的平台', () => {
  const list = [
    { name: 'Lemon', singer: '米津玄師', source: 'wy', duration: 255 },
    { name: 'Lemon', singer: '米津玄師', source: 'tx', duration: 255 },
  ]
  assert.equal(pickSearchHit(list, { name: 'Lemon', artist: '米津玄師' }, 'tx').source, 'tx')
  assert.equal(pickSearchHit(list, { name: 'Lemon', artist: '米津玄師', source: 'wy' }).source, 'wy')
})

test('pickSearchHit：不认识的平台（洛雪伪源）不当候选', () => {
  const list = [{ name: 'Lemon', singer: '米津玄師', source: 'lx:全豆要', duration: 255 }]
  assert.equal(pickSearchHit(list, { name: 'Lemon', artist: '米津玄師' }), null)
})

test('nameOnlyFallbackAllowed：只有原生渠道时不能交（交上去必失败）', () => {
  assert.equal(nameOnlyFallbackAllowed([{ id: 'native', enabled: true }]), false)
  assert.equal(nameOnlyFallbackAllowed([]), false)
  assert.equal(nameOnlyFallbackAllowed(null), false)
})

test('nameOnlyFallbackAllowed：洛雪宿主或 musicdl 在就算能交', () => {
  assert.equal(nameOnlyFallbackAllowed([{ id: 'native' }, { id: 'lx', enabled: true }]), true)
  assert.equal(nameOnlyFallbackAllowed([{ id: 'dl', enabled: true }]), true)
  assert.equal(nameOnlyFallbackAllowed([{ id: 'lx', enabled: false }]), false)
})

test('KNOWN_PLATFORMS 与 downloadMigration 的 PLATFORM_SOURCES 保持一致', async () => {
  const { PLATFORM_SOURCES } = await import('./downloadMigration.js')
  assert.deepEqual([...KNOWN_PLATFORMS].sort(), [...PLATFORM_SOURCES].sort())
})
