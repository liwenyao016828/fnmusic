/**
 * require 白名单验证脚本（**不是真音源**，别丢进 preset_sources）——
 * 把「放行了什么 / 拒绝了什么」原样报出来，供 scripts/lx-utils-check.mjs 断言。
 *
 * 为什么要单独验：沙箱里的 `require` 原来是真 Node require，任何丢进音源目录的 .js
 * 都能 require('node:fs') 读写 NAS、require('node:child_process') 起进程。
 * 白名单化之后必须**每一条都钉死**：放行的真能用、拒绝的真抛错且错误信息看得懂。
 *
 * 结果走 lx.send(inited)，探针不截断它（handlerResult 才会被截到 200 字符）。
 */
const { EVENT_NAMES, on, send } = globalThis.lx;
const report = { allowed: {}, denied: {}, usage: {} };

// ① 白名单内：不带前缀 / 带 node: 前缀，都必须能拿到
for (const id of ['buffer', 'crypto', 'url', 'zlib', 'node:buffer', 'node:crypto', 'node:url', 'node:zlib']) {
  try { report.allowed[id] = typeof require(id); }
  catch (e) { report.allowed[id] = '❌ THROW: ' + e.message; }
}

// ② 白名单外：必须**抛错**（不是返回 undefined），错误信息要能看懂
const denied = ['node:fs', 'fs', 'node:fs/promises', 'node:child_process', 'child_process',
  'node:os', 'node:net', 'node:vm', 'node:module', 'module', 'crypto-js', 'Crypto',
  'crypto/promises', 'node:zlib/promises', 'node:worker_threads', 'node:process', ''];
for (const id of denied) {
  try { require(id); report.denied[id] = '❌ ALLOWED（不该放行）'; }
  catch (e) { report.denied[id] = e.message; }
}
try { require(); report.denied['(no arg)'] = '❌ ALLOWED（不该放行）'; }
catch (e) { report.denied['(no arg)'] = e.message; }

// ③ 放行的模块要**真能用**（不只 typeof 是 object）
try {
  const nodeBuffer = require('buffer');
  report.usage.buffer = nodeBuffer.Buffer.from('hi').toString('hex');
} catch (e) { report.usage.buffer = '❌ ' + e.message; }
try {
  const c = require('crypto');
  report.usage.crypto_createHash = c.createHash('sha256').update('x').digest('hex');
  report.usage.crypto_md5_via_hash = c.createHash('md5').update('hello').digest('hex');
  report.usage.crypto_randomBytes = Buffer.isBuffer(c.randomBytes(4)) && c.randomBytes(4).length === 4;
} catch (e) { report.usage.crypto_createHash = '❌ ' + e.message; }
try {
  const z = require('zlib');
  report.usage.zlib_roundtrip = z.inflateSync(z.deflateSync(Buffer.from('白名单 zlib 往返'))).toString('utf8');
  report.usage.zlib_has_promises_key = 'promises' in z;
} catch (e) { report.usage.zlib_roundtrip = '❌ ' + e.message; }
try {
  const { URL } = require('url');
  report.usage.url = new URL('https://a.test/p?q=1&r=2').searchParams.get('q');
} catch (e) { report.usage.url = '❌ ' + e.message; }

// ④ 同一个 id 的两种写法必须拿到**同一个模块对象**（缓存语义）
report.usage.cache_same_object = require('crypto') === require('node:crypto');

send(EVENT_NAMES.inited, report);
on(EVENT_NAMES.request, () => 'https://example.com/whitelist-ok');
