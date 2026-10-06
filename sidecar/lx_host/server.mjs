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
 *   curl -X POST localhost:8920/search  -d '{"keyword":"晴天","page":1,"limit":5}'
 *
 * ⚠️ `musicSearch` **不在官方 LX 契约里**（官方文档的 `sources[k].actions` 只认
 * musicUrl / lyric / pic，`info` 的形状也只对这几个动作有定义）。它是音源脚本作者与
 * 第三方宿主（觅音那一族）之间的约定 —— 所以形状**只能靠真脚本实测**，照文档写会全错。
 * /search 那段顶部与 normalizeSearchItem 顶部记着实测结果，改之前先看那几条。
 */
import http from 'node:http';
import vm from 'node:vm';
import fs from 'node:fs';
import path from 'node:path';
import nodeBuffer from 'node:buffer';
import nodeCrypto from 'node:crypto';
import nodeHttps from 'node:https';
import nodeUrl from 'node:url';
import nodeZlib from 'node:zlib';

const argv = process.argv.slice(2);
const arg = (n, d) => { const i = argv.indexOf('--' + n); return i >= 0 && argv[i + 1] ? argv[i + 1] : d; };
const DIR = arg('dir', path.join(process.cwd(), 'preset_sources'));
const PORT = Number(arg('port', '8920'));

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

// ─────────────────────────────────────────────────────────────
// require 白名单 —— 从浏览器侧 lx-compat.js 的 createRequireShim() 移植
// ─────────────────────────────────────────────────────────────
// 为什么必须有：沙箱里的 `require` 原来是**真的 Node require**（`createRequire(import.meta.url)`），
// 于是任何丢进音源目录的 .js 都能 `require('node:fs')` 读写宿主的文件、
// `require('node:child_process')` 起进程 —— 宿主对脚本的边界就是从这条缝漏掉的。
//
// 白名单**照抄**浏览器侧 createRequireShim()：清单只有 buffer / crypto / url / zlib，
// 查表语义也照抄（这几条正是两边行为必须一致的部分）：
//   · 先 `String(id || '')` 再剥掉开头的 `node:` —— `require('node:crypto')` ≡ `require('crypto')`
//   · **整名精确匹配**、大小写敏感：`Crypto`、`crypto/promises`、`node:zlib/promises` 一律拒绝
//   · 按剥掉前缀后的名字缓存：同一次加载里拿回同一个模块对象
//   · 不在名单里 → **抛错**，绝不返回 undefined：返回 undefined 会让脚本在几百行外
//     以「Cannot read properties of undefined」这种看不懂的方式炸（这正是本文件的诊断信条）
//
// ⚠️ 与浏览器侧**唯一**的差异，在这里说明（不默默改）：模块的**内容**用 Node 原生内建，
// 不用浏览器侧那份手写替身。三点理由：
//   ① 这是本文件已有的移植规则 —— 上面 lx.utils 就是「成员名照抄浏览器侧、实现换成 Node 原生」；
//   ② 真桌面版洛雪给脚本的本来就是真 Node 内建，浏览器替身才是偏差，照抄替身会**静默算错**：
//      浏览器版 `require('crypto').createHash('md5').update(s).digest()` 返回 hex **字符串**，
//      而 Node/桌面版返回 **Buffer**；浏览器版 `require('zlib')` 只有 Promise 写法，
//      桌面版是回调 + `*Sync` 写法。差别不报错，只是数据悄悄不对 —— 这一层最忌讳这个。
//   ③ 白名单挡的是**模块名**，而 buffer / crypto / url / zlib 这四个本身都摸不到文件系统与进程，
//      放行它们不削弱上面那条边界。
// 若以后决定连内容也要与浏览器侧逐字节等价，把下面四个 case 换成 `{ Buffer, SlowBuffer: Buffer,
// INSPECT_MAX_BYTES: 50 }` / lx-compat.js 的 createCryptoModule() / zlib 包装即可 ——
// 但那时要同时接受 ② 里那两个偏差。
//
// ⚠️ 宿主自己（node:http/https 取直链、node:crypto/zlib 实现 utils）**不走这里**：
// 本文件顶层用 ESM `import` 拿内建，沙箱里的 `require` 只发给脚本。走这条被限制的路会把自己也挡住。
function createLxRequire() {
  const cache = new Map();

  return function lxRequire(id) {
    const name = String(id || '').replace(/^node:/, '');
    if (cache.has(name)) return cache.get(name);

    let mod;
    switch (name) {
      case 'buffer': mod = nodeBuffer; break;
      case 'crypto': mod = nodeCrypto; break;
      case 'url': mod = nodeUrl; break;
      case 'zlib': mod = nodeZlib; break;
      default:
        // 明确的错误信息：说清是**被宿主白名单拒绝**，以及到底放行了什么 ——
        // 不然脚本只会拿到 undefined，然后在很远的地方崩。
        throw new Error(
          `沙箱拒绝 require('${id}')：该模块不在宿主白名单里，只提供 buffer / crypto / url / zlib`,
        );
    }
    cache.set(name, mod);
    return mod;
  };
}

