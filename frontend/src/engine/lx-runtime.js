import { ref } from 'vue'
import { Buffer } from 'buffer'
import CryptoJS from 'crypto-js'
// ⚠️ 本地模块一律写全 `.js` 后缀：Node 的 ESM 不做后缀补全，
// 少写后缀会让本文件无法被 node --test 直接导入（Vite 两者都认）。
import { ProxyAPI, SourcesAPI, PlayerAPI } from '../api/client.js'
import { sourceHealth } from '../services/sourceHealth.js'
import { isHighestQuality, buildQualityTiers } from './quality.js'
import { explainSourceError } from './source-error.js'
import {
  attachSandboxGlobals,
  rsaEncryptNoPadding,
  zlibInflate,
  zlibDeflate,
  md5Hex,
  bufToString as lxBufToString,
  aesEncrypt,
} from './lx-compat.js'

// Reactive version token so Vue computed properties re-evaluate when sources init
export const runtimeVersion = ref(0)

// 单次音源解析调用超时（对齐 miyin CALL_TIMEOUT_MS 的保守值）
const CALL_TIMEOUT_MS = 15000

// 取链前等待「脚本异步注册 handler」的上限。
// 两段式脚本（先拉远程配置再 lx.on）实测约 250ms 完成，2s 足够宽裕。
const HANDLER_WAIT_MS = 2000

/**
 * 各平台的探针曲目（「测速 / 导入后自动测试」用）。
 *
 * ⚠️ **不能所有平台共用一首歌** —— 曲目 ID 是平台私有的，
 * 拿酷我的 ID 去问网易/QQ 一定取不到，探测会把好音源误判成坏的。
 *
 * 曲目表照抄参考实现觅音（miyin）的 `PROBE_TRACKS`，都是各平台能正常取到链的曲子。
 * `hash` 只有酷狗用得上（酷狗接口按 hash 取链）。
 */
const PROBE_TRACKS = {
  wy: { id: '421423806', songmid: '421423806', name: '小半', singer: '陈粒' },
  kw: { id: '228908', songmid: '228908', name: '测试', singer: '测试' },
  kg: {
    id: 'EEDBDCB8E3A9453390DBF897FEBB629',
    songmid: 'EEDBDCB8E3A9453390DBF897FEBB629',
    hash: 'EEDBDCB8E3A9453390DBF897FEBB629',
    name: '测试',
    singer: '测试',
  },
  tx: { id: '0039MnYb0qxYhV', songmid: '0039MnYb0qxYhV', name: '晴天', singer: '周杰伦' },
  mg: { id: '60054701942', songmid: '60054701942', name: '测试', singer: '测试' },
}

// Expose Buffer globally so scripts can use Buffer.from directly
if (typeof globalThis.Buffer === 'undefined') {
  globalThis.Buffer = Buffer
}

/**
 * 第三方音源脚本自带的定时器，默认**不让它们进事件循环**。
 *
 * ⚠️ 实测证据（2026-10-06，活体浏览器 + `PerformanceObserver`）：
 * 「洛雪音乐源」那条脚本（obfuscator.io 混淆，函数名 `_0x2bcf9e`）在加载时
 * 注册了一个 `setInterval(fn, 2000)`，回调是混淆器的**反调试 / 自我完整性校验** ——
 * `while(!![]){}` 死循环 + `debugger` 断点 + 自递归重跑字符串数组解码。
 *
 * 浏览器侧量到的代价：
 *   - `longtask` 每 **2000ms** 一条，单条 **650~1112ms**；
 *   - `long-performance-frame` 归因 `_0x2bcf9e ×11 = 8375ms  invoker=TimerHandler:setInterval`；
 *   - rAF 帧间隔出现 668 / 982 / 1083 / 1112ms 的空洞，与长任务一一对应。
 * 即主线程 **35%~45% 的时间被占死**。表现就是飞牛宿主里拖窗口时
 * 「每 3~4 秒卡一下、拖不动，然后又恢复，循环」—— 跟 CSS、跟渲染都无关。
 *
 * 判断：lx 音源的**契约里没有定时器**，它该在 `lx.on('request', …)` 里干活。
 * 脚本自己起周期任务本身就是错的，何况这类定时器十有八九只服务混淆器的反调试。
 * 所以从这一版起默认不调度，只记账 + 打一条日志。
 *
 * 真要放行（某个音源确实靠定时器刷 token）：
 *   window.__LX_ALLOW_SCRIPT_TIMERS__ = true            // 当场生效，刷新即失效
 *   localStorage["lx.allowScriptTimers"] = "1"          // 持久
 */
function scriptTimersAllowed() {
  try {
    if (globalThis.__LX_ALLOW_SCRIPT_TIMERS__ === true) return true
    return globalThis.localStorage?.getItem('lx.allowScriptTimers') === '1'
  } catch {
    // 隐私模式 / 被禁用 localStorage：按「不允许」处理，这是安全的一侧。
    return false
  }
}

/**
 * 造一组定时器闸门，交给脚本作用域用。
 *
 * @param {(kind: string, delay: number) => void} record 挡下时记账
 */
