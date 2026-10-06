#!/usr/bin/env node
/**
 * lx-probe —— 把一个洛雪(LX)音源脚本放进 Node 里跑一遍，报告它到底要什么。
 *
 * 为什么需要它：goal-a5eb2a23 的 ⑤ 选了方案 (b)（服务端 Node 子进程）。
 * 宿主必须提供的「浏览器全局」不能靠读 lx-runtime.js 推断 —— 得让脚本自己说。
 * 这里用 node:vm 的 Proxy 当沙箱：**脚本读取任何宿主没给的东西都会被记下来**。
 *
 * 用法：node scripts/lx-probe.mjs <音源脚本.js>
 * 注意：探测阶段**不出网**（lx.request 直接回错），只看脚本的结构与依赖。
 */
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import vm from 'node:vm';

const file = process.argv[2];
if (!file) { console.error('用法: node scripts/lx-probe.mjs <音源脚本.js>'); process.exit(2); }

const code = readFileSync(file, 'utf8');
const require = createRequire(import.meta.url);
const out = { file, bytes: code.length, inited: null, handlers: [], wants: [], requests: [], errors: [] };

// 按 frontend/src/engine/lx-runtime.js 的契约复刻宿主（只给脚本可能碰到的部分）
const handlers = new Map();
const lx = {
  currentScriptInfo: { name: 'probe', version: 'probe', rawScript: code },
  on(ev, fn) { handlers.set(ev, fn); out.handlers.push(ev); },
  send(ev, data) { if (ev === 'inited') out.inited = data; },
  request(url, options, callback) {
    const cb = typeof options === 'function' ? options : callback;
    out.requests.push(String(url).slice(0, 140));
    if (typeof cb === 'function') cb(new Error('probe: 不出网'), null); // 不真出网
  },
  utils: { buffer: {}, bufToString: () => '', deflate: () => '', crypto: {} },
};

const base = {
  lx, require, module: { exports: {} }, exports: {}, global: {},
  Buffer, console: { log() {}, warn() {}, error() {} },
  setInterval: () => 0, setTimeout: (f) => { try { f?.(); } catch {} return 0; },
  clearInterval: () => {}, clearTimeout: () => {}, requestAnimationFrame: () => 0,
};
// Proxy 是探测的核心：任何「脚本要了、宿主没给」的全局都会落进 out.wants
const sandbox = new Proxy(base, {
  has: () => true,
  get(t, k) {
    if (k in t) return t[k];
    // ⚠️ 必须先看**真全局**：vm 上下文的内建（JSON/Promise/encodeURIComponent…）
    // 也要走这个 trap。直接返回 undefined 会把它们全遮蔽掉 —— 集成测试抓出来的。
    const real = globalThis[k];
    if (real !== undefined) return real;
    if (typeof k === 'string' && !k.startsWith('Symbol(')) out.wants.push(k);
    return undefined;
  },
});

try {
  new vm.Script(code, { filename: file }).runInContext(vm.createContext(sandbox), { timeout: 8000 });
} catch (e) {
  out.errors.push('加载: ' + String(e.message).slice(0, 240));
}

// 有 request 处理器就真叫它一次：这才是「在 Node 里能不能干活」的实测
const onReq = handlers.get('request');
if (onReq) {
  const probe = { source: 'wy', action: 'musicUrl', info: { id: '1', songmid: '1', name: 'probe', singer: 'probe', album: 'probe' }, quality: '320k' };
  try {
    const r = await Promise.race([
      Promise.resolve(onReq(probe)),
      new Promise((res) => setTimeout(() => res('__TIMEOUT__'), 4000)),
    ]);
    out.handlerResult = r === '__TIMEOUT__' ? '超时（4s 没返回）' : { type: typeof r, value: String(r).slice(0, 200) };
  } catch (e) {
    out.handlerResult = '抛错: ' + String(e.message).slice(0, 240); // 期望的失败分支也算情报
  }
} else {
  out.handlerResult = '（脚本没注册 request 处理器）';
}

out.wants = [...new Set(out.wants)].slice(0, 40);
console.log(JSON.stringify(out, null, 1));
