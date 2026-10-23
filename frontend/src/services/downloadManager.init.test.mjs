/**
 * 模块初始化顺序的回归钉子。
 *
 * 2026-10-23 v2.18.0 首发：`watch(backendDownload.items, syncTasks, { immediate: true })`
 * 在模块求值阶段就会跑 syncTasks → persistUnsent，而 `unsentReady` 那时还声明在
 * 文件后半段（TDZ），抛 ReferenceError「Cannot access 'unsentReady' before
 * initialization」，整个应用白屏。单测和 vite build 都拦不住这种错误，只有真
 * 打开页面才会暴露 —— 所以在这里按源码顺序钉一条，改回去立刻红。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(join(here, 'downloadManager.js'), 'utf8')

test('unsentReady 必须声明在 immediate watcher 之前（否则白屏）', () => {
  const decl = source.search(/^\s*let unsentReady = false\s*$/m)
  const watcher = source.search(/watch\(\s*backendDownload\.items,\s*syncTasks,\s*\{\s*immediate:\s*true\s*\}/)
  assert.ok(decl > 0, '找不到 `let unsentReady = false` 声明')
  assert.ok(watcher > 0, '找不到 immediate watcher 声明')
  assert.ok(
    decl < watcher,
    '`let unsentReady` 必须在 immediate watcher 之前：watcher 在模块求值时就调用 syncTasks，'
    + '晚于它声明会让整个应用白屏（v2.18.0 事故）',
  )
})

test('模块级只该有这一处 unsentReady 声明', () => {
  const hits = source.match(/let unsentReady/g) || []
  assert.equal(hits.length, 1, `unsentReady 只应声明一次，实际 ${hits.length} 次`)
})