function createScriptTimerShim(record) {
  // ⚠️ 先把真实实现**抓在手里**：下面的全局替换会把 `globalThis.setInterval`
  // 换成这个 shim 本身，留到调用时才取就等于自己调自己，直接爆栈。
  const realSetInterval = globalThis.setInterval
  const realSetTimeout = globalThis.setTimeout
  const realRaf = globalThis.requestAnimationFrame
  // setTimeout 只计数、不改行为：一次性定时器是有界的，而且我们自己的
  // 调用超时（CALL_TIMEOUT_MS）就走它 —— 一刀切会把正常逻辑也掐掉。
  // 计数是**按秒开窗**的：音源活很久、累计上千次 setTimeout 是正常的，
  // 只有「1 秒内塞 200 个」才算突发（那通常是混淆器拿 setTimeout(f,0) 顶替 interval）。
  let burstStart = 0
  let burst = 0
  let burstWarned = false
  const BURST_LIMIT = 200

  return {
    setInterval(fn, delay, ...args) {
      if (scriptTimersAllowed()) return realSetInterval.call(globalThis, fn, delay, ...args)
      record('setInterval', Number(delay) || 0)
      // 返回数字句柄：脚本可能拿它去 clearInterval()，数字无害。
      return 0
    },
    setTimeout(fn, delay, ...args) {
      const now = Date.now()
      if (now - burstStart > 1000) {
        burstStart = now
        burst = 0
      }
      if (++burst > BURST_LIMIT) {
        if (!burstWarned) {
          burstWarned = true
          record(`setTimeout(>${BURST_LIMIT}/秒)`, Number(delay) || 0)
        }
        return 0
      }
      return realSetTimeout.call(globalThis, fn, delay, ...args)
    },
    requestAnimationFrame(fn) {
      // 音源脚本不该有渲染诉求；放行等于允许它在主线程上**每帧**跑一段混淆代码。
      if (scriptTimersAllowed() && realRaf) return realRaf.call(globalThis, fn)
      record('requestAnimationFrame', 16)
      return 0
    },
  }
}

/**
 * 在脚本求值的**同步窗口**里，把真实全局的 `setInterval` / `requestAnimationFrame`
 * 临时也换成同一道闸门 —— 专门兜住写死 `window.setInterval(...)` /
 * `globalThis.setInterval(...)` 的混淆器（参数遮蔽对这类写法无效）。
 *
 * 只换这两个：
 *   - 它们不会被应用自己在「加载音源」这条同步路径上使用；
 *   - `setTimeout` 故意不换 —— 我们的调用超时就用它，换了会把自己也掐掉。
 *
 * 返回 `restore()`，调用方必须放在 `finally` 里。
 */
function swapGlobalTimers(timers) {
  const hadInterval = Object.prototype.hasOwnProperty.call(globalThis, 'setInterval')
  const prevInterval = globalThis.setInterval
  const prevRaf = globalThis.requestAnimationFrame
  globalThis.setInterval = timers.setInterval
  if (prevRaf) globalThis.requestAnimationFrame = timers.requestAnimationFrame
  return () => {
    if (hadInterval) globalThis.setInterval = prevInterval
    else delete globalThis.setInterval
    if (prevRaf) globalThis.requestAnimationFrame = prevRaf
  }
}

/**
 * 真实 console 的存档 + 抢回。
 *
 * ⚠️ 又一条实测踩到的坑（同一个音源）：那条混淆脚本在加载时**把
 * `console.log` / `console.warn` 整个换成了自己的函数**。
 * 后果不是报错，而是**静默**：它之后的每一条日志都不见了 ——
 * 包括我们自己的 `[LX-RUNTIME] Loaded OK`、`Load error`，以及后面几个音源的加载日志。
 * 排查时表现为「程序像是卡死在第三条音源上」，白白浪费了一整轮定位
 * （真凶要到「比对 console.log 的函数引用」才现形）。
 *
 * 第三方脚本不该拿走控制台：它一劫持，**所有**诊断能力就都没了，
 * 而我们连「出错了」都看不到。所以每次加载完都检查一遍、被换掉就抢回来，并喊一声。
 */
const NATIVE_CONSOLE = (() => {
  const snap = {}
  for (const k of ['log', 'info', 'warn', 'error', 'debug', 'trace']) {
    if (typeof console[k] === 'function') snap[k] = console[k]
  }
  return snap
})()

function reclaimConsole(sourceId) {
  const taken = []
  for (const k of Object.keys(NATIVE_CONSOLE)) {
    if (console[k] !== NATIVE_CONSOLE[k]) {
      taken.push(k)
      console[k] = NATIVE_CONSOLE[k]
    }
  }
  if (taken.length) {
    NATIVE_CONSOLE.warn.call(
      console,
      `[LX-RUNTIME] 音源 ${sourceId} 改写了 console.${taken.join(' / console.')}，已抢回：` +
        '脚本劫持控制台会让所有日志（含它自己的报错）静默消失，诊断能力不能交给第三方。'
    )
  }
  return taken
}

