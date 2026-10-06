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
import nodeCrypto from 'node:crypto';
import * as nodeZlib from 'node:zlib';

const file = process.argv[2];
if (!file) { console.error('用法: node scripts/lx-probe.mjs <音源脚本.js>'); process.exit(2); }

const code = readFileSync(file, 'utf8');
const require = createRequire(import.meta.url);
const out = { file, bytes: code.length, inited: null, handlers: [], wants: [], requests: [], errors: [] };

// ─────────────────────────────────────────────────────────────
// 陷阱：读 miss 就记账（**含嵌套层**）
// ─────────────────────────────────────────────────────────────
// 为什么每层都要包：沙箱那个 `has: () => true` 的 Proxy 只拦**全局**读取。
// `lx` 是普通对象 → `lx.缺的成员` 静默 undefined；上一轮在 `lx` 上套了一层 Proxy 记 `lx.<成员>`，
// 但 `lx.utils` 自己**也是个普通对象**（而且它**存在**，所以 `lx` 那层不报），
// 于是 `lx.utils.md5Hex` 这种两级缺失照样摸黑 —— 脚本拿到 undefined 后在很远的地方崩，
// 报错点离病因几百行（`lx.EVENT_NAMES` 就是这么漏掉的）。
// 现在 `lx` / `lx.utils` / `lx.utils.{buffer,crypto,zlib}` **每层各包一次**，
// miss 会记成带完整路径的名字（如 `lx.utils.crypto.aesDecrypt`、`lx.utils.md5Hex`）。
//
// ⚠️ 只记 `!(k in target)` 的读取：**真实存在的成员（含显式 undefined）不进报告** ——
// 否则报告会被噪声淹没，等于没报。这条是本段的验收标准：
// 探测同一个脚本时，`lx.utils.crypto.md5` 绝不能出现在 wants 里。

/** 只有字符串键（且不是 vm/inspect 伪造出来的 Symbol 描述串）才值得记账 */
const isRecordableKey = (k) => typeof k === 'string' && !k.startsWith('Symbol(');

/** 把**一层**普通对象包成「读到不存在的成员就记一笔」的 Proxy：record('<前缀><成员名>') */
function missTrap(target, prefix, record) {
  return new Proxy(target, {
    get(t, k, r) {
      if (k in t) return Reflect.get(t, k, r);
      if (isRecordableKey(k)) record(prefix + k);
      return undefined;
    },
  });
}

// ─────────────────────────────────────────────────────────────
// lx.utils —— 从浏览器侧 frontend/src/engine/lx-compat.js 移植
// ─────────────────────────────────────────────────────────────
// 权威参考是浏览器那份**已上线验证过**的实现（lx-runtime.js 的 lx.utils → lx-compat.js）。
// 成员表也与官方文档（https://lxmusic.toside.cn/desktop/custom-source）逐条对得上：
//   buffer.from / buffer.bufToString / crypto.{md5,randomBytes,aesEncrypt,rsaEncrypt}
//   / zlib.{inflate,deflate}（外加旧写法 utils.bufToString）
// 移植只改「浏览器专有 API → Node 原生 API」这一层，语义、边界、报错行为一律照抄：
//   · md5         CryptoJS.MD5(..).toString(Hex)  → node:crypto createHash('md5')
//   · aesEncrypt  CryptoJS.AES + 模式/填充对照表  → node:crypto createCipheriv（位数从密钥长度推）
//   · rsaEncrypt  BigInt 裸模幂                   → **逐字移植同一份代码**（见下，别改成 publicEncrypt）
//   · zlib        原生 CompressionStream（异步）  → node:zlib（**仍返回 Promise**，理由见 zlibInflate）
//   · randomBytes crypto.getRandomValues          → node:crypto randomBytes（同为 CSPRNG）
// 返回类型也照样搬：`md5` 是 hex 字符串而不是 Buffer（历史事故，见下）。
//
// ⚠️ 本段（含 createLxUtils）在 scripts/lx-probe.mjs 与 sidecar/lx_host/server.mjs 里
// **逐字相同** —— 改一处必须同步改另一处（两个文件的宿主逻辑必须一致，否则 probe 的结论对不上生产宿主）。

