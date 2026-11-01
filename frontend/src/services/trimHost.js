/**
 * 飞牛宿主 JS SDK（@trimjs/web-app）薄封装。
 *
 * 为什么要它：曲率是「微应用」（fpk manifest 里 `micro_app=true`），跑在飞牛
 * 桌面的 iframe 里。以前选曲库目录只能靠自己画的 `NasDirPicker`（走
 * `/api/nas/browse`）一层层点，或者干脆手打路径 —— 用户明确抱怨过
 * 「选择目录的按键…点击后不是弹窗选择，而是必须自己手动输入」。
 *
 * 现在接官方 SDK 后，多了一条路：直接调起**宿主自己的目录选择器**，而且选中
 * 的路径会被宿主**授权给本应用**（这是自己在页面里画选择器做不到的，授权是
 * 宿主权限模型的一部分）。
 *
 * ⚠️ 2026-10-31 真机返工：第一版用 `getHostCapabilities().runtime` 当「有没有宿主」
 * 的判据，结果在飞牛桌面里**永远是 unknown** —— 门户父窗口注入给 iframe 的 config
 * 只有 `{appName, os, appMeta}`，从来不注入 `host`（实测 /usr/trim/www 全量 grep：
 * `host:` 与 `sessionCredentials` 命中 0 次）。于是按钮永不渲染、点了立刻返回
 * 「当前不在飞牛宿主环境中」，用户看到的就是「点了，没反应」。
 *
 * 正确的判据是**桥接方法表**：`osConnector.methods` 里有没有 `pickSharedFile`。
 * 那是宿主真正实现的函数（门户 bundle 里 pickSharedFile / authorizeSharedFile /
 * openAppSetting 都在），跟 `config.host` 这个可选字段无关。官方应用
 * （fn-appdownload、clash-for-fnos）内嵌同一份 SDK 时也不做 runtime 门控。
 *
 * 三条设计约束（都踩过坑，别改回去）：
 *
 * 1. **探测失败只降级，不隐藏入口**。SDK 在普通浏览器里连不上宿主，探测会失败；
 *    但把按钮整个藏起来会让飞牛用户也看不到入口（见上面 2026-10-31 的返工）。
 *    所以按钮始终在，点击时才去探测 + 明确告诉用户「为什么不行、该怎么办」。
 *
 * 2. **SDK 必须懒加载 + 代码分割**。它是个 40KB 的自包含包，只有真正要选目录
 *    时才需要；`import()` 动态加载让 Vite 把它切成独立 chunk，首屏不受影响。
 *
 * 3. **探测结果缓存，但可强制重探**。宿主握手有往返，每次开弹窗都探一遍会卡；
 *    但宿主也可能「第一次握慢了」，所以调用前发现没桥接时会强制重探一次。
 */

import { ref } from 'vue'

/**
 * 预览开关：`localStorage.trim_force_host = '1'` 后刷新页面，
 * 「用飞牛的文件选择器」那行会**假装**宿主存在并显示出来，方便在开发机
 * （纯浏览器，没有飞牛宿主）上核对排版。点了会得到一句明确的「预览模式」提示，
 * 不会真的去调宿主。看完 `localStorage.removeItem('trim_force_host')` 复原。
 */
const FORCE_KEY = 'trim_force_host'

function forced() {
  try {
    return localStorage.getItem(FORCE_KEY) === '1'
  } catch {
    return false
  }
}

/** 宿主运行时类型（仅供日志/排障；`unknown` 不代表不能调，见文件头）。 */
export const trimRuntime = ref('unknown')
/** 是否已经探测过（UI 据此区分「还没探完」和「探完但没有」）。 */
export const trimProbed = ref(false)
/** 是否是上面的预览模式（点了按钮只会给提示，不会真调宿主）。 */
export const trimForced = ref(false)
/** 桥接里到底有没有目录选择能力 —— 这是唯一的可用性判据。 */
export const trimBridgeReady = ref(false)

let sdk = null
let detectPromise = null

/** 懒加载 SDK 单例。 */
async function loadSdk() {
  if (!sdk) {
    const mod = await import('@trimjs/web-app')
    sdk = new mod.TrimApp()
  }
  return sdk
}

/** 给 SDK 调用套一个超时，避免宿主无响应时永久挂住 UI。 */
function withTimeout(promise, ms, label) {
  return Promise.race([
    promise,
    new Promise((_, reject) => {
      setTimeout(() => reject(new Error(`${label} 超时（${ms}ms）`)), ms)
    }),
  ])
}

