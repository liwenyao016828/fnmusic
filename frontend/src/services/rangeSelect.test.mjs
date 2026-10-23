import { test } from 'node:test'
import assert from 'node:assert/strict'
import { parseRangeSpec, pickByIndexes } from './rangeSelect.js'

/* 守的判据（用户 2026-10-23 提的功能）：
 *   「下载页面中，多选框上方增加多选功能，目的为快速选择歌曲，
 *     比如用户输入 100，就是 0-100；用户也可以输入开始歌曲到最终歌曲比如 11-200。」
 * 所以两条硬规则：
 *   1. 单独一个数 = 前 N 首（从第 1 行开始，含第 N 行）；
 *   2. a-b = 第 a 行到第 b 行（含两端），写反了也认。
 * 其余是保险：越界夹到实际行数而不是报错；夹完为空才给错误提示。
 */

test('单独一个数 = 前 N 首', () => {
  const r = parseRangeSpec('100', 500)
  assert.ok(r.ok)
  assert.equal(r.indexes.length, 100)
  assert.equal(r.indexes[0], 1)
  assert.equal(r.indexes[99], 100)
})

test('输入 100 但只有 60 行：夹到 60，不报错', () => {
  const r = parseRangeSpec('100', 60)
  assert.ok(r.ok)
  assert.equal(r.indexes.length, 60)
  assert.equal(r.indexes[59], 60)
})

test('区间 11-200 含两端', () => {
  const r = parseRangeSpec('11-200', 300)
  assert.ok(r.ok)
  assert.equal(r.indexes.length, 190)
  assert.equal(r.indexes[0], 11)
  assert.equal(r.indexes[189], 200)
})

test('0-100 里的 0 当作「从第一行开始」', () => {
  const r = parseRangeSpec('0-100', 500)
  assert.ok(r.ok)
  assert.equal(r.indexes.length, 100)
  assert.equal(r.indexes[0], 1)
})

test('写反的 200-11 与 11-200 等价', () => {
  assert.deepEqual(parseRangeSpec('200-11', 300).indexes, parseRangeSpec('11-200', 300).indexes)
})

test('越界区间夹到实际行数', () => {
  const r = parseRangeSpec('5-5000', 20)
  assert.ok(r.ok)
  assert.deepEqual(r.indexes.slice(0, 3), [5, 6, 7])
  assert.equal(r.indexes[r.indexes.length - 1], 20)
})

test('整段都在列表外：给错误提示而不是静默空选', () => {
  const r = parseRangeSpec('500-600', 20)
  assert.ok(!r.ok)
  assert.equal(r.indexes.length, 0)
  assert.match(r.error, /没有命中/)
})

test('逗号多段取并集且不重复', () => {
  const r = parseRangeSpec('3-5, 4-6', 100)
  assert.ok(r.ok)
  assert.deepEqual(r.indexes, [3, 4, 5, 6])
})

test('中文分隔符与空格也能解析', () => {
  assert.deepEqual(parseRangeSpec('3-5，4-6', 100).indexes, [3, 4, 5, 6])
  assert.deepEqual(parseRangeSpec(' 11 - 13 ', 100).indexes, [11, 12, 13])
  assert.deepEqual(parseRangeSpec('11到13', 100).indexes, [11, 12, 13])
})

test('空输入 / 非法输入：回话说清可以怎么填', () => {
  assert.match(parseRangeSpec('', 10).error, /行号/)
  assert.match(parseRangeSpec('abc', 10).error, /看不懂/)
  assert.match(parseRangeSpec('3-x', 10).error, /看不懂/)
})

test('列表为空时提示没有可选项', () => {
  const r = parseRangeSpec('10', 0)
  assert.ok(!r.ok)
  assert.match(r.error, /没有可选/)
})

test('pickByIndexes 越界的行号直接忽略', () => {
  const list = ['a', 'b', 'c']
  assert.deepEqual(pickByIndexes(list, [1, 3, 99, 0]), ['a', 'c'])
})

test('pickByIndexes 拿到的就是列表里那一项（同引用，勾选后要按对象判定）', () => {
  const song = { id: 'x', name: '夜曲' }
  assert.equal(pickByIndexes([song], [1])[0], song)
})