/** 极简 DER 读取器：够解析 RSA 公钥即可。逐字移植自 lx-compat.js。 */
function readTLV(buf, pos) {
  const tag = buf[pos];
  let len = buf[pos + 1];
  let lenBytes = 1;
  if (len & 0x80) {
    const n = len & 0x7f;
    len = 0;
    for (let i = 0; i < n; i++) len = (len << 8) | buf[pos + 2 + i];
    lenBytes = 1 + n;
  }
  const start = pos + 1 + lenBytes;
  return { tag, start, end: start + len, next: start + len };
}

/**
 * 从 PEM / 裸 base64 的 RSA 公钥里取 n 与 e。逐字移植自 lx-compat.js。
 * 结构（SubjectPublicKeyInfo）：SEQUENCE { SEQUENCE { OID, NULL }, BIT STRING { SEQUENCE { n, e } } }
 */
function parseRsaPublicKey(pem) {
  const b64 = String(pem)
    .replace(/-----BEGIN[^-]+-----/g, '')
    .replace(/-----END[^-]+-----/g, '')
    .replace(/\s+/g, '');
  const der = Buffer.from(b64, 'base64');

  const p = readTLV(der, 0); // 最外层 SEQUENCE
  const q = readTLV(der, p.start); // AlgorithmIdentifier
  const r = readTLV(der, q.next); // BIT STRING
  // BIT STRING 头一个字节是「未用位数」，跳过
  const inner = r.start + 1;
  const s = readTLV(der, inner); // RSAPublicKey SEQUENCE
  const m = readTLV(der, s.start); // INTEGER n
  const e = readTLV(der, m.next); // INTEGER e

  const toBig = (t) => {
    const bytes = der.subarray(t.start, t.end);
    // 去掉前导 0（DER 里正数会补一个 0x00）
    let i = 0;
    while (i < bytes.length - 1 && bytes[i] === 0) i++;
    return BigInt('0x' + Buffer.from(bytes.subarray(i)).toString('hex'));
  };
  return { n: toBig(m), e: toBig(e) };
}

function modPow(base, exp, mod) {
  let result = 1n;
  base %= mod;
  while (exp > 0n) {
    if (exp & 1n) result = (result * base) % mod;
    base = (base * base) % mod;
    exp >>= 1n;
  }
  return result;
}

/**
 * 裸 RSA 加密（无填充），对齐 Node 的 `RSA_NO_PADDING` —— 洛雪桌面端/觅音就是这个语义。
 *
 * 语义要点（照抄 lx-compat.js）：
 *   · 输入按模长右对齐，**左侧补零**（不是 PKCS#1 填充）
 *   · 输出长度恒等于模长
 *   · 输入比模长还长 → **抛错**（不静默截断）
 *
 * ⚠️ 现在是 Node 环境，本可以改用 `crypto.publicEncrypt({ padding: RSA_NO_PADDING })`，
 * 但**不换**：这份 BigInt 实现是浏览器侧验证过、且专门为对齐 RSA_NO_PADDING 写的，
 * 换实现等于把「补零/长度/报错」这些边界重新赌一遍，收益只有一点速度。
 */
function rsaEncryptNoPadding(input, pemKey) {
  const { n, e } = parseRsaPublicKey(pemKey);
  const buf = Buffer.isBuffer(input) ? input : Buffer.from(input);

  // 模长（字节）
  let k = 0;
  for (let t = n; t > 0n; t >>= 8n) k++;

  if (buf.length > k) {
    throw new Error(`rsaEncrypt 输入过长（${buf.length} > ${k} 字节）`);
  }

  const padded = Buffer.concat([Buffer.alloc(k - buf.length), buf]);
  const c = modPow(BigInt('0x' + padded.toString('hex')), e, n);

  let hex = c.toString(16);
  if (hex.length % 2) hex = '0' + hex;
  // 输出补足到模长
  return Buffer.from(hex.padStart(k * 2, '0'), 'hex');
}

