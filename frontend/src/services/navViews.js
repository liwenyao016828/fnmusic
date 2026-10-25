/**
 * 顶层视图注册表（单一数据源）。
 *
 * 2026-10-15 引入；2026-10-17 随「导航框架重做」扩成 8 视图 + 分组。
 * 「有哪些顶层页」只在这里定义一次：导航（桌面侧栏 / 手机底部标签栏）、
 * 滑动顺序、页内分段、深链解析、角标全部读它。
 *
 * 2026-10-17 重做的动机：原「曲库管家」一页塞了 5 个标签（本地音乐/推送/下载/
 * 补全整理/自动下载记录），一个 3480 行的巨型组件；顶层导航只有 5 项，
 * 但真正高频的功能（下载队列、订阅）都藏在二级标签里。现在按**用户功能**拆平：
 *
 * ```
 * listen 组：发现音乐（发现/榜单） · 本地曲库
 * manage 组：补全整理 · 订阅更新 · 下载（队列/歌单导入/记录）
 * config 组：音源管理 · 账号连接 · 系统（日志/设置）
 * ```
 *
 * 页内分段（segments）：把「二级但不该单独占导航位」的页并进顶层视图，
 * 深链仍可直接命中某一段（resolveRoute('autodl') → download/autodl）。
 *
 * 分组（group）只影响**呈现**（桌面侧栏的分组标题；手机底部栏取前 4 个
 * 高频视图 + 「更多」抽屉），不影响 id 与深链。
 */

/** 导航分组。桌面侧栏按此顺序分节；手机底部标签栏不用它。 */
export const NAV_GROUPS = [
  { id: 'listen', name: '听' },
  { id: 'manage', name: '管' },
  { id: 'config', name: '配置' },
]

/**
 * 顶层视图。数组顺序 = 桌面侧栏顺序 = 手指左右滑动的顺序。
 *
 * `desc` = 该页的**一句简介**，2026-10-18 用户口径后它的唯一去处是
 * **顶部一条栏里页面名的悬停提示**（「把导航里的其他页面的简介也去掉改hover」）。
 * 各页组件里那份常驻的说明文字、以及 PageShell 的 desc 参数都已撤掉 ——
 * 要改页面简介，只改这里，别再往页面里塞常驻文字。
 * 分段（segments）也可以带自己的 desc，悬停时会优先用它（段比页更具体）。
 */
export const VIEWS = [
  {
    id: 'search',
    name: '发现音乐',
    short: '发现',
    group: 'listen',
    desc: '搜索全网曲库，或浏览官方榜单与精选歌单',
    segments: [
      { id: 'discover', name: '发现', short: '发现', desc: '官方平台的推荐歌单：点卡片进歌单详情，右下角可以整单下载' },
      { id: 'charts', name: '榜单', short: '榜单', desc: '勾选要注入的榜单（它们会以在线歌单出现在飞牛音乐里），只保留能播放的曲目' },
    ],
  },
  {
    id: 'library',
    name: '本地曲库',
    short: '曲库',
    group: 'listen',
    desc: '浏览 NAS 上的本地音乐：目录 / 歌手 / 专辑视图，点击即播',
  },
  {
    id: 'tidy',
    name: '补全整理',
    short: '整理',
    group: 'manage',
    desc: '扫描缺失，按字段补全歌词 / 封面 / 年份，以及歌名修复与查重',
    segments: [
      { id: 'complete', name: '补全', short: '补全', desc: '扫描字段缺口，勾选要补的字段；预览确认后才写回文件' },
      { id: 'dedupe', name: '查重', short: '查重', desc: '内容相同 / 同名不同版的重复曲目：保留一份，其余移入回收站' },
      // 2026-10-24 用户：「去除体检中的曲库体检，更名为歌名修复，页面加入歌名修复功能」；
      // 「记录更名为数据快照」。id 保持不变（老的 URL / 上次停留的段还能对上）。
      { id: 'audit', name: '歌名修复', short: '修复', desc: '没有歌名 / 歌名就是文件名的曲目交给 AI 修；另含文件名体检与修复记录' },
      { id: 'records', name: '数据快照', short: '快照', desc: '每次补全 / 修复前的快照，可逐条撤回；另有定时与手动快照' },
      { id: 'quality', name: '替换音质', short: '音质', desc: '逐条对比洛雪 / musicdl / 原生能拿到的最高档位，勾选后替换本地文件' },
    ],
  },
  {
    id: 'push',
    name: '订阅更新',
    short: '订阅',
    group: 'manage',
    desc: '订阅歌单，自动追新下载，并同步到飞牛音乐的歌单',
  },
  {
    id: 'download',
    name: '下载',
    short: '下载',
    group: 'manage',
    desc: '下载队列 · 歌单下载 · 自动下载记录',
    segments: [
      { id: 'queue', name: '队列', short: '队列', desc: '正在进行与已完成的下载任务；取消过的任务不会自己复活' },
      { id: 'playlist', name: '歌单导入', short: '歌单', desc: '粘贴歌单链接，整单解析后批量下载' },
      { id: 'autodl', name: '自动记录', short: '记录', desc: '收藏 / 播放触发的自动下载流水' },
    ],
  },
  {
    id: 'sources',
    name: '音源管理',
    short: '音源',
    group: 'config',
    desc: '官方协议（免脚本，负责搜索 / 榜单 / 歌单 / 歌词）与第三方音源的导入、测速与开关',
  },
  {
    id: 'accounts',
    name: '账号连接',
    short: '账号',
    group: 'config',
    desc: '飞牛音乐与 AI 大模型的连接凭据都放在这里，只保存在本机（权限 0600），不上传任何第三方',
  },
  {
    id: 'system',
    name: '系统',
    short: '系统',
    group: 'config',
    desc: '运行日志与各项设置',
    // 顺序 = 分段器上的先后，也决定「打开这一页先落在哪一段」（defaultSegment 取 [0]）。
    // 用户 2026-10-24：设置在前，日志在后 —— 打开「系统」是想改东西，不是先看日志。
    segments: [
      { id: 'settings', name: '设置', desc: '下载音质、文件命名、存储目录与运行日志；改完即刻生效，不需要重启' },
      { id: 'logs', name: '日志', desc: '界面上的报错只给一句结论，完整信息都在这里；也可以一键导出打包' },
    ],
  },
]

