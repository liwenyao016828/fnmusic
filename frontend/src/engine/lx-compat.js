/**
 * LX 音源脚本的 Node 兼容层。
 *
 * ── 为什么需要这个 ──
 *
 * 参考实现（觅音）把音源脚本放在 **Node 的 vm 沙箱**里跑，并提供了：
 *   · 沙箱全局：require（限 crypto/buffer/url）、module、exports、global、Buffer
 *   · lx.utils.crypto：aesEncrypt / rsaEncrypt / randomBytes / md5
 *   · lx.utils.zlib：inflate / deflate
 *
 * 我们跑在**浏览器**里，上面这些默认一个都没有。于是出现
 * 「同一个音源，觅音能用、我们报错」—— 典型的两种崩法：
 *   1. `require is not defined` / `lx.utils.zlib is undefined` → 直接 TypeError
 *   2. `rsaEncrypt` 我们原来是空壳（原样返回入参）→ 不报错但数据是错的，
 *      脚本后续解密得到乱码，表现为「搜不到 / 取不到链」这种看不懂的现象
 *
 * 这里把能真实现的都真实现：
 *   · zlib      → 浏览器原生 CompressionStream / DecompressionStream
 *   · rsaEncrypt→ BigInt 做**裸模幂**（对齐 Node 的 RSA_NO_PADDING 语义）
 *   · require   → 映射到本文件的 crypto/buffer/url 三个模块
 *
 * 做不到的一律**明确抛错**，不静默返回错数据 —— 后者更难排查。
 */

import { Buffer } from 'buffer'
import CryptoJS from 'crypto-js'

// ─────────────────────────────────────────────────────────────
// zlib：用浏览器原生的压缩流实现
// ─────────────────────────────────────────────────────────────

/** 把一段字节喂给某个 (De)CompressionStream，拿回结果 */
async function throughStream(stream, bytes) {
  const writer = stream.writable.getWriter()

  // ⚠️ 这里**不能 await**：流出错时 writable 侧也会 reject，
  // 而我们要靠 readable 侧的错误来触发「换一种封装重试」。
  // 不自己接住的话它会变成 unhandledRejection —— 错误信息还是空的，极难排查。
  writer
    .write(bytes)
    .then(() => writer.close())
    .catch(() => {})

  const chunks = []
  const reader = stream.readable.getReader()
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    chunks.push(value)
  }
  let total = 0
  for (const c of chunks) total += c.length
  const out = new Uint8Array(total)
  let off = 0
  for (const c of chunks) {
    out.set(c, off)
    off += c.length
  }
  return out
}

function hasStreamSupport() {
  return typeof DecompressionStream !== 'undefined' && typeof CompressionStream !== 'undefined'
}

/**
 * 解压。自动兼容两种封装：
 *   · 带 zlib 头（多数音源接口返回的 deflate）
 *   · 裸 deflate（少数）
 */
export async function zlibInflate(input) {
  if (!hasStreamSupport()) {
    throw new Error('当前浏览器不支持解压（缺少 DecompressionStream）')
  }
  const bytes = Buffer.isBuffer(input) ? input : Buffer.from(input)
  try {
    return Buffer.from(await throughStream(new DecompressionStream('deflate'), bytes))
  } catch (_) {
    // 带 zlib 头解不开 → 多半是裸 deflate
    return Buffer.from(await throughStream(new DecompressionStream('deflate-raw'), bytes))
  }
}

export async function zlibDeflate(input) {
  if (!hasStreamSupport()) {
    throw new Error('当前浏览器不支持压缩（缺少 CompressionStream）')
  }
  const bytes = Buffer.isBuffer(input) ? input : Buffer.from(input)
  return Buffer.from(await throughStream(new CompressionStream('deflate'), bytes))
}

// ─────────────────────────────────────────────────────────────
// RSA：BigInt 裸模幂，对齐 Node crypto 的 RSA_NO_PADDING
// ─────────────────────────────────────────────────────────────

