#!/usr/bin/env node
/**
 * lx.utils / 沙箱 require 白名单 —— 常驻回归校验（node:test，零依赖）。
 *
 * 一条命令跑完：
 *
 *     node scripts/lx-utils-check.mjs
 *
 * （也可以交给 node 自带的测试运行器：`node --test scripts/lx-utils-check.mjs`）
 * 每条断言单独一行；有失败则退出码非 0。
 *
 * ── 为什么要常驻 ──
 *
 * `lx.utils` 是**静默数据损坏**高发区：md5 / AES / RSA 算错了不报错，只是标签、密文、
 * 解出来的明文悄悄不对，最后表现成「同一个音源觅音能用、我们搜不到歌」这种看不懂的现象。
 * 所以这一层必须有能随时重跑的护栏 —— 而不是躺在一个临时目录里（上一轮它住在 /tmp/lx-u，
 * 重启就没了；那份 86 项断言就是从这里搬过来的，只补了「不依赖 /tmp」）。
 *
 * ── 被测的是哪份实现 ──
 *
 * 实现有**两份**，共享块必须逐字节相同（并有一条断言直接比对它们的字节）：
 *   · scripts/lx-probe.mjs        —— 探测工具
 *   · sidecar/lx_host/server.mjs  —— 上线宿主
 * 所以下面分两条路径打：
 *   ① 第 1–9 组：经由**真探针**跑真夹具 —— 覆盖 probe 那份，且走的就是真音源脚本的同一条 vm 沙箱路径；
 *   ② 第 10 组：起一个**真宿主**，`POST /resolve` 拿到直链才算过 —— 覆盖 host 那份 + 端到端契约。
 * 直接 `import` 那两个文件是做不到的：它们 import 就有副作用（读 argv / 立刻起 HTTP 服务）。
 *
 * ── 期望值来源（**绝不拿被测实现给自己打分**）──
 *
 *   ① frontend/src/engine/lx-compat.js —— 浏览器侧**已上线验证过**的参考实现，在 Node 里直接跑。
 *      只用来回答「两份实现是否一致」。它 import crypto-js；没有 frontend/node_modules 时整组跳过（见下）。
 *   ② openssl CLI —— AES 五种模式的**独立实现**（C 写的，跟 JS 实现没有任何血缘）
 *   ③ md5sum CLI + 公开常量 —— md5
 *   ④ node:crypto —— publicEncrypt(RSA_NO_PADDING) 做 RSA 的正向 oracle；
 *      createDecipheriv 把我们的密文**反解回明文**（方向相反：两边都错才会同时对上）
 *   ⑤ node:zlib + 浏览器 CompressionStream —— zlib 的双向互操作
 *   ⑥ Buffer 原语 —— bufToString 的「先按 binary 包一层再解码」语义由 Buffer 自己推导，不问被测实现
 *   ⑦ 夹具自报的放行/拒绝清单 + 真宿主 /health 的 ok 字段 —— require 白名单
 *
 * ── 没有 node_modules 时怎么办（选择 + 理由）──
 *
 * 只有 ① 需要 frontend/node_modules（crypto-js）。拿不到时**整组跳过并打出理由**，不用 CLI 顶替。
 * 理由：① 回答的是一个 CLI 回答不了的问题 —— 「Node 移植版与浏览器那份**逐字节一致**吗」。
 * openssl / md5sum / node:crypto 是另一个**角度**的独立核对，它们和 ① 并列存在、不互相取代；
 * 少了 ① 就少一个角度，如实说少一个角度，比拿 CLI 假装等价要诚实。
 */

import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { readFileSync, mkdtempSync, cpSync, rmSync } from 'node:fs';
import { once } from 'node:events';
import { createServer as createNetServer } from 'node:net';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import nodeCrypto from 'node:crypto';
import nodeZlib from 'node:zlib';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(HERE, '..');
const FIXTURES = path.join(HERE, 'lx-fixtures');
const PROBE = path.join(HERE, 'lx-probe.mjs');
const HOST = path.join(ROOT, 'sidecar', 'lx_host', 'server.mjs');
const REF_IMPL = path.join(ROOT, 'frontend', 'src', 'engine', 'lx-compat.js');

// ─────────────────────────────────────────────────────────────
// 小工具
// ─────────────────────────────────────────────────────────────

