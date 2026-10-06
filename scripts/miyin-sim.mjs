/**
 * 对照实验：把同一个音源脚本放进**忠实复刻觅音（miyin）的 Node vm 沙箱**里跑，
 * 看它在「标准实现」下是不是也失败。
 *
 * 目的：给「是脚本/上游的问题」还是「我们运行时的问题」一个定论。
 *
 * 关键点（比第一版 sim 补齐的）：
 *   - currentScriptInfo 必须带 rawScript（脚本会拿它做完整性校验）
 *   - lx.version 固定 '2.0.0'（自定义源 API 版本）
 *   - md5 返回 hex 字符串；rsaEncrypt 走 Node 原生 RSA_NO_PADDING；zlib 用 node:zlib
 *   - globalThis / global 指向 sandbox 自身（vm 语义）
 *
 * 跑法：node /tmp/lxtest/miyin-sim2.mjs /tmp/lxtest/src.js wy,tx,kg,kw
 */
import { Script, createContext } from 'node:vm'
import { createHash, createCipheriv, publicEncrypt, constants, randomBytes } from 'node:crypto'
import { inflate, deflate } from 'node:zlib'
import { promisify } from 'node:util'
import { readFileSync } from 'node:fs'
import { request as httpRequest } from 'node:http'
import { request as httpsRequest } from 'node:https'
import { createRequire } from 'node:module'

const parentRequire = createRequire(import.meta.url)
const inflateAsync = promisify(inflate)
const deflateAsync = promisify(deflate)

const SCRIPT_PATH = process.argv[2] || '/tmp/lxtest/src.js'
const PLATFORMS = (process.argv[3] || 'wy,tx,kg,kw').split(',')

const EVENT_NAMES = { request: 'request', inited: 'inited', updateAlert: 'updateAlert' }
const handlers = []
const logs = []
const requests = []

function nodeHttpRequest(url, options, cb) {
  let settled = false
  const done = (err, resp) => {
    if (settled) return
    settled = true
    cb(err, resp)
  }
  try {
    const u = new URL(url)
    const lib = u.protocol === 'http:' ? httpRequest : httpsRequest
    const method = (options.method || 'GET').toUpperCase()
    const headers = { ...(options.headers || {}) }
    let payload
    if (options.body != null) {
      payload = typeof options.body === 'string' ? options.body : JSON.stringify(options.body)
      if (!headers['Content-Type'] && !headers['content-type']) headers['Content-Type'] = 'application/json'
      headers['Content-Length'] = String(Buffer.byteLength(payload))
    }
    const rec = { url: String(url).slice(0, 300), method, hasBody: !!payload }
    requests.push(rec)
    const req = lib(
      {
        protocol: u.protocol,
        hostname: u.hostname,
        port: u.port || (u.protocol === 'http:' ? 80 : 443),
        path: `${u.pathname}${u.search}`,
        method,
        headers,
        timeout: 15000,
      },
      (res) => {
        const chunks = []
        res.on('data', (c) => chunks.push(c))
        res.on('end', () => {
          const raw = Buffer.concat(chunks).toString('utf8')
          let body = raw
          const ct = String(res.headers['content-type'] || '')
          if (ct.includes('json') || raw.trim().startsWith('{') || raw.trim().startsWith('[')) {
            try {
              body = JSON.parse(raw)
            } catch {
              body = raw
            }
          }
          rec.status = res.statusCode
          rec.resp = raw.slice(0, 300)
          done(null, { statusCode: res.statusCode || 0, body, headers: res.headers, raw })
        })
        res.on('error', (err) => done(err))
      },
    )
    req.on('error', (err) => {
      rec.err = String(err.message || err)
      done(err)
    })
    req.on('timeout', () => {
      req.destroy()
      done(new Error('request timeout'))
    })
    if (payload) req.write(payload)
    req.end()
  } catch (err) {
    done(err)
  }
}

function lxRequest(url, options, callback) {
  let opts = {}
  let cb = callback
  if (typeof options === 'function') {
    cb = options
    opts = {}
  } else if (options) {
    opts = options
  }
  const p = new Promise((resolve, reject) => {
    nodeHttpRequest(url, opts, (err, resp) => (err ? reject(err) : resolve(resp)))
  })
  if (cb) {
    p.then((r) => cb(null, r), (e) => cb(e))
    p.catch(() => {})
  }
  return p
}

const code = readFileSync(SCRIPT_PATH, 'utf8')