/** 极简 DER 读取器：够解析 RSA 公钥即可 */
function readTLV(buf, pos) {
  const tag = buf[pos]
  let len = buf[pos + 1]
  let lenBytes = 1
  if (len & 0x80) {
    const n = len & 0x7f
    len = 0
    for (let i = 0; i < n; i++) len = (len << 8) | buf[pos + 2 + i]
    lenBytes = 1 + n
  }
  const start = pos + 1 + lenBytes
  return { tag, start, end: start + len, next: start + len }
}

/**
 * 从 PEM / 裸 base64 的 RSA 公钥里取出 n 与 e。
 *
 * 结构（SubjectPublicKeyInfo）：
 *   SEQUENCE { SEQUENCE { OID, NULL }, BIT STRING { SEQUENCE { INTEGER n, INTEGER e } } }
 */
export function parseRsaPublicKey(pem) {
  const b64 = String(pem)
    .replace(/-----BEGIN[^-]+-----/g, '')
    .replace(/-----END[^-]+-----/g, '')
    .replace(/\s+/g, '')
  const der = Buffer.from(b64, 'base64')

  let p = readTLV(der, 0) // 最外层 SEQUENCE
  let q = readTLV(der, p.start) // AlgorithmIdentifier
  let r = readTLV(der, q.next) // BIT STRING
  // BIT STRING 头一个字节是「未用位数」，跳过
  const inner = r.start + 1
  let s = readTLV(der, inner) // RSAPublicKey SEQUENCE
  let m = readTLV(der, s.start) // INTEGER n
  let e = readTLV(der, m.next) // INTEGER e

  const toBig = (t) => {
    let bytes = der.subarray(t.start, t.end)
    // 去掉前导 0（DER 里正数会补一个 0x00）
    let i = 0
    while (i < bytes.length - 1 && bytes[i] === 0) i++
    return BigInt('0x' + Buffer.from(bytes.subarray(i)).toString('hex'))
  }
  return { n: toBig(m), e: toBig(e) }
}

function modPow(base, exp, mod) {
  let result = 1n
  base %= mod
  while (exp > 0n) {
    if (exp & 1n) result = (result * base) % mod
    base = (base * base) % mod
    exp >>= 1n
  }
  return result
}

/**
 * 裸 RSA 加密（无填充），对齐 Node 的 `RSA_NO_PADDING`。
 *
 * 语义要点：
 *   · 输入按模长右对齐，**左侧补零**（不是 PKCS#1 填充）
 *   · 输出长度恒等于模长
 */
export function rsaEncryptNoPadding(input, pemKey) {
  const { n, e } = parseRsaPublicKey(pemKey)
  const buf = Buffer.isBuffer(input) ? input : Buffer.from(input)

  // 模长（字节）
  let k = 0
  for (let t = n; t > 0n; t >>= 8n) k++

  if (buf.length > k) {
    throw new Error(`rsaEncrypt 输入过长（${buf.length} > ${k} 字节）`)
  }

  const padded = Buffer.concat([Buffer.alloc(k - buf.length), buf])
  const c = modPow(BigInt('0x' + padded.toString('hex')), e, n)

  let hex = c.toString(16)
  if (hex.length % 2) hex = '0' + hex
  // 输出补足到模长
  return Buffer.from(hex.padStart(k * 2, '0'), 'hex')
}

// ─────────────────────────────────────────────────────────────
// lx.utils.crypto.md5 —— **必须返回 hex 字符串**
// ─────────────────────────────────────────────────────────────

/**
 * 洛雪桌面端 / 觅音的 `lx.utils.crypto.md5(str)`：返回 **32 字符 hex 字符串**。
 *
 * ⚠️ **不要改回 Buffer**。曾经我们返回 16 字节 Buffer（还给 `toString` 打补丁让它
 * 输出 hex），看着「两种用法都能用」，但：
 *   · `md5(x).length` 得到 16 而不是 32
 *   · `.slice()` / `.substring()` 按**字节**切，而不是按 hex 字符切
 *
 * 实测真实音源脚本 `lx-music-source-v6` 因此在签名里算出 `'0'.repeat(负数)`
 * → `RangeError: Invalid count value: -N`，表现为「同一个音源觅音能用、我们报错」。
 * 改成字符串后该脚本一路走到网络请求。
 *
 * 兼容性：脚本写 `md5(x).toString('hex')` 依然成立 ——
 * `String.prototype.toString` 忽略参数，返回字符串本身。
 */