/**
 * 桥接方法表里有没有指定的宿主方法。
 *
 * 导出成纯函数是为了能单测：这是「能不能用宿主选择器」的**唯一**判据，
 * 判错了整条授权链路就哑了（2026-10-31 就是这么哑的）。
 */
export function bridgeSupportsPicker(methods, name = 'pickSharedFile') {
  return !!methods && typeof methods[name] === 'function'
}

/**
 * 飞牛 **App（手机端 / WebView）** 里有没有目录选择能力。
 *
 * 为什么要单独一个判据（2026-10-31 用户追问「我在飞牛 app 里打开应用，为什么就没出现
 * 飞牛选择器」）：手机端根本不走 `osConnector`。SDK 的初始化分支是
 *   · 桌面 = iframe + penpal 桥接 → 能力挂在 `osConnector.methods`（上面那个判据）；
 *   · App  = Flutter WebView → 能力挂在 `window.flutter_inappwebview`，
 *     `getHostCapabilities()` 直接返回 `{runtime:'mobile'}`，`osConnector` 恒为 null。
 * 所以光看 `osConnector.methods` 会把手机端判成「没有宿主」，按钮点了只说「不在飞牛桌面里」——
 * 这正是用户看到的症状。
 *
 * 判据同样不信任 runtime 字段（它对桌面端已经错了一次），而是问 SDK 自己有没有拿到
 * WebView 通道：`flutterInAppWebView` 是 SDK 在 init 里探测到的句柄，有它才可能调通
 * `pickSharedFile`。纯函数导出是为了能单测。
 */
export function mobileBridgeReady(app) {
  if (!app) return false
  // SDK 内部字段（dist 里 `flutterInAppWebView`），初始化完成后才被赋值。
  if (app.flutterInAppWebView) return true
  return false
}

/** 宿主握手最长等多久。SDK 自己的 qe() 探测就占 1.5s，加上 penpal 往返，8s 有余量。 */
const HANDSHAKE_TIMEOUT_MS = 8000

/**
 * 探测是否跑在飞牛宿主里。幂等、并发安全、结果缓存；`force` 丢弃缓存重探。
 *
 * 判据（按可靠性排序）：
 *   1. `osConnector.methods.pickSharedFile` 是不是函数 —— 决定一切；
 *   2. `getHostCapabilities().runtime` 只作为写进 `trimRuntime` 的旁证：
 *      飞牛桌面里它恒为 `unknown`（父窗口不注入 config.host），**不能**当判据。
 */
export async function detectTrimHost({ force = false } = {}) {
  if (force) {
    detectPromise = null
    trimProbed.value = false
    trimBridgeReady.value = false
    trimRuntime.value = 'unknown'
  }
  if (trimProbed.value) return trimRuntime.value
  if (forced()) {
    // 预览模式：不加载 SDK、不发任何请求，直接装成宿主在旁边。
    trimRuntime.value = 'desktop'
    trimBridgeReady.value = true
    trimForced.value = true
    trimProbed.value = true
    return trimRuntime.value
  }
  if (!detectPromise) {
    detectPromise = (async () => {
      let app = null
      try {
        app = await loadSdk()
        // 等 SDK 与父窗口握手完成（ready() 就是 initPromise）。顶层窗口
        // （window.parent === window）时 SDK 会直接跳过桥接，这里会很快返回，
        // 随后 methods 为空 → 正确判成「没有宿主」。
        await withTimeout(app.ready ? app.ready() : app.initPromise, HANDSHAKE_TIMEOUT_MS, '宿主桥接握手')
        // 桌面走 osConnector.methods，App（WebView）走 flutterInAppWebview 通道 —— 两条都要认，
        // 否则手机端会被判成「没有宿主」（2026-10-31 用户反馈的正是这个）。
        trimBridgeReady.value = bridgeSupportsPicker(app?.osConnector?.methods) || mobileBridgeReady(app)
      } catch {
        // 连不上宿主属于预期情况（开发机就是纯浏览器），静默降级。
        trimBridgeReady.value = false
      }
      // runtime 只用于日志与排障：桥接可用时记 desktop，否则问一次 SDK 的真实答案。
      if (trimBridgeReady.value) {
        // 有桥接就按通道类型标注：桌面 osConnector / App WebView。
        trimRuntime.value = bridgeSupportsPicker(app?.osConnector?.methods) ? 'desktop' : 'mobile'
      } else {
        try {
          const caps = await withTimeout(loadSdk().then((a) => a.getHostCapabilities()), 2500, '宿主能力探测')
          trimRuntime.value = caps?.runtime || 'unknown'
        } catch {
          trimRuntime.value = 'unknown'
        }
      }
      trimProbed.value = true
      return trimRuntime.value
    })()
  }
  return detectPromise
}

