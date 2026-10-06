#!/usr/bin/env node
/**
 * lx_host —— 服务端洛雪(LX)音源宿主（goal-a5eb2a23 ⑤ 方案 (b)）。
 *
 * 它做什么：把用户自己的 .js 音源脚本加载进来，在 **Node 里**（不需要浏览器、不需要曲率窗口开着）
 * 跑 `lx.on('request')` 处理器，把解析出的直链通过 HTTP 交给 Go 后端。
 *
 * ⚠️ 这条路径**故意**违反原来「后端不执行第三方 JS」的约束 —— 用户 2026-10-06 明确选了方案 (b)。
 *
 * 设计上最重要的一点：**不猜脚本缺什么，让它自己说。**
 * 每个脚本在 Proxy 沙箱里跑，它读取任何宿主没提供的全局都会被记进 `wants` 并暴露在 /health 里。
 * 这样放进一个真脚本时，我们看到的是「它要 document / localStorage / …」的**事实**，
 * 而不是「我猜它要什么」。
 *
 * 用法：
 *   node server.mjs --dir <脚本目录> --port 8920
 *   curl localhost:8920/health
 *   curl -X POST localhost:8920/resolve -d '{"source":"wy","info":{"name":"x"},"quality":"320k"}'
 */
import http from 'node:http';
import vm from 'node:vm';
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';

const argv = process.argv.slice(2);
const arg = (n, d) => { const i = argv.indexOf('--' + n); return i >= 0 && argv[i + 1] ? argv[i + 1] : d; };
const DIR = arg('dir', path.join(process.cwd(), 'preset_sources'));
const PORT = Number(arg('port', '8920'));

/** 按 frontend/src/engine/lx-runtime.js 的契约造一个 lx 宿主（每个脚本一份）。 */
function makeLx(meta, onRequestDone) {
  const handlers = new Map();
  const lx = {
    currentScriptInfo: meta,
    on(ev, fn) { handlers.set(ev, fn); },
    send(ev, data) { if (ev === 'inited') meta.inited = data; },
    request(url, options, callback) {
      const cb = typeof options === 'function' ? options : callback;
      const u = String(url);
      if (!/^https?:\/\//i.test(u)) { if (cb) cb(new Error('只允许 http/https'), null); return; }
      meta.requests.push(u.slice(0, 140));
      const mod = u.startsWith('https:') ? require('node:https') : require('node:http');
      mod.get(u, (res) => {
        const chunks = [];
        res.on('data', (c) => chunks.push(c));
        res.on('end', () => cb && cb(null, { statusCode: res.statusCode, headers: res.headers, body: Buffer.concat(chunks), raw: Buffer.concat(chunks).toString('utf8') }));
      }).on('error', (e) => cb && cb(e, null));
    },
    utils: { buffer: {}, bufToString: (b) => Buffer.from(b || '').toString('utf8'), deflate: (b) => b, crypto: {} },
  };
  void onRequestDone;
  return { lx, handlers };
}

/** 加载一个脚本：在 Proxy 沙箱里求值，记下它伸手要了哪些宿主没给的东西。 */
function loadOne(file) {
  const code = fs.readFileSync(file, 'utf8');
  const meta = { file: path.basename(file), ok: false, wants: [], requests: [], error: '', inited: null, actions: [] };
  const { lx, handlers } = makeLx(meta, null);
  const require = createRequire(import.meta.url);
  const base = {
    lx, require, module: { exports: {} }, exports: {}, global: {},
    Buffer, console: { log() {}, warn() {}, error() {} },
    setInterval: () => 0, clearInterval: () => {}, setTimeout: (f) => { try { f && f(); } catch {} return 0; }, clearTimeout: () => {}, requestAnimationFrame: () => 0,
  };
  const sandbox = new Proxy(base, {
    has: () => true,
    get(t, k) {
      if (k in t) return t[k];
      if (typeof k === 'string' && !k.startsWith('Symbol(')) meta.wants.push(k);
      return undefined;
    },
  });
  try {
    new vm.Script(code, { filename: file }).runInContext(vm.createContext(sandbox), { timeout: 8000 });
    meta.ok = true;
  } catch (e) { meta.error = String(e.message).slice(0, 300); }
  meta.actions = [...handlers.keys()];
  meta.wants = [...new Set(meta.wants)].slice(0, 40);
  return { meta, handler: handlers.get('request') };
}

let loaded = [];
function loadAll() {
  loaded = [];
  let files = [];
  try { files = fs.readdirSync(DIR).filter((f) => f.endsWith('.js')); } catch (e) { files = []; }
  for (const f of files) {
    try { const { meta, handler } = loadOne(path.join(DIR, f)); loaded.push({ meta, handler }); }
    catch (e) { loaded.push({ meta: { file: f, ok: false, error: String(e.message).slice(0, 200), wants: [] }, handler: null }); }
  }
}
loadAll();

const readBody = (req) => new Promise((res) => { let b = ''; req.on('data', (c) => b += c); req.on('end', () => res(b)); });

http.createServer(async (req, res) => {
  const send = (code, obj) => { const s = JSON.stringify(obj); res.writeHead(code, { 'Content-Type': 'application/json; charset=utf-8', 'Content-Length': Buffer.byteLength(s) }); res.end(s); };
  if (req.url === '/health') {
    return send(200, { ok: true, dir: DIR, scripts: loaded.map((l) => l.meta) });
  }
  if (req.url === '/resolve' && req.method === 'POST') {
    let payload = {};
    try { payload = JSON.parse((await readBody(req)) || '{}'); } catch { return send(400, { error: 'bad json' }); }
    const tried = [];
    for (const l of loaded) {
      if (!l.handler) continue;
      try {
        const url = await Promise.race([
          Promise.resolve(l.handler({ source: payload.source, action: 'musicUrl', info: payload.info || {}, quality: payload.quality || '320k' })),
          new Promise((_, rej) => setTimeout(() => rej(new Error('timeout 8s')), 8000)),
        ]);
        if (typeof url === 'string' && /^https?:\/\//i.test(url)) return send(200, { url, script: l.meta.file, tried });
        tried.push({ script: l.meta.file, got: String(url).slice(0, 120) });
      } catch (e) { tried.push({ script: l.meta.file, error: String(e.message).slice(0, 160) }); }
    }
    return send(502, { error: '没有脚本能解析出直链', tried });
  }
  if (req.url === '/reload' && req.method === 'POST') { loadAll(); return send(200, { reloaded: loaded.length }); }
  send(404, { error: 'not found', routes: ['GET /health', 'POST /resolve', 'POST /reload'] });
}).listen(PORT, '127.0.0.1', () => console.log(`[lx_host] 监听 127.0.0.1:${PORT}，脚本目录 ${DIR}，加载 ${loaded.length} 个脚本`));
