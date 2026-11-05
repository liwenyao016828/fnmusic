import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  DEFAULT_COLUMNS,
  FIXED_COLUMNS,
  COLUMN_OPTIONS,
  normalizeColumns,
  listColumnClass,
  columnSummary,
} from './listColumns.js'

test('0 表示自动，原样保留', () => {
  assert.equal(normalizeColumns(0), 0)
  assert.equal(normalizeColumns('0'), 0)
})

test('2~6 的整数原样保留', () => {
  for (const n of FIXED_COLUMNS) {
    assert.equal(normalizeColumns(n), n)
    assert.equal(normalizeColumns(String(n)), n)
  }
})

test('超出范围的值回落默认（0）', () => {
  assert.equal(normalizeColumns(1), 0)
  assert.equal(normalizeColumns(7), 0)
  assert.equal(normalizeColumns(-2), 0)
})

test('非数字 / 空值回落默认，不抛异常', () => {
  assert.equal(normalizeColumns(''), 0)
  assert.equal(normalizeColumns('abc'), 0)
  assert.equal(normalizeColumns(null), 0)
  assert.equal(normalizeColumns(undefined), 0)
  assert.equal(normalizeColumns(NaN), 0)
})

test('浮点向下取整后再判定（4.9 → 4，1.5 → 回落）', () => {
  assert.equal(normalizeColumns(4.9), 4)
  assert.equal(normalizeColumns(1.5), 0)
})

test('非法值回落到调用方给的 fallback', () => {
  assert.equal(normalizeColumns('x', 4), 4)
  assert.equal(normalizeColumns(9, 4), 4)
  // fallback 本身不合法时回落默认 0
  assert.equal(normalizeColumns('x', 9), 0)
})

test('自动(0) 时返回调用方自己的响应式类串', () => {
  const auto = 'grid-cols-2 sm:grid-cols-4'
  assert.equal(listColumnClass(0, auto), auto)
})

test('固定列数时返回全断位钉死的字面量类串（Tailwind 可扫描）', () => {
  assert.equal(
    listColumnClass(4, 'grid-cols-2 sm:grid-cols-3'),
    'grid-cols-4 sm:grid-cols-4 md:grid-cols-4 lg:grid-cols-4 xl:grid-cols-4',
  )
  assert.equal(
    listColumnClass(6, ''),
    'grid-cols-6 sm:grid-cols-6 md:grid-cols-6 lg:grid-cols-6 xl:grid-cols-6',
  )
})

test('非法列数在 listColumnClass 里同样按自动处理', () => {
  assert.equal(listColumnClass('x', 'auto-x'), 'auto-x')
})

test('摘要文案：自动 / 每行 N 列', () => {
  assert.equal(columnSummary(0), '自动（按屏幕宽度）')
  assert.equal(columnSummary(3), '每行 3 列')
  assert.equal(columnSummary('bad'), '自动（按屏幕宽度）')
})

test('选项表覆盖 0 与 2~6，且与 FIXED_COLUMNS 一致', () => {
  assert.deepEqual(COLUMN_OPTIONS.map((o) => o.value), [0, ...FIXED_COLUMNS])
  assert.equal(DEFAULT_COLUMNS, 0)
})
