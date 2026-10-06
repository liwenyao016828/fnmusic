import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  MAX_IMPORT_ITEMS,
  isScriptFile,
  splitScriptNames,
  rejectedText,
  overflowText,
  urlsFromText,
  mergePicked,
} from './sourceImport.js'

test('isScriptFile：只认 .js/.mjs/.cjs，大小写不敏感', () => {
  assert.equal(isScriptFile('a.js'), true)
  assert.equal(isScriptFile('a.mjs'), true)
  assert.equal(isScriptFile('a.cjs'), true)
  assert.equal(isScriptFile('A.JS'), true)
  assert.equal(isScriptFile(' 星海.js '), true)
  assert.equal(isScriptFile('a.txt'), false)
  assert.equal(isScriptFile('a.js.txt'), false)
  assert.equal(isScriptFile('a.png'), false)
  assert.equal(isScriptFile('js'), false)
  assert.equal(isScriptFile(''), false)
  assert.equal(isScriptFile(undefined), false)
  assert.equal(isScriptFile(null), false)
})

test('splitScriptNames：分拣并保持原顺序', () => {
  const { scripts, rejected } = splitScriptNames(['b.js', 'note.txt', 'a.mjs', 'x.png'])
  assert.deepEqual(scripts, ['b.js', 'a.mjs'])
  assert.deepEqual(rejected, ['note.txt', 'x.png'])
})

test('splitScriptNames：空名字（拖文件夹）只报一次，不刷屏', () => {
  const { scripts, rejected } = splitScriptNames(['', '', 'ok.js'])
  assert.deepEqual(scripts, ['ok.js'])
  assert.equal(rejected.length, 1)
  assert.match(rejected[0], /文件夹/)
})

test('splitScriptNames：重复的拒绝项只报一次；脚本同名不去重（交给 mergePicked 处理）', () => {
  const { scripts, rejected } = splitScriptNames(['a.txt', 'a.txt', 'dup.js', 'dup.js'])
  assert.deepEqual(scripts, ['dup.js', 'dup.js'])
  assert.deepEqual(rejected, ['a.txt'])
})

test('rejectedText：没有拒绝项就说空话（模板靠它决定显不显示）', () => {
  assert.equal(rejectedText([]), '')
  assert.equal(rejectedText(), '')
})

test('rejectedText：超过 3 个只列前 3 个 + 总数', () => {
  const text = rejectedText(['a.txt', 'b.png', 'c.zip', 'd.md', 'e.pdf'])
  assert.match(text, /已忽略 5 个非脚本文件/)
  assert.match(text, /a\.txt、b\.png、c\.zip/)
  assert.match(text, /等 5 个/)
  assert.equal(text.includes('d.md'), false)
})

test('overflowText：到上限不拦，超了才拦', () => {
  assert.equal(overflowText(MAX_IMPORT_ITEMS), '')
  assert.equal(overflowText(1), '')
  const text = overflowText(MAX_IMPORT_ITEMS + 2)
  assert.match(text, /最多导入 50 条/)
  assert.match(text, /当前已选 52 条/)
})

test('urlsFromText：捞出直链并去掉句尾标点', () => {
  const urls = urlsFromText('看这个 https://a.com/x.js。还有 https://b.com/y.js,')
  assert.deepEqual(urls, ['https://a.com/x.js', 'https://b.com/y.js'])
})

test('urlsFromText：去重且只认 http(s)', () => {
  const urls = urlsFromText('https://a.com/x.js\nhttps://a.com/x.js\nftp://c.com/z.js')
  assert.deepEqual(urls, ['https://a.com/x.js'])
})

test('urlsFromText：非文本/空值不抛异常', () => {
  assert.deepEqual(urlsFromText(''), [])
  assert.deepEqual(urlsFromText(undefined), [])
  assert.deepEqual(urlsFromText(null), [])
  assert.deepEqual(urlsFromText(123), [])
  assert.deepEqual(urlsFromText('没有链接'), [])
})

test('mergePicked：同名以新来的为准，顺序保持「老的在原位、新的追加」', () => {
  const out = mergePicked(
    [{ filename: 'a.js', content: 'old' }, { filename: 'b.js', content: 'b' }],
    [{ filename: 'a.js', content: 'new' }, { filename: 'c.js', content: 'c' }],
  )
  assert.deepEqual(out.map(x => x.filename), ['a.js', 'b.js', 'c.js'])
  assert.equal(out[0].content, 'new')
})

test('mergePicked：新来的覆盖旧的 fromNas 标记（本地重选同名文件 = 不再是 NAS 来的）', () => {
  const out = mergePicked(
    [{ filename: 'a.js', content: 'nas', fromNas: '/vol1/Music/a.js' }],
    [{ filename: 'a.js', content: 'local' }],
  )
  assert.equal(out.length, 1)
  assert.equal(out[0].content, 'local')
  assert.equal(out[0].fromNas, undefined)
})

test('mergePicked：脏输入（缺 filename / 非数组 / null 项）不炸也不混进结果', () => {
  assert.deepEqual(mergePicked(null, null), [])
  assert.deepEqual(mergePicked([{ content: 'no-name' }, null], []), [])
  assert.deepEqual(mergePicked([], [{ filename: 'ok.js', content: '' }]).length, 1)
})
