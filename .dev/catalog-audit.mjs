// 线上接口目录核对（开发期工具，不进包）
//
// 只做一件事：把**正在跑的那个进程**返回的 /api/catalog 与源码里的目录条目比一遍。
// 源码 ↔ 真实路由的两边对账已经搬进 Go 测试了 —— backend/pkg/api/catalog_routes_test.go
// （路由表在 routes.go）。这边原来也做这件事，但用「前缀包含」猜匹配：
// 它把 /api/sources 当成覆盖了整个 /api/sources/* 家族、把 /api/ai/usage 当成覆盖了
// /api/ai/usage/reset，于是真漏登记的接口被算成「已登记」，输出「（无）」。
// 那种判断方式的假清白比没有检查更糟，所以源码级检查只留 Go 测试一处。
//
// 这个脚本保留的理由是另一件事：**源码对得上不代表线上就是这一版** ——
// 旧进程没重启、旧包没替换，是排查「明明改了却没生效」时最常见的假象。
//
// 用法：node .dev/catalog-audit.mjs [baseURL]      # 默认 http://127.0.0.1:8900
// 源码级：go test ./pkg/api/ -run 'Catalog|Route' -count=1
import fs from 'node:fs'

const root = new URL('..', import.meta.url).pathname
const base = (process.argv[2] || 'http://127.0.0.1:8900').replace(/\/$/, '')

// ── 源码侧的期望值（只数条目，不做路由匹配）──
const catalogSrc = fs.readFileSync(root + 'backend/pkg/api/catalog.go', 'utf8')
const entries = []
const objRe = /Method:\s*http\.Method(\w+),\s*Path:\s*"([^"]+)"(?:[\s\S]{0,4000}?)Category:\s*(cat\w+)/g
for (const m of catalogSrc.matchAll(objRe)) {
  entries.push({ key: `${m[1].toUpperCase()} ${m[2]}`, category: m[3] })
}

// ── 线上侧 ──
let live
try {
  const res = await fetch(base + '/api/catalog')
  live = await res.json()
} catch (e) {
  console.error(`取不到 ${base}/api/catalog：${e.message}`)
  console.error('（服务没起来？或者端口不对。源码级检查不用起服务：go test ./pkg/api/ -run \'Catalog|Route\'）')
  process.exit(1)
}

const eps = live.endpoints || []
const core = eps.filter((e) => e.role === 'core').length
const internal = eps.filter((e) => e.role === 'internal').length
const maxNotes = Math.max(0, ...eps.map((e) => [...(e.notes || '')].length))
const badHidden = eps.filter((e) => (e.role === 'internal') !== !!e.hidden).map((e) => `${e.method} ${e.path}`)

console.log(`源码 ${entries.length} 条 / 线上 ${eps.length} 条`)
console.log(`version=${live.version} app=${live.app} auth_mode=${live.auth_mode}`)
console.log(`core=${core} internal=${internal} guide.workflows=${(live.guide?.workflows || []).length} usage=${(live.usage || []).length} 最长 notes=${maxNotes} 字`)
console.log(`role/hidden 不一致：${badHidden.length ? badHidden.join(', ') : '无'}`)

// 差异用 "METHOD path" 比，避免同路径多方法时误报
const liveKeys = new Set(eps.map((e) => `${e.method} ${e.path}`))
const srcKeys = new Set(entries.map((e) => e.key))
const onlyLive = [...liveKeys].filter((k) => !srcKeys.has(k)).sort()
const onlySrc = [...srcKeys].filter((k) => !liveKeys.has(k)).sort()
if (onlyLive.length || onlySrc.length || eps.length !== entries.length) {
  console.log('\n⚠️ 线上与源码不一致 —— 大概率跑的是旧进程 / 旧包：')
  if (onlyLive.length) console.log('   只在线上有：\n' + onlyLive.map((k) => '     ' + k).join('\n'))
  if (onlySrc.length) console.log('   只在源码里有：\n' + onlySrc.map((k) => '     ' + k).join('\n'))
  process.exitCode = 2
} else {
  console.log('\n线上这一版与源码一致。')
}

const byCat = {}
for (const e of eps) byCat[e.category] = (byCat[e.category] || 0) + 1
console.log('\n分类统计（线上）:')
for (const [k, v] of Object.entries(byCat).sort((a, b) => b[1] - a[1])) console.log(`   ${String(v).padStart(3)}  ${k}`)
