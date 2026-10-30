import { test } from 'node:test'
import assert from 'node:assert/strict'
import { parseLyric, currentLyricLine, offsetLyric } from './lyricLine.js'

// 实测样本：曲库里的歌（夜曲.lrc）把 [offset:0] 放在第 9 行，前面全是 [id:]/[ar:]/[ti:]…
// 之前只在首行找 offset → 偏移被整段忽略 → 歌词与声音错位（用户口径：「怎么对不上」）。
const SAMPLE = [
  '\uFEFF[id:$00000000]',
  '[ar:周杰伦]',
  '[ti:夜曲]',
  '[by:story]',
  '[hash:16c8ab298231370293d16bcf9e5ff9b6]',
  '[al:十一月的萧邦]',
  '[sign:]',
  '[qq:]',
  '[total:226000]',
  '[offset:500]',
  '[00:24.74]一群嗜血的蚂蚁',
  '[00:26.52]被腐肉所吸引',
].join('\n')

test('offset 不在首行时也要生效（+0.5s → 24.24 / 26.02）', () => {
  const lines = parseLyric(SAMPLE)
  assert.equal(lines.length, 2)
  assert.equal(lines[0].text, '一群嗜血的蚂蚁')
  assert.ok(Math.abs(lines[0].time - 24.24) < 1e-6, `got ${lines[0].time}`)
  assert.ok(Math.abs(lines[1].time - 26.02) < 1e-6, `got ${lines[1].time}`)
})

test('没有 offset 时按原时间；标签行不会混进歌词', () => {
  const lrc = '[ti:晴天]\n[00:01.00]第一行\n[00:03.00]第二行'
  const lines = parseLyric(lrc)
  assert.deepEqual(lines.map(l => l.text), ['第一行', '第二行'])
  assert.ok(Math.abs(lines[0].time - 1) < 1e-6)
})

// LRC 语义：offset 为「提前量」——正值让歌词提前（时间变小），负值让歌词延后（时间变大）。
// 与 ImmersivePlayer 的内联实现（time - offsetSec）保持一致，两处不能各算一种。
test('负 offset（歌词延后 1s）同样生效', () => {
  const lrc = '[ti:x]\n[offset:-1000]\n[00:10.00]啊'
  assert.ok(Math.abs(parseLyric(lrc)[0].time - 11) < 1e-6, `got ${parseLyric(lrc)[0].time}`)
})

test('同一行多个时间标签展开成多条', () => {
  const lrc = '[00:01.00][00:11.00]重复的一句'
  const lines = parseLyric(lrc)
  assert.equal(lines.length, 2)
  assert.deepEqual(lines.map(l => l.text), ['重复的一句', '重复的一句'])
})

test('currentLyricLine：取 <= 当前时间的最后一行；早于首行取首行', () => {
  const lrc = '[00:10.00]A\n[00:20.00]B\n[00:30.00]C'
  assert.equal(currentLyricLine(lrc, 0), 'A')
  assert.equal(currentLyricLine(lrc, 19.9), 'A')
  assert.equal(currentLyricLine(lrc, 20), 'B')
  assert.equal(currentLyricLine(lrc, 999), 'C')
})

test('空歌词 / 无时间标签 → 空数组、空字符串', () => {
  assert.deepEqual(parseLyric(''), [])
  assert.deepEqual(parseLyric('[ti:只有标题]'), [])
  assert.equal(currentLyricLine('', 5), '')
})

// ── offsetLyric：歌曲修复里的「调整歌词时间段」（用户 2026-10-28）──
//
// 口径：按秒整体平移，正数让歌词**更晚**出现（时间变大），负数提前。
// 平移后必须还能被 parseLyric 读回来 —— 写盘用的是这个结果，格式不对等于写坏歌词。

test('offsetLyric：正数整体延后，且能被 parseLyric 读回', () => {
  const lrc = '[00:10.00]第一句\n[00:20.50]第二句'
  const moved = offsetLyric(lrc, 0.5)
  const times = parseLyric(moved).map(l => l.time)
  assert.ok(Math.abs(times[0] - 10.5) < 1e-6, `got ${times[0]}`)
  assert.ok(Math.abs(times[1] - 21) < 1e-6, `got ${times[1]}`)
  // 文本一个字都不许动
  assert.deepEqual(parseLyric(moved).map(l => l.text), ['第一句', '第二句'])
})

test('offsetLyric：负数提前；平移过头贴 0，不出负时间戳', () => {
  const lrc = '[00:00.30]开头\n[00:10.00]后面'
  const times = parseLyric(offsetLyric(lrc, -1)).map(l => l.time)
  assert.equal(times[0], 0, '早于 0 的时间戳要贴 0')
  assert.ok(Math.abs(times[1] - 9) < 1e-6, `got ${times[1]}`)
})

test('offsetLyric：偏移为 0 / 空歌词原样返回', () => {
  const lrc = '[00:01.00]x'
  assert.equal(offsetLyric(lrc, 0), lrc)
  assert.equal(offsetLyric('', 2), '')
})

test('offsetLyric：毫秒补零（.5 是 500ms 不是 5ms）', () => {
  const times = parseLyric(offsetLyric('[00:01.5]x', 0.25)).map(l => l.time)
  assert.ok(Math.abs(times[0] - 1.75) < 1e-6, `got ${times[0]}`)
})

test('offsetLyric：分钟进位正确（59.x + 2s 跨到下一分钟）', () => {
  const moved = offsetLyric('[00:59.80]x', 0.5)
  assert.ok(moved.includes('[01:00.300]'), moved)
})
