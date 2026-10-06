/**
 * lx.utils 守门脚本 —— 放进**真宿主**里跑，验证宿主给脚本的 lx.utils 是真的算对了。
 *
 * 逻辑：任何一项不符就 reject（宿主 /resolve 会返回 502 并带上错误），全对才吐出一个直链。
 * 所以「/resolve 拿到 URL」这一件事本身就证明了 utils 的五档能力都对 —— 包括
 * 「要求宿主算出的密文能被独立实现解开」，而不只是「不报错」。
 *
 * （直链是 example.com 的假地址：这里验的是 utils 的计算，不是网络。）
 *
 * ⚠️ 它**不是**音源脚本，别丢进 preset_sources。只由 scripts/lx-utils-check.mjs 起一个真宿主
 * （sidecar/lx_host/server.mjs）加载它，所以这个脚本 = 宿主侧的端到端断言。
 */
const { EVENT_NAMES, on, send } = globalThis.lx;
const U = globalThis.lx.utils;
const nodeCrypto = require('node:crypto');
const nodeZlib = require('node:zlib');

const fail = [];
const check = (name, cond) => { if (!cond) fail.push(name); };

// ① md5：hex 字符串、32 字符、等于公开常量
check('md5', U.crypto.md5('hello') === '5d41402abc4b2a76b9719d911017c592');
check('md5-type', typeof U.crypto.md5('hello') === 'string');
check('md5-len', U.crypto.md5('hello').length === 32);

// ② AES：我们算的密文必须能被**独立实现**解回原文
const K = '0123456789abcdef';
const IV = 'fedcba9876543210';
const PT = 'The quick brown fox jumps over the lazy dog';
const ct = U.crypto.aesEncrypt(Buffer.from(PT), 'aes-128-cbc', K, IV);
const dec = nodeCrypto.createDecipheriv('aes-128-cbc', Buffer.from(K), Buffer.from(IV));
check('aes', Buffer.concat([dec.update(ct), dec.final()]).toString('utf8') === PT);

// ③ RSA：与 node:crypto 的 RSA_NO_PADDING 逐字节相同
const RSA_PUB = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA0x0FxvuywdmWhn+R3qhX
fT3E2eNsIixmVr+dUpTXSdW/35d4amCQEW7GMBtslyKB2y8MtC6Gu5akXlnDIZAQ
r8AbHXAQGa+6bUE9Dj4FQ3QmQOnSOBNARYsmX2xXXv0UXW3SUcGYXyKcGF3mhKZn
zjaaFSj02OmiUyX2lo3c9iNM1zaU10BlbB1YDN8rG2t0vOM8nNe1iWg/pGDoGWIy
h6X0rz20YVbcBzW6QAD0+yKXwbm1VqlM4S1NHHJ/beEpx7b8QL1lMbAbAZmyzl0r
rQIYHKOzpPa0iKe6f6mtbbwIGg+bDAish2z6nc9lNSPfk2aqqpse2g4f1kuwFUhx
TwIDAQAB
-----END PUBLIC KEY-----`;
const mine = U.crypto.rsaEncrypt(Buffer.from('hello'), RSA_PUB);
const native = nodeCrypto.publicEncrypt(
  { key: RSA_PUB, padding: nodeCrypto.constants.RSA_NO_PADDING },
  Buffer.concat([Buffer.alloc(251), Buffer.from('hello')]),
);
check('rsa', mine.length === 256 && mine.equals(native));

// ④ buffer / bufToString
check('bufToString', U.buffer.bufToString(Buffer.from('中', 'utf8'), 'utf8') === '中');
check('buffer.from', U.buffer.from('68656c6c6f', 'hex').toString('utf8') === 'hello');

// ⑤ 成员存在性（宿主漏给会立刻在这里显形）
for (const [k, v] of Object.entries({
  'utils.buffer.from': U.buffer.from,
  'utils.buffer.bufToString': U.buffer.bufToString,
  'utils.crypto.md5': U.crypto.md5,
  'utils.crypto.randomBytes': U.crypto.randomBytes,
  'utils.crypto.aesEncrypt': U.crypto.aesEncrypt,
  'utils.crypto.rsaEncrypt': U.crypto.rsaEncrypt,
  'utils.zlib.inflate': U.zlib.inflate,
  'utils.zlib.deflate': U.zlib.deflate,
})) check('missing:' + k, typeof v === 'function');
check('randomBytes', U.crypto.randomBytes(16).length === 16);

send(EVENT_NAMES.inited, {
  openDevTools: false,
  sources: { wy: { name: 'utils-guard', type: 'music', actions: ['musicUrl'], qualitys: ['320k'] } },
});

on(EVENT_NAMES.request, async ({ source, action, info }) => {
  if (action !== 'musicUrl') throw new Error('unsupported');
  if (!info || !info.musicInfo) throw new Error('请求参数不完整');

  // ⑥ zlib：Promise 契约 + 往返 + 裸 deflate + 跨实现
  const dp = U.zlib.deflate(Buffer.from('宿主 zlib 往返 你好'));
  if (typeof dp.then !== 'function') fail.push('zlib-not-promise');
  const deflated = await dp;
  check('zlib-roundtrip', (await U.zlib.inflate(deflated)).toString('utf8') === '宿主 zlib 往返 你好');
  check('zlib-cross-node', nodeZlib.inflateSync(deflated).toString('utf8') === '宿主 zlib 往返 你好');
  check('zlib-raw', (await U.zlib.inflate(nodeZlib.deflateRawSync(Buffer.from('裸流')))).toString('utf8') === '裸流');

  if (fail.length) throw new Error('utils 校验失败: ' + fail.join(','));
  void source;
  return 'https://example.com/utils-guard-ok-' + U.crypto.md5('hello').slice(0, 8) + '-' + ct.toString('hex').slice(0, 8);
});
