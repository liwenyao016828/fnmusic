// 模板/脚本一致性体检：模板里用到的标识符必须在 <script setup> 里有绑定。
// 踩过的坑（2026-10 白屏）：TidyView 模板引用了未定义的 planStreaming，
// vite build 与 compileTemplate 都不报错，运行时才炸成整页空白。
import { parse, compileScript, compileTemplate } from '@vue/compiler-sfc'
import fs from 'fs'

const files = process.argv.slice(2)
let bad = 0
for (const f of files) {
  const src = fs.readFileSync(f, 'utf8')
  const { descriptor, errors } = parse(src, { filename: f })
  if (errors.length) {
    console.log(f, 'PARSE', errors.map(e => e.message))
    bad++
    continue
  }
  const sc = compileScript(descriptor, { id: 'x' })
  const tpl = compileTemplate({
    source: descriptor.template.content,
    filename: f,
    id: 'x',
    compilerOptions: { bindingMetadata: sc.bindings },
  })
  if (tpl.errors.length) {
    console.log(f, 'TEMPLATE ERRORS:', tpl.errors.map(String))
    bad++
  }
  const names = new Set(Object.keys(sc.bindings))
  const used = new Set()
  for (const m of tpl.code.matchAll(/\$setup\.([A-Za-z0-9_$]+)/g)) used.add(m[1])
  const unbound = [...used].filter(n => !names.has(n))
  console.log(f, '=> template errors:', tpl.errors.length, '| unbound in template:', unbound)
  if (unbound.length) bad++
}
process.exit(bad ? 1 : 0)
