/**
 * 底部播放栏的「搜索滑出/收回」状态机 —— **纯判定逻辑**（v2.1.119）。
 *
 * # 现状与目标
 *
 * 播放栏原来常驻；这版起默认隐藏，只在底部右下角留一颗搜索图标。
 * 点图标 → 搜索框从图标的位置**滑动放大**出来（transform 过渡，见 style.css 的
 * `.ys-barsearch-*`）；输入框聚焦期间绝不收回；点别处 / 超时 / 开始播放 →
 * 整框**滑动缩小**回图标位置。系统开了「减弱动效」就直接显隐，不做动画。
 *
 * # 为什么单独一个文件
 *
 * 本项目前端测试是 `node --test`，**没有 DOM**（见 swipeNav.js 的同款说明）。
 * 「什么时候能收、聚焦算不算别处、超时怎么续命」这些状态转换全是纯判断 ——
 * 拆到这里用 `playerBarSearch.test.mjs` 钉住；组件只负责把 DOM 事件翻译成输入。
 *
 * # 状态与转换表
 *
 * state: 'hidden'（只剩图标）| 'open'（框已展开）
 * 变量：focus（输入框是否聚焦）、query（输入内容，非空也要算「正在用」）、
 *       playing（是否正在播放）、timer（展开时长哨兵，由组件持有）
 *
 * ┌───────────────────────┬────────────────────────────────────────────┐
 * │ 输入（在什么状态下）    │ 结果                                          │
 * ├───────────────────────┼────────────────────────────────────────────┤
 * │ 点搜索图标（hidden）    │ → open，重置展开计时                          │
 * │ 输入框聚焦（open）      │ → 保持 open，**取消收回计时**（聚焦绝不收）      │
 * │ 输入框失焦（open）      │ query 为空 → 收回；非空 → 保持（用户还要提交）    │
 * │ 点了框外（open）        │ → 收回（视同失焦，见 outsideClickCloses）       │
 * │ 提交搜索（任意）        │ → 收回（词已交给发现页，框没有留下的理由）        │
 * │ 开始播放（open）        │ → 收回                                        │
 * │ Esc（open）            │ → 收回（可访问性：键盘逃生口）                   │
 * │ 展开超时（open，未聚焦）│ → 收回；聚焦期间超时**不许**触发                 │
 * │ reduced-motion（任意）  │ 动画时长 = 0（直接显隐，状态转换不变）           │
 * └───────────────────────┴────────────────────────────────────────────┘
 */

/** 展开后无操作多少毫秒自动收回（用户在选词 / 抄歌词，别收太快） */
export const SEARCH_OPEN_TIMEOUT_MS = 8000

/** 展开/收回动画时长（ms）。CSS 里 .ys-barsearch 的 transition 用同一个数 */
export const SEARCH_ANIM_MS = 240

/**
 * 收回判定的唯一入口：给当前快照，回答「现在该不该收」。
 *
 * 两类触发源待遇不同：
 *   · **闲置类**（timeout 展开超时 / outside 点了别处）—— 有词或聚焦就不算闲置：
 *     用户可能还在组织词句，或者刚把焦点给出去。聚焦在任何情况下都不收（硬规则）。
 *   · **意图类**（submitting 提交 / playing 开始播放 / escape 按 Esc）—— 明确的
 *     「我要走了」，有词也收（词已经交给发现页了，框没有留下的理由）。
 * 组件里超时哨兵触发前会再问一次这个函数，所以打盹唤醒后不会把正在输入的人收掉。
 * `query` 按**去掉首尾空白**判「有没有词」—— 与提交侧的 trim() 同一口径：
 * 打了几个空格不算「在组织词句」，超时照收。
 */
export function shouldCollapseSearch(snapshot = {}) {
  const { focus = false, query = '', submitting = false, playing = false, escape = false, outside = false, timeout = false } = snapshot
  if (focus) return false            // 聚焦绝不收（硬规则，压过一切）
  if (submitting || playing || escape) return true // 意图类：直接收
  // 闲置类：有词 = 还在用。空格不算词（trim 后判，与提交侧同口径）。
  if (String(query).trim()) return false
  return !!(outside || timeout)
}

/**
 * 输入框失焦该怎么走。
 * query 非空时失焦不收 —— 用户可能要点搜索按钮（按钮被点那一刻输入框已失焦）。
 * 口径与 shouldCollapseSearch 相同：trim 后判空。
 */
export function blurOutcome(query) {
  return String(query ?? '').trim() ? 'keep' : 'collapse'
}

/**
 * 点了搜索图标（当前 hidden）之后的下一状态。
 * 返回新状态与是否要（重新）起超时哨兵 —— 组件据此 setTimeout。
 */
export function iconClickNext(state) {
  if (state === 'open') return { state: 'open', armTimer: true } // 再点一次=续命
  return { state: 'open', armTimer: true }
}

/**
 * 超时哨兵触发时：只有「未聚焦、无内容」才真的收。
 * （竞态兜底：定时器到点前用户刚聚焦 —— 以此刻快照为准，不能凭定时器资格收人。）
 */
export function timeoutNext(snapshot = {}) {
  return shouldCollapseSearch({ ...snapshot, timeout: true }) ? 'hidden' : 'open'
}

/** 尊重系统「减弱动效」：开了就 0ms，动画自然跳到终态 */
export function animMs(reducedMotion) {
  return reducedMotion ? 0 : SEARCH_ANIM_MS
}
