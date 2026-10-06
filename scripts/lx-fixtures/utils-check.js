/**
 * lx.utils 验证脚本 —— **不是真音源**，只为验证宿主补的 lx.utils 到底算得对不对。
 *
 * 为什么要有它：真音源脚本（preset_sources / 用户导入的那些）一个都没碰 lx.utils，
 * 「几个脚本跑通」根本证明不了 utils 的实现是对的（连「有没有被调用」都证明不了）。
 * 所以这里造一个把每个成员都真调一遍的脚本，结果通过 lx.send(inited, ...) 送出 ——
 * 探针把 inited 原样放进输出（不像 handlerResult 那样截断到 200 字符）。
 *
 * ⚠️ 它**不是**音源脚本，别丢进 preset_sources。只由 scripts/lx-utils-check.mjs 加载。
 *
 * 期望值来源（**不是被测实现自己**，见 scripts/lx-utils-check.mjs 顶部那张清单）：
 *   · md5           公开常量（hello / 123）+ frontend/src/engine/lx-compat.js 的参考实现
 *   · aes           同参考实现 + openssl CLI（逐项比对）
 *   · rsa           同参考实现 + node:crypto publicEncrypt(RSA_NO_PADDING)
 *   · bufToString   同参考实现（binary 再解码）+ 用 Buffer 原语独立推导
 *   · zlib          用 node:zlib 造输入/独立解开，验证两段式与互操作
 *
 * 注：`require('node:zlib')` / `require('node:crypto')` 走的是宿主白名单（两者都在名单里），
 * 所以这个脚本同时也证明了「白名单放行的模块真能用」。
 */
const { EVENT_NAMES, on, send, request } = globalThis.lx;
const U = globalThis.lx.utils;
const nodeZlib = require('node:zlib');

