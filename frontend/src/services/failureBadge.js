/**
 * 下载失败的「一句话原因」与「分渠道明细」。
 *
 * 纯函数，无 Vue / 网络，便于 node --test 钉住几条会误导用户的边界。
 *
 * 背景（2026-11-01，issue #10-B）：队列里失败任务只显示「下载失败」，
 * 具体原因藏在 title 悬停里，用户根本看不到 —— 原话「个别下载任务成功，
 * 歌单全部失败，这是废了吗」。后端其实把原因链写全了
 * （`没有拿到任何可用直链：原生：该平台没有这一档音质；洛雪：宿主未开启`），
 * 只是界面上没把它摊开。
 */

/** 原因链的总前缀（后端 fetch.go 的汇总文案） */
const CHAIN_PREFIX = /^没有拿到任何可用直链：|^所有取音尝试都失败：/

/**
 * 把后端给的原因串拆成「一句话结论」+「分渠道明细」。
 *
 * 分渠道明细的每一行形如 `原生：该平台没有这一档音质`，渠道名与原因之间
 * 用全角冒号，多条之间用「；」分隔（后端就是这么拼的）。拆不出来时整串
 * 原样放进 detail，绝不让用户看到空白。
 *
 * @param {string} raw 后端给的 error 文本
 * @returns {{label:string, detail:string[]}} label 给徽标旁的小字，detail 给展开区
 */
export function explainFailure(raw) {
  const text = String(raw || '').trim()
  if (!text) return { label: '下载失败', detail: [] }

  const body = text.replace(CHAIN_PREFIX, '').trim()
  const detail = []
  for (const part of body.split('；')) {
    const seg = part.trim()
    if (seg) detail.push(seg)
  }

  // 一句话结论：挑**第一条**明细里的「是什么问题」，而不是把整串塞进徽标。
  // 用户扫一眼队列时只需要知道「是没链、还是平台不给这一档、还是写不进去」。
  const first = detail[0] || body
  return { label: briefLabel(first), detail: detail.length ? detail : [body] }
}

/** 单条明细 → 4~6 个字的小标签（认不出就截前 12 字） */
function briefLabel(seg) {
  const s = String(seg || '')
  const rules = [
    [/没有这一档|无此档/, '平台无此档'],
    [/不可播放|没有版权|收费/, '平台不可播'],
    [/宿主未开启|宿主未就绪|宿主启动失败/, '洛雪宿主未开'],
    [/写入文件失败|写盘|落盘失败/, '写盘失败'],
    [/超时|timeout/, '超时'],
    [/解析器未初始化|没有解析器/, '解析器未就绪'],
    [/没有启用的外挂|musicdl 未启用/, '外挂池未启用'],
    [/没有同名同版|没有同名/, '搜不到同一版'],
    [/没有可用直链|没有拿到/, '无可用直链'],
    [/HTTP 4\d\d|403|404|410/, '直链被拒'],
    [/不是音频|无效流|过小/, '下回来的不是音频'],
  ]
  for (const [re, label] of rules) if (re.test(s)) return label
  const t = s.replace(/^[^：]*：/, '').trim() // 去掉「原生：」这类渠道前缀再截
  return t.length > 12 ? `${t.slice(0, 12)}…` : t || '下载失败'
}
