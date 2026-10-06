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
 */
export function createWriteQueue({ send, onBusy = () => {}, onError = () => {} }) {
  let pendingIds
  let pendingEnabled
  let pendingUI
  let running = false

  async function pump() {
    if (running) return
    running = true
    onBusy(true)
    try {
      while (pendingIds !== undefined || pendingEnabled !== undefined || pendingUI !== undefined) {
        // 取走「当下的意图」；取值之后到的改动会记进下一轮
        const writes = { ids: pendingIds, enabled: pendingEnabled, ui: pendingUI }
        pendingIds = undefined
        pendingEnabled = undefined
        pendingUI = undefined
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
    get running() {
      return running
    },
  }
}
