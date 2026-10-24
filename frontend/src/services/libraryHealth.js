// 库体检的前端服务：把后端 `/api/nas/health/*` 的响应整理成界面能直接用的形状。
//
// 背景（用户 2026-10-24）：「飞牛文件管理和官方音乐 App 打开音乐文件夹显示未知错误」。
// 根因之一是文件名里带着歌曲标签的 GBK 脏字节（非法 UTF-8），fnOS 的文件/媒体接口
// 读到这里就整层报错。下载侧已经在写入前清洗，这里负责把**盘上已有的**坏名字找出来修掉。
//
// 为什么直接 fetch 而不是加进 api/client.js 的 API 表：那几个表是多人并行编辑的热点，
// 新增两个小接口不值得在那里制造冲突；路径与超时在这里写死反而更好审查。

import { apiError } from './userMsg.js'

const SCAN_TIMEOUT = 120000 // 全盘扫一遍很慢，别用默认 30s 把用户逼成重试
const FIX_TIMEOUT = 120000

/** 分类的中文名。后端给的是稳定英文 kind（见 backend/pkg/nas/health.go）。 */
export const ISSUE_LABELS = {
  bad_utf8: '文件名编码损坏',
  control_char: '控制字符',
  illegal_char: '非法字符',
  trailing_dot: '首尾点/空格',
  too_long: '名字过长',
  zero_byte: '0 字节音频',
  leftover_part: '下载残留',
  no_permission: '权限 000',
  not_regular: '非常规文件',
  broken_symlink: '软链接失效',
  unreadable_dir: '目录读不了',
}

export function issueLabel(kind) {
  return ISSUE_LABELS[kind] || kind || '未知问题'
}

/**
 * 哪些问题会真的让飞牛报「未知错误」。
 *
 * 只有名字类问题会让整层目录读不出来；0 字节音频与 .part 残留只是垃圾。
 * 界面上要分开说，否则用户会以为「删掉残留就能修好未知错误」。
 */
export function causesFolderError(kind) {
  return ['bad_utf8', 'control_char', 'illegal_char', 'trailing_dot', 'too_long', 'no_permission', 'unreadable_dir'].includes(kind)
}

/** 汇总：总条数、会引发文件夹报错的条数、可自动修复的条数。 */
export function summarizeIssues(issues) {
  const list = Array.isArray(issues) ? issues : []
  let fatal = 0
  let fixable = 0
  for (const it of list) {
    if (causesFolderError(it.kind)) fatal++
    if (it.fix === 'rename' || it.fix === 'delete' || it.fix === 'chmod') fixable++
  }
  return { total: list.length, fatal, fixable }
}

/**
 * 一键修复时挑哪些。
 *
 * `names` = 只修名字类（最安全，用户来这一趟通常就是为了让文件夹能打开）；
 * `all`   = 连残留/0 字节音频一起清。
 * 需要人工处理的（fix === 'manual'）永远不自动动 —— 它们要么是 socket/软链接，
 * 要么算不出更好的名字，自动处理只会制造新问题。
 */
export function pickFixable(issues, mode = 'names') {
  const list = Array.isArray(issues) ? issues : []
  return list.filter(it => {
    if (it.fix !== 'rename' && it.fix !== 'delete' && it.fix !== 'chmod') return false
    if (mode === 'names') return it.fix === 'rename' || it.fix === 'chmod'
    return true
  })
}

async function postJSON(url, body, timeout) {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeout)
  try {
    const res = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body || {}),
      signal: controller.signal,
    })
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error(apiError(data, `请求失败（HTTP ${res.status}）`))
    if (data && data.code && data.code !== 200) throw new Error(apiError(data, '请求失败'))
    return data
  } finally {
    clearTimeout(timer)
  }
}

/** 扫描：dir 为空时后端会用自己认定的曲库根目录。 */
export async function scanHealth(dir = '') {
  const data = await postJSON('/api/nas/health/scan', dir ? { dir } : {}, SCAN_TIMEOUT)
  const issues = Array.isArray(data.issues) ? data.issues : []
  return {
    dir: data.dir || dir || '',
    scanned: Number(data.scanned) || 0,
    issues,
    truncated: !!data.truncated,
    elapsedMs: Number(data.elapsed_ms) || 0,
    summary: summarizeIssues(issues),
  }
}

/**
 * 修复：只提交挑出来的那几条。
 *
 * 传的是 `id`（路径字节的 base64）而不是 `path` —— 非法 UTF-8 的名字过一趟 JSON
 * 就会变成 U+FFFD，后端拿去 os.Stat 必然找不到，而那正是最需要修的一类。
 * 后端只接受自己扫描缓存里的 id，所以这里送不了「任意路径」。
 */
export async function fixHealth(dir, issues, { dryRun = false } = {}) {
  const list = Array.isArray(issues) ? issues : []
  const payload = { ids: list.map(it => it && it.id).filter(Boolean), dry_run: !!dryRun }
  if (dir) payload.dir = dir
  const data = await postJSON('/api/nas/health/fix', payload, FIX_TIMEOUT)
  return {
    fixed: Number(data.fixed) || 0,
    wouldFix: Number(data.would_fix) || 0,
    missed: Number(data.missed) || 0,
    failed: Array.isArray(data.failed) ? data.failed : [],
    results: Array.isArray(data.results) ? data.results : [],
  }
}