class LxRuntime {
  constructor() {
    this.requestHandlers = new Map()   // sourceId -> handler function
    this.sourceStatus = new Map()      // sourceId -> { inited, sources, qualityMap }
    this.loadedSources = new Map()     // sourceId -> sourceMeta
    // sourceId -> Set<resolve>：等「handler 注册」的等待者。
    // 见 waitHandler() —— 有些脚本是**异步**注册的（先拉远程配置再 lx.on('request')）。
    this._handlerWaiters = new Map()
    // sourceId -> { id, name, protocol, platforms }
    // 「服务型」音源：没有可执行的 handler，取链走后端 POST /api/sources/api/resolve
    this.apiSources = new Map()
    // sourceId -> 被闸门挡下的定时器次数（诊断用，见 createScriptTimerShim）
    this.droppedTimers = new Map()
    // `${sourceId}|${kind}|${delay}`：已经为它打过日志，不重复刷屏
    this._dropLogged = new Set()
  }

  /**
   * 记账：某个音源起的周期定时器被闸门挡下了。
   *
   * 同 `(kind, delay)` 只打一条 —— 这类定时器一次注册会**反复**触发，
   * 不收敛的话控制台会被它刷爆（而它每次触发还会顺带阻塞主线程）。
   */
  _recordDroppedTimer(sourceId, kind, delay) {
    this.droppedTimers.set(sourceId, (this.droppedTimers.get(sourceId) || 0) + 1)
    const key = `${sourceId}|${kind}|${delay}`
    if (this._dropLogged.has(key)) return
    this._dropLogged.add(key)
    console.warn(
      `[LX-RUNTIME] 已拦截音源 ${sourceId} 的 ${kind}(${delay}ms)：` +
        '第三方脚本不该在主线程上起周期任务（实测有音源用它做反调试，每 2 秒阻塞 600~990ms）。' +
        '确实靠定时器干活的音源 → localStorage["lx.allowScriptTimers"]="1" 或 window.__LX_ALLOW_SCRIPT_TIMERS__=true'
    )
  }

  /**
   * 登记「服务型」音源（按服务地址接入的那种）。
   *
   * 这类音源是一段 HTTP 服务而不是脚本，浏览器里没有可执行的 handler，
   * 取链由**后端**代为请求。登记之后它们就和脚本型音源一样参与轮询与熔断。
   *
   * @param {Array} list 后端 GET /api/sources/api 返回的列表
   */
  setApiSources(list) {
    this.apiSources.clear()
    for (const s of list || []) {
      if (!s?.id || s.enabled === false) continue
      this.apiSources.set(s.id, {
        id: s.id,
        name: s.name || s.base_url || s.id,
        protocol: s.protocol,
        // 服务型音源没有能力上报接口，按官方四个平台处理；
        // 具体能不能解析由服务端决定，失败会走熔断降级。
        platforms: ['wy', 'tx', 'kg', 'kw'],
      })
    }
    runtimeVersion.value++
  }

  /** 该 id 是否属于「服务型」音源 */
  isApiSource(sourceId) {
    return this.apiSources.has(sourceId)
  }