export function md5Hex(str) {
  return CryptoJS.MD5(String(str)).toString(CryptoJS.enc.Hex)
}

/**
 * 洛雪桌面端 / 觅音的 `lx.utils.buffer.bufToString(buf, format)`。
 *
 * 字符串入参要**按 `binary` 编码包一层 Buffer 再按目标编码解出**，
 * 而不是原样返回 —— 当字符串含 >0x7F 的字符（中文歌词、签名串）时两者结果不同。
 */
export function bufToString(buf, format = 'utf8') {
  const enc = format || 'utf8'
  if (typeof buf === 'string') return Buffer.from(buf, 'binary').toString(enc)
  if (Buffer.isBuffer(buf)) return buf.toString(enc)
  try {
    return Buffer.from(buf).toString(enc)
  } catch (_) {
    return String(buf)
  }
}

// ─────────────────────────────────────────────────────────────
// lx.utils.crypto.aesEncrypt —— 必须真支持全部模式
// ─────────────────────────────────────────────────────────────

/**
 * Node 的 `aes-<位数>-<模式>` 名 → CryptoJS 的模式与填充。
 *
 * 分组模式（cbc/ecb）Node 默认自动补 PKCS#7；流模式（cfb/ofb/ctr）不填充。
 */
const AES_MODES = {
  cbc: { mode: () => CryptoJS.mode.CBC, padding: () => CryptoJS.pad.Pkcs7 },
  ecb: { mode: () => CryptoJS.mode.ECB, padding: () => CryptoJS.pad.Pkcs7 },
  cfb: { mode: () => CryptoJS.mode.CFB, padding: () => CryptoJS.pad.NoPadding },
  ofb: { mode: () => CryptoJS.mode.OFB, padding: () => CryptoJS.pad.NoPadding },
  ctr: { mode: () => CryptoJS.mode.CTR, padding: () => CryptoJS.pad.NoPadding },
}

/**
 * 洛雪 / 觅音的 `lx.utils.crypto.aesEncrypt(buffer, mode, key, iv)`：
 * 等价于 Node 的 `createCipheriv(mode, key, iv)` + `cipher.update/final` 拼接。
 *
 * ⚠️ **这里曾经只区分「是不是 ECB」，其余一律当 CBC 处理** —— 于是脚本用
 * cfb / ofb / ctr 时会拿到**完全错误的密文却不报错**（和 md5 那个 bug 同一类：
 * 静默数据损坏，比抛错难查得多）。现在按真实模式分派。
 *
 * 未知模式**直接抛错**，不再猜一个模式凑合 —— 宁可报错也不要静默算错。
 *
 * @returns {Buffer} 原始密文（不是 hex 字符串；洛雪这里返回的就是 Buffer）
 */
export function aesEncrypt(buffer, mode, key, iv) {
  const raw = String(mode == null ? '' : mode).trim().toLowerCase()
  // Node 的写法是 'aes-128-cbc' / 'aes-256-ecb'，取最后一段
  const short = raw.split('-').pop()
  const spec = AES_MODES[short]
  if (!spec) {
    throw new Error(`不支持的 AES 模式: ${mode}（支持 cbc / ecb / cfb / ofb / ctr）`)
  }

  const keyWA = CryptoJS.enc.Hex.parse(Buffer.from(key).toString('hex'))
  const cfg = { mode: spec.mode(), padding: spec.padding() }

  if (short !== 'ecb') {
    if (iv == null) throw new Error(`${mode} 需要 iv`)
    cfg.iv = CryptoJS.enc.Hex.parse(Buffer.from(iv).toString('hex'))
  }

  const dataWA = CryptoJS.lib.WordArray.create(buffer)
  const out = CryptoJS.AES.encrypt(dataWA, keyWA, cfg)
  const hex = out.ciphertext.toString(CryptoJS.enc.Hex)

  const res = Buffer.from(hex, 'hex')
  // 保留「无参或 'hex' 时输出 hex」的便利 —— 部分脚本这么用
  res.toString = function (enc) {
    if (!enc || enc === 'hex') return hex
    return Buffer.prototype.toString.call(this, enc)
  }
  return res
}