/** 按 frontend/src/engine/lx-runtime.js 的契约造一个 lx 宿主（每个脚本一份）。 */
function makeLx(meta, onRequestDone) {
  const handlers = new Map();
  // ⚠️ EVENT_NAMES 必须给：真脚本 `const { EVENT_NAMES, request, on, send } = globalThis.lx`
  // 拿不到就静默 undefined，然后在 `on(EVENT_NAMES.request, …)` 抛
  //   Cannot read properties of undefined (reading 'request')
  // —— 报错点离病因近千行，看名字还以为是 request 没了。对齐 lx-runtime.js。
  const EVENT_NAMES = { request: 'request', inited: 'inited', updateAlert: 'updateAlert' };
  // 探测到的缺失都进 meta.wants（createLxUtils / missTrap 用这个回调记账）
  const record = (p) => meta.wants.push(p);
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
      // ⚠️ 走宿主自己的内建（顶层 import），**不是**沙箱那个白名单 require ——
      // 宿主内部的取直链能力不能被发给脚本的那份限制挡住。
      // 顺带修了一个潜伏 bug：这里原来写的是裸 `require('node:http')`，而 makeLx 是模块作用域的
      // 函数、ESM 里并没有 `require` 绑定 → 真脚本一旦调 lx.request 就 100% 抛
      // `require is not defined`（probe 里有模块级 `const require`，所以只在宿主上暴露）。
      const mod = u.startsWith('https:') ? nodeHttps : http;
      mod.get(u, (res) => {
        const chunks = [];
        res.on('data', (c) => chunks.push(c));
        res.on('end', () => cb && cb(null, { statusCode: res.statusCode, headers: res.headers, body: Buffer.concat(chunks), raw: Buffer.concat(chunks).toString('utf8') }));
      }).on('error', (e) => cb && cb(e, null));
    },
    utils: createLxUtils(record),
  };
  // lx 成员的 read-miss 也要能被看见（原来 `lx` 是普通对象，缺成员直接静默 undefined，
  // 沙箱 Proxy 只拦全局读取，这类缺失既不报错也不进 wants —— 上面的 EVENT_NAMES 就是这么漏的）。
  // 现在每层都套 missTrap（见上面那段），`lx.缺的成员` 和 `lx.utils.crypto.缺的成员` 都会带完整路径记下来。
  const lx = missTrap(lxRaw, 'lx.', record);
  void onRequestDone;
  return { lx, handlers };
}

