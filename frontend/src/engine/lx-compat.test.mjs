/**
 * LX 兼容层的测试。
 *
 * 这一层是「同一个音源觅音能用、我们报错」的修复，属于**静默失效高发区**
 * （比如 rsaEncrypt 原来是空壳：不报错但返回错数据，表现成「搜不到歌」），
 * 所以关键路径都要有测试守着。
 */

import test from 'node:test'
import assert from 'node:assert/strict'
import { Buffer } from 'buffer'

import {
  parseRsaPublicKey,
  rsaEncryptNoPadding,
  createRequireShim,
  zlibInflate,
  zlibDeflate,
  md5Hex,
  bufToString,
  __testing,
} from './lx-compat.js'

// ─────────────────────────────────────────────────────────────
// md5 的返回类型 —— 曾经返回 Buffer 导致真实音源直接崩
// ─────────────────────────────────────────────────────────────

test('md5Hex 返回 32 字符 hex 字符串，不是 Buffer', () => {
  const r = md5Hex('hello')
  assert.equal(typeof r, 'string', '必须是字符串 —— 返回 Buffer 会让 md5(x).length 变成 16')
  assert.equal(r, '5d41402abc4b2a76b9719d911017c592')
  assert.equal(r.length, 32)
})

test('md5Hex 兼容 md5(x).toString("hex") 的写法（洛雪脚本常见）', () => {
  // String.prototype.toString 忽略参数并返回自身，所以这个写法必须仍然成立
  assert.equal(md5Hex('hello').toString('hex'), '5d41402abc4b2a76b9719d911017c592')
})

test('md5Hex 对非字符串入参也做 String() 转换（对齐觅音）', () => {
  assert.equal(md5Hex(123), md5Hex('123'))
  assert.equal(md5Hex(null), md5Hex('null'))
})

test('md5Hex 的长度可安全用于补零运算（回归：曾算出负数）', () => {
  // 真实脚本会做 `'0'.repeat(32 - md5(x).length)` 之类的补零；
  // 若 md5 返回 16 字节 Buffer，这里就是 16（不崩），
  // 但脚本里其它按 hex 字符数算的地方会算出负数并抛
  // `RangeError: Invalid count value: -N`。
  const n = 32 - md5Hex('晴天').length
  assert.equal(n, 0)
  assert.ok(n >= 0, '补零数量不能为负')
})

// ─────────────────────────────────────────────────────────────
// bufToString 的字符串语义
// ─────────────────────────────────────────────────────────────

test('bufToString: Buffer 入参按指定编码转字符串', () => {
  assert.equal(bufToString(Buffer.from('abc'), 'utf8'), 'abc')
  assert.equal(bufToString(Buffer.from('616263', 'hex'), 'utf8'), 'abc')
})

test('bufToString: 字符串入参走 binary 再解码（不是原样返回）', () => {
  // 'é' 的 code point 是 233（>0x7F）。binary 编码 = latin1，
  // 再按 utf8 解出来是两字节的替换字符 —— 与「原样返回」结果不同。
  const r = bufToString('é', 'utf8')
  assert.notEqual(r, 'é', '不能原样返回 —— 那样与洛雪/觅音语义不一致')
  assert.equal(r, Buffer.from('é', 'binary').toString('utf8'))
})

test('bufToString: 纯 ASCII 字符串两种语义结果相同（不影响老脚本）', () => {
  assert.equal(bufToString('hello', 'utf8'), 'hello')
})

test('bufToString: 默认编码是 utf8', () => {
  assert.equal(bufToString(Buffer.from('abc')), 'abc')
})