  async loadScript(sourceMeta, scriptText) {
    const sourceId = sourceMeta.id
    console.log('[LX-RUNTIME] Loading script:', sourceId)

    const EVENT_NAMES = {
      request: 'request',
      inited: 'inited',
      updateAlert: 'updateAlert'
    }

    const self = this

    const lxEnv = {
      EVENT_NAMES,
      env: 'desktop',
      version: '2.0.0',
      currentScriptInfo: {
        name: sourceMeta.name,
        description: sourceMeta.description || '',
        version: sourceMeta.version || '1.0.0',
        author: sourceMeta.author || '',
        homepage: sourceMeta.homepage || '',
        rawScript: scriptText,
      },

      request(url, options, callback) {
        let opts = {}, cb = callback
        if (typeof options === 'function') {
          cb = options
          opts = {}
        } else if (options) {
          opts = options
        }

        const method = (opts.method || 'GET').toUpperCase()
        const headers = { ...(opts.headers || {}) }
        let bodyData = opts.body || opts.form || opts.formData || opts.json || null
        if (opts.json) {
          headers['Content-Type'] = headers['Content-Type'] || 'application/json; charset=utf-8'
          if (typeof opts.json === 'object' && opts.json !== null) {
            bodyData = JSON.stringify(opts.json)
          }
        }
        const timeout = opts.timeout || 15000

        let aborted = false

        ProxyAPI.request({
          url,
          method,
          headers,
          body: typeof bodyData === 'object' && bodyData !== null ? JSON.stringify(bodyData) : bodyData,
          timeout,
        }).then(res => {
          if (aborted || !cb) return
          if (res.error && (!res.body || res.statusCode >= 500 || !res.statusCode)) {
            cb(new Error(res.error || `HTTP ${res.statusCode}`), null, null)
            return
          }

          let parsedBody = res.body
          if (typeof parsedBody === 'string') {
            try {
              parsedBody = JSON.parse(parsedBody)
            } catch (_) {}
          }

          const resp = {
            statusCode: res.statusCode || 200,
            statusMessage: 'OK',
            headers: res.headers || {},
            bytes: res.body ? res.body.length : 0,
            raw: res.body,
            body: parsedBody,
          }

          // Official signature: callback(err, resp, body)
          cb(null, resp, parsedBody)
        }).catch(err => {
          if (aborted || !cb) return
          console.warn('[LX-RUNTIME] Proxy request failed:', url, err)
          cb(err, null, null)
        })

        // Official signature returns abort function
        return () => {
          aborted = true
        }
      },

      on(eventName, handler) {
        if (eventName === EVENT_NAMES.request) {
          self.requestHandlers.set(sourceId, handler)
          console.log('[LX-RUNTIME] Registered request handler for:', sourceId)
          self._resolveHandlerWaiters(sourceId)
        }
      },

      send(eventName, data) {
        if (eventName === EVENT_NAMES.inited) {
          // 兼容三种洛雪脚本上报结构：{sources} / {sources:{sources}} / {init:{sources}}
          const rawSources = data?.sources || data?.init?.sources || {}
          const srcObj = rawSources?.sources && typeof rawSources.sources === 'object'
            ? rawSources.sources
            : rawSources

          const platforms = {}
          const qualityMap = {}
          for (const [k, v] of Object.entries(srcObj || {})) {
            if (!v || typeof v !== 'object') continue
            platforms[k] = v
            qualityMap[k] = (Array.isArray(v.qualitys) && v.qualitys.length)
              ? v.qualitys.map(String)
              : ['128k']
          }

          self.sourceStatus.set(sourceId, { inited: true, sources: platforms, qualityMap })
          runtimeVersion.value++
          console.log('[LX-RUNTIME] Source inited:', sourceId, Object.keys(platforms), qualityMap)
        } else if (eventName === EVENT_NAMES.updateAlert) {
          console.log('[LX-RUNTIME] updateAlert from:', sourceId, data)
        }
      },

      utils: {
        buffer: {
          from(...args) {
            return Buffer.from(...args)
          },
          bufToString(buf, format = 'utf8') {
            // 对齐洛雪 / 觅音语义（字符串入参按 binary 包一层再解码），
            // 原因见 lx-compat.js 的 bufToString()。
            return lxBufToString(buf, format)
          },
        },
        crypto: {
          md5(str) {
            // ⚠️ 返回 **hex 字符串**（32 字符），不是 Buffer —— 洛雪桌面端 / 觅音的行为。
            // 曾经返回 16 字节 Buffer，导致 `md5(x).length` 是 16 而不是 32，
            // 真实音源脚本因此在签名里算出 `'0'.repeat(负数)` 并抛
            // `RangeError: Invalid count value: -N`。
            // 完整原因与兼容性说明见 lx-compat.js 的 md5Hex()。
            return md5Hex(str)
          },
          randomBytes(size) {
            const arr = new Uint8Array(size)
            if (typeof crypto !== 'undefined' && crypto.getRandomValues) {
              crypto.getRandomValues(arr)
            } else {
              for (let i = 0; i < size; i++) arr[i] = Math.floor(Math.random() * 256)
            }
            return Buffer.from(arr)
          },
          aesEncrypt(buffer, mode, key, iv) {
            // 见 lx-compat.js 的 aesEncrypt()。
            // ⚠️ 原来这里只区分 ECB / 非 ECB（非 ECB 一律当 CBC），
            // 并且 catch 之后**原样返回明文**当密文 —— 两处都是静默数据损坏。
            // 现在按真实模式分派，出错就抛（不再假装成功）。
            return aesEncrypt(buffer, mode, key, iv)
          },
          rsaEncrypt(buffer, key) {
            // 原来这里是空壳（原样返回入参）—— 脚本拿到错数据却不报错，
            // 表现为「搜不到 / 取不到链」这种看不懂的现象。
            // 现在用 BigInt 做裸模幂，对齐 Node 的 RSA_NO_PADDING（见 lx-compat.js）。
            return rsaEncryptNoPadding(buffer, key)
          }
        },
        // zlib 之前完全没提供 —— 用了它的脚本会直接
        // `Cannot read properties of undefined`。浏览器原生就能做（CompressionStream）。
        zlib: {
          inflate: zlibInflate,
          deflate: zlibDeflate,
        },
        // 兼容旧写法 lx.utils.bufToString（规范位置是 lx.utils.buffer.bufToString）
        bufToString(buf, format = 'utf8') {
          return lxBufToString(buf, format)
        }
      }
    }

    // Set globally
    globalThis.lx = lxEnv
    this.loadedSources.set(sourceId, sourceMeta)

    // 补齐 Node 风格的沙箱全局（require / module / exports / global / Buffer）。
    // 不挂这些的话，用了 `require('crypto')` 的音源会直接 ReferenceError ——
    // 这正是「同一个音源觅音能用、我们报错」的主因之一。见 lx-compat.js。
    attachSandboxGlobals(lxEnv)

    try {
      // 把沙箱全局作为**函数参数**传进去（而不是挂到真实 globalThis 上）：
      // 这样它们在脚本作用域里可用，又不会污染整个应用，脚本之间也互不干扰。
      //
      // 定时器同理走参数 —— 参数遮蔽的是脚本**整个作用域**（含它注册的回调里的闭包），
      // 所以脚本自己起的周期任务会落到闸门上，而不是真的进事件循环。
      // 详见 createScriptTimerShim() 上面那段实测证据。
      const timers = createScriptTimerShim((kind, delay) =>
        this._recordDroppedTimer(sourceId, kind, delay)
      )
      const fn = new Function(
        'lx', 'require', 'module', 'exports', 'global', 'Buffer',
        'setInterval', 'requestAnimationFrame',
        scriptText
      )
      const restoreTimers = swapGlobalTimers(timers)
      try {
        fn(
          lxEnv, lxEnv.require, lxEnv.module, lxEnv.exports, lxEnv.global, Buffer,
          timers.setInterval, timers.requestAnimationFrame
        )
      } finally {
        restoreTimers()
        // 抢在下面两行日志之前抢回控制台 —— 被脚本换掉的话，
        // 连「Loaded OK / Load error」都会一起消失，到时候根本不知道发生了什么。
        reclaimConsole(sourceId)
      }
      console.log('[LX-RUNTIME] Loaded OK:', sourceId)
      return true
    } catch (e) {
      console.error('[LX-RUNTIME] Load error for:', sourceId, e)
      // 脚本加载失败同样计入熔断，避免坏音源反复被选中
      sourceHealth.recordFailure(sourceId, `脚本加载失败: ${e.message}`)
      return false
    }
  }