/** 加载一个脚本：在 Proxy 沙箱里求值，记下它伸手要了哪些宿主没给的东西。 */
function loadOne(file) {
  const code = fs.readFileSync(file, 'utf8');
  const meta = { file: path.basename(file), ok: false, wants: [], requests: [], error: '', inited: null, actions: [] };
  const { lx, handlers } = makeLx(meta, null);
  const base = {
    // 脚本拿到的 `require` 是**白名单函数**（createLxRequire），不是真 require ——
    // 原来这里是 `createRequire(import.meta.url)`，等于把宿主的文件系统/进程交给任何丢进来的 .js。
    lx, require: createLxRequire(), module: { exports: {} }, exports: {}, global: {},
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

// ─────────────────────────────────────────────────────────────
// POST /search 的归一化辅助
// ─────────────────────────────────────────────────────────────
/**
 * 把脚本返回的一条搜索结果归一成调用方（Go 侧）能直接用的形状。
 *
 * 逐字段的取舍 —— **全部来自实测**（2026-10-06，真脚本 `全豆要-聚合音源-V4.1.js`
 * 的 `qsvip` 源），不是照文档或猜的：
 *   · id       ← `songmid` ‖ `id` ‖ `hash`：脚本给这三个字段塞的是同一个值（实测），
 *                而 `songmid` 是脚本解析那一步 `getSongId()` **优先读**的字段名，
 *                也是曲率池里的「平台内 id」口径 —— 两处对齐，后续直接用。
 *   · name     ← `name`；singer ← `singer`（字符串，不是数组）；album ← `albumName`。
 *   · duration ← **秒**（脚本把上游毫秒 floor 成秒：实测 269000 → 269）。
 *                正好是曲率统一结构要的单位，不需要再换算。
 *   · pic      ← `pic`。
 *   · raw      ← 脚本那条记录**原样**，只删掉 `_raw`。
 *                `_raw` 是脚本自己的「上游原对象」透传，内容与体积都不可知（可能不是
 *                纯 JSON），而 `raw` 本身已经能证明「脚本自己报的字段一个都没丢」。
 *   · quality / size ← **只有脚本真的给了才带**。实测的 qsvip 两个都没给，
 *                所以现在恒为缺省。**不替它编一个档位**：池里对「缺体积」
 *                （不主动占优）与「缺音质」（算最低档，见 qualityRank）的处理完全不同，
 *                编一个就等于伪造决策依据。
 */
function normalizeSearchItem(script, source, item) {
  const it = item && typeof item === 'object' ? item : {};
  const raw = Object.assign({}, it);
  delete raw._raw; // 理由见上（脚本的上游透传，形状不可知）
  const out = {
    script,
    source,
    id: String(it.songmid || it.id || it.hash || ''),
    name: String(it.name || ''),
    singer: String(it.singer || ''),
    album: String(it.albumName || it.album || ''),
    duration: Number(it.duration) > 0 ? Math.floor(Number(it.duration)) : 0,
    pic: String(it.pic || it.cover || ''),
    raw,
  };
  // 只在**脚本真给了**的时候透传 —— 缺就是缺，不补默认值（见函数顶部）
  if (it.quality) out.quality = String(it.quality);
  if (Number(it.size) > 0) out.size = Math.floor(Number(it.size));
  return out;
}

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
  // ── POST /search：让「声明了 musicSearch 的源」把搜索结果交出来 ──────
  //
  // 实测形状（真脚本 qsvip，见 normalizeSearchItem 顶部）：入参是
  //   `{ source, action: 'musicSearch', info: { keyword, page, pagesize } }`
  // ⚠️ 搜索参数放在 `info` **顶层**，不是 `info.musicInfo` 里（与 musicUrl 正相反）——
  // 放错层时脚本读不到 keyword，**不报错**，静默返回 `{isEnd:true, list:[]}`（实测）。
  // 同理 `pagesize` 不能叫 `limit`：脚本只认 `pagesize`，给 `limit` 它会按默认 30 搜（实测）。
  //
  // 两条硬规矩（与 /resolve 同一个风格）：
  //   · 只调**在 inited 里声明了 musicSearch** 的源；没声明的明确记一笔「已跳过」，
  //     不盲调（盲调对这类脚本是 reject `action not support`，实测）。
  //   · 单个源失败**只记一笔**，绝不影响别的源 —— 一个音源挂了不该让整次搜索空手。
  // 结果为空**不是错误**（搜索本来就可能没有命中）：照常 200 + results:[]，
  // 原因留在 tried 里。真正的错误只有请求体不是 JSON（400）与缺 keyword（400）。
  if (req.url === '/search' && req.method === 'POST') {
    let payload = {};
    try { payload = JSON.parse((await readBody(req)) || '{}'); } catch { return send(400, { error: 'bad json' }); }
    const keyword = String(payload.keyword == null ? '' : payload.keyword).trim();
    if (!keyword) return send(400, { error: '缺少 keyword' });
    const page = Number(payload.page) > 0 ? Math.floor(Number(payload.page)) : 1;
    const limit = Number(payload.limit) > 0 ? Math.floor(Number(payload.limit)) : 10;
    // 留空 = 所有声明了 musicSearch 的源（调用方通常不指定）
    const wantSource = String(payload.source == null ? '' : payload.source).trim();

    const results = [];
    const tried = [];
    for (const l of loaded) {
      if (!l.handler) { tried.push({ script: l.meta.file, source: wantSource, error: l.meta.error || '脚本没注册 request 处理器' }); continue; }
      const sources = (l.meta.inited && l.meta.inited.sources) || {};
      for (const src of Object.keys(sources)) {
        if (wantSource && src !== wantSource) continue;
        const info = sources[src] || {};
        const actions = Array.isArray(info.actions) ? info.actions : [];
        if (!actions.includes('musicSearch')) {
          // 「不支持搜索」是**正常事实**，不是故障：说出来，别让调用方以为搜过而没结果
          tried.push({ script: l.meta.file, source: src, error: '该源未声明 musicSearch，已跳过' });
          continue;
        }
        try {
          const out = await Promise.race([
            Promise.resolve(l.handler({ source: src, action: 'musicSearch', info: { keyword, page, pagesize: limit } })),
            new Promise((_, rej) => setTimeout(() => rej(new Error('timeout 8s')), 8000)),
          ]);
          const list = Array.isArray(out && out.list) ? out.list : [];
          for (const it of list) results.push(normalizeSearchItem(l.meta.file, src, it));
          if (!list.length) tried.push({ script: l.meta.file, source: src, error: '上游没返回结果' });
        } catch (e) {
          tried.push({ script: l.meta.file, source: src, error: String((e && e.message) || e).slice(0, 160) });
        }
      }
    }
    return send(200, { ok: true, keyword, page, limit, results, tried });
  }
  if (req.url === '/reload' && req.method === 'POST') { loadAll(); return send(200, { reloaded: loaded.length }); }
  send(404, { error: 'not found', routes: ['GET /health', 'POST /resolve', 'POST /search', 'POST /reload'] });
}).listen(PORT, '127.0.0.1', () => console.log(`[lx_host] 监听 127.0.0.1:${PORT}，脚本目录 ${DIR}，加载 ${loaded.length} 个脚本`));