/** 跑一次探针，拿它吐出的 JSON（探针把 console 换成空实现，stdout 只有那一个 JSON） */
function runProbe(fixture) {
  const file = path.join(FIXTURES, fixture);
  let stdout;
  try {
    stdout = execFileSync(process.execPath, [PROBE, file], {
      encoding: 'utf8', maxBuffer: 1 << 26, timeout: 60_000,
    });
  } catch (e) {
    assert.fail(`探针跑 ${fixture} 失败：${e.message}\nstdout=${String(e.stdout).slice(0, 2000)}\nstderr=${String(e.stderr).slice(0, 2000)}`);
  }
  try {
    return JSON.parse(stdout);
  } catch (e) {
    assert.fail(`探针 ${fixture} 的 stdout 不是 JSON：${e.message}\n前 2000 字：${stdout.slice(0, 2000)}`);
  }
}

/** 探针输出只跑一次，断言复用 */
const probeCache = new Map();
const probe = (fixture) => {
  if (!probeCache.has(fixture)) probeCache.set(fixture, runProbe(fixture));
  return probeCache.get(fixture);
};

const hasCmd = (cmd) => {
  try { execFileSync('sh', ['-c', `command -v ${cmd}`], { stdio: 'ignore' }); return true; } catch { return false; }
};
const HAS_OPENSSL = hasCmd('openssl');
const HAS_MD5SUM = hasCmd('md5sum');

const md5sumCli = (s) =>
  execFileSync('md5sum', [], { input: s, encoding: 'utf8' }).trim().split(/\s+/)[0];

/** openssl 独立算 AES 密文（hex）。流模式要 -nopad，否则 openssl 会补到整块。 */
const opensslAes = (cipher, keyHex, ivHex, noPad = false) => {
  const args = ['enc', `-${cipher}`, '-K', keyHex];
  if (ivHex) args.push('-iv', ivHex);
  args.push('-nosalt');
  if (noPad) args.push('-nopad');
  return execFileSync('openssl', args, { input: Buffer.from(AES_PT), encoding: 'buffer' }).toString('hex');
};

const hex = (b) => Buffer.from(b).toString('hex');

const SKIP = (t, why) => { t.skip(why); return true; };

// ─────────────────────────────────────────────────────────────
// 夹具里的那个 RSA 公钥 —— 只此一份，测试从这里读，避免两处各存一份然后漂移
// ─────────────────────────────────────────────────────────────
const utilsFixtureSrc = readFileSync(path.join(FIXTURES, 'utils-check.js'), 'utf8');
const RSA_PUB = (() => {
  const m = /const RSA_PUB = `([\s\S]*?)`/.exec(utilsFixtureSrc);
  assert.ok(m, 'scripts/lx-fixtures/utils-check.js 里找不到 RSA_PUB 模板串 —— 测试与夹具的约定变了，先修这里');
  return m[1];
})();

// 固定输入（与夹具 utils-check.js 里那组必须一致，否则比对无意义）
const AES_KEY = '0123456789abcdef';
const AES_KEY32 = '0123456789abcdef0123456789abcdef';
const AES_IV = 'fedcba9876543210';
const AES_PT = 'The quick brown fox jumps over the lazy dog';

// ─────────────────────────────────────────────────────────────
// oracle ①：浏览器参考实现（缺 crypto-js 就整组跳过）
// ─────────────────────────────────────────────────────────────
let refMod = null;
let refWhy = '';
try {
  refMod = await import(pathToFileURL(REF_IMPL).href);
} catch (e) {
  refWhy = String(e.message).split('\n')[0];
}

