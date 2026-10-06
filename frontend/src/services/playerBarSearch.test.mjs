/* 底部播放栏「搜索滑出/收回」状态机的纯逻辑测试（v2.1.119）。
 *
 * 被测对象是 services/playerBarSearch.js —— 组件里的 computed 没法单测
 * （项目没有 DOM 环境），所以状态转换全拆成了纯函数，这里把转换表逐行钉住。
 * 口径与转换表见 playerBarSearch.js 顶部的表。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  SEARCH_ANIM_MS,
  SEARCH_OPEN_TIMEOUT_MS,
  animMs,
  blurOutcome,
  iconClickNext,
  shouldCollapseSearch,
  timeoutNext,
} from './playerBarSearch.js'

// ── 常量契约 ──────────────────────────────────────────────────────────────

test('常量：超时 8 秒、动画 240ms，CSS 侧要用同一个数', () => {
  assert.equal(SEARCH_OPEN_TIMEOUT_MS, 8000)
  assert.equal(SEARCH_ANIM_MS, 240)
  // 减弱动效：时长必须真的变 0（transition-duration: 0ms = 直接跳终态）
  assert.equal(animMs(true), 0)
  assert.equal(animMs(false), SEARCH_ANIM_MS)
  assert.equal(animMs(undefined), SEARCH_ANIM_MS, '没探测到时按正常动画算')
})

// ── 默认态 ────────────────────────────────────────────────────────────────

test('默认隐藏：不做任何操作时，任何「该收吗」的快照都不能把收归咎于聚焦豁免', () => {
  // 空快照（hidden 态什么都没发生）不该误收 —— 组件里 hidden 态根本不问这个函数，
  // 但这里钉住「没有触发源时结果必须是 false」，防止有人把默认值写成 true。
  assert.equal(shouldCollapseSearch({}), false)
  assert.equal(shouldCollapseSearch(), false)
})

// ── 收回触发源：点别处 / 提交 / 开始播放 / Esc / 超时 ─────────────────────

test('shouldCollapseSearch：五个触发源各自成立，合取也成立', () => {
  for (const trigger of [
    { outside: true },    // 点了别处
    { submitting: true }, // 提交搜索
    { playing: true },    // 开始播放
    { escape: true },     // Esc
    { timeout: true },    // 展开超时
  ]) {
    assert.equal(shouldCollapseSearch(trigger), true, JSON.stringify(trigger))
  }
  assert.equal(shouldCollapseSearch({ outside: true, timeout: true }), true)
})

// ── 聚焦与内容豁免（两条硬规则）──────────────────────────────────────────

test('聚焦绝不收：任何触发源都压不过 focus=true', () => {
  for (const trigger of [
    { focus: true, outside: true },
    { focus: true, timeout: true },
    { focus: true, submitting: true },
    { focus: true, playing: true },
    { focus: true, escape: true },
  ]) {
    assert.equal(shouldCollapseSearch(trigger), false, `聚焦时 ${JSON.stringify(trigger)} 不许收`)
  }
})

test('有词未提交不算闲置：超时/点别处都不收（用户还在组织词句）', () => {
  assert.equal(shouldCollapseSearch({ timeout: true, query: '晴天' }), false)
  assert.equal(shouldCollapseSearch({ outside: true, query: '晴天' }), false)
  // 但「播放/提交/Esc」是明确的意图信号 —— 有词也要收
  assert.equal(shouldCollapseSearch({ submitting: true, query: '晴天' }), true)
  assert.equal(shouldCollapseSearch({ playing: true, query: '晴天' }), true)
  assert.equal(shouldCollapseSearch({ escape: true, query: '晴天' }), true)
})

// ── 失焦分支 ─────────────────────────────────────────────────────────────

test('blurOutcome：空词失焦就收，有词失焦留着（要留出点按钮的时间）', () => {
  assert.equal(blurOutcome(''), 'collapse')
  assert.equal(blurOutcome('   '), 'collapse', '纯空格当空词')
  assert.equal(blurOutcome('周杰伦'), 'keep')
})

// ── 图标点击 ─────────────────────────────────────────────────────────────

test('iconClickNext：点图标总是到 open 并（重新）武装超时哨兵', () => {
  assert.deepEqual(iconClickNext('hidden'), { state: 'open', armTimer: true })
  assert.deepEqual(iconClickNext('open'), { state: 'open', armTimer: true })
})

// ── 超时哨兵的竞态兜底 ───────────────────────────────────────────────────

test('timeoutNext：到点时再问一次快照 —— 聚焦/有词就不收', () => {
  assert.equal(timeoutNext({}), 'hidden')
  assert.equal(timeoutNext({ focus: true }), 'open', '定时器到点前用户刚聚焦 → 不能收')
  assert.equal(timeoutNext({ query: '晴天' }), 'open')
  assert.equal(timeoutNext({ focus: false, query: '' }), 'hidden')
})

// ── 源码约定：组件必须真的用这套状态机 ───────────────────────────────────
// 组件可以写出「看起来对」的 CSS，但如果聚焦豁免/超时竞态没接上，
// 真机上就是「打着字框没了」。这里钉住接线（判据见 pageUi.test.mjs 的同款做法）。

test('PlayerBar 源码约定：聚焦/失焦/超时/播放都接到 shouldCollapseSearch', async () => {
  const { readFileSync } = await import('node:fs')
  const { dirname, join } = await import('node:path')
  const { fileURLToPath } = await import('node:url')
  const here = dirname(fileURLToPath(import.meta.url))
  const src = readFileSync(join(here, '../components/PlayerBar.vue'), 'utf8')

  assert.ok(src.includes('shouldCollapseSearch'), '组件要做收回判定时必须问状态机')
  assert.ok(src.includes('blurOutcome'), '失焦必须走 blurOutcome（有词不收）')
  assert.ok(src.includes('SEARCH_OPEN_TIMEOUT_MS'), '超时时长必须用常量，不许组件里私定一个')
  assert.ok(src.includes('prefers-reduced-motion') || src.includes('reducedMotion'), '组件要探测减弱动效')
  // Esc 逃生口（可访问性）：keydown 监听里要有 Escape
  assert.ok(/Escape/i.test(src), '必须响应 Esc 收起搜索框')
  // 聚焦硬规则：focusin/focusout 至少接一个
  assert.ok(/@focus(in|out)|onFocus|focus/.test(src), '输入框要有焦点事件处理')
})

test('样式约定：滑出动画走 transform（合成器通道），且减弱动效下直接显隐', async () => {
  const { readFileSync } = await import('node:fs')
  const { dirname, join } = await import('node:path')
  const { fileURLToPath } = await import('node:url')
  const here = dirname(fileURLToPath(import.meta.url))
  const css = readFileSync(join(here, '../../src/style.css'), 'utf8')

  assert.ok(css.includes('.ys-barsearch'), '搜索框滑出容器类名 .ys-barsearch 必须存在')
  // 拿动画只许碰 transform/opacity —— 会触发 layout 的属性（width/height/left/top）会掉帧
  const block = css.match(/\.ys-barsearch\s*{[^}]*}/)?.[0] || ''
  assert.ok(block, '.ys-barsearch 规则块存在')
  assert.ok(!/\b(width|height|left|top|margin)\s*:/.test(block.replace(/min-width|max-height/g, '')), '容器块里不许有触发重排的动画属性')
  // 减弱动效：transition-duration 0
  const rm = css.match(/@media \(prefers-reduced-motion: reduce\)[^}]*{[^@]*\.ys-barsearch[^}]*}/)
  assert.ok(rm, 'prefers-reduced-motion 块里要覆盖 .ys-barsearch')
  assert.match(rm[0], /transition-duration:\s*0/)
})
