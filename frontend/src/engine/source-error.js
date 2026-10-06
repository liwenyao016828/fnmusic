/**
 * 把音源脚本抛出来的**原始报错**翻译成人话。
 *
 * ── 为什么需要 ──
 *
 * 音源脚本的报错基本都是它自己的 catch-all，内容对用户毫无信息量：
 * `unknow error`（原文拼写就少一个 n）、`get music url failed`、`block ip`、
 * `get url failed`…… 用户看到这些只能干瞪眼，我们排查时也得再抓一次包才知道
 * 到底是「上游 502」「IP 被封」还是「脚本过旧」。
 *
 * ── 这套映射是照抄参考实现觅音（miyin）的 ──
 *
 * 它把上游错误按特征分类，翻译成能直接指导下一步的中文，例如：
 *   `404`                    → API 接口不存在（HTTP 404），脚本可能过旧或 API 地址已变更
 *   `block ip`               → API 拒绝访问（IP 被封禁）
 *   `too many requests`/429  → API 限流（请求过于频繁）
 *   `unknow error`           → API 返回异常（可能服务已停服、返回 HTML 或响应格式变更）
 *   `get music url failed`   → 取链失败，API 未返回有效播放地址
 *
 * 我们照这套思路实现，并补上它没覆盖的 502 / 503（实测该音源 wy/kw 就是上游 nginx 502）。
 *
 * ⚠️ **只用于展示**。健康度/熔断记录里仍然存**原始**错误，
 * 否则以后排查时看到的是被翻译过的二手信息。
 */

// 顺序有意义：先匹配的先生效，所以「具体」的规则要排在「笼统」的前面。
const RULES = [
  {
    re: /block ip|ip 被封|ip被封|ip blocked/i,
    text: 'API 拒绝访问（IP 被封禁）—— 音源服务端按 IP 拦了请求，换网络或换音源',
  },
  {
    re: /too many requests|请求过速|请求过于频繁|\b429\b/i,
    text: 'API 限流（请求过于频繁）—— 稍后重试，或减少并发',
  },
  {
    re: /\b404\b|not found|接口不存在/i,
    text: 'API 接口不存在（HTTP 404）—— 脚本可能过旧，或音源方已变更接口地址',
  },
  {
    re: /\b50[23]\b|bad gateway|service unavailable|upstream/i,
    text: 'API 网关错误（HTTP 502/503）—— 音源方自己的上游挂了，不是本地问题，稍后重试或换音源',
  },
  {
    re: /internal server error|\b500\b|服务端错误/i,
    text: 'API 服务端错误（HTTP 500）—— 音源方服务异常',
  },
  {
    re: /param error|参数错误|source not match/i,
    text: 'API 参数错误或平台不匹配 —— 这首曲子可能不受该音源支持',
  },
  {
    re: /unknow error|unknown error|无法解析|unexpected token|<!doctype|<html/i,
    text: 'API 返回异常（可能服务已停服、返回了网页而非数据，或响应格式变更）',
  },
  {
    re: /未能获取播放地址|get music url failed|get url failed|取链.*失败/i,
    text: '取链失败，API 未返回有效播放地址 —— 常见于该曲目在音源方无版权或已下架',
  },
  {
    re: /超时|timeout/i,
    text: '取链超时，远端 API 长时间无响应',
  },
  {
    // 我们自己产生的、本身已经说清楚了的状态，原样保留（不要二次包装）
    re: /音源尚未就绪|音源未注册|初始化超时|未完成初始化|脚本加载失败|脚本过大|禁止 require|熔断|未导入任何可用音源/,
    text: null,
  },
]

/**
 * 这次失败是「还没准备好」，还是「真的坏了」？
 *
 * ── 为什么单列一个判据 ──
 *
 * 音源脚本是**两段式**的：脚本先异步拉远程配置/校验版本，这期间它往往
 * **已经注册了 handler**，但取链时回一句自己的 `服务初始化中，请稍后`。
 * 导入后立刻自动测试正好撞进这个窗口 —— 实测 2026-09-30：
 * 一个完全正常的音源副本被判成「不可用」，用户看到的是「测试完成：0 个可用」。
 *
 * 这一类的错误**值得立刻重试**（几百毫秒后通常就好了）；
 * 502 / IP 被封 / 404 / 不支持该平台那种，重试多少次都一样。
 *
 * ⚠️ 只用来决定「要不要再试一次」。展示文案与健康度记录仍用**原始**报错。
 *
 * @param {string|Error} raw 原始错误（字符串或 Error）
 * @returns {boolean} 是否属于「尚未就绪，值得重试」
 */
export function isNotReadyError(raw) {
  const msg = String((raw && raw.message) || raw || '')
  if (!msg) return false
  // 「初始化超时」是**已经等过**得出的结论，不是「还没好」—— 再试一次没有意义
  if (/初始化超时/.test(msg)) return false
  return /初始化中|尚未就绪|正在初始化|未完成初始化|not\s*ready|initializing|please\s*wait|请稍后|加载中|正在加载/i.test(msg)
}

/**
 * 把原始错误翻译成可展示的中文说明。
 *
 * @param {string|Error} raw 原始错误（字符串或 Error）
 * @returns {string} 展示用文案；无法归类时原样返回（绝不吞掉信息）
 */
export function explainSourceError(raw) {
  const msg = String((raw && raw.message) || raw || '').trim()
  if (!msg) return '未知错误（音源没有给出任何错误信息）'

  // 幂等：已经是翻译过的文案就不再包装。
  // 实测 2026-09-30：lx-runtime.probeSource 返回的 error 已经是 explain 过的，
  // 调用方再 explain 一次 → 规则会命中文案里的「HTTP 502/503」→ 套娃成
  // 「…（原始报错：…（原始报错：Request failed with status code 502））」，用户看到一坨。
  if (msg.includes('（原始报错：')) return msg

  for (const r of RULES) {
    if (!r.re.test(msg)) continue
    // text 为 null 表示「已经够清楚，原样用」
    if (r.text === null) return msg
    // 把原始错误附在后面 —— 翻译只是方便阅读，原始信息不能丢
    return `${r.text}（原始报错：${msg}）`
  }

  return msg
}
