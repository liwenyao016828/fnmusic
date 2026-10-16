/**
 * 写队列的单测（`npm test` 跑的就是这些）。
 *
 * 这几条守的都是**用户能看见的失败**：
 * 连点丢改动、全部取消不生效、失败后 UI 永远卡在「保存中」、错误之后再也不能写。
 * 每条都对着一个具体写法 —— 注释里写明「换个写法就红」，改实现时照着试一下。
 */
import test from 'node:test'
import assert from 'node:assert/strict'

import { createWriteQueue } from './writeQueue.js'

/** 等队列跑空（跑不完就是实现卡住了，直接抛，别让断言在错的地方失败） */
async function settle(q, turns = 200) {
  for (let i = 0; i < turns; i++) {
    if (!q.running) return
    await new Promise((r) => setTimeout(r, 0))
  }
  throw new Error('队列没有停下来')
}

test('写的时候再改：最后一次意图一定被写出去（不丢）', async () => {
  const writes = []
  let release
  const gate = new Promise((r) => { release = r })
  const q = createWriteQueue({
    send: async (w) => {
      writes.push(w.ids)
      if (writes.length === 1) await gate       // 第一次故意吊住
    },
  })

  q.ids(['wy_3778678'])
  q.ids(['wy_3778678', 'tx_26'])                // 第一次还在飞，这个只能排队
  release()
  await settle(q)

  // 换个写法就红：动作里加 `if (running) return`（旧实现就是这么丢改动的）
  assert.equal(writes.length, 2, '排队的改动必须被补写')
  assert.deepEqual(writes[1], ['wy_3778678', 'tx_26'], '补写的必须是最后一次意图')
})

test('「全部取消」写的是空数组：不能被当成「没有待写」吃掉', async () => {
  const writes = []
  const q = createWriteQueue({ send: async (w) => writes.push(w.ids) })

  q.ids(['wy_1'])
  await settle(q)
  q.ids([])
  await settle(q)

  // 换个写法就红：`while (pendingIds)` / setter 里 `if (!list) return` —— 空数组是合法值
  assert.equal(writes.length, 2)
  assert.deepEqual(writes[1], [])
})

test('写失败：报错一次、丢掉排队中的意图、队列还能继续用', async () => {
  const writes = []
  const errs = []
  let release
  const gate = new Promise((r) => { release = r })
  const q = createWriteQueue({
    send: async (w) => {
      writes.push(w)
      if (writes.length === 1) {
        await gate
        throw new Error('boom')
      }
    },
    onError: async (e) => errs.push(e.message),
  })

  q.ids(['wy_1'])
  q.enabled(false)          // 第一次还在飞：这个进了排队
  release()
  await settle(q)

  assert.deepEqual(errs, ['boom'])
  // 失败后本地会被重读的真相覆盖，这时候再补写排队里的旧值 = 拿旧值盖真相
  assert.equal(writes.length, 1, '失败后不该补写排队中的意图')
  // 换个写法就红：finally 里漏了 `running = false` —— 之后所有改动都写不出去
  assert.equal(q.running, false, '失败后队列必须复位')

  q.enabled(false)
  await settle(q)
  assert.equal(writes.length, 2, '错误之后还得能写')
  assert.equal(writes[1].enabled, false)
})

test('busy 回调成对出现（否则界面永远卡在「保存中…」）', async () => {
  const states = []
  const q = createWriteQueue({ send: async () => {}, onBusy: (b) => states.push(b) })

  q.ids(['wy_1'])
  await settle(q)

  // 换个写法就红：漏掉 finally 里的 onBusy(false)
  assert.deepEqual(states, [true, false])
})

// ── 回归（2026-10-18）：音质下载这一路曾整条漏掉 ──────────────────────────────
// 现场表现：榜单段里改「音质下载」下拉，界面上选了、刷新页面又变回去 —— 因为
// chartInject.js 调的 `writes.adq(...)` 根本不存在，抛的 TypeError 没人接。
// 这几条钉住「每一个对外承诺的键都真的有方法」，不只是 adq 一个。
test('音质下载：adq 是队列的方法，且真的会被写出去', async () => {
  const writes = []
  const q = createWriteQueue({ send: async (w) => { writes.push(w) } })
  assert.equal(typeof q.adq, 'function', 'writes.adq 必须存在 —— 缺了就是改音质静默不生效')
  await q.adq('320k')
  await settle(q)
  assert.deepEqual(writes, [{ ids: undefined, enabled: undefined, ui: undefined, adq: '320k' }])
})

test('音质下载：空串（「跟随」）是合法取值，不能被当成「没有待写」吃掉', async () => {
  const writes = []
  const q = createWriteQueue({ send: async (w) => { writes.push(w) } })
  await q.adq('')
  await settle(q)
  assert.equal(writes.length, 1, '写「跟随」也必须发一次 —— 用真假值判会把它吃掉')
  assert.equal(writes[0].adq, '')
})

test('四个键互不干扰：改音质不会顺手把勾选/开关写一遍', async () => {
  const writes = []
  const q = createWriteQueue({ send: async (w) => { writes.push(w) } })
  await q.adq('flac')
  await settle(q)
  assert.equal(writes[0].ids, undefined, '没动的键必须是 undefined（后端选择性合并，undefined 才代表「不改」）')
  assert.equal(writes[0].enabled, undefined)
  assert.equal(writes[0].ui, undefined)
})

test('chartInject 调的每一个 writes.X 都真的存在（防「漏登记一个键」再演一次）', async () => {
  const { readFileSync } = await import('node:fs')
  const { fileURLToPath } = await import('node:url')
  const { dirname, join } = await import('node:path')
  const here = dirname(fileURLToPath(import.meta.url))
  const src = readFileSync(join(here, './chartInject.js'), 'utf8')
  const q = createWriteQueue({ send: async () => {} })
  const used = [...src.matchAll(/writes\.([a-zA-Z_$][\w$]*)\s*\(/g)].map((m) => m[1])
  assert.ok(used.length >= 4, `chartInject 应该用到 4 个写入口，实际只认出 ${used.length} 个`)
  for (const k of new Set(used)) {
    assert.equal(typeof q[k], 'function', `writeQueue 少了 ${k}() —— chartInject 正在调它，会静默失败`)
  }
})
