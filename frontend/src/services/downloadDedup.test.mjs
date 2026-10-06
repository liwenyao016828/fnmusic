import { test } from 'node:test'
import assert from 'node:assert/strict'
import { pickAlreadyLocal } from './downloadDedup.js'

// 守的判据（用户 2026-09-22 原话）：
// 「同一首，同歌手，不需要，只下载不一样的版本」。

test('pickAlreadyLocal：按「歌手 - 歌名」命中', () => {
  const items = [
    { name: '夜曲', artist: '周杰伦' },
    { name: '晴天', artist: '周杰伦' },
  ]
  const exists = { '周杰伦 - 夜曲': true, '周杰伦 - 晴天': false }
  assert.deepEqual([...pickAlreadyLocal(items, exists)], [0])
})

test('pickAlreadyLocal：按平台曲目 id 命中（推送侧 missing 没有 song_id，监控侧有）', () => {
  const items = [
    { name: '七里香', artist: '周杰伦', song_id: 'wy_1' },
    { name: '稻香', artist: '周杰伦', song_id: 'wy_2' },
  ]
  const exists = { wy_2: true }
  assert.deepEqual([...pickAlreadyLocal(items, exists)], [1])
})

test('pickAlreadyLocal：id 和「歌手 - 歌名」任一命中就算已有', () => {
  const items = [{ name: '青花瓷', artist: '周杰伦', song_id: 'wy_9' }]
  assert.deepEqual([...pickAlreadyLocal(items, { '周杰伦 - 青花瓷': true })], [0])
  assert.deepEqual([...pickAlreadyLocal(items, { wy_9: true })], [0])
})

test('pickAlreadyLocal：exists 里是 false 不算已有', () => {
  const items = [{ name: '夜曲', artist: '周杰伦', song_id: 'wy_1' }]
  assert.equal(pickAlreadyLocal(items, { '周杰伦 - 夜曲': false, wy_1: false }).size, 0)
})

test('pickAlreadyLocal：歌名和 id 都缺时不误判', () => {
  // ⚠️ 别拿空拼接 `' - '` 去查表 —— 后端万一返回了同名键就会误判成「已有」，
  // 白白漏掉一首歌（而且用户完全看不出来为什么没下）。
  const items = [{ name: '', artist: '' }, {}]
  assert.equal(pickAlreadyLocal(items, { ' - ': true }).size, 0)
})

test('pickAlreadyLocal：没有 artist 时只按歌名拼（仍能命中）', () => {
  const items = [{ name: '未知歌手曲', artist: '' }]
  assert.deepEqual([...pickAlreadyLocal(items, { ' - 未知歌手曲': true })], [0])
})

test('pickAlreadyLocal：空输入不抛异常', () => {
  assert.equal(pickAlreadyLocal().size, 0)
  assert.equal(pickAlreadyLocal([], {}).size, 0)
  assert.equal(pickAlreadyLocal(null, null).size, 0)
  assert.equal(pickAlreadyLocal([{ name: 'x' }], null).size, 0)
})

test('pickAlreadyLocal：多条命中时按原始下标返回（调用方靠下标回写曲目）', () => {
  const items = [
    { name: 'a', artist: 'X', song_id: '1' },
    { name: 'b', artist: 'X', song_id: '2' },
    { name: 'c', artist: 'X', song_id: '3' },
  ]
  const got = pickAlreadyLocal(items, { 'X - a': true, 3: true })
  assert.deepEqual([...got], [0, 2])
})