  /** 该音源是否已注册可用的取链处理器（脚本型或服务型都算） */
  hasHandler(sourceId) {
    return this.requestHandlers.has(sourceId) || this.apiSources.has(sourceId)
  }

  /** handler 注册后唤醒所有等待者（由 lx.on('request') 调用） */
  _resolveHandlerWaiters(sourceId) {
    const waiters = this._handlerWaiters.get(sourceId)
    if (!waiters) return
    this._handlerWaiters.delete(sourceId)
    for (const w of waiters) w()
  }

  /**
   * 等某个音源的 handler 注册好。
   *
   * ⚠️ **必须等**，不能假设 `loadScript()` 返回时 handler 就绪。
   * 相当一部分音源是**两段式**的：脚本先异步拉远程配置/校验版本，
   * 拿到结果后才 `lx.on('request', ...)`。实测用户反馈的那个音源
   * 在加载后约 250ms 才注册 —— 期间任何取链都会被判成「音源尚未就绪」，
   * 而 `probeSource` 会把它记成**一次失败并计入熔断**，
   * 于是一个完全正常的音源被「一键清理失效音源」误杀。
   *
   * @returns {Promise<boolean>} 是否在超时前注册成功
   */
  waitHandler(sourceId, timeoutMs = 2000) {
    if (this.requestHandlers.has(sourceId) || this.apiSources.has(sourceId)) {
      return Promise.resolve(true)
    }
    return new Promise((resolve) => {
      const waiters = this._handlerWaiters.get(sourceId) || new Set()
      this._handlerWaiters.set(sourceId, waiters)

      let settled = false
      const finish = (ok) => {
        if (settled) return
        settled = true
        clearTimeout(timer)
        waiters.delete(finish)
        resolve(ok)
      }
      const timer = setTimeout(() => finish(false), timeoutMs)
      waiters.add(() => finish(true))
    })
  }

  /**
   * 等音源**真正初始化完成**（脚本自己 `lx.send('inited', ...)`）。
   *
   * ⚠️ 和 `waitHandler` 不是一回事：**handler 注册 ≠ 就绪**。
   * 实测 2026-09-30：星海音乐源加载后立刻注册了 handler，但内部还在异步拉远程配置，
   * 这期间取链只会回它自己的一句「服务初始化中，请稍后」。
   * 导入后立刻自动测试正好撞进这个窗口 —— 一个完全正常的音源被判成「不可用」。
   *
   * `getPlatforms()` 读的正是 inited 事件写进 `sourceStatus` 的数据，为空即说明脚本还没 init 完。
   * 所以只在**探测已经明确回了「还没好」**之后才来等它，别拿它当每次探测的前置门
   * （少数脚本压根不发 inited，前置等待会让它们白等一轮）。
   *
   * @returns {Promise<boolean>} 是否在超时前完成初始化
   */
  async waitInited(sourceId, timeoutMs = 5000) {
    if (!sourceId) return false
    // 服务型音源没有 init 事件，按已就绪处理
    if (this.apiSources.has(sourceId)) return true
    const deadline = Date.now() + timeoutMs
    for (;;) {
      if (this.sourceStatus.has(sourceId)) return true
      if (Date.now() >= deadline) return false
      await new Promise(r => setTimeout(r, 150))
    }
  }

  /** 已就绪的音源 id 列表（按健康度排序，熔断中的排在最后） */
  getReadySourceIds() {
    return this._candidateSources('').map(c => c.id)
  }

  /** 音源显示名（用于下载队列展示实际取链音源） */
  getSourceName(sourceId) {
    if (!sourceId) return ''
    return this.loadedSources.get(sourceId)?.name || sourceId
  }

  /** 该音源宣称支持的平台列表 */
  getPlatforms(sourceId) {
    // 服务型音源没有能力上报，按官方四平台处理
    if (this.apiSources.has(sourceId)) {
      return [...this.apiSources.get(sourceId).platforms]
    }
    const st = this.sourceStatus.get(sourceId)
    if (!st || !st.sources) return []
    return Object.keys(st.sources)
  }

  /** 该音源在指定平台宣称支持的音质档位 */
  getQualities(sourceId, platform) {
    const st = this.sourceStatus.get(sourceId)
    if (!st || !st.qualityMap) return []
    return st.qualityMap[platform] || []
  }

