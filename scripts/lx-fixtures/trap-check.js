/**
 * 陷阱验证脚本 —— 专门验证「嵌套成员缺失也能被看见」。
 *
 * ⚠️ 它**不是**音源脚本，别丢进 preset_sources。只由 scripts/lx-utils-check.mjs 加载。
 *
 * 断言（由 scripts/lx-utils-check.mjs 检查探针输出）：
 *   ① 这些**真实存在**的成员读出来是 function，且**绝不能**出现在 wants 里：
 *        lx.utils.crypto.md5 / lx.utils.buffer.bufToString / lx.utils.zlib.inflate / lx.EVENT_NAMES …
 *   ② 这些**存在但缺成员**的读取必须出现在 wants 里（带完整路径）：
 *        lx.utils.md5Hex（两级）
 *        lx.utils.crypto.aesDecrypt、lx.utils.zlib.gunzip（三级）
 *        lx.不存在的成员（一级，老的 trap 也要保持工作）
 *   ③ 读缺失成员不能抛错，必须是 undefined（与真浏览器/LX 桌面端一致的行为——
 *      它们也只是 undefined，脚本照样会崩；我们能做的只是**把它记下来**）。
 */
const lx = globalThis.lx;
const U = lx.utils;

const result = { readMissingThrows: 'no', levels: {} };

// ① 真实成员：读了绝不能进 wants
result.levels.real = {
  md5: typeof U.crypto.md5,
  aesEncrypt: typeof U.crypto.aesEncrypt,
  rsaEncrypt: typeof U.crypto.rsaEncrypt,
  randomBytes: typeof U.crypto.randomBytes,
  bufferFrom: typeof U.buffer.from,
  bufferBufToString: typeof U.buffer.bufToString,
  zlibInflate: typeof U.zlib.inflate,
  zlibDeflate: typeof U.zlib.deflate,
  legacyBufToString: typeof U.bufToString,
  eventNames: typeof lx.EVENT_NAMES,
  request: typeof lx.request,
  on: typeof lx.on,
  send: typeof lx.send,
  env: typeof lx.env,
  currentScriptInfo: typeof lx.currentScriptInfo,
  version: typeof lx.version,
};

// ② 缺失成员：必须被记进 wants（读出来是 undefined）
try {
  result.levels.missingL1 = typeof lx.someMissingMember;                     // 一级
  result.levels.missingL2 = typeof U.md5Hex;                                 // 二级（本轮新堵的洞）
  result.levels.missingL2b = typeof U.inflate;                               // 二级：常见错位写法
  result.levels.missingL3crypto = typeof U.crypto.aesDecrypt;                // 三级
  result.levels.missingL3zlib = typeof U.zlib.gunzip;                        // 三级
  result.levels.missingL3buffer = typeof U.buffer.allocUnsafe;               // 三级
} catch (e) {
  result.readMissingThrows = 'yes: ' + e.message;
}

// ③ 解构 / Object.keys 这类正常操作不能被 trap 弄坏
const { buffer, crypto } = U;
const { md5, aesEncrypt } = crypto;
result.destructure = { md5: typeof md5, aesEncrypt: typeof aesEncrypt, from: typeof buffer.from };
result.keys_utils = Object.keys(U).sort();
result.keys_crypto = Object.keys(U.crypto).sort();

lx.send(lx.EVENT_NAMES.inited, result);
lx.on(lx.EVENT_NAMES.request, () => Promise.resolve('https://example.com/trap-check'));
