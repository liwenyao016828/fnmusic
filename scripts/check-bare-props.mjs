#!/usr/bin/env node
// 检测 `<script setup>` 里的「裸用 props 名」缺陷。
//
// 背景：Vue 的编译器只把**模板**里的标识符映射到 `__props.x`。普通函数里裸写
// `activeSource?.id` 会抛 `ReferenceError`，按钮点下去没任何反应 ——
// 2026-10-23 用户报的「全部恢复 / 重试失败失效」就是这个（DownloadQueuePanel 的 onQueueMenu）。
//
// 判据（保守，宁少报不误报）：
//   1. 取 defineProps({...}) / defineProps({...}) 里的键名；
//   2. 取 <script setup> 段（不含模板）；
//   3. 找 `function 名(...) { ... }` 与 `const x = (…) =>` 的**函数体**，
//      在体里搜裸标识符（前面不是 `props.` / `.` / `__props`，且不在字符串/注释里）；
//   4. 该标识符若是 import 绑定或本地 ref/const 的名字则跳过。
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

/**
 * 把注释与字符串字面量抹成等长空白，这样标识符匹配就不会误报 ——
 * 「注释里提到 song」和「emit('update:current')」都不是裸用 prop。
 * 保持长度不变，行号与偏移才对得上。
 */
function blankOut(src) {
  const out = src.split('')
  const blank = (from, to) => {
    for (let i = from; i < to && i < out.length; i++) if (out[i] !== '\n') out[i] = ' '
  }
  let i = 0
  while (i < src.length) {
    const c = src[i]
    const two = src.slice(i, i + 2)
    if (two === '//') {
      let j = src.indexOf('\n', i)
      if (j === -1) j = src.length
      blank(i, j)
      i = j
      continue
    }
    if (two === '/*') {
      let j = src.indexOf('*/', i + 2)
      j = j === -1 ? src.length : j + 2
      blank(i, j)
      i = j
      continue
    }
    if (c === '"' || c === "'" || c === '`') {
      let j = i + 1
      while (j < src.length) {
        if (src[j] === '\\') { j += 2; continue }
        if (src[j] === c) { j++; break }
        // 模板字符串里的 `${…}` 是代码，不是文本：跳过这段再回到字符串处理
        if (c === '`' && src.slice(j, j + 2) === '${') {
          let depth = 1
          let k = j + 2
          while (k < src.length && depth > 0) {
            if (src[k] === '{') depth++
            else if (src[k] === '}') depth--
            k++
          }
          j = k
          continue
        }
        j++
      }
      blank(i, Math.min(j, src.length))
      i = j
      continue
    }
    i++
  }
  return out.join('')
}

const dir = process.argv[2] || 'src/components'
const files = readdirSync(dir).filter((f) => f.endsWith('.vue'))

let hits = 0
for (const f of files) {
  const raw = readFileSync(join(dir, f), 'utf8')
  if (!raw.includes('<script setup>')) continue

  const src = blankOut(raw)          // 匹配用（注释/字符串已抹白）
  const sm = src.indexOf('<script setup>')
  const se = src.indexOf('</script>', sm)
  const script = src.slice(sm, se)

  const propsMatch = script.match(/defineProps\(\s*\{([\s\S]*?)\}\s*\)/)
  if (!propsMatch) continue
  const propNames = [...propsMatch[1].matchAll(/^\s*([A-Za-z_$][\w$]*)\s*:/gm)].map((m) => m[1])
  if (!propNames.length) continue

  // 本地绑定：只要同名，函数里的裸用就是合法的（可能是解构/参数遮蔽），不报。
  // 宁可漏报也不误报 —— 误报两次就没人再看这个检查了。
  const locals = new Set()
  const addAll = (text) => { for (const t of text.split(',')) { const n = t.trim().split(/\s+as\s+/).pop().replace(/[=\s{}[\]]/g, ''); if (/^[A-Za-z_$][\w$]*$/.test(n)) locals.add(n) } }
  for (const m of script.matchAll(/^\s*import\s+(?:\{([\s\S]*?)\}|([A-Za-z_$][\w$]*))\s+from/gm)) addAll(m[1] || m[2] || '')
  for (const m of script.matchAll(/(?:const|let|var|function|class)\s+\{?([^=;{(]*)/g)) addAll(m[1])
  // 参数表：`(a, b) =>` / `(a, b) {` / `function f(a, b)`
  for (const m of script.matchAll(/\(([^()]*)\)\s*(?:=>|\{)/g)) addAll(m[1])
  for (const m of script.matchAll(/function\s+[A-Za-z_$][\w$]*\s*\(([^()]*)\)/g)) addAll(m[1])
  for (const m of script.matchAll(/([A-Za-z_$][\w$]*)\s*=>/g)) locals.add(m[1])
  for (const m of script.matchAll(/for\s*\(\s*(?:const|let|var)\s+([^)]*)\s+of/g)) addAll(m[1])
  for (const m of script.matchAll(/catch\s*\(\s*([A-Za-z_$][\w$]*)/g)) locals.add(m[1])

  // defineProps 那一段本身要跳过（`activeSource: Object` 看着就像裸用）
  const dpStart = script.indexOf(propsMatch[0])
  const dpEnd = dpStart + propsMatch[0].length

  // 真正的缺陷形态：**裸 prop 名 + 属性访问**（`activeSource?.id`、`song.name`）。
  // 模板里可以裸写，脚本里不行 —— 编译器只改写模板，函数里留着裸名就是 ReferenceError。
  for (const name of propNames) {
    if (locals.has(name)) continue
    const re = new RegExp(`(?<![\\w$.])${name}\\s*(?:\\?\\.|\\.)`, 'g')
    for (const m of script.matchAll(re)) {
      if (m.index >= dpStart && m.index < dpEnd) continue
      const line = src.slice(0, sm + m.index).split('\n').length
      console.log(`${f}:${line}  函数体内裸用 prop "${name}" 做属性访问 → ReferenceError（改用 props.${name}）`)
      hits++
    }
  }
}
console.log(hits ? `\n共 ${hits} 处待修` : 'OK：未发现 <script setup> 函数里裸用 props')
process.exit(hits ? 1 : 0)