/**
 * 是否具备宿主目录选择能力（同步读缓存，供模板判断文案用）。
 *
 * ⚠️ 不要用它来决定「按钮显不显示」：模板里藏按钮会让用户在飞牛里也看不到入口
 * （2026-10-31 的故障就是这么来的）。按钮始终显示，点击时再探测。
 */
export function hasTrimPicker() {
  return trimProbed.value && trimBridgeReady.value
}

/** 调用前的统一准备：先探，没探到再强制重探一次（宿往往第一次握手会慢）。 */
async function ensureBridge() {
  await detectTrimHost()
  if (!trimBridgeReady.value && !trimForced.value) {
    await detectTrimHost({ force: true })
  }
  return trimBridgeReady.value
}

/** 不在宿主里时给用户看的**可执行**文案（不是「失败了」三个字）。 */
const NO_HOST_HINT =
  '当前不在飞牛桌面里（浏览器直接打开时没有系统文件选择器）。请从飞牛桌面的「曲率」图标进入，' +
  '或到「应用中心 → 曲率 → 设置 → 授权目录」里添加目录。'

/**
 * 把 SDK 的 `<AppBridgeResponse<string[]>>` 归一化。
 *
 * 取消不是错误：宿主点「取消」返回的是空 data 或 undefined，调用方据此静默收场，
 * 不能弹红色报错吓人。
 */
function normalize(res) {
  const list = Array.isArray(res?.data) ? res.data : []
  const path = list.find((p) => typeof p === 'string' && p.trim()) || ''
  if (path) return { ok: true, path }
  // code 非 0 才算宿主明确报错；否则当作「用户取消」。
  if (res && typeof res.code === 'number' && res.code !== 0) {
    return { ok: false, cancelled: false, error: res.msg || `宿主返回 code=${res.code}` }
  }
  return { ok: false, cancelled: true, error: '' }
}

/**
 * 打开「本应用的设置页」（官方 JS SDK `openAppSetting()`，飞牛 1.2.0401 / App 1.34.0 起）。
 *
 * 这是官方文档给「没接入授权接口的应用」准备的兜底入口，也是用户想手动管理
 * 「授权目录」时唯一正确的一页 —— 比自己写一句「你去应用中心点」有用得多。
 */
export async function openAppSetting() {
  if (trimForced.value) {
    return { ok: false, error: '这是排版预览模式，设置页要在飞牛桌面的应用里才能打开' }
  }
  const ready = await ensureBridge()
  if (!ready) return { ok: false, error: NO_HOST_HINT }
  const app = await loadSdk()
  try {
    await withTimeout(app.openAppSetting(), 15000, '打开应用设置')
    return { ok: true, error: '' }
  } catch (e) {
    return { ok: false, error: String(e?.message || e || '打开应用设置失败') }
  }
}

/**
 * 把目录授权给本应用（共享授权 = 全应用可用，仅管理员）。
 *
 * `authorizeSharedFile(path)` 返回 `data: boolean`，true 表示授权成功。
 * 注意它和 `pickSharedFile` 的分工：picker 选目录时宿主一般已经顺手授权了，
 * 这个函数用于「路径已知、只补授权」的补救场景。
 */
export async function authorizeSharedDir(path) {
  const p = String(path || '').trim()
  if (!p) return { ok: false, error: '路径为空' }
  if (trimForced.value) return { ok: false, error: '这是排版预览模式，授权要在飞牛桌面的应用里才能做' }
  const ready = await ensureBridge()
  if (!ready) return { ok: false, error: NO_HOST_HINT }
  const app = await loadSdk()
  try {
    const res = await withTimeout(app.authorizeSharedFile(p), 30000, '共享授权')
    return normalizeBool(res)
  } catch (e) {
    return { ok: false, error: String(e?.message || e || '共享授权失败') }
  }
}