  /**
   * 生成候选音源顺序：
   *  1. 显式指定的音源排首位
   *  2. 其余按健康度排序（成功率/延迟，熔断中的置底）
   *  3. 过滤掉处于熔断冷却期的音源
   */
  _candidateSources(preferredId) {
    // 脚本型与服务型一起参与轮询 —— 两者对上层是等价的音源
    const ids = [...this.requestHandlers.keys(), ...this.apiSources.keys()]
    const ordered = sourceHealth.sortIds(ids)
    const withPreferred = (preferredId && ordered.includes(preferredId))
      ? [preferredId, ...ordered.filter(id => id !== preferredId)]
      : ordered

    return withPreferred.map(id => ({
      id,
      name: this.loadedSources.get(id)?.name || this.apiSources.get(id)?.name || id,
      available: sourceHealth.isAvailable(id),
    }))
  }

  /**
   * 解析音频直链（核心）
   *
   * @param {string} sourceId  优先音源 id（仍会轮询其余音源）
   * @param {string} platform  平台标识 wy/tx/kg/kw
   * @param {Object} musicInfo 歌曲信息
   * @param {string} quality   'highest' 表示自动轮询最高音质；或指定 'flac' / '320k' / '128k' 等
   * @returns {Promise<{url:string, headers:Object, quality:string, sourceId:string, sourceName:string}>}
   */
  async getMusicUrl(sourceId, platform, musicInfo, quality = 'highest') {
    const preferred = quality || 'highest'
    const highest = isHighestQuality(preferred)

    const all = this._candidateSources(sourceId)
    if (!all.length) {
      throw new Error('未导入任何可用音源脚本，请先在「音源管理」导入第三方音源')
    }

    const candidates = all.filter(c => c.available)
    if (!candidates.length) {
      const wait = Math.min(...all.map(c => sourceHealth.remainingBreakMs(c.id)).filter(ms => ms > 0))
      const sec = Number.isFinite(wait) ? Math.ceil(wait / 1000) : 0
      throw new Error(`全部音源均处于熔断冷却中，请 ${sec}s 后重试，或到「音源管理」重置音源熔断状态`)
    }

    // 汇总各候选音源在该平台宣称支持的音质，生成本次尝试的档位阶梯
    const tiers = buildQualityTiers(
      preferred,
      candidates.map(c => this.getQualities(c.id, platform))
    )

    const errors = []

    for (const q of tiers) {
      for (const cand of candidates) {
        const claimed = this.getQualities(cand.id, platform)
        // highest 模式下，音源明确声明不支持该档位就跳过，避免无效请求
        if (highest && claimed.length > 0 && !claimed.includes(q)) continue

        const t0 = (typeof performance !== 'undefined' ? performance.now() : Date.now())
        try {
          const res = await this._callHandler(cand.id, platform, musicInfo, q)

          // ⚠️ 地址可达性校验：音源可能返回**形状合法但文件不存在**的地址。
          // 实测「聚合API接口 v3」对 QQ 音乐返回 `RS02...mp3?guid=api.vkeys.cn`，
          // 上游直接回 `{"errorcode":-46628,"errormsg":"file not exist"}`。
          // 不校验的话这里会认为取链成功 → 播放器再去拉就 404 ——
          // 用户看到「无法在线播放」，界面上却没有任何提示，也看不出坏在哪一环。
          const reachable = await this._verifyPlayable(res.url)
          if (!reachable.ok) {
            throw new Error(`音源返回的地址取不到音频（${reachable.note || '上游拒绝'}）`)
          }

          const latency = (typeof performance !== 'undefined' ? performance.now() : Date.now()) - t0
          sourceHealth.recordSuccess(cand.id, latency)
          return {
            url: res.url,
            headers: res.headers || {},
            quality: q,
            sourceId: cand.id,
            sourceName: cand.name,
          }
        } catch (e) {
          // 健康度记**原始**错误（排查时要原文）；给用户看的是翻译过的人话。
          // 映射表照参考实现觅音的做法，见 source-error.js。
          sourceHealth.recordFailure(cand.id, e?.message || String(e))
          errors.push(`${cand.name}@${q}: ${explainSourceError(e)}`)
        }
      }
    }

    const detail = errors.slice(0, 8).join(' | ') || '无详细错误'
    throw new Error(
      highest
        ? `取链失败（已轮询 ${candidates.length} 个音源并逐级降质）：${detail}`
        : `取链失败（已轮询 ${candidates.length} 个音源）：${detail}`
    )
  }