/** 用参考实现算同一张表 —— 就是上一轮 /tmp/lx-u/ref.mjs 干的事，现在住在仓库里 */
async function buildRefTable(m) {
  const out = {
    md5_hello: m.md5Hex('hello'),
    md5_hello_type: typeof m.md5Hex('hello'),
    md5_hello_len: m.md5Hex('hello').length,
    md5_cn: m.md5Hex('晴天'),
    md5_int: m.md5Hex(123),

    buffer_from_hex: Buffer.from('68656c6c6f', 'hex').toString('utf8'),
    bufToString_utf8: m.bufToString(Buffer.from('中', 'utf8'), 'utf8'),
    bufToString_str_binary: m.bufToString('é', 'utf8'),
    bufToString_legacy: m.bufToString('é', 'utf8'),
    bufToString_hex: m.bufToString(Buffer.from('616263', 'hex'), 'hex'),

    aes_bad_mode: (() => {
      try { m.aesEncrypt(Buffer.from('x'), 'aes-128-zzz', AES_KEY, AES_IV); return 'NO THROW'; }
      catch (e) { return 'throw: ' + e.message; }
    })(),
    rsa_overlong: (() => {
      try { m.rsaEncryptNoPadding(Buffer.alloc(300, 1), RSA_PUB); return 'NO THROW'; }
      catch (e) { return 'throw: ' + e.message; }
    })(),
  };
  out.aes_128_cbc = hex(m.aesEncrypt(Buffer.from(AES_PT), 'aes-128-cbc', AES_KEY, AES_IV));
  out.aes_cbc_short_name = hex(m.aesEncrypt(Buffer.from(AES_PT), 'cbc', AES_KEY, AES_IV));
  out.aes_128_ecb = hex(m.aesEncrypt(Buffer.from(AES_PT), 'aes-128-ecb', AES_KEY));
  out.aes_128_cfb = hex(m.aesEncrypt(Buffer.from(AES_PT), 'aes-128-cfb', AES_KEY, AES_IV));
  out.aes_128_ofb = hex(m.aesEncrypt(Buffer.from(AES_PT), 'aes-128-ofb', AES_KEY, AES_IV));
  out.aes_128_ctr = hex(m.aesEncrypt(Buffer.from(AES_PT), 'aes-128-ctr', AES_KEY, AES_IV));
  out.aes_256_cbc_key_infer = hex(m.aesEncrypt(Buffer.from(AES_PT), 'aes-128-cbc', AES_KEY32, AES_IV));
  out.aes_toString_hex = m.aesEncrypt(Buffer.from(AES_PT), 'aes-128-cbc', AES_KEY, AES_IV).toString();
  out.rsa_len = m.rsaEncryptNoPadding(Buffer.from('hello'), RSA_PUB).length;
  out.rsa_hello = hex(m.rsaEncryptNoPadding(Buffer.from('hello'), RSA_PUB));
  out.rsa_empty = hex(m.rsaEncryptNoPadding(Buffer.from(''), RSA_PUB));

  // 压缩产物字节不比对（两边实现不同，字节本来就不保证相同），只比对解出来的明文
  const deflated = await m.zlibDeflate(Buffer.from('zlib roundtrip 你好'));
  out.zlib_deflate_is_promise = typeof m.zlibDeflate(Buffer.from('x')).then === 'function';
  out.zlib_inflate_is_promise = typeof m.zlibInflate(deflated).then === 'function';
  out.zlib_inflate_roundtrip = (await m.zlibInflate(deflated)).toString('utf8');

  // 裸 deflate（无 zlib 头）→ 必须走两段式的第二个分支。用浏览器原生的 deflate-raw 造。
  const cs = new CompressionStream('deflate-raw');
  const w = cs.writable.getWriter();
  await w.write(Buffer.from('raw deflate 裸流'));
  await w.close();
  const chunks = [];
  const reader = cs.readable.getReader();
  for (;;) { const { done, value } = await reader.read(); if (done) break; chunks.push(Buffer.from(value)); }
  out.zlib_inflate_raw = (await m.zlibInflate(Buffer.concat(chunks))).toString('utf8');
  return out;
}

const refTable = refMod ? await buildRefTable(refMod) : null;
const REF_SKIP_WHY =
  `拿不到浏览器参考实现 frontend/src/engine/lx-compat.js（${refWhy}）—— 通常是 frontend/node_modules 没装（它 import crypto-js）。` +
  '本组跳过：这一组回答的是「两份实现是否逐字节一致」，CLI 顶不了这个问题；' +
  'openssl / md5sum / node:crypto 那几组是另一个角度的独立核对，不受影响。';

// ─────────────────────────────────────────────────────────────
// 探针报告（跑真夹具）
// ─────────────────────────────────────────────────────────────
const report = probe('utils-check.js').inited;
const trap = probe('trap-check.js');
const wl = probe('require-whitelist-check.js').inited;