// 真实生成的 2048 位 RSA 公钥（测试专用，私钥已丢弃）
const TEST_PUBLIC_KEY = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA0x0FxvuywdmWhn+R3qhX
fT3E2eNsIixmVr+dUpTXSdW/35d4amCQEW7GMBtslyKB2y8MtC6Gu5akXlnDIZAQ
r8AbHXAQGa+6bUE9Dj4FQ3QmQOnSOBNARYsmX2xXXv0UXW3SUcGYXyKcGF3mhKZn
zjaaFSj02OmiUyX2lo3c9iNM1zaU10BlbB1YDN8rG2t0vOM8nNe1iWg/pGDoGWIy
h6X0rz20YVbcBzW6QAD0+yKXwbm1VqlM4S1NHHJ/beEpx7b8QL1lMbAbAZmyzl0r
rQIYHKOzpPa0iKe6f6mtbbwIGg+bDAish2z6nc9lNSPfk2aqqpse2g4f1kuwFUhx
TwIDAQAB
-----END PUBLIC KEY-----`

// ── DER 解析 ──

test('parseRsaPublicKey 能解析真实 PEM 并取出 e', () => {
  const { n, e } = parseRsaPublicKey(TEST_PUBLIC_KEY)
  // RSA 公钥指数几乎总是 65537
  assert.equal(e, 65537n)
  // 2048 位模数
  assert.equal(n.toString(2).length, 2048)
})

test('parseRsaPublicKey 容忍无 PEM 头尾的裸 base64', () => {
  const bare = TEST_PUBLIC_KEY.replace(/-----[^-]+-----/g, '').replace(/\s+/g, '')
  const a = parseRsaPublicKey(TEST_PUBLIC_KEY)
  const b = parseRsaPublicKey(bare)
  assert.equal(a.n, b.n)
  assert.equal(a.e, b.e)
})

// ── 裸 RSA 加密 ──

test('rsaEncryptNoPadding 输出长度等于模长（256 字节）', () => {
  const out = rsaEncryptNoPadding(Buffer.from('hello'), TEST_PUBLIC_KEY)
  assert.equal(out.length, 256)
})

test('rsaEncryptNoPadding 对同一输入是确定性的', () => {
  const a = rsaEncryptNoPadding(Buffer.from('same input'), TEST_PUBLIC_KEY)
  const b = rsaEncryptNoPadding(Buffer.from('same input'), TEST_PUBLIC_KEY)
  assert.deepEqual([...a], [...b])
})

test('rsaEncryptNoPadding 对不同输入产生不同密文', () => {
  const a = rsaEncryptNoPadding(Buffer.from('aaa'), TEST_PUBLIC_KEY)
  const b = rsaEncryptNoPadding(Buffer.from('bbb'), TEST_PUBLIC_KEY)
  assert.notDeepEqual([...a], [...b])
})

test('rsaEncryptNoPadding 输入超长要报错（不静默截断）', () => {
  const tooLong = Buffer.alloc(300, 1)
  assert.throws(() => rsaEncryptNoPadding(tooLong, TEST_PUBLIC_KEY), /输入过长/)
})

// 裸模幂的正确性：用玩具数字手算验证
test('modPow 与手算一致', () => {
  // 3^5 mod 7 = 243 mod 7 = 5
  assert.equal(__testing.modPow(3n, 5n, 7n), 5n)
  // 4^13 mod 497 = 445（经典例子）
  assert.equal(__testing.modPow(4n, 13n, 497n), 445n)
  // 指数为 0 时结果为 1
  assert.equal(__testing.modPow(123n, 0n, 7n), 1n)
})

// ── zlib ──

test('zlib deflate → inflate 往返一致', async () => {
  const original = Buffer.from('hello zlib roundtrip 你好')
  const deflated = await zlibDeflate(original)
  const back = await zlibInflate(deflated)
  assert.equal(back.toString('utf8'), original.toString('utf8'))
})

test('zlib inflate 能解裸 deflate（无 zlib 头）', async () => {
  // 用 deflate-raw 造一个裸流，inflate 应能自动识别
  const original = Buffer.from('raw deflate payload')
  const cs = new CompressionStream('deflate-raw')
  const writer = cs.writable.getWriter()
  writer.write(original)
  writer.close()
  const chunks = []
  const reader = cs.readable.getReader()
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    chunks.push(value)
  }
  const raw = Buffer.concat(chunks.map(c => Buffer.from(c)))

  const back = await zlibInflate(raw)
  assert.equal(back.toString('utf8'), original.toString('utf8'))
})

// ── require 兼容 ──

test('require 放行白名单模块', () => {
  const req = createRequireShim({ crypto: { randomBytes: () => Buffer.alloc(4) } })
  assert.equal(typeof req('buffer').Buffer, 'function')
  assert.equal(typeof req('node:buffer').Buffer, 'function') // node: 前缀要能剥掉
  assert.equal(typeof req('crypto').createHash, 'function')
  assert.equal(typeof req('url').URL, 'function')
  assert.equal(typeof req('zlib').inflate, 'function')
})

test("require 对白名单外的模块要明确报错（不返回 undefined）", () => {
  const req = createRequireShim({ crypto: {} })
  assert.throws(() => req('fs'), /沙箱不支持/)
  assert.throws(() => req('child_process'), /沙箱不支持/)
  assert.throws(() => req('express'), /沙箱不支持/)
})

test('require("crypto").createHash("md5") 结果正确', () => {
  const req = createRequireShim({ crypto: {} })
  const c = req('crypto')
  // md5("hello") 是公开的已知值
  assert.equal(c.createHash('md5').update('hello').digest('hex'), '5d41402abc4b2a76b9719d911017c592')
})

test('require("crypto").publicEncrypt 走的是裸 RSA', () => {
  const req = createRequireShim({ crypto: {} })
  const c = req('crypto')
  const out = c.publicEncrypt({ key: TEST_PUBLIC_KEY }, Buffer.from('x'))
  assert.equal(out.length, 256)
})
