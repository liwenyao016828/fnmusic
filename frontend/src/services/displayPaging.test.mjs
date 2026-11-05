import { test } from 'node:test'
import assert from 'node:assert/strict'
import { activeLyricIndex, clampPage, displayPage, normalizePageSize, pageLabel, parseDisplayLyrics } from './displayPaging.js'

test('展示分页保留完整队列，页码始终夹在有效区间', () => {
  const songs = Array.from({ length: 97 }, (_, i) => ({ id: i }))
  assert.equal(displayPage(songs, 2, 24).length, 24)
  assert.deepEqual(displayPage(songs, 2, 24)[0], { id: 24 })
  assert.equal(displayPage(songs, 5, 24).length, 1)
  assert.equal(displayPage(songs, 999, 24)[0].id, 96)
  assert.equal(displayPage(songs, -1, 24)[0].id, 0)
  assert.equal(songs.length, 97)
  assert.equal(clampPage(4, 0, 24), 1)
  assert.equal(pageLabel(2, 97, 24), '第 2 / 5 页 · 共 97 项')
  assert.equal(normalizePageSize(48), 48)
  assert.equal(normalizePageSize(31), 24)
})

test('LRC 支持多时间戳、毫秒、offset 与翻译', () => {
  const lines = parseDisplayLyrics('[offset:-1000]\n[00:02.50][00:04.00]重复\n[01:01.5]第二行', '[01:01.500]翻译')
  assert.deepEqual(lines.map(x => x.time), [3.5, 5, 62.5])
  assert.equal(lines[2].trans, '翻译')
  assert.equal(activeLyricIndex(lines, 0), 0)
  assert.equal(activeLyricIndex(lines, 4.9), 0)
  assert.equal(activeLyricIndex(lines, 5), 1)
  assert.equal(activeLyricIndex(lines, 100), 2)
  assert.equal(activeLyricIndex([], 5), -1)
})
