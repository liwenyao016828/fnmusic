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
// ⚠️ EVENT_NAMES 必须给：真脚本前 113 行就 `const { EVENT_NAMES, request, on, send } = globalThis.lx`，
// 拿不到就是 undefined，然后 1069 行 `on(EVENT_NAMES.request, …)` 抛
//   Cannot read properties of undefined (reading 'request')
// —— 报错位置离病因 950 行，看名字还以为是 request 没了。对齐 lx-runtime.js。
const EVENT_NAMES = { request: 'request', inited: 'inited', updateAlert: 'updateAlert' };
const lxRaw = {
  EVENT_NAMES,
  env: 'desktop',
  version: '2.0.0',
  currentScriptInfo: { name: 'probe', description: '', version: 'probe', author: '', homepage: '', rawScript: code },
  on(ev, fn) { handlers.set(ev, fn); out.handlers.push(ev); },
  send(ev, data) { if (ev === 'inited') out.inited = data; },
  request(url, options, callback) {
    const cb = typeof options === 'function' ? options : callback;
    out.requests.push(String(url).slice(0, 140));
    if (typeof cb === 'function') cb(new Error('probe: 不出网'), null); // 不真出网
  },
  utils: { buffer: {}, bufToString: () => '', deflate: () => '', crypto: {} },
};
// lx 成员的 read-miss 也要能被看见。
// 上面那条 EVENT_NAMES 之所以是「跑到 1069 行才炸」，就是因为 `lx` 是普通对象：
// 沙箱 Proxy 只拦**全局**读取，`lx.缺的成员` 直接静默 undefined，既不报错也不进 wants。
// 套一层 Proxy 把这类成员读取记成 `lx.<name>`，探测才名副其实。
const lx = new Proxy(lxRaw, {
  get(t, k, r) {
    if (!(k in t) && typeof k === 'string' && !k.startsWith('Symbol(')) out.wants.push('lx.' + k);
    return Reflect.get(t, k, r);
  },
});

const base = {
  lx, require, module: { exports: {} }, exports: {}, global: {},
  Buffer, console: { log() {}, warn() {}, error() {} },
  setInterval: () => 0, setTimeout: (f) => { try { f?.(); } catch {} return 0; },
  clearInterval: () => {}, clearTimeout: () => {}, requestAnimationFrame: () => 0,
};
// Proxy 是探测的核心：任何「脚本要了、宿主没给」的全局都会落进 out.wants
const sandbox = new Proxy(base, {
// 这是真脚本实测出来的，不是推断 —— 缺的既不是 document 也不是 localStorage。
// ⚠️ has:()=>true 是双刃剑，读 wants 时必须知道这一点（实测，不是推断）：
//   脚本里**从未声明**的标识符（如本脚本的 HUIBQ_API）在真浏览器里是
//     ReferenceError: HUIBQ_API is not defined
//   但这里 has 恒为 true，标识符解析「找得到」，于是静默变成 undefined
//   （`HUIBQ_API + "/url"` → "undefined/url"，看起来像宿主没给 API）。
//   所以 wants 里混了两类东西：①宿主真该补的浏览器全局 ②脚本自己漏声明的常量。
//   区分办法是静态查声明（grep "const <名字>"），不要靠猜。
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
} catch (e) {
  out.errors.push('加载: ' + String(e.message).slice(0, 240));
}

// 有 request 处理器就真叫它一次：这才是「在 Node 里能不能干活」的实测
const onReq = handlers.get('request');
if (onReq) {
  // payload 必须照 lx-runtime.js 的 _callHandler() 复刻：歌曲信息在 **info.musicInfo** 里，
  // 音质在 **info.type** 里。原来把 id/name/singer 直接摊在 info 顶层，
  // 所有 LX 脚本的第一个判断 `if (!info?.musicInfo) reject('请求参数不完整')` 就短路了 ——
  // probe 于是在「真调一次」这个环节上其实什么都没测到。
  const probe = {
    source: 'wy',
    action: 'musicUrl',
    info: {
      type: '320k',
      musicInfo: { source: 'wy', id: '1', songmid: '1', name: 'probe', singer: 'probe', album: 'probe' },
    },
  };
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