// ─────────────────────────────────────────────────────────────
// 模块系统：把 require('crypto'/'buffer'/'url') 映射到浏览器实现
// ─────────────────────────────────────────────────────────────

/** crypto 模块（与 lx.utils.crypto 同一套实现） */
function createCryptoModule(utils) {
  return {
    // 用 require 拿到的常见用法
    createHash: (algo) => {
      const a = String(algo).toLowerCase()
      let data = ''
      return {
        update(v) {
          data += Buffer.isBuffer(v) ? v.toString('utf8') : String(v)
          return this
        },
        digest(enc) {
          const hex = a === 'md5' ? CryptoJS.MD5(data).toString() : CryptoJS.SHA256(data).toString()
          return enc === 'hex' || !enc ? hex : Buffer.from(hex, 'hex')
        },
      }
    },
    randomBytes: (size) => utils.crypto.randomBytes(size),
    createCipheriv: (mode, key, iv) => {
      const chunks = []
      return {
        update(buf) {
          chunks.push(Buffer.isBuffer(buf) ? buf : Buffer.from(buf))
          return Buffer.alloc(0)
        },
        final() {
          const all = Buffer.concat(chunks)
          return utils.crypto.aesEncrypt(all, mode, key, iv)
        },
      }
    },
    publicEncrypt: (opts, buffer) => rsaEncryptNoPadding(buffer, opts?.key),
    constants: { RSA_NO_PADDING: 3 },
  }
}

/**
 * 构造 `require`。只放行白名单模块 —— 与觅音的受限 require 保持同一套名单，
 * 不放行 fs / child_process 之类（浏览器里本来也没有，但错误要说得清楚）。
 */
export function createRequireShim(utils) {
  const cache = new Map()

  return function lxRequire(id) {
    const name = String(id || '').replace(/^node:/, '')

    if (cache.has(name)) return cache.get(name)

    let mod
    switch (name) {
      case 'buffer':
        mod = { Buffer, SlowBuffer: Buffer, INSPECT_MAX_BYTES: 50 }
        break
      case 'crypto':
        mod = createCryptoModule(utils)
        break
      case 'url':
        mod = { URL, URLSearchParams }
        break
      case 'zlib':
        mod = { inflate: zlibInflate, deflate: zlibDeflate }
        break
      default:
        // 明确报错，不返回 undefined —— 否则脚本会在更远的地方崩，难排查
        throw new Error(
          `沙箱不支持 require('${id}')。浏览器环境仅提供：buffer / crypto / url / zlib`,
        )
    }
    cache.set(name, mod)
    return mod
  }
}

// ─────────────────────────────────────────────────────────────
// 给 lxEnv 挂上沙箱全局
// ─────────────────────────────────────────────────────────────

/**
 * 把 Node 风格的沙箱全局挂到 lxEnv 上。
 *
 * 注意 `global` / `globalThis`：桌面版 LX Music 把它们指向同一个全局对象，
 * 脚本会写 `global.xxx = ...`。我们**不覆盖真实的 globalThis**（那会污染整个应用），
 * 而是让脚本作用域里的这两个名字指向 lxEnv 本身 —— 脚本之间因此互不干扰，
 * 这也是更安全的做法。
 */
export function attachSandboxGlobals(lxEnv) {
  lxEnv.Buffer = Buffer
  lxEnv.module = { exports: {} }
  lxEnv.exports = lxEnv.module.exports
  lxEnv.require = createRequireShim(lxEnv.utils)
  lxEnv.global = lxEnv
  return lxEnv
}

export const __testing = { zlibInflate, zlibDeflate, modPow }