const RSA_PUB = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA0x0FxvuywdmWhn+R3qhX
fT3E2eNsIixmVr+dUpTXSdW/35d4amCQEW7GMBtslyKB2y8MtC6Gu5akXlnDIZAQ
r8AbHXAQGa+6bUE9Dj4FQ3QmQOnSOBNARYsmX2xXXv0UXW3SUcGYXyKcGF3mhKZn
zjaaFSj02OmiUyX2lo3c9iNM1zaU10BlbB1YDN8rG2t0vOM8nNe1iWg/pGDoGWIy
h6X0rz20YVbcBzW6QAD0+yKXwbm1VqlM4S1NHHJ/beEpx7b8QL1lMbAbAZmyzl0r
rQIYHKOzpPa0iKe6f6mtbbwIGg+bDAish2z6nc9lNSPfk2aqqpse2g4f1kuwFUhx
TwIDAQAB
-----END PUBLIC KEY-----`;

const hex = (b) => Buffer.from(b).toString('hex');

const AES_KEY = '0123456789abcdef';
const AES_KEY32 = '0123456789abcdef0123456789abcdef';
const AES_IV = 'fedcba9876543210';
const AES_PT = 'The quick brown fox jumps over the lazy dog';

const report = {};

// ── crypto.md5 ──
report.md5_hello = U.crypto.md5('hello');
report.md5_hello_type = typeof U.crypto.md5('hello');
report.md5_hello_len = U.crypto.md5('hello').length;
report.md5_hello_toString_hex = U.crypto.md5('hello').toString('hex'); // 老脚本的写法必须仍成立
report.md5_cn = U.crypto.md5('晴天');
report.md5_int = U.crypto.md5(123);

// ── buffer ──
report.buffer_from_hex = U.buffer.from('68656c6c6f', 'hex').toString('utf8');
report.bufToString_utf8 = U.buffer.bufToString(Buffer.from('中', 'utf8'), 'utf8');
report.bufToString_str_binary = U.buffer.bufToString('é', 'utf8'); // 「原样返回」的实现会得到 'é'，这里是替换字符
report.bufToString_legacy = U.bufToString('é', 'utf8'); // 旧写法 utils.bufToString
report.bufToString_hex = U.buffer.bufToString(Buffer.from('616263', 'hex'), 'hex');

// ── crypto.aesEncrypt ──
report.aes_128_cbc = hex(U.crypto.aesEncrypt(Buffer.from(AES_PT), 'aes-128-cbc', AES_KEY, AES_IV));
report.aes_cbc_short_name = hex(U.crypto.aesEncrypt(Buffer.from(AES_PT), 'cbc', AES_KEY, AES_IV));
report.aes_128_ecb = hex(U.crypto.aesEncrypt(Buffer.from(AES_PT), 'aes-128-ecb', AES_KEY));
report.aes_128_cfb = hex(U.crypto.aesEncrypt(Buffer.from(AES_PT), 'aes-128-cfb', AES_KEY, AES_IV));
report.aes_128_ofb = hex(U.crypto.aesEncrypt(Buffer.from(AES_PT), 'aes-128-ofb', AES_KEY, AES_IV));
report.aes_128_ctr = hex(U.crypto.aesEncrypt(Buffer.from(AES_PT), 'aes-128-ctr', AES_KEY, AES_IV));
report.aes_256_cbc_key_infer = hex(U.crypto.aesEncrypt(Buffer.from(AES_PT), 'aes-128-cbc', AES_KEY32, AES_IV));
report.aes_toString_hex = U.crypto.aesEncrypt(Buffer.from(AES_PT), 'aes-128-cbc', AES_KEY, AES_IV).toString();
try { U.crypto.aesEncrypt(Buffer.from('x'), 'aes-128-zzz', AES_KEY, AES_IV); report.aes_bad_mode = 'NO THROW'; }
catch (e) { report.aes_bad_mode = 'throw: ' + e.message; }
// 反向验证：我们算出的密文能被一个**独立实现**解开（node:crypto 的 decipheriv），解回原文才算真算对
try {
  const d = nodeZlib && require('node:crypto').createDecipheriv('aes-128-cbc', Buffer.from(AES_KEY), Buffer.from(AES_IV));
  const back = Buffer.concat([d.update(Buffer.from(report.aes_128_cbc, 'hex')), d.final()]).toString('utf8');
  report.aes_128_cbc_decrypts_back = back;
} catch (e) { report.aes_128_cbc_decrypts_back = 'throw: ' + e.message; }

// ── crypto.rsaEncrypt ──
report.rsa_len = U.crypto.rsaEncrypt(Buffer.from('hello'), RSA_PUB).length;
report.rsa_hello = hex(U.crypto.rsaEncrypt(Buffer.from('hello'), RSA_PUB));
report.rsa_empty = hex(U.crypto.rsaEncrypt(Buffer.from(''), RSA_PUB));
try { U.crypto.rsaEncrypt(Buffer.alloc(300, 1), RSA_PUB); report.rsa_overlong = 'NO THROW'; }
catch (e) { report.rsa_overlong = 'throw: ' + e.message; }
// 独立实现（node:crypto publicEncrypt + RSA_NO_PADDING）—— 需要输入与模长等长，所以左补零
try {
  const nodeCrypto = require('node:crypto');
  const nodeOut = nodeCrypto.publicEncrypt(
    { key: RSA_PUB, padding: nodeCrypto.constants.RSA_NO_PADDING },
    Buffer.concat([Buffer.alloc(251), Buffer.from('hello')]),
  );
  report.rsa_node_publicEncrypt_hello = nodeOut.toString('hex');
} catch (e) { report.rsa_node_publicEncrypt_hello = 'throw: ' + e.message; }

// ── crypto.randomBytes ──
report.rand_len = U.crypto.randomBytes(16).length;
report.rand_is_buffer = Buffer.isBuffer(U.crypto.randomBytes(4));
report.rand_two_differ = hex(U.crypto.randomBytes(8)) !== hex(U.crypto.randomBytes(8));

// ── 成员存在性（这些**真实存在**，绝不能出现在 wants 里）──
report.has = {
  bufferFrom: typeof U.buffer.from,
  bufferBufToString: typeof U.buffer.bufToString,
  md5: typeof U.crypto.md5,
  randomBytes: typeof U.crypto.randomBytes,
  aesEncrypt: typeof U.crypto.aesEncrypt,
  rsaEncrypt: typeof U.crypto.rsaEncrypt,
  zlibInflate: typeof U.zlib.inflate,
  zlibDeflate: typeof U.zlib.deflate,
  legacyBufToString: typeof U.bufToString,
  topDeflate: typeof U.deflate,
};

// ⚠️ 同步部分先送一次：探针不等事件循环（zlib 的线程池回调要等 I/O 阶段），
// 所以「加载期就 send」只能拿到不需要事件循环的那些值。
send(EVENT_NAMES.inited, report);

// 探针会 await request 处理器的返回值，所以 zlib 这种要等事件循环的检查放在这里做，
// 做完再把**完整**报告重发一次（探针的 out.inited 会被后一次覆盖）。
on(EVENT_NAMES.request, async ({ source, action, info }) => {
  if (action !== 'musicUrl') throw new Error('unsupported');
  if (!info || !info.musicInfo) throw new Error('请求参数不完整');

  // ── zlib：契约是 Promise<Buffer>（官方文档 + 浏览器参考实现都是）──
  const dp = U.zlib.deflate(Buffer.from('zlib roundtrip 你好'));
  report.zlib_deflate_is_promise = typeof dp.then === 'function';
  const deflated = await dp;
  report.zlib_deflate_is_buffer = Buffer.isBuffer(deflated);
  report.zlib_deflate_hex = deflated.toString('hex'); // 交给浏览器参考实现去解（跨实现验证）

  const ip = U.zlib.inflate(deflated);
  report.zlib_inflate_is_promise = typeof ip.then === 'function';
  report.zlib_inflate_roundtrip = (await ip).toString('utf8');

  // 我们压出来的字节，用独立实现(node:zlib)解得开吗
  report.zlib_deflate_cross_node = nodeZlib.inflateSync(deflated).toString('utf8');

  // 裸 deflate（无 zlib 头，node:zlib 造）→ 必须走两段式的第二个分支
  const raw = nodeZlib.deflateRawSync(Buffer.from('raw deflate 裸流'));
  report.zlib_inflate_raw = (await U.zlib.inflate(raw)).toString('utf8');

  // 顶层遗留成员 utils.deflate 与 zlib.deflate 是同一份实现
  report.zlib_top_deflate_same = nodeZlib.inflateSync(await U.deflate(Buffer.from('top level'))).toString('utf8');

  void source;
  send(EVENT_NAMES.inited, report);
  return 'https://example.com/utils-check-ok-' + U.crypto.md5('hello');
});
