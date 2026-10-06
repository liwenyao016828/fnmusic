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
  // ⚠️ EVENT_NAMES 必须给：真脚本 `const { EVENT_NAMES, request, on, send } = globalThis.lx`
  // 拿不到就静默 undefined，然后在 `on(EVENT_NAMES.request, …)` 抛
  //   Cannot read properties of undefined (reading 'request')
  // —— 报错点离病因近千行，看名字还以为是 request 没了。对齐 lx-runtime.js。
  const EVENT_NAMES = { request: 'request', inited: 'inited', updateAlert: 'updateAlert' };
  const lxRaw = {
    EVENT_NAMES,
    env: 'desktop',
    version: '2.0.0',
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
  // lx 成员的 read-miss 也要能被看见（原来 `lx` 是普通对象，缺成员直接静默 undefined，
  // 沙箱 Proxy 只拦全局读取，这类缺失既不报错也不进 wants —— 上面的 EVENT_NAMES 就是这么漏的）。
  const lx = new Proxy(lxRaw, {
    get(t, k, r) {
      if (!(k in t) && typeof k === 'string' && !k.startsWith('Symbol(')) meta.wants.push('lx.' + k);
      return Reflect.get(t, k, r);
    },
  });
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
  // 这是真脚本实测出来的，不是推断 —— 缺的既不是 document 也不是 localStorage。
  // ⚠️ has:()=>true 是双刃剑（实测，不是推断）：脚本里**从未声明**的标识符
  // （如真脚本的 HUIBQ_API）在真浏览器里是 ReferenceError: HUIBQ_API is not defined，
  // 但这里 has 恒为 true，标识符解析「找得到」，于是静默变 undefined，
  // 最后拼出 "undefined/url/…" 这种请求 —— 看起来像宿主没给 API。
  // 所以 wants 里混了两类：①宿主真该补的全局 ②脚本自己漏声明的常量（看声明即可区分）。
  // 生产侧只靠 lx.request 的 scheme 校验兜底：非 http(s) 一律回错，不会真发出去。
    has: () => true,
    get(t, k) {
      if (k in t) return t[k];
      // ⚠️ 必须先看**真全局**：vm 上下文的内建（JSON/Promise/encodeURIComponent…）
      // 也要走这个 trap。直接返回 undefined 会把它们全遮蔽掉 —— 集成测试抓出来的。
      const real = globalThis[k];
      if (real !== undefined) return real;
      if (typeof k === 'string' && !k.startsWith('Symbol(')) meta.wants.push(k);
      return undefined;
    },
  });

  // 脚本读的是 globalThis.lx（**不是**函数参数 lx）。沙箱里的
  // globalThis/window/self 必须指回**沙箱自身**，否则会落到宿主的
  // globalThis 上，真脚本报的是这句：
  //   Cannot destructure property 'EVENT_NAMES' of 'globalThis.lx' as it is undefined
  // 这是真脚本实测出来的，不是推断 —— 缺的既不是 document 也不是 localStorage。
  base.globalThis = sandbox;
  base.window = sandbox;
  base.self = sandbox;
  try {
    new vm.Script(code, { filename: file }).runInContext(vm.createContext(sandbox), { timeout: 8000 });
    meta.ok = true;
  } catch (e) { meta.error = String(e.message).slice(0, 300); }
  meta.actions = [...handlers.keys()];
  meta.wants = [...new Set(meta.wants)].slice(0, 40);
  // 直接打进日志：这样**不用开任何接口**就能看出脚本缺什么。
  // 放进真脚本时，这条日志就是「宿主还要补哪些全局」的答案。
  const platform = Object.keys(((meta.inited || {}).sources) || {});
  if (!meta.ok) {
    console.log(`[lx_host] ✗ ${meta.file} 加载失败：${meta.error}`);
  } else {
    console.log(`[lx_host] ✓ ${meta.file} 平台=[${platform.join(',') || '未声明'}] 动作=[${meta.actions.join(',') || '无'}]` +
      (meta.wants.length ? ` ⚠️ 缺全局=${meta.wants.join(',')}` : ' 未发现缺失的全局'));
  }
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