// ─────────────────────────────────────────────────────────────
// [0] 两份实现：共享块必须逐字节相同
// ─────────────────────────────────────────────────────────────
// probe 与宿主各自内嵌同一段沙箱实现（miss 陷阱 / lx.utils / require 白名单）。
// 它们必须**逐字节**相同 —— 否则「探针说能跑」对生产宿主没有意义。
// 上一轮是靠手工 diff 守的，这里变成一条断言。
test('[0] lx-probe.mjs 与 server.mjs 的共享块逐字节相同', async (t) => {
  const read = (p) => readFileSync(path.join(ROOT, p), 'utf8');
  // 共享块的边界用两端各自独有的标志串切出来（两边共用同一段注释开头）
  const START = '// 陷阱：读 miss 就记账';
  const ENDS = {
    'scripts/lx-probe.mjs': '// 按 frontend/src/engine/lx-runtime.js 的契约复刻宿主',
    'sidecar/lx_host/server.mjs': '/** 按 frontend/src/engine/lx-runtime.js 的契约造一个 lx 宿主',
  };
  const slice = (p) => {
    const src = read(p);
    const a = src.indexOf(START);
    const b = src.indexOf(ENDS[p]);
    assert.ok(a >= 0 && b > a, `${p} 找不到共享块的边界（标志串变了？先修这条断言）`);
    return src.slice(a, b);
  };
  const mine = slice('scripts/lx-probe.mjs');
  const theirs = slice('sidecar/lx_host/server.mjs');

  await t.test('两段共享块长度相同', () => assert.equal(mine.length, theirs.length));
  await t.test('两段共享块内容逐字节相同', () => assert.equal(mine, theirs));
  await t.test('共享块里含 require 白名单（防止哪天被删掉）', () => {
    assert.match(mine, /function createLxRequire\(\)/);
    assert.match(mine, /沙箱拒绝 require\('\$\{id\}'\)：该模块不在宿主白名单里/);
  });
  // ⚠️ 注释里的例子不算 —— 两个文件的注释里**故意**留了「原来是 createRequire(import.meta.url)」这种记录
  const codeOnly = (p) => readFileSync(path.join(ROOT, p), 'utf8')
    .split('\n')
    .filter((l) => !/^\s*(\/\/|\*)/.test(l));

  await t.test('两个文件里都不再残留真的 Node require（代码行里不许有 createRequire）', () => {
    for (const p of ['scripts/lx-probe.mjs', 'sidecar/lx_host/server.mjs']) {
      const hits = codeOnly(p).filter((l) => l.includes('createRequire'));
      assert.deepEqual(hits, [], `${p} 里还有 createRequire（真 require 又回到宿主手上了）：\n${hits.join('\n')}`);
    }
  });
  await t.test('代码行里唯一的 require( 字样就是白名单那条错误信息（模板串里）', () => {
    for (const p of ['scripts/lx-probe.mjs', 'sidecar/lx_host/server.mjs']) {
      // 白名单函数抛错那条模板串里的 `require(` 是**报错文案**，不是调用 —— 它是唯一豁免
      const bare = codeOnly(p)
        .filter((l) => /(^|[^.\w`])require\s*\(/.test(l))
        .filter((l) => !l.includes('沙箱拒绝 require('));
      assert.deepEqual(bare, [], `${p} 里还有裸 require 调用：\n${bare.join('\n')}`);
    }
  });
});

// ─────────────────────────────────────────────────────────────
// [1] 两份实现是否逐字节一致（浏览器参考实现 vs Node 移植版）
// ─────────────────────────────────────────────────────────────
test('[1] lx.utils：Node 移植版 vs 浏览器参考实现（逐字段）', async (t) => {
  if (!refTable) return void SKIP(t, REF_SKIP_WHY);
  for (const k of Object.keys(refTable)) {
    await t.test(`${k} 一致`, () => {
      assert.equal(report[k], refTable[k]);
    });
  }
});

// ─────────────────────────────────────────────────────────────
// [2] md5：公开常量 + md5sum CLI
// ─────────────────────────────────────────────────────────────
test('[2] md5', async (t) => {
  await t.test("md5('hello') = 5d41402abc4b2a76b9719d911017c592（公开常量）", () => {
    assert.equal(report.md5_hello, '5d41402abc4b2a76b9719d911017c592');
  });
  await t.test("md5('123') = 202cb962ac59075b964b07152d234b70（公开常量）", () => {
    assert.equal(report.md5_int, '202cb962ac59075b964b07152d234b70');
  });
  await t.test('md5 返回 string 且 length=32（回归护栏：不能是 16 字节 Buffer）', () => {
    assert.equal(report.md5_hello_type, 'string');
    assert.equal(report.md5_hello_len, 32);
  });
  await t.test("md5(x).toString('hex') 仍成立（老脚本写法）", () => {
    assert.equal(report.md5_hello_toString_hex, report.md5_hello);
  });
  await t.test("md5('晴天') vs md5sum CLI", (t2) => {
    if (!HAS_MD5SUM) return void SKIP(t2, '本机没有 md5sum，跳过（其余 md5 断言仍在）');
    assert.equal(report.md5_cn, md5sumCli('晴天'));
  });
  await t.test("md5('hello') vs md5sum CLI", (t2) => {
    if (!HAS_MD5SUM) return void SKIP(t2, '本机没有 md5sum，跳过');
    assert.equal(report.md5_hello, md5sumCli('hello'));
  });
});

// ─────────────────────────────────────────────────────────────
// [3] AES：密文 vs openssl CLI（独立实现）+ 反向解密
// ─────────────────────────────────────────────────────────────
test('[3] AES 各模式', async (t) => {
  const K = Buffer.from(AES_KEY).toString('hex');
  const K32 = Buffer.from(AES_KEY32).toString('hex');
  const IV = Buffer.from(AES_IV).toString('hex');

  const vsOpenssl = (name, cipher, keyHex, ivHex, noPad, got) =>
    t.test(name, (t2) => {
      if (!HAS_OPENSSL) return void SKIP(t2, '本机没有 openssl，跳过（其余 AES 断言仍在）');
      assert.equal(got, opensslAes(cipher, keyHex, ivHex, noPad));
    });

  await t.test('mode 只写 "cbc"（不带位数）结果相同 → 位数按密钥长度推', () => {
    assert.equal(report.aes_cbc_short_name, report.aes_128_cbc);
  });
  await vsOpenssl('aes-128-cbc 与 openssl 一致', 'aes-128-cbc', K, IV, false, report.aes_128_cbc);
  await vsOpenssl('aes-128-ecb 与 openssl 一致', 'aes-128-ecb', K, null, false, report.aes_128_ecb);
  await vsOpenssl('aes-128-cfb 与 openssl 一致（-nopad）', 'aes-128-cfb', K, IV, true, report.aes_128_cfb);
  await vsOpenssl('aes-128-ofb 与 openssl 一致（-nopad）', 'aes-128-ofb', K, IV, true, report.aes_128_ofb);
  await vsOpenssl('aes-128-ctr 与 openssl 一致（-nopad）', 'aes-128-ctr', K, IV, true, report.aes_128_ctr);
  await vsOpenssl(
    '32 字节密钥 + mode 写 aes-128-cbc → 按 256 位算（= openssl aes-256-cbc）',
    'aes-256-cbc', K32, IV, false, report.aes_256_cbc_key_infer,
  );
  await t.test('密文能被独立实现（node:crypto decipheriv）解回原文', () => {
    assert.equal(report.aes_128_cbc_decrypts_back, AES_PT);
  });
  await t.test('未知模式抛错而不是猜一个模式', () => {
    assert.match(report.aes_bad_mode, /^throw: 不支持的 AES 模式/);
  });
  await t.test('toString() 无参返回 hex（部分脚本这么用）', () => {
    assert.equal(report.aes_toString_hex, report.aes_128_cbc);
  });
});

// ─────────────────────────────────────────────────────────────
// [4] RSA：vs node:crypto 的 RSA_NO_PADDING（独立实现）
// ─────────────────────────────────────────────────────────────
test('[4] RSA（裸模幂 / RSA_NO_PADDING 语义）', async (t) => {
  await t.test('输出长度 == 模长 256', () => {
    assert.equal(report.rsa_len, 256);
  });
  await t.test('密文与 node:crypto 的 RSA_NO_PADDING 逐字节相同（左补零语义）', () => {
    const native = nodeCrypto.publicEncrypt(
      { key: RSA_PUB, padding: nodeCrypto.constants.RSA_NO_PADDING },
      Buffer.concat([Buffer.alloc(251), Buffer.from('hello')]),
    );
    assert.equal(report.rsa_hello, native.toString('hex'));
  });
  await t.test('与浏览器参考实现一致', (t2) => {
    if (!refTable) return void SKIP(t2, REF_SKIP_WHY);
    assert.equal(report.rsa_hello, refTable.rsa_hello);
  });
  await t.test('空输入 → 全 0（0^e mod n = 0），但长度仍是 256', () => {
    assert.equal(report.rsa_empty, '00'.repeat(256));
  });
  await t.test('超长输入抛错而不是静默截断', () => {
    assert.match(report.rsa_overlong, /^throw: rsaEncrypt 输入过长/);
  });
});

// ─────────────────────────────────────────────────────────────
// [5] buffer / bufToString
// ─────────────────────────────────────────────────────────────
test('[5] buffer / bufToString', async (t) => {
  await t.test("buffer.from('68656c6c6f','hex').toString() === 'hello'", () => {
    assert.equal(report.buffer_from_hex, 'hello');
  });
  await t.test("bufToString(Buffer('中','utf8')) === '中'", () => {
    assert.equal(report.bufToString_utf8, '中');
  });
  await t.test("bufToString('é','utf8') 走 binary 再解码（由 Buffer 原语独立推导，≠ 原样返回）", () => {
    // 「先 Buffer.from(s,'binary') 再 toString(enc)」是这一层的语义，用 Buffer 自己推导期望值
    assert.equal(report.bufToString_str_binary, Buffer.from('é', 'binary').toString('utf8'));
    assert.notEqual(report.bufToString_str_binary, 'é');
  });
  await t.test('旧写法 utils.bufToString 与 buffer.bufToString 同结果', () => {
    assert.equal(report.bufToString_legacy, report.bufToString_str_binary);
  });
  await t.test("format='hex' 生效", () => {
    assert.equal(report.bufToString_hex, '616263');
  });
});

// ─────────────────────────────────────────────────────────────
// [6] zlib：Promise 契约 + 往返 + 跨实现互操作
// ─────────────────────────────────────────────────────────────
test('[6] zlib', async (t) => {
  await t.test('zlib.deflate 返回 Promise', () => {
    assert.equal(report.zlib_deflate_is_promise, true);
  });
  await t.test('zlib.inflate 返回 Promise（对齐官方文档 Promise<Buffer>）', () => {
    assert.equal(report.zlib_inflate_is_promise, true);
  });
  await t.test('resolve 出来是 Buffer', () => {
    assert.equal(report.zlib_deflate_is_buffer, true);
  });
  await t.test('deflate→inflate 往返一致', () => {
    assert.equal(report.zlib_inflate_roundtrip, 'zlib roundtrip 你好');
  });
  await t.test('裸 deflate（无 zlib 头）能解 → 两段式 fallback 生效', () => {
    assert.equal(report.zlib_inflate_raw, 'raw deflate 裸流');
  });
  await t.test('我们压出的字节能被独立实现 node:zlib 解开（夹具自己也这么验过一次）', () => {
    assert.equal(nodeZlib.inflateSync(Buffer.from(report.zlib_deflate_hex, 'hex')).toString('utf8'), 'zlib roundtrip 你好');
    assert.equal(report.zlib_deflate_cross_node, 'zlib roundtrip 你好');
  });
  await t.test('我们压出的字节能被浏览器参考实现（CompressionStream）解开', (t2) => {
    if (!refTable) return void SKIP(t2, REF_SKIP_WHY);
    // 这一段是「浏览器实现（CompressionStream）解我们压出来的字节」—— 与上面那条 node:zlib 互查是两件事
    return refMod.zlibInflate(Buffer.from(report.zlib_deflate_hex, 'hex')).then((b) => {
      assert.equal(b.toString('utf8'), 'zlib roundtrip 你好');
    });
  });
  await t.test('顶层遗留 utils.deflate 与 zlib.deflate 同实现', () => {
    assert.equal(report.zlib_top_deflate_same, 'top level');
  });
});

// ─────────────────────────────────────────────────────────────
// [7] randomBytes
// ─────────────────────────────────────────────────────────────
test('[7] randomBytes', async (t) => {
  await t.test('长度正确且是 Buffer', () => {
    assert.equal(report.rand_len, 16);
    assert.equal(report.rand_is_buffer, true);
  });
  await t.test('两次调用不同（不是常量）', () => {
    assert.equal(report.rand_two_differ, true);
  });
});

// ─────────────────────────────────────────────────────────────
// [8] 陷阱：嵌套缺失可见 + 真实成员零误报
// ─────────────────────────────────────────────────────────────
test('[8] miss 陷阱', async (t) => {
  const wants = trap.wants;
  const MUST_REPORT = [
    'lx.someMissingMember',         // 一级
    'lx.utils.md5Hex',              // 二级（错位写法）
    'lx.utils.inflate',             // 二级（常见错位写法）
    'lx.utils.crypto.aesDecrypt',   // 三级
    'lx.utils.zlib.gunzip',         // 三级
    'lx.utils.buffer.allocUnsafe',  // 三级
  ];
  const MUST_NOT_REPORT = [
    'lx.utils', 'lx.utils.bufToString', 'lx.EVENT_NAMES', 'lx.request', 'lx.on', 'lx.send',
    'lx.env', 'lx.version', 'lx.currentScriptInfo',
    'lx.utils.crypto.md5', 'lx.utils.buffer.from', 'lx.utils.zlib.inflate',
    'lx.utils.crypto.rsaEncrypt', 'lx.utils.crypto.randomBytes',
  ];
  for (const k of MUST_REPORT) {
    await t.test(`wants 含 ${k}`, () => assert.ok(wants.includes(k), `wants=${JSON.stringify(wants)}`));
  }
  for (const k of MUST_NOT_REPORT) {
    await t.test(`wants 不含 ${k}（真实存在的成员不能误报）`, () => assert.ok(!wants.includes(k), `wants=${JSON.stringify(wants)}`));
  }
  await t.test('读缺失成员不抛错（与桌面端行为一致，只是我们记下来了）', () => {
    assert.equal(trap.inited.readMissingThrows, 'no');
  });
  await t.test('解构 lx.utils 正常工作', () => {
    assert.equal(trap.inited.destructure.md5, 'function');
    assert.equal(trap.inited.destructure.from, 'function');
  });
  await t.test('Object.keys(lx.utils) 正常', () => {
    assert.deepEqual(trap.inited.keys_utils, ['bufToString', 'buffer', 'crypto', 'deflate', 'zlib']);
  });
  await t.test('Object.keys(lx.utils.crypto) 正常', () => {
    assert.deepEqual(trap.inited.keys_crypto, ['aesEncrypt', 'md5', 'randomBytes', 'rsaEncrypt']);
  });
  await t.test('真实成员读出来都是 function', () => {
    for (const [k, v] of Object.entries(trap.inited.levels.real)) {
      assert.notEqual(v, 'undefined', `${k} 读出来是 undefined —— 宿主漏给了`);
    }
  });
});

// ─────────────────────────────────────────────────────────────
// [9] 沙箱 require 白名单
// ─────────────────────────────────────────────────────────────
test('[9] require 白名单', async (t) => {
  const ALLOWED = ['buffer', 'crypto', 'url', 'zlib', 'node:buffer', 'node:crypto', 'node:url', 'node:zlib'];
  const DENIED = ['node:fs', 'fs', 'node:fs/promises', 'node:child_process', 'child_process',
    'node:os', 'node:net', 'node:vm', 'node:module', 'module', 'crypto-js', 'Crypto',
    'crypto/promises', 'node:zlib/promises', 'node:worker_threads', 'node:process', '',
    '(no arg)'];

  for (const id of ALLOWED) {
    await t.test(`放行 ${id}`, () => assert.equal(wl.allowed[id], 'object'));
  }
  for (const id of DENIED) {
    await t.test(`拒绝 ${id}（抛错且信息可读）`, () => {
      const msg = wl.denied[id];
      assert.notEqual(msg, '❌ ALLOWED（不该放行）', `${id} 被放行了`);
      assert.match(msg, /^沙箱拒绝 require\('/, `${id} 的报错没说清是沙箱拒绝：${msg}`);
      assert.match(msg, /宿主白名单/, `${id} 的报错没提到宿主白名单：${msg}`);
      assert.match(msg, /buffer \/ crypto \/ url \/ zlib/, `${id} 的报错没说清放行了什么：${msg}`);
    });
  }

  await t.test("require('buffer') 真能用", () => assert.equal(wl.usage.buffer, '6869'));
  await t.test("require('crypto') 真能用（sha256 与 node:crypto 对齐）", () => {
    assert.equal(wl.usage.crypto_createHash, nodeCrypto.createHash('sha256').update('x').digest('hex'));
  });
  await t.test("require('crypto') 真能用（md5）", () => {
    assert.equal(wl.usage.crypto_md5_via_hash, '5d41402abc4b2a76b9719d911017c592');
  });
  await t.test("require('crypto') 的 randomBytes 可用", () => assert.equal(wl.usage.crypto_randomBytes, true));
  await t.test("require('zlib') 真能用（往返）", () => assert.equal(wl.usage.zlib_roundtrip, '白名单 zlib 往返'));
  await t.test("require('url') 真能用（URL/URLSearchParams）", () => assert.equal(wl.usage.url, '1'));
  await t.test("require('crypto') === require('node:crypto')（缓存语义：两种写法同一个对象）", () => {
    assert.equal(wl.usage.cache_same_object, true);
  });

  // 顶层就撞白名单 → 加载期必须明确失败（不是 undefined 拖到后面炸）
  for (const [fixture, mod] of [['require-denied-fs.js', 'node:fs'], ['require-denied-childprocess.js', 'node:child_process']]) {
    await t.test(`${fixture}：加载期明确失败`, () => {
      const out = probe(fixture);
      assert.equal(out.errors.length, 1, `errors=${JSON.stringify(out.errors)}`);
      assert.match(out.errors[0], new RegExp(`沙箱拒绝 require\\('${mod.replace(/[._]/g, '\\$&')}'\\)`));
      assert.match(out.errors[0], /宿主白名单/);
      assert.deepEqual(out.handlers, [], '脚本没加载成功，不该注册到 handler');
    });
  }
});

// ─────────────────────────────────────────────────────────────
// [10] 真宿主端到端：/health + POST /resolve
// ─────────────────────────────────────────────────────────────
test('[10] 真宿主（sidecar/lx_host/server.mjs）端到端', { timeout: 60_000 }, async (t) => {
  // 挑一个空闲端口（不写死，避免与别的东西撞）
  const port = await new Promise((res, rej) => {
    const srv = createNetServer();
    srv.on('error', rej);
    srv.listen(0, '127.0.0.1', () => {
      const p = srv.address().port;
      srv.close(() => res(p));
    });
  });

  // 宿主会加载目录里**所有** .js —— 所以在一个临时目录里只放要验的那三个
  const dir = mkdtempSync(path.join(os.tmpdir(), 'lx-utils-check-'));
  for (const f of ['utils-guard.js', 'require-denied-fs.js', 'require-denied-childprocess.js']) {
    cpSync(path.join(FIXTURES, f), path.join(dir, f));
  }

  const child = spawn(process.execPath, [HOST, '--dir', dir, '--port', String(port)], {
    cwd: ROOT, stdio: ['ignore', 'pipe', 'pipe'],
  });
  let hostLog = '';
  child.stdout.on('data', (c) => { hostLog += c; });
  child.stderr.on('data', (c) => { hostLog += c; });

  // 按 pid 收尸（别用 pkill -f：发起命令的 shell 自己的命令行里也有那个串，会一起被杀）
  t.after(async () => {
    if (child.exitCode === null && child.signalCode === null) {
      child.kill('SIGTERM');
      await once(child, 'exit').catch(() => {});
    }
    rmSync(dir, { recursive: true, force: true });
  });

  const request = (method, p, body) => new Promise((resolve, reject) => {
    const payload = body ? JSON.stringify(body) : null;
    const req = http.request({
      host: '127.0.0.1', port, method, path: p,
      headers: payload ? { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(payload) } : {},
    }, (res) => {
      let b = '';
      res.setEncoding('utf8');
      res.on('data', (c) => { b += c; });
      res.on('end', () => resolve({ status: res.statusCode, text: b }));
    });
    req.on('error', reject);
    if (payload) req.write(payload);
    req.end();
  });

  // 等它起来（最多 15s）
  let health = null;
  for (let i = 0; i < 60; i++) {
    try {
      const r = await request('GET', '/health');
      if (r.status === 200) { health = JSON.parse(r.text); break; }
    } catch { /* 还没监听 */ }
    await new Promise((r) => setTimeout(r, 250));
  }

  await t.test('GET /health 起来了', () => {
    assert.ok(health, `宿主 15s 没起来。日志：\n${hostLog}`);
    assert.equal(health.ok, true);
  });
  if (!health) return;

  const byFile = Object.fromEntries(health.scripts.map((s) => [s.file, s]));

  await t.test('utils-guard.js 加载成功且注册了 request', () => {
    assert.ok(byFile['utils-guard.js'], 'health 里没有 utils-guard.js');
    assert.equal(byFile['utils-guard.js'].ok, true, JSON.stringify(byFile['utils-guard.js']));
    assert.deepEqual(byFile['utils-guard.js'].actions, ['request']);
    assert.deepEqual(byFile['utils-guard.js'].wants, []);
  });
  await t.test('/health 把「白名单拒绝」如实报出来（ok=false + 可读的 error）', () => {
    for (const [f, mod] of [['require-denied-fs.js', 'node:fs'], ['require-denied-childprocess.js', 'node:child_process']]) {
      assert.equal(byFile[f].ok, false, `${f} 不该加载成功`);
      assert.match(byFile[f].error, /宿主白名单/, `${f} 的 error=${byFile[f].error}`);
      assert.ok(byFile[f].error.includes(mod), `${f} 的 error 没点明是哪个模块：${byFile[f].error}`);
    }
  });
  await t.test('POST /resolve 拿到真直链（契约：info.musicInfo + info.type）', async () => {
    const r = await request('POST', '/resolve', {
      source: 'wy',
      action: 'musicUrl',
      quality: '320k',
      info: {
        type: '320k',
        musicInfo: { source: 'wy', id: '1', songmid: '1', name: 'probe', singer: 'probe', album: 'probe' },
      },
    });
    assert.equal(r.status, 200, `返回 ${r.status}：${r.text}\n宿主日志：\n${hostLog}`);
    const body = JSON.parse(r.text);
    assert.equal(body.script, 'utils-guard.js');
    assert.match(body.url, /^https:\/\/example\.com\/utils-guard-ok-[0-9a-f]{8}-[0-9a-f]{8}$/,
      '拿到的不是 utils-guard 的直链 —— 说明宿主那份 utils 有某项没算对（uuid 里含 md5 与 AES 密文前 8 位）');
  });
});