const lx = {
  EVENT_NAMES,
  env: 'desktop',
  version: '2.0.0',
  currentScriptInfo: {
    name: 'lx-music-source-v6',
    description: '',
    version: '6',
    author: '',
    homepage: '',
    rawScript: code, // ← 关键：脚本可能拿它做完整性校验
  },
  utils: {
    crypto: {
      aesEncrypt(buffer, mode, key, iv) {
        const cipher = createCipheriv(mode, key, iv)
        return Buffer.concat([cipher.update(buffer), cipher.final()])
      },
      rsaEncrypt(buffer, key) {
        const buf = Buffer.isBuffer(buffer) ? buffer : Buffer.from(buffer)
        const padded = Buffer.concat([Buffer.alloc(Math.max(0, 128 - buf.length)), buf])
        return publicEncrypt({ key, padding: constants.RSA_NO_PADDING }, padded)
      },
      randomBytes(size) {
        return randomBytes(size)
      },
      md5(str) {
        return createHash('md5').update(String(str)).digest('hex')
      },
    },
    buffer: {
      from: (...args) => Buffer.from(...args),
      bufToString: (buf, format) =>
        typeof buf === 'string'
          ? Buffer.from(buf, 'binary').toString(format || 'utf8')
          : Buffer.from(buf).toString(format || 'utf8'),
    },
    zlib: { inflate: inflateAsync, deflate: deflateAsync },
  },
  request: lxRequest,
  on(name, fn) {
    if (name === EVENT_NAMES.request) handlers.push(fn)
    return Promise.resolve()
  },
  send(name, payload) {
    if (name === EVENT_NAMES.inited) {
      const sources = payload?.sources || payload?.init?.sources || {}
      const srcObj = sources.sources || sources
      logs.push('inited: ' + Object.keys(srcObj || {}).join(','))
    }
    if (name === EVENT_NAMES.updateAlert) logs.push('updateAlert: ' + JSON.stringify(payload).slice(0, 200))
    return Promise.resolve()
  },
}

const sandbox = {
  console: {
    log: (...a) => logs.push('log: ' + a.map(String).join(' ').slice(0, 300)),
    warn: (...a) => logs.push('warn: ' + a.map(String).join(' ').slice(0, 300)),
    error: (...a) => logs.push('error: ' + a.map(String).join(' ').slice(0, 300)),
    info: (...a) => logs.push('info: ' + a.map(String).join(' ').slice(0, 300)),
    group: () => {},
    groupEnd: () => {},
  },
  setTimeout,
  clearTimeout,
  setInterval,
  clearInterval,
  Buffer,
  URL,
  module: { exports: {} },
  exports: {},
  require: (id) => {
    const n = String(id).replace(/^node:/, '')
    if (n === 'crypto' || n === 'buffer' || n === 'url') return parentRequire(id)
    throw new Error(`沙箱禁止 require('${id}')`)
  },
}
sandbox.globalThis = sandbox
sandbox.global = sandbox
sandbox.lx = lx

let loadErr = null
try {
  const script = new Script(code, { filename: SCRIPT_PATH })
  const context = createContext(sandbox, { name: 'miyin-source:test' })
  script.runInContext(context, { timeout: 5000 })
} catch (e) {
  loadErr = e
}

console.log('=== 加载（觅音沙箱）===')
console.log('loadErr:', loadErr ? loadErr.message || String(loadErr) : 'null')
console.log('handlers:', handlers.length)

// 等异步注册
const deadline = Date.now() + 5000
while (handlers.length === 0 && Date.now() < deadline) await new Promise((r) => setTimeout(r, 100))
console.log('handlers after wait:', handlers.length)
console.log('logs:', JSON.stringify(logs.slice(0, 8), null, 1))

const SONGS = {
  wy: { id: '2708694508', songmid: '2708694508', name: '晴天', singer: 'jaycd' },
  tx: { id: '0039MnYb0qxYhV', songmid: '0039MnYb0qxYhV', name: '晴天', singer: '周杰伦' },
  kg: { id: 'B3A52A7A958BF0AED0EBFBA2E9A818B7', songmid: 'B3A52A7A958BF0AED0EBFBA2E9A818B7', name: '晴天', singer: '周杰伦', hash: 'B3A52A7A958BF0AED0EBFBA2E9A818B7' },
  kw: { id: '649013869', songmid: '649013869', name: '晴天', singer: '皮卡丨邱' },
}

if (handlers.length) {
  console.log('\n=== 逐平台取链（同一个脚本，标准实现）===')
  for (const plat of PLATFORMS) {
    const song = SONGS[plat]
    if (!song) continue
    requests.length = 0
    const payload = {
      source: plat,
      action: 'musicUrl',
      info: { type: 'flac24bit', musicInfo: { source: plat, album: '', ...song } },
    }
    try {
      const r = await Promise.race([
        Promise.resolve().then(() => handlers[0](payload)),
        new Promise((_, rej) => setTimeout(() => rej(new Error('timeout 20s')), 20000)),
      ])
      console.log(`\n[${plat}] OK ->`, String(r).slice(0, 150))
    } catch (e) {
      console.log(`\n[${plat}] ERR ->`, e?.message || String(e))
    }
    for (const q of requests.slice(0, 3)) {
      console.log(`   ${q.method} ${q.url.slice(0, 200)}`)
      console.log(`   -> ${q.status} ${String(q.resp).slice(0, 160)}`)
    }
  }
}