export const VIEW_IDS = VIEWS.map((v) => v.id)

/**
 * 手机底部标签栏：4 个高频视图 + 「更多」抽屉（抽屉里是其余全部）。
 *
 * 2026-10-18 用户口径：「补全整理和下载替换一下，下载放到更多里面」——
 * 手机上真正天天点的是**整理**（歌词/封面/查重，进去就要处理一批），
 * 而下载队列是「挂上去就不管」的，更适合收进抽屉（抽屉里还有任务状态列表兜着）。
 * 桌面侧栏不受影响：8 个视图照样全在。
 */
export const MOBILE_TAB_IDS = ['search', 'library', 'push', 'tidy']

export function viewsOfGroup(groupId) {
  return VIEWS.filter((v) => v.group === groupId)
}

/** 默认落在哪一段（没有分段时返回空串） */
export function defaultSegment(viewId) {
  const v = VIEWS.find((x) => x.id === viewId)
  if (!v || !v.segments || !v.segments.length) return ''
  return v.segments[0].id
}

export function segmentsOf(viewId) {
  const v = VIEWS.find((x) => x.id === viewId)
  return (v && v.segments) || []
}

export function isView(viewId) {
  return VIEW_IDS.includes(viewId)
}

/**
 * 老 id / 别名 → 新位置。
 *
 * 必须保留这层映射，否则这些历史入口会静默失效：
 *   · 组件里的 `navigateTo('logs')` / `navigateTo('charts')` / `navigateTo('settings')`
 *     （十几处：报错提示里的「看日志」、榜单的「▶ 看一眼」、设置里跳日志等）
 *   · 浏览器偏好里存的旧 id（library_tab='nas'/'complete'/'autodl'…）
 *   · 收藏/外链里手工敲过的地址
 * 2026-10-17 把「曲库管家」的 5 个标签拆成 4 个顶层视图，这里就是那一刀。
 */
const ALIASES = {
  charts: { view: 'search', segment: 'charts' },
  discover: { view: 'search', segment: 'discover' },
  logs: { view: 'system', segment: 'logs' },
  settings: { view: 'system', segment: 'settings' },
  // 历史写法：顶栏胶囊曾是唯一的音源入口，个别地方写成 netsources
  netsources: { view: 'sources', segment: '' },
  // ── 原「曲库管家」侧栏项（library_tab 偏好 / statusItems 跳转 / 下载抽屉「查看队列」）──
  nas: { view: 'library', segment: '' },
  complete: { view: 'tidy', segment: '' },
  queue: { view: 'download', segment: 'queue' },
  playlist: { view: 'download', segment: 'playlist' },
  autodl: { view: 'download', segment: 'autodl' },
}

/**
 * 解析任意视图 id（含历史别名）成 { view, segment }。
 *
 * 认不出时**回落到发现页并标记 unknown**：宁可多跳一次首页，也不要让
 * 「点了一下什么反应都没有」发生（那种 bug 用户只会报「按钮坏了」）。
 * 调用方可以据此记一条 warn 日志，把漏登记的 id 暴露出来。
 *
 * @param {string} id 视图 id（可能是老 id / 别名）
 * @param {string} [segment] 想去的分段；不传或该视图没有这段时用默认段
 * @returns {{ view: string, segment: string, unknown: boolean }}
 */
export function resolveRoute(id, segment) {
  const alias = ALIASES[id]
  const view = alias ? alias.view : id
  const unknown = !alias && !isView(id)
  const target = unknown ? 'search' : view

  const segs = segmentsOf(target)
  let seg = segment || (alias ? alias.segment : '') || defaultSegment(target)
  // 没有分段的页一律空串（模板据此决定渲不渲分段器）；有分段但认不出指定值时回落第一段
  if (!segs.length) seg = ''
  else if (!segs.some((s) => s.id === seg)) seg = segs[0].id

  return { view: target, segment: seg, unknown }
}
