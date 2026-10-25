/**
 * 「最后一次意图为准」的写队列。
 *
 * 场景：一串小开关（勾选、总开关、退路开关）都写同一份配置，用户点得比请求回得快。
 * 三种做法各有毛病：
 *   - 直接发 → 响应乱序，后发的先到，本地状态与服务端就不一致了；
 *   - 写的时候禁止再点（busy 期间直接 return）→ 连点会**静默丢掉**后面几下
 *     （批量按钮上很致命：点了「全选」再点「仅网易云」，只生效前一个）；
 *   - 每次改动都排队串行发 → 请求数等于点击数。
 *
 * 这里取：**正在写的时候记下「最终想写成的样子」，这一轮回来再补写最后一次**。
 * 于是连点只剩两次写（第一次 + 最后一次），中间那些被吸收掉，且不会丢。
 *
 * 独立成文件是为了**可单测**：它不依赖 vue、不依赖 axios、不碰网络，
 * 真正发请求的动作由调用方通过 `send` 注入。
 *
 * ⚠️ 判定「有没有待写」必须用 `undefined`，不能用真假值 —— 清空勾选时
 * 要写的就是一个**合法的空数组**，用 `if (pendingIds)` 会把「全部取消」吃掉。
 * 同理「音质跟随」写的是合法空串，必须走 `!== undefined` 判。
 *
 * ⚠️ 2026-10-18 修：`adq`（音质下载）这一路以前**漏登记了** —— chartInject.js 一直
 * 在调 `writes.adq(...)`，而这里既没有 pendingAdq 也没导出 adq()，于是每次改音质都
 * 抛 `TypeError: writes.adq is not a function`，被调用方（没人 await）静静吞掉：
 * 界面上下拉明明变了，**服务端一次都没写进去**。加新键时，这里的四处（pending 变量 /
 * pump 的取值 / while 条件 / return 的方法）必须一起加，缺一处就是又一个静默失效。
 */
export function createWriteQueue({ send, onBusy = () => {}, onError = () => {} }) {
  let pendingIds
  let pendingEnabled
  let pendingUI
  let pendingAdq
  let pendingSize
  let running = false

  async function pump() {
    if (running) return
    running = true
    onBusy(true)
    try {
      while (
        pendingIds !== undefined || pendingEnabled !== undefined ||
        pendingUI !== undefined || pendingAdq !== undefined || pendingSize !== undefined
      ) {
        // 取走「当下的意图」；取值之后到的改动会记进下一轮
        const writes = {
          ids: pendingIds, enabled: pendingEnabled, ui: pendingUI,
          adq: pendingAdq, size: pendingSize,
        }
        pendingIds = undefined
        pendingEnabled = undefined
        pendingUI = undefined
        pendingAdq = undefined
        pendingSize = undefined
        try {
          await send(writes)
        } catch (e) {
          // 失败时**丢掉**排队中的意图：调用方会重读服务端配置把本地拉回真相，
          // 这时候再拿旧意图补写，就等于用刚被否掉的值去覆盖真相。
          try {
            await onError(e)
          } catch {
            // 错误处理自己出错不该再往外炸（它是收场动作，不是主流程）
          }
          return
        }
      }
    } finally {
      running = false
      onBusy(false)
    }
  }

  return {
    ids(list) {
      pendingIds = list
      return pump()
    },
    enabled(on) {
      pendingEnabled = !!on
      return pump()
    },
    ui(on) {
      pendingUI = !!on
      return pump()
    },
    /** 音质下载（`highest` / `flac` / `320k` / `128k`）。空串是合法值（「跟随」），别用真假值判。 */
    adq(q) {
      pendingAdq = q === undefined ? '' : String(q)
      return pump()
    },
    /**
     * 榜单显示首数（`{ global, per }`）。
     * `{ global: null }` 是合法取值（「跟随默认」），所以判待写只能用 `!== undefined`。
     */
    size(v) {
      pendingSize = v === undefined ? { global: null } : v
      return pump()
    },
    get running() {
      return running
    },
  }
}
