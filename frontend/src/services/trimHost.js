/**
 * 飞牛宿主 JS SDK（@trimjs/web-app）薄封装。
 *
 * 为什么要它：曲率是「微应用」（fpk manifest 里 `micro_app=true`），跑在飞牛
 * 桌面的 iframe 里。以前选曲库目录只能靠自己画的 `NasDirPicker`（走
 * `/api/nas/browse`）一层层点，或者干脆手打路径 —— 用户 2026-10-25 明确抱怨过
 * 「选择目录的按键…点击后不是弹窗选择，而是必须自己手动输入」。
 *
 * 现在接官方 SDK 后，多了一条路：直接调起**宿主自己的目录选择器**，而且选中
 * 的路径会被宿主**授权给本应用**（这是自己在页面里画选择器做不到的，授权是
 * 宿主权限模型的一部分）。两条路并存，不互相替代。
 *
 * 三条设计约束（都踩过坑，别改回去）：
 *
 * 1. **不在宿主里就绝不显示按钮**。SDK 在普通浏览器里连不上宿主，
 *    `getHostCapabilities()` 会挂到超时。开发机热更新走的是
 *    `http://192.168.1.230:3000/`（纯浏览器，不是宿主 iframe），要是无条件显示
 *    按钮，用户点下去就是一个永远不弹窗的死按钮。所以先探测、探到才显示。
 *
 * 2. **SDK 必须懒加载 + 代码分割**。它是个 40KB 的自包含包，只有真正要选目录
 *    时才需要；`import()` 动态加载让 Vite 把它切成独立 chunk，首屏不受影响。
 *
 * 3. **探测结果缓存，且失败不抛**。宿主探测有网络往返，每次开弹窗都探一遍会卡；
 *    探测失败只是「没有宿主能力」，不是错误，调用方拿 `unknown` 正常降级。
 */

import { ref } from 'vue'

/**
 * 预览开关：`localStorage.trim_force_host = '1'` 后刷新页面，
 * 「用飞牛的文件选择器」那行会**假装**宿主存在并显示出来，方便在开发机
 * （纯浏览器，没有飞牛宿主）上核对排版。点了会得到一句明确的「预览模式」提示，
 * 不会真的去调宿主。看完 `localStorage.removeItem('trim_force_host')` 复原。
 *
 * 之所以要这个口子：这个按钮**只**在飞牛桌面里出现，开发机热更新看不到它，
 * 排版对不对就没法当场验。
 */
const FORCE_KEY = 'trim_force_host'

function forced() {
  try {
    return localStorage.getItem(FORCE_KEY) === '1'
  } catch {
    return false
  }
}

/** 宿主运行时类型；`unknown` = 没探测到宿主（普通浏览器 / 探测失败）。 */
export const trimRuntime = ref('unknown')
/** 是否已经探测过（UI 据此决定「还没探完」和「探完但没有」两种状态）。 */
export const trimProbed = ref(false)
/** 是否是上面的预览模式（点了按钮只会给提示，不会真调宿主）。 */
export const trimForced = ref(false)

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
 * 探测是否跑在飞牛宿主里。幂等、并发安全、结果缓存。
 *
 * 注意 SDK 内部对 `getHostCapabilities()` 自己有 5s 兜底超时，这里再套一层 4s，
 * 是为了让 UI 尽早拿到「没有宿主」的结论，不要白等 5 秒。
 */
export async function detectTrimHost() {
  if (trimProbed.value) return trimRuntime.value
  if (forced()) {
    // 预览模式：不加载 SDK、不发任何请求，直接装成宿主在旁边。
    trimRuntime.value = 'desktop'
    trimForced.value = true
    trimProbed.value = true
    return trimRuntime.value
  }
  if (!detectPromise) {
    detectPromise = (async () => {
      try {
        const app = await loadSdk()
        const caps = await withTimeout(app.getHostCapabilities(), 4000, '宿主能力探测')
        trimRuntime.value = caps?.runtime || 'unknown'
      } catch {
        // 连不上宿主属于预期情况（开发机就是纯浏览器），静默降级。
        trimRuntime.value = 'unknown'
      } finally {
        trimProbed.value = true
      }
      return trimRuntime.value
    })()
  }
  return detectPromise
}

/** 是否具备宿主目录选择能力（同步读缓存，供模板用）。 */
export function hasTrimPicker() {
  return trimProbed.value && trimRuntime.value !== 'unknown'
}

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
 * 调起宿主目录选择器，返回选中的单个目录路径。
 *
 * 优先 `pickUserFile({directory:true})`：曲库一般在 `/vol1/...` 这类**用户文件**
 * 目录下，而且这个接口会在选中后把路径授权给本应用 —— 后端要读曲库，授权是必须的。
 *
 * 只有宿主明确说「不支持 pickUserFile」时才退到 `pickSharedFile`（共享文件选择器，
 * 宿主管辖它是否允许选目录，参数里也不让传 directory）。**取消不触发回退**，
 * 否则用户点一次取消会连着蹦两个选择器。
 */
export async function pickDirectory({ title = '选择目录', okText = '用这个目录' } = {}) {
  if (!hasTrimPicker()) return { ok: false, cancelled: false, error: '当前不在飞牛宿主环境中' }
  if (trimForced.value) {
    return { ok: false, cancelled: false, error: '这是排版预览模式，飞牛选择器要在飞牛桌面的应用里才能用' }
  }
  const app = await loadSdk()

  try {
    const res = await withTimeout(
      app.pickUserFile({ directory: true, title, okText }),
      120000,
      '目录选择',
    )
    return normalize(res)
  } catch (e) {
    const msg = String(e?.message || e || '')
    const timedOut = /超时/.test(msg)
    const notSupported = /not.?support|unsupported|不支持/i.test(msg)
    // 超时不回退 —— 用户很可能只是还在慢慢翻目录，再蹦一个选择器只会更烦。
    // 只有宿主明确说「不支持这个接口」时才换共享选择器。
    if (timedOut || !notSupported) {
      return { ok: false, cancelled: false, error: msg }
    }
    try {
      const res = await withTimeout(app.pickSharedFile({ title }), 120000, '共享目录选择')
      return normalize(res)
    } catch (e2) {
      return { ok: false, cancelled: false, error: String(e2?.message || e2 || '') }
    }
  }
}