  /**
   * 真实可用性探测：实际发起一次取链，并计入健康度。
   * 供「音源管理」的测速/检测使用（区别于仅探测脚本可获取）。
   */
  async probeSource(sourceId, platform = 'kw', musicInfo = null, quality = '128k') {
    // 探针曲目**必须按平台给**：拿酷我的 ID 去问网易/QQ 必然取不到，
    // 探测结果就没有意义（会得出「音源坏了」的错误结论）。
    // 曲目表取自参考实现觅音（miyin）的 PROBE_TRACKS。
    const track = PROBE_TRACKS[platform] || PROBE_TRACKS.kw
    const probeInfo = musicInfo || { ...track, source: platform }
    const t0 = (typeof performance !== 'undefined' ? performance.now() : Date.now())
    try {
      // 绕过熔断：探测本身就是为了确认音源是否恢复
      sourceHealth.reset(sourceId)
      const res = await this._callHandler(sourceId, platform, probeInfo, quality)

      // 探针也要校验地址可达性 —— 否则「测速通过」是假的：
      // 音源能返回一个地址，不代表这个地址能取到音频。
      const reachable = await this._verifyPlayable(res.url)
      if (!reachable.ok) {
        const latencyMs = Math.round(
          (typeof performance !== 'undefined' ? performance.now() : Date.now()) - t0
        )
        const msg = `音源返回的地址取不到音频（${reachable.note || '上游拒绝'}）`
        sourceHealth.recordFailure(sourceId, msg)
        return { ok: false, latencyMs, error: msg, rawError: msg, platform, quality }
      }

      const latencyMs = Math.round(
        (typeof performance !== 'undefined' ? performance.now() : Date.now()) - t0
      )
      sourceHealth.recordSuccess(sourceId, latencyMs)
      return { ok: true, latencyMs, url: res.url, platform, quality }
    } catch (e) {
      const latencyMs = Math.round(
        (typeof performance !== 'undefined' ? performance.now() : Date.now()) - t0
      )
      // 健康度存原文，展示用翻译后的人话（见 source-error.js）
      sourceHealth.recordFailure(sourceId, e?.message || String(e))
      return { ok: false, latencyMs, error: explainSourceError(e), rawError: e?.message || String(e), platform, quality }
    }
  }

  /**
   * 服务型音源取链：由**后端**代发请求。
   *
   * 为什么不直接在浏览器里 fetch：这类服务是用户自建/社区的 HTTP 接口，
   * 浏览器直连会撞 CORS，而且后端本来就该负责出站访问（走 pkg/security 守卫）。
   * 前端只做「转发 + 统一错误」，不执行任何第三方代码。
   */
  async _callApiSource(sourceId, platform, musicInfo, quality) {
    // 与脚本型一致：先剥掉平台前缀，服务端要的是裸 id
    const cleanId = String(musicInfo.songmid || musicInfo.id || '')
      .trim()
      .replace(/^(wy|tx|kw|kg|mg)_/i, '')
    if (!cleanId) {
      throw new Error('缺少歌曲 ID，无法向服务型音源取链')
    }

    const res = await SourcesAPI.apiResolve(sourceId, platform, cleanId, quality)
    if (res?.code !== 200 || !res?.data?.url) {
      throw new Error(res?.message || '服务型音源未返回可播地址')
    }
    return { url: res.data.url, headers: {} }
  }

  /**
   * 校验解析出来的播放地址**是否真的能取到音频**。
   *
   * 后端只取 2 字节就断开（`Range: bytes=0-1`），成本很低。
   * 详见 lx-runtime 里调用处的注释（为什么必须校验）。
   *
   * ⚠️ **校验本身失败时不阻断播放**（fail-open）：校验接口可能因后端版本旧、
   * 网络抖动而不可用 —— 那种情况下宁可让播放器去试，
   * 也不要因为「校验不了」而拒掉一个本来能播的地址。
   */
  async _verifyPlayable(url) {
    if (!url || !/^https?:/i.test(url)) return { ok: false, note: '地址格式无效' }
    try {
      const res = await PlayerAPI.check(url)
      if (res?.code === 200 && res?.data && typeof res.data.ok === 'boolean') {
        return { ok: res.data.ok, note: res.data.note || '' }
      }
      return { ok: true, note: '' }
    } catch (_) {
      return { ok: true, note: '' }
    }
  }

  async _callHandler(sourceId, platform, musicInfo, quality) {
    // 服务型音源没有可执行的 handler，交给后端代取（见 _callApiSource）
    if (this.apiSources.has(sourceId)) {
      return this._callApiSource(sourceId, platform, musicInfo, quality)
    }

    let handler = this.requestHandlers.get(sourceId)
    if (!handler) {
      // 两段式脚本的 handler 是**异步**注册的，这里等一下再判定失败 ——
      // 否则会把「刚加载完、还没注册好」误报成「音源尚未就绪」，
      // 并被 probeSource 计成一次失败（见 waitHandler 的注释）。
      await this.waitHandler(sourceId, HANDLER_WAIT_MS)
      handler = this.requestHandlers.get(sourceId)
    }
    if (!handler) {
      throw new Error(`音源尚未就绪（等待 ${HANDLER_WAIT_MS / 1000}s 仍未注册 handler）`)
    }

    // 彻底清洗平台前缀，恢复纯净 ID / songmid / hash
    let cleanId = String(musicInfo.id || musicInfo.songmid || '').trim()
    cleanId = cleanId.replace(/^(wy|tx|kw|kg|mg)_/i, '')

    let cleanSongmid = String(musicInfo.songmid || musicInfo.id || '').trim()
    cleanSongmid = cleanSongmid.replace(/^(wy|tx|kw|kg|mg)_/i, '')

    let cleanHash = ''
    if (platform === 'kg') {
      cleanHash = String(musicInfo.hash || cleanId).trim().replace(/^(wy|tx|kw|kg|mg)_/i, '')
    }

    const cleanMusicInfo = {
      ...musicInfo,
      source: platform,
      id: cleanId,
      songmid: cleanSongmid,
      name: musicInfo.name || musicInfo.title || '',
      singer: musicInfo.singer || musicInfo.artist || '',
      album: musicInfo.album || '',
    }

    if (platform === 'kg') {
      cleanMusicInfo.hash = cleanHash
    } else {
      // 关键：非酷狗平台彻底删除 hash 字段，杜绝第三方脚本误判为酷狗或引发非法 hash 校验失败！
      delete cleanMusicInfo.hash
    }

    const payload = {
      source: platform,
      action: 'musicUrl',
      info: {
        type: quality || '128k',
        musicInfo: cleanMusicInfo
      }
    }

    // Call handler with timeout for reliable third-party resolution
    const result = await Promise.race([
      Promise.resolve().then(() => handler(payload)),
      new Promise((_, reject) => setTimeout(() => reject(new Error(`音源响应超时(${CALL_TIMEOUT_MS / 1000}s)`)), CALL_TIMEOUT_MS))
    ])

    let finalUrl = ''
    let finalHeaders = {}

    if (typeof result === 'string') {
      finalUrl = result.trim()
    } else if (result && typeof result === 'object') {
      finalUrl = result.url || result.data?.url || ''
      finalHeaders = result.headers || {}
    }

    // Critical Shield: reject panspace fake / notice audio / error mp3
    if (finalUrl && /^https?:/.test(finalUrl)) {
      const lower = finalUrl.toLowerCase()
      if (lower.includes('panspace') || lower.includes('notice') || lower.includes('audio_forbidden') || lower.includes('error.mp3')) {
        throw new Error('第三方音源返回了渠道限制提示录音(panspace)，已自动屏蔽')
      }
      return { url: finalUrl, headers: finalHeaders }
    }

    throw new Error('返回的播放地址无效')
  }