/**
 * `lx.utils.crypto.md5(str)`：返回 **32 字符 hex 字符串**（洛雪桌面端/觅音的行为），**不是 Buffer**。
 *
 * ⚠️ **不要改回 Buffer**。曾经返回 16 字节 Buffer（还给 toString 打了补丁看着「两种用法都能用」），
 * 但 `md5(x).length` 会变成 16 而不是 32，真实音源脚本因此在签名里算出 `'0'.repeat(负数)` →
 * `RangeError: Invalid count value: -N`，表现为「同一个音源觅音能用、我们报错」。
 * 完整事故说明见 lx-compat.js 的 md5Hex()。
 *
 * 移植差异：CryptoJS.MD5(String(str)) 先按 UTF-8 编码再摘要，等价于 `update(str, 'utf8')`。
 * 唯一不同是字符串含**未配对代理项**时 CryptoJS 会抛 URIError（它内部走 encodeURIComponent），
 * 而 Node 的 utf8 编码会替换成 U+FFFD —— 音源脚本不会出现这种字符串，不为它加分支。
 */
function md5Hex(str) {
  return nodeCrypto.createHash('md5').update(String(str), 'utf8').digest('hex');
}

/**
 * `lx.utils.buffer.bufToString(buf, format='utf8')`（旧写法 `lx.utils.bufToString`）。
 *
 * 字符串入参要**按 `binary` 编码包一层 Buffer 再按目标编码解出**，而不是原样返回 ——
 * 当字符串含 >0x7F 的字符（中文歌词、签名串）时两者结果不同。
 * 逐字移植：Node 的 Buffer 与浏览器侧用的 npm buffer 在这里行为一致，无需改动。
 */
function bufToString(buf, format = 'utf8') {
  const enc = format || 'utf8';
  if (typeof buf === 'string') return Buffer.from(buf, 'binary').toString(enc);
  if (Buffer.isBuffer(buf)) return buf.toString(enc);
  try {
    return Buffer.from(buf).toString(enc);
  } catch (_) {
    return String(buf);
  }
}

/**
 * `lx.utils.crypto.aesEncrypt(buffer, mode, key, iv)`：
 * 等价于 Node 的 `createCipheriv('aes-<位数>-<模式>', key, iv)` + update/final 拼接。
 *
 * ⚠️ 位数从**密钥长度**推，不从 mode 字符串里的数字读 —— CryptoJS（浏览器版）就是这么定的
 * （`CryptoJS.AES.encrypt` 只认 key 的实际长度），所以 `('aes-128-cbc', 32字节key)` 两边都按 256 位算。
 * 照抄这个行为，不「修正」它，否则同一个脚本在两边算出不同密文。
 *
 * 填充：分组模式（cbc/ecb）Node 默认 PKCS#7，与 CryptoJS 的 Pkcs7 一致；
 * 流模式（cfb/ofb/ctr）不填充，与 CryptoJS 的 NoPadding 一致 —— 两边都不用显式设置。
 *
 * 未知模式**直接抛错**（浏览器版同样），不猜一个模式凑合：宁可报错也不要静默算错。
 *
 * @returns {Buffer} 原始密文（不是 hex 字符串；洛雪这里返回的就是 Buffer）。
 *   toString 额外调成「无参或 'hex' 时给 hex」—— 与浏览器版一致（部分脚本这么用）。
 */
