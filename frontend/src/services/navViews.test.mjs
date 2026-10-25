import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  VIEWS,
  VIEW_IDS,
  defaultSegment,
  segmentsOf,
  isView,
  resolveRoute,
} from './navViews.js'

/* 守的判据（2026-10-15 全站 UI/UX 重构 P0）：
 *
 * 重构前「有哪些顶层页」这件事散在三处各写一遍（App.vue 的 navItems、
 * 模板里的 `#view-xxx` 槽位、组件里的 navigateTo 字面量），于是长出了
 * **音源管理**这种「是个整页、却不在导航里，只能靠顶栏胶囊进」的孤儿页。
 *
 * 这里把它变成**会失败的测试**：注册表与真实代码对不上就红。
 * 第 3、4 条是结构性检查（扫源码），不是纯函数单测 —— 正是它们能抓住
 * 「加了页忘了登记」这类只会在用户手里暴露的问题。
 */

const SRC = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

/** 递归收集业务源码（排除测试文件本身） */
function collectSources(dir) {
  const out = []
  for (const name of readdirSync(dir)) {
    const full = path.join(dir, name)
    if (statSync(full).isDirectory()) {
      if (name === 'node_modules' || name === 'dist') continue
      out.push(...collectSources(full))
    } else if (/\.(vue|js|mjs)$/.test(name) && !/\.test\.mjs$/.test(name)) {
      out.push(full)
    }
  }
  return out
}

test('八个顶层视图，顺序 = 导航顺序 = 滑动顺序', () => {
  assert.deepEqual(VIEW_IDS, ['search', 'library', 'tidy', 'push', 'download', 'sources', 'accounts', 'system'])
})

test('每个视图都有全名与窄屏短名（缺一个导航上就是空白按钮）', () => {
  for (const v of VIEWS) {
    assert.ok(v.name && v.name.length, `${v.id} 缺 name`)
    assert.ok(v.short && v.short.length, `${v.id} 缺 short`)
    assert.ok(v.short.length <= 4, `${v.id} 的 short 太长，窄屏放不下：${v.short}`)
  }
})

test('历史 id / 深链全部能解析到正确的顶层页与分段', () => {
  // 榜单管理并进「发现」、日志与设置并进「系统」—— 这一层映射是改版不打断老入口的保险
  assert.deepEqual(resolveRoute('charts'), { view: 'search', segment: 'charts', unknown: false })
  assert.deepEqual(resolveRoute('logs'), { view: 'system', segment: 'logs', unknown: false })
  assert.deepEqual(resolveRoute('settings'), { view: 'system', segment: 'settings', unknown: false })
  assert.deepEqual(resolveRoute('discover'), { view: 'search', segment: 'discover', unknown: false })
  assert.deepEqual(resolveRoute('netsources'), { view: 'sources', segment: '', unknown: false })

  // 顶层 id 直接进，落在默认段
  assert.deepEqual(resolveRoute('search'), { view: 'search', segment: 'discover', unknown: false })
  // 2026-10-24 用户：「设置在前，日志在后」—— 系统页默认段随之改成 settings
  assert.deepEqual(resolveRoute('system'), { view: 'system', segment: 'settings', unknown: false })
  // 没有分段的页，段位是空串（模板据此决定渲不渲分段器）
  assert.deepEqual(resolveRoute('library'), { view: 'library', segment: '', unknown: false })
  assert.deepEqual(resolveRoute('sources'), { view: 'sources', segment: '', unknown: false })

  // 指定段位；给了不存在的段位回落默认段，而不是渲染出一个空页
  assert.equal(resolveRoute('system', 'settings').segment, 'settings')
  assert.equal(resolveRoute('system', 'nope').segment, 'settings')
  assert.equal(resolveRoute('search', 'charts').segment, 'charts')
  // 没有分段的页带上段位也安全
  assert.equal(resolveRoute('library', 'whatever').segment, '')
})

test('认不出的 id 不静默失败：回落发现页并标记 unknown 供记日志', () => {
  const r = resolveRoute('veiw-typo')
  assert.equal(r.view, 'search')
  assert.equal(r.segment, 'discover')
  assert.equal(r.unknown, true)
  assert.equal(resolveRoute(undefined).view, 'search')
})

test('辅助函数与注册表一致', () => {
  assert.equal(defaultSegment('system'), 'settings')
  assert.equal(defaultSegment('library'), '')
  assert.deepEqual(segmentsOf('search').map((s) => s.id), ['discover', 'charts'])
  assert.deepEqual(segmentsOf('library'), [])
  // 补全整理的顶部分段（2026-10-21 用户口径：功能从右上角 ⋯ 升为常驻分段）
  assert.deepEqual(segmentsOf('tidy').map((s) => s.id), ['complete', 'dedupe', 'audit', 'records', 'quality'])
  assert.equal(defaultSegment('tidy'), 'complete')
  assert.equal(resolveRoute('complete').view, 'tidy')
  assert.equal(resolveRoute('complete').segment, 'complete')
  assert.equal(isView('sources'), true)
  assert.equal(isView('charts'), false) // 榜单已不是顶层页
})

test('源码里每一个 navigateTo(字面量) 都指向已登记的视图（防孤儿页 ①）', () => {
  const ids = new Set()
  for (const file of collectSources(SRC)) {
    const text = readFileSync(file, 'utf8')
    for (const m of text.matchAll(/navigateTo\(\s*'([^']+)'/g)) ids.add(m[1])
  }
  assert.ok(ids.size > 0, '没扫到任何 navigateTo，正则或目录结构变了 —— 这条测试已经失去意义')
  const bad = [...ids].filter((id) => resolveRoute(id).unknown)
  assert.deepEqual(bad, [], `这些 navigateTo 的目标没有登记：${bad.join(', ')}`)
})

test('每个顶层视图与每个分段都登记了图标（漏了就是一个没有图标的按钮）', () => {
  const app = readFileSync(path.join(SRC, 'App.vue'), 'utf8')
  const block = (name) => {
    const m = app.match(new RegExp(`const ${name} = \\{([^}]*)\\}`))
    assert.ok(m, `App.vue 里找不到 ${name}`)
    return new Set([...m[1].matchAll(/([a-zA-Z0-9_]+):/g)].map((x) => x[1]))
  }
  const navIcons = block('NAV_ICONS')
  const segIcons = block('SEGMENT_ICONS')

  const missingNav = VIEW_IDS.filter((id) => !navIcons.has(id))
  assert.deepEqual(missingNav, [], `这些顶层视图缺图标：${missingNav.join(', ')}`)

  const segIds = VIEWS.flatMap((v) => (v.segments || []).map((s) => s.id))
  const missingSeg = segIds.filter((id) => !segIcons.has(id))
  assert.deepEqual(missingSeg, [], `这些分段缺图标：${missingSeg.join(', ')}`)
})

test('App.vue 的分页槽位与注册表一一对应（防孤儿页 ②）', () => {
  const app = readFileSync(path.join(SRC, 'App.vue'), 'utf8')
  const slots = new Set()
  // 只认真正的插槽声明（`<template #view-…>`）——注释里会举例写 `#view-xxx`，别把它当槽位
  for (const m of app.matchAll(/<template\s+#view-([a-z0-9_-]+)/g)) slots.add(m[1])
  assert.deepEqual(
    [...slots].sort(),
    [...VIEW_IDS].sort(),
    'App.vue 的 #view-* 槽位与 navViews.js 的顶层视图不一致（多一个 = 死页，少一个 = 空白页）',
  )
})
