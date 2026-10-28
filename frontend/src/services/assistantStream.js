/**
 * 助手 SSE 帧解析（纯函数，便于单测）。
 *
 * 后端 /api/assistant/chat 的帧格式是：
 *
 *   data: {"type":"text","data":{"text":"你好"}}
 *   data: {"type":"done"}
 *
 * 注意后端**只写 `data:` 行，不写 `event:` 行**，事件名在 JSON 的 type 字段里。
 * 早期版本在客户端把事件名默认成 'message'，导致助手回复解析到了却从不渲染
 * （表现：助手气泡一片空白）。
 *
 * 规则：
 *   - 帧以空行分隔；最后半帧留在 buffer 里等下一批数据；
 *   - 事件名优先取 JSON 的 type，其次取 `event:` 行（兼容其它写法）；
 *   - 业务负载取帧内 JSON 的 data 字段；没有 data 字段时整帧就是负载。
 */

/**
 * 从 buffer 中切出所有完整帧，剩余的半帧返回在 rest 里。
 * @param {string} buffer 已累积的原始文本
 * @returns {{frames: string[], rest: string}}
 */
export function splitFrames(buffer) {
  const frames = []
  let rest = buffer || ''
  let idx
  while ((idx = rest.indexOf('\n\n')) !== -1) {
    frames.push(rest.slice(0, idx))
    rest = rest.slice(idx + 2)
  }
  return { frames, rest }
}

/**
 * 解析一个完整帧。
 * @param {string} frame
 * @returns {{event: string, data: object}|null} 帧无效时返回 null
 */
export function parseFrame(frame) {
  let evt = 'message'
  let dataStr = ''
  for (const line of String(frame || '').split('\n')) {
    if (line.startsWith('event:')) {
      evt = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      dataStr += line.slice(5).trim()
    }
  }
  if (!dataStr) return null
  let data
  try {
    data = JSON.parse(dataStr)
  } catch (e) {
    return null
  }
  if (!data || typeof data !== 'object') return null
  const type = data.type || evt
  // 业务负载：优先取帧内 JSON 的 data 字段；没有 data 字段时整帧就是负载。
  // type 只是事件名，不混进负载（组件按 data.text / data.name 读）。
  let payload
  if ('data' in data) {
    payload = data.data || {}
  } else {
    payload = { ...data }
    delete payload.type
  }
  return { event: type, data: payload }
}

/**
 * 一次性把一段原始流解析成事件列表（单测与回放用）。
 * @param {string} raw
 * @returns {Array<{event: string, data: object}>}
 */
export function parseStream(raw) {
  const { frames } = splitFrames(raw)
  const out = []
  for (const f of frames) {
    const ev = parseFrame(f)
    if (ev) out.push(ev)
  }
  return out
}