/**
 * 统一「选目录」入口：**能弹宿主选择器就直接弹，弹不出来才退回自绘目录树**。
 *
 * 为什么要有这一层（2026-10-31 用户原话）：「我觉得用和点击选择后可以不需要出我自己的
 * 目录选择器了，直接用飞牛的文件选择器。」在那之前，AppSettings / TidyDirChip 点「选择」
 * 打开的都是曲率自绘的 NasDirPicker，飞牛选择器只是那颗弹窗**里面**的一颗按钮 ——
 * 用户必须先穿过一层自己的 UI 才能摸到系统选择器，而且曲率自己画的树在「还没授权」
 * 时根本是空的（没授权就看不见任何目录），第一次配曲库等于没路可走。
 *
 * 顺序：
 *   1. 宿主桥接可用 → 直接 pickSharedDirectory()（选完即共享授权给本应用）；
 *   2. 桥接不可用（普通浏览器 / 开发机 / 飞牛里 SDK 没握上手）→ `fallback: true`，
 *      调用方打开自绘 NasDirPicker —— **不能留死按钮**，浏览器里也得能选目录；
 *   3. 宿主报错（非管理员、路径不支持等）→ 同样带回 `fallback: true` 和一句人话。
 *
 * 返回值统一成 `{ ok, path, cancelled, error, fallback }`：
 *   ok=true            → 拿到了路径，调用方直接用；
 *   cancelled=true     → 用户自己点了取消，**静默**收场（不要弹红字）；
 *   fallback=true      → 调用方该打开自绘弹窗，并把 error 当提示挂上去；
 *   其余               → 真失败，error 是要显示的那句话。
 *
 * ⚠️ 不要把探测结果当门控藏按钮（2026-10-31 真机返工）：这里只在**点击之后**才探测，
 * 而且探不到就走回退路径，界面上的按钮永远在。
 */
export async function chooseDirectory({
  title = '选择目录',
  okText = '用这个目录',
  creatable = true,
} = {}) {
  const ready = await ensureBridge()
  if (!ready) {
    // 不在宿主里不是「错了」，是「这条路在浏览器里不通」—— 直接走回退，
    // 让用户用自绘目录树选（开发机 / 手机浏览器都有这条路）。
    return { ok: false, cancelled: false, fallback: true, error: '' }
  }
  const res = await pickSharedDirectory({ title, okText, creatable })
  if (res.ok) return { ok: true, path: res.path, cancelled: false, error: '', fallback: false }
  if (res.cancelled) return { ok: false, cancelled: true, error: '', fallback: false }
  // 宿主明确不肯弹（仅管理员可授权 / 当前窗口类型不支持等）：把原话带上去，
  // 同时允许回退到自绘树 —— 自绘树只需要「已授权目录」，可能还读得出来。
  return { ok: false, cancelled: false, fallback: true, error: res.error || '' }
}

/** `authorize*File` 的返回是 boolean（不是路径数组），单独归一化。 */
function normalizeBool(res) {
  if (res && typeof res.code === 'number' && res.code !== 0) {
    return { ok: false, error: res.msg || `宿主返回 code=${res.code}` }
  }
  if (res?.data === true) return { ok: true, error: '' }
  if (res?.data === false) {
    return { ok: false, error: res.msg || '宿主没有确认授权（可能被拒绝或路径不支持）' }
  }
  return { ok: false, error: '宿主未返回授权结果' }
}

/**
 * 选一个目录并**共享授权**给本应用（唯一的授权入口，管理员专属）。
 *
 * 官方语义：`pickSharedFile` / `authorizeSharedFile` 只允许管理员调用；普通用户
 * 直调会得到 `code:1 "仅管理员可进行此操作"`。
 *
 * 2026-10-31 用户明确「本应用只给管理员自己用」，于是把两个授权入口收敛成这一个：
 * 个人授权（pickUserFile）已移除 —— 它绑在登录账号上，换账号或 uid 变化就要重新
 * 授权，单管理员场景只会多一个会失效的来源。共享授权不按用户区分，更稳。
 *
 * 参数说明（用户要求「点盘位就能选、还能新建文件夹」）：
 * · `creatable: true` —— 官方 SharedFilePickerParams 里的「宿主支持时允许在选择器里
 *   新建文件夹」。用户常常是「想建一个 Music 目录但还没建」。宿主不支持时选择器
 *   自己就不显示新建按钮，不会报错。
 * · `sidebarGroup` —— 官方参数表里**没有**「初始路径」，能影响「从哪儿开始看」的只有
 *   左侧分组：露出「我的文件 / 其他共享 / 外接存储」三组，飞牛的盘位就是从这三处进去的。
 */
export async function pickSharedDirectory({
  title = '选择要授权给曲率的目录',
  okText = '授权这个目录',
  creatable = true,
} = {}) {
  if (trimForced.value) {
    return { ok: false, cancelled: false, error: '这是排版预览模式，飞牛选择器要在飞牛桌面的应用里才能用' }
  }
  const ready = await ensureBridge()
  if (!ready) return { ok: false, cancelled: false, error: NO_HOST_HINT }
  const app = await loadSdk()
  try {
    const res = await withTimeout(
      app.pickSharedFile({ title, okText, creatable, sidebarGroup: ['myFiles', 'otherShare', 'external'] }),
      120000,
      '共享目录选择',
    )
    return normalize(res)
  } catch (e) {
    return { ok: false, cancelled: false, error: String(e?.message || e || '') }
  }
}