function aesEncrypt(buffer, mode, key, iv) {
  const raw = String(mode == null ? '' : mode).trim().toLowerCase();
  // Node 的写法是 'aes-128-cbc' / 'aes-256-ecb'，取最后一段
  const short = raw.split('-').pop();
  if (!['cbc', 'ecb', 'cfb', 'ofb', 'ctr'].includes(short)) {
    throw new Error(`不支持的 AES 模式: ${mode}（支持 cbc / ecb / cfb / ofb / ctr）`);
  }

  const keyBuf = Buffer.isBuffer(key) ? key : Buffer.from(key);
  if (![16, 24, 32].includes(keyBuf.length)) {
    throw new Error(`aesEncrypt 密钥长度非法：${keyBuf.length} 字节（aes 只接受 16/24/32）`);
  }
  const data = Buffer.isBuffer(buffer) ? buffer : Buffer.from(buffer);
  const algo = `aes-${keyBuf.length * 8}-${short}`;

  let cipher;
  if (short === 'ecb') {
    cipher = nodeCrypto.createCipheriv(algo, keyBuf, null); // ecb 无 iv（浏览器版也忽略传入的 iv）
  } else {
    if (iv == null) throw new Error(`${mode} 需要 iv`);
    cipher = nodeCrypto.createCipheriv(algo, keyBuf, Buffer.isBuffer(iv) ? iv : Buffer.from(iv));
  }

  const res = Buffer.concat([cipher.update(data), cipher.final()]);
  const hex = res.toString('hex');
  res.toString = function (enc) {
    if (!enc || enc === 'hex') return hex;
    return Buffer.prototype.toString.call(this, enc);
  };
  return res;
}

/**
 * node:zlib 的回调 API → Promise。
 *
 * ⚠️ 不用 `node:util` 的 promisify，也**不依赖 `zlib.promises`** —— 实测本机这个 node 构建里
 * `require('node:zlib').promises` 是 undefined（`node:zlib/promises` 子路径也解析不了），
 * 少一个环境假设。
 */
function zlibAsync(fn) {
  return (buf) => new Promise((resolve, reject) => {
    fn(buf, (err, out) => (err ? reject(err) : resolve(out)));
  });
}

const zlibInflateAsync = zlibAsync(nodeZlib.inflate);
const zlibInflateRawAsync = zlibAsync(nodeZlib.inflateRaw);
const zlibDeflateAsync = zlibAsync(nodeZlib.deflate);

/**
 * `lx.utils.zlib.inflate(buffer)`：先按 **zlib 头**解（浏览器版 `CompressionStream('deflate')` 的对应物），
 * 解不开再当**裸 deflate**（`'deflate-raw'`）—— 两段式与浏览器版一模一样。
 *
 * ⚠️ **返回 Promise**，与官方文档 `inflate(buffer: Buffer) => Promise<Buffer>` 及浏览器版一致。
 * Node 明明有 inflateSync，这里**故意不用**：那会造出「桌面版给 Promise、我们这儿给 Buffer」的差异，
 * 从桌面版搬来的脚本会静默把 Promise 当 Buffer 用（`bufToString(promise)` → `"[object Promise]"`），
 * 属于这一层最忌讳的静默数据损坏。**宁可跟参考实现一样是 Promise。**
 * （Node 的 inflate 是回调式，用 node:zlib/promises 包成 Promise，不改语义。）
 */
async function zlibInflate(input) {
  const bytes = Buffer.isBuffer(input) ? input : Buffer.from(input);
  try {
    return await zlibInflateAsync(bytes);
  } catch (_) {
    // 带 zlib 头解不开 → 多半是裸 deflate
    return await zlibInflateRawAsync(bytes);
  }
}

/**
 * `lx.utils.zlib.deflate(buffer)`：对应浏览器版 `CompressionStream('deflate')`（zlib 封装）。同样返回 Promise。
 *
 * 差异（无害）：浏览器版走 CompressionStream，Node 走 zlib，**压缩产物的字节不保证逐字节相同**
 * （压缩级别/实现不同），但两者都是合法 zlib 流，互相都解得开。脚本只该用它的解压结果，不该比对压缩字节。
 */
