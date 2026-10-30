// 「元数据补全一扫描，缺歌词清单就自动列出来」这条联动的护栏（2026-10-29 用户要求）。
//
// 用户原话：「元数据扫描后，只补歌词的列表要显示缺的歌曲」。
// 这条联动很容易被后来的重构悄悄拆掉（它就是两行 if），而拆掉之后界面**不会报错**，
// 只是用户又得回那张卡手点一次「扫描缺歌词」—— 没人会注意到。
// 所以这里钉三件事：
//   ① composable 的 scan 支持安静模式，并且会把「这份清单是顺手填的」标出来；
//   ② 自己点扫描（非安静）要把那个标记清掉，否则文案会一直说「刚顺带列的」；
//   ③ TidyView 的补全扫描真的调了它，且已有清单时**不覆盖**（覆盖会清掉用户的勾选）。
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const src = (rel) => readFileSync(join(here, '..', rel), 'utf8')

const lyricSrc = src('services/useLyricGaps.js')
const tidySrc = src('components/TidyView.vue')

/** 取源码里一段（用于把断言限定在函数体内，避免「文件里碰巧有这个词」就算过） */
function slice(text, startMark, endMark) {
  const i = text.indexOf(startMark)
  assert.ok(i >= 0, `源码里找不到起点：${startMark}`)
  const j = text.indexOf(endMark, i)
  assert.ok(j > i, `源码里找不到终点：${endMark}`)
  return text.slice(i, j)
}

test('scan 支持 quiet 模式，并把「顺手填的」标出来', () => {
  const fn = slice(lyricSrc, 'async function scan(opts = {})', 'function toggle(it)')
  assert.match(fn, /const quiet = !!opts\.quiet/, 'scan 要读 opts.quiet')
  assert.match(fn, /autoFilled\.value = quiet/, 'quiet 扫出来的清单要标成自动填的，非 quiet 要清掉这个标记')
  // 安静模式不能再刷「都已有歌词」这类好消息，否则会盖掉「预览完成」那条
  assert.match(fn, /!list\.length && !quiet/, '安静模式下不该再弹「都已有歌词了」')
  // 清单为空时也不能在安静模式下弹错（补全扫描时目录可能还没填）
  assert.match(fn, /if \(!quiet\) flash\('先在「选项」里填曲库目录', true\)/)
})

test('composable 把 autoFilled 暴露给页面', () => {
  const ret = slice(lyricSrc, 'return {\n    items,', '\n}')
  assert.match(ret, /\bautoFilled\b/, 'autoFilled 必须导出，否则页面无从判断清单来源')
})

test('补全扫描会顺手拉缺歌词清单，但已有清单时不覆盖', () => {
  const fn = slice(tidySrc, 'async function startPreview()', 'let failure')
  assert.match(fn, /void lyric\.scan\(\{ quiet: true \}\)/, '补全扫描要顺手拉一次缺歌词清单')
  assert.match(fn, /if \(!lyricItems\.value\.length && !lyricScanning\.value\)/,
    '只在「本来没有清单、也没在扫」时拉 —— 已有清单里的勾选不能被覆盖')
})

test('缺歌词卡的文案说清了清单从哪来', () => {
  assert.match(tidySrc, /lyricAutoFilled \? '（下面这份是刚才元数据扫描时顺带列的）'/,
    '自动填的清单要在描述里说明来源')
  assert.match(tidySrc, /上面的「元数据补全」一扫描，这里会自动列出缺词的歌/,
    '空态要告诉用户「扫补全就会自动列」，而不是只让他点扫描')
})