  async getLyric(sourceId, platform, musicInfo) {
    // 优先指定音源；失败则按健康度顺序尝试其余音源（歌词获取不应因单源故障而失败）
    const ids = this._candidateSources(sourceId)
      .filter(c => c.available)
      .map(c => c.id)

    for (const id of ids) {
      const handler = this.requestHandlers.get(id)
      if (!handler) continue
      try {
        let cleanId = String(musicInfo.id || musicInfo.songmid || '').trim().replace(/^(wy|tx|kw|kg|mg)_/i, '')
        let cleanSongmid = String(musicInfo.songmid || musicInfo.id || '').trim().replace(/^(wy|tx|kw|kg|mg)_/i, '')
        const payload = {
          source: platform,
          action: 'lyric',
          info: {
            musicInfo: {
              source: platform,
              id: cleanId,
              songmid: cleanSongmid,
              name: musicInfo.name || musicInfo.title || '',
              singer: musicInfo.singer || musicInfo.artist || '',
              album: musicInfo.album || '',
            }
          }
        }
        const res = await Promise.race([
          Promise.resolve().then(() => handler(payload)),
          new Promise((_, reject) => setTimeout(() => reject(new Error('timeout')), 5000))
        ])
        if (res && (res.lyric || res.lrc)) {
          return { lyric: res.lyric || res.lrc, tlyric: res.tlyric || '', sourceId: id }
        }
      } catch (_) {}
    }
    return null
  }

  /**
   * 从**启用池**里挑一个能干这活儿的音源（多选激活，2026-09-30）。
   * 池是有序的（顺序 = 优先级）：返回第一个支持该平台的；都不支持则回退池首，
   * 让调用方照旧报「当前音源不支持该平台」。
   * @param {string[]} poolIds 启用池（有序）
   * @param {string} platform 目标平台 id
   */
  pickSourceForPlatform(poolIds, platform) {
    const pool = (poolIds || []).filter(Boolean)
    if (!pool.length) return ''
    if (!platform) return pool[0]
    for (const id of pool) {
      try {
        if (this.getSupportedSources(id).includes(platform)) return id
      } catch (_) { /* 单个源查不了就跳过，不影响池里其它源 */ }
    }
    return pool[0]
  }

  /**
   * 启用池支持的平台**并集**（保序：先按池顺序，再按各自声明顺序）。
   * 搜索页的平台按钮用它 —— 池里任一源支持该平台就该能搜。
   */
  unionPlatforms(poolIds) {
    const out = []
    for (const id of (poolIds || []).filter(Boolean)) {
      let list = []
      try { list = this.getSupportedSources(id) } catch (_) { continue }
      for (const p of list) if (!out.includes(p)) out.push(p)
    }
    return out
  }

  getSupportedSources(sourceId) {
    // Touch reactive ref so Vue computed properties re-evaluate when runtime changes!
    const _ = runtimeVersion.value
    // 服务型音源没有能力上报，按官方四平台处理
    if (sourceId && this.apiSources.has(sourceId)) {
      return [...this.apiSources.get(sourceId).platforms]
    }
    if (sourceId && this.sourceStatus.has(sourceId)) {
      const status = this.sourceStatus.get(sourceId)
      if (status && status.sources && Object.keys(status.sources).length > 0) {
        return Object.keys(status.sources).filter(s => s !== 'mg' && s !== 'local')
      }
    }
    for (const s of this.sourceStatus.values()) {
      if (s.sources && Object.keys(s.sources).length > 0) {
        return Object.keys(s.sources).filter(k => k !== 'mg' && k !== 'local')
      }
    }
    return []
  }
}

export const lxRuntime = new LxRuntime()

export const __testing = {
  scriptTimersAllowed,
  createScriptTimerShim,
  swapGlobalTimers,
  reclaimConsole,
}