async function zlibDeflate(input) {
  const bytes = Buffer.isBuffer(input) ? input : Buffer.from(input);
  return await zlibDeflateAsync(bytes);
}

/**
 * `lx.utils.crypto.randomBytes(size)`：返回 size 字节随机 Buffer。
 * 浏览器版用 `crypto.getRandomValues`（WebCrypto），这里用 node:crypto 的 randomBytes —— 同为 CSPRNG。
 */
function randomBytes(size) {
  return nodeCrypto.randomBytes(size);
}

/**
 * 造一份 lx.utils：**每一层都套 miss 陷阱**，所以在沙箱里 `lx.utils.<缺的>`、
 * `lx.utils.crypto.<缺的>` 都会被记成完整路径。
 */
function createLxUtils(record) {
  const buffer = {
    from(...args) { return Buffer.from(...args); },
    bufToString,
  };
  const crypto = {
    md5: md5Hex,
    randomBytes,
    aesEncrypt,
    rsaEncrypt: rsaEncryptNoPadding,
  };
  const zlib = {
    inflate: zlibInflate,
    deflate: zlibDeflate,
  };
  // ⚠️ **这一层（lx.utils 本身）也必须包** —— `lx.utils.md5Hex` 这种两级缺失就是在这里被看见的。
  // 只包 crypto/zlib/buffer 的话，`lx.utils.<缺的>` 仍然是静默 undefined（实测漏了一次）。
  return missTrap({
    buffer: missTrap(buffer, 'lx.utils.buffer.', record),
    crypto: missTrap(crypto, 'lx.utils.crypto.', record),
    zlib: missTrap(zlib, 'lx.utils.zlib.', record),
    // 兼容旧写法 lx.utils.bufToString（规范位置是 lx.utils.buffer.bufToString）—— 浏览器版也有这一层
    bufToString,
    // ⚠️ 这一层**参考实现和官方文档里都没有**，是上一版空壳里遗留的成员（原来是 `() => ''`，
    // 也就是「静默返回错数据」）。用真实现替换空壳，指向与 zlib.deflate 完全同一份实现。
    // 若确认 LX 从来没有顶层 deflate，删掉它更干净 —— 删掉后脚本读它会进 wants 报告（这正是想要的可见性）。
    deflate: zlibDeflate,
  }, 'lx.utils.', record);
}

// 按 frontend/src/engine/lx-runtime.js 的契约复刻宿主（只给脚本可能碰到的部分）
const handlers = new Map();
// ⚠️ EVENT_NAMES 必须给：真脚本前 113 行就 `const { EVENT_NAMES, request, on, send } = globalThis.lx`，
// 拿不到就是 undefined，然后 1069 行 `on(EVENT_NAMES.request, …)` 抛
//   Cannot read properties of undefined (reading 'request')
// —— 报错位置离病因 950 行，看名字还以为是 request 没了。对齐 lx-runtime.js。
const EVENT_NAMES = { request: 'request', inited: 'inited', updateAlert: 'updateAlert' };
// 探测到的缺失都进 out.wants（createLxUtils / missTrap 用这个回调记账）
const record = (p) => out.wants.push(p);
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
  utils: createLxUtils(record),
};
// lx 成员的 read-miss 也要能被看见。
// 上面那条 EVENT_NAMES 之所以是「跑到 1069 行才炸」，就是因为 `lx` 是普通对象：
// 沙箱 Proxy 只拦**全局**读取，`lx.缺的成员` 直接静默 undefined，既不报错也不进 wants。
// 现在每层都套 missTrap（见上面那段），所以 `lx.缺的成员` **和** `lx.utils.crypto.缺的成员`
// 这类嵌套缺失都会被记成带完整路径的名字。
const lx = missTrap(lxRaw, 'lx.', record);

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
