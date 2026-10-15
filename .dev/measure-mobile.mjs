// 手机端布局实测（跑在 NAS 上，用本机 Chrome）。
//
// 为什么要有它：用户 2026-10-08 要求「适配手机端，一行可以用两个元素」。
// 改完之后「看起来对不对」在开发机上没法验（那边没浏览器、也没有真数据），
// 而 NAS 上有 Chrome、有真后端 —— 于是直接按 390×844 的手机视口把每个页面
// 跑一遍，量两件**能自动判对错**的事：
//
//   1. 有没有元素伸出视口（横向溢出）—— 手机端最常见的破版，而且它在桌面上看不见；
//   2. 两列网格是不是真的排成了两列（量一行里并排的元素数）。
//
// 顺带截图，人再扫一眼。
//
// 用法：node measure.mjs [url] [width] [height] [outdir]
import { spawn } from 'node:child_process';
import { writeFileSync, mkdirSync, existsSync } from 'node:fs';

const URL_BASE = process.argv[2] || 'http://127.0.0.1:8898/';
const W = Number(process.argv[3] || 390);
const H = Number(process.argv[4] || 844);
const OUT = process.argv[5] || '/tmp/qulv-shots';
const PORT = 9333;
mkdirSync(OUT, { recursive: true });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// 浏览器怎么找（2026-10-15 修）：原来硬编码 /usr/local/bin/google-chrome，
// 而**这台 NAS 上那个 Chrome 已经没了**（2026-10-15 实测：google-chrome / chromium /
// firefox 全无，也没有 playwright 下载的浏览器）→ 脚本今天第一次跑就 ENOENT 崩，
// 属「工具失效」而不是「页面破版」。三级兜底：
//   ① CHROME_BIN=/path/to/chrome   显式指定
//   ② CDP_URL=http://host:9222     复用**已经开着的**浏览器（不自己起，最省事）
//   ③ 按候选名单自动找（Linux + macOS 常见路径）
const CANDIDATES = [
  process.env.CHROME_BIN,
  '/usr/local/bin/google-chrome', '/usr/bin/google-chrome', '/usr/bin/google-chrome-stable',
  '/usr/bin/chromium', '/usr/bin/chromium-browser', '/snap/bin/chromium',
  '/usr/bin/microsoft-edge', '/usr/bin/microsoft-edge-stable',
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
].filter(Boolean);
const findChrome = () => CANDIDATES.find((p) => { try { return existsSync(p); } catch { return false; } }) || null;

const CDP_URL = (process.env.CDP_URL || '').replace(/\/+$/, '');
const chrome = CDP_URL ? null : (() => {
  const bin = findChrome();
  if (!bin) {
    console.log(JSON.stringify({ fatal:
      '本机没有找到浏览器（Chrome/Chromium 都不在）。两种办法：' +
      '① CHROME_BIN=/path/to/chrome node .dev/measure-mobile.mjs …；' +
      '② 复用已开着的浏览器：CDP_URL=http://127.0.0.1:9222 node .dev/measure-mobile.mjs …' }));
    process.exit(1);
  }
  return spawn(bin, [
    '--headless=new', '--disable-gpu', '--no-sandbox', '--disable-dev-shm-usage',
    `--remote-debugging-port=${PORT}`,
    '--user-data-dir=/tmp/qulv-measure-profile',
    'about:blank',
  ], { stdio: 'ignore' });
})();
const CDP_BASE = CDP_URL || `http://127.0.0.1:${PORT}`;

function cdpClient(ws) {
  let id = 0;
  const pending = new Map();
  ws.addEventListener('message', (e) => {
    const m = JSON.parse(e.data);
    if (m.id && pending.has(m.id)) {
      const { res, rej } = pending.get(m.id);
      pending.delete(m.id);
      m.error ? rej(new Error(JSON.stringify(m.error))) : res(m.result);
    }
  });
  return (method, params = {}) =>
    new Promise((res, rej) => {
      const i = ++id;
      pending.set(i, { res, rej });
      ws.send(JSON.stringify({ id: i, method, params }));
    });
}

// ⚠️ 只报**从可见位置向右伸出**的元素。
// 滑动切页（SwipePager）把其它视图整条排在左边（left 是负的），那是正常结构，
// 不是溢出 —— 第一版没排除它，每个页面都「溢出 8 处」，全是离屏兄弟页。
const MEASURE = `(() => {
  const vw = window.innerWidth;
  const bad = [];
  // ⚠️ 横向可滚动的一行（chip 行 / 标签行）里，靠滑动才能看到的元素不算「溢出」——
  // 2026-10-15 加：原来不排它，于是「本地音乐」的「+ 更多目录」每轮都被报成溢出，
  // 追下去发现父容器是 overflow-x-auto（cw=322 / sw=441）的设计意图，属误报。
  const clippedByScroll = (el) => {
    let n = el.parentElement;
    while (n && n !== document.body) {
      const cs = getComputedStyle(n);
      if ((cs.overflowX === 'auto' || cs.overflowX === 'scroll') && n.scrollWidth > n.clientWidth + 1) return true;
      n = n.parentElement;
    }
    return false;
  };
  for (const el of document.querySelectorAll('body *')) {
    const r = el.getBoundingClientRect();
    if (r.width < 2 || r.height < 2) continue;
    if (r.left < -1) continue;                       // 离屏兄弟页，跳过
    if (clippedByScroll(el)) continue;               // 可横滑祖先里的元素，跳过
    if (r.right > vw + 1) {
      bad.push({ t: el.tagName, c: String(el.className || '').slice(0, 46),
                 l: Math.round(r.left), r: Math.round(r.right) });
      if (bad.length >= 8) break;
    }
  }
  // 一行里并排了几个：按子元素 left 去重数「列」
  const grids = [];
  for (const g of document.querySelectorAll('[class*="grid-cols"]')) {
    const r = g.getBoundingClientRect();
    if (r.width < 2 || r.left < -1 || r.top > window.innerHeight) continue;
    const kids = [...g.children].filter((c) => c.getBoundingClientRect().width > 2);
    if (kids.length < 2) continue;
    const cols = new Set(kids.map((c) => Math.round(c.getBoundingClientRect().left))).size;
    grids.push({ cols, n: kids.length, c: String(g.className || '').slice(0, 40) });
  }
  return JSON.stringify({ vw, docW: document.documentElement.scrollWidth, bad, grids: grids.slice(0, 6) });
})()`;

// ⚠️ 手机上顶部导航**只显示图标**（App.vue 的 navDensity），文字在 DOM 里但被
// `navDensityEffective === 'icon'` 判掉 —— 所以按 textContent 找不到它。
// aria-label 是 App.vue 一直设着的（无障碍用途），拿它当点击依据最稳。
const CLICK = (text) => `(() => {
  const want = ${JSON.stringify(text)};
  const cands = [...document.querySelectorAll('button, a, [role=button]')];
  const hit = cands.find((b) => (b.getAttribute('aria-label') || '') === want)
           || cands.find((b) => (b.getAttribute('title') || '') === want)
           || cands.find((b) => (b.textContent || '').trim() === want)
           || cands.find((b) => (b.textContent || '').includes(want));
  if (!hit) return 'NOT_FOUND';
  hit.click();
  return 'OK';
})()`;

const main = async () => {
  let ver = null;
  for (let i = 0; i < 40; i++) {
    try { const r = await fetch(`${CDP_BASE}/json/version`); if (r.ok) { ver = await r.json(); break; } } catch {}
    await sleep(500);
  }
  if (!ver) throw new Error('Chrome 没起来');

  const tab = await (await fetch(`${CDP_BASE}/json/new?about:blank`, { method: 'PUT' })).json();
  const ws = new WebSocket(tab.webSocketDebuggerUrl);
  await new Promise((r) => ws.addEventListener('open', r));
  const send = cdpClient(ws);

  await send('Page.enable');
  await send('Runtime.enable');
  await send('Emulation.setDeviceMetricsOverride',
    { width: W, height: H, deviceScaleFactor: 2, mobile: true });
  await send('Page.navigate', { url: URL_BASE });
  await sleep(7000);

  const evalJs = async (expr) => {
    const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
    return r?.result?.value;
  };

  const shot = async (name) => {
    const r = await send('Page.captureScreenshot', { format: 'png' });
    writeFileSync(`${OUT}/${name}.png`, Buffer.from(r.data, 'base64'));
  };

  const report = { viewport: `${W}x${H}`, pages: [] };

  const step = async (label, name, clickText) => {
    if (clickText) {
      const ok = await evalJs(CLICK(clickText));
      if (ok === 'NOT_FOUND') { report.pages.push({ label, click: clickText, error: 'NOT_FOUND' }); return; }
      await sleep(2200);
    }
    const raw = await evalJs(MEASURE);
    const m = JSON.parse(raw);
    await shot(name);
    report.pages.push({
      label, vw: m.vw, docW: m.docW,
      overflow: m.bad.length, worst: m.bad,
      // 一行几个：把「两列以上」的网格列出来，用来确认「一行两个元素」真的生效
      twoCol: m.grids.filter((g) => g.cols >= 2).map((g) => `${g.cols}列x${g.n}`),
    });
  };

  // 顶层导航（点完回到顶）
  await step('发现音乐', '01-discover', '发现音乐');
  await step('榜单管理', '02-charts', '榜单管理');
  await step('曲库管家', '03-library', '曲库管家');
  // 曲库管家的侧栏子页
  await step('曲库管家 · 本地音乐', '04-nas', '本地音乐');
  await step('曲库管家 · 推送', '05-push', '推送');
  await step('曲库管家 · 下载', '06-download', '下载');
  await step('曲库管家 · 补全整理', '07-complete', '补全整理');
  await step('曲库管家 · 自动下载记录', '08-autodl', '自动下载记录');
  await step('账号连接', '09-accounts', '账号连接');
  await step('日志', '10-logs', '日志');
  await step('设置', '11-settings', '设置');   // 2026-10-15 新增页（音质/命名/存储目录/关于）

  console.log(JSON.stringify(report));
  try { chrome.kill(); } catch {}
  process.exit(0);
};

main().catch((e) => { console.log(JSON.stringify({ fatal: String(e) })); try { chrome.kill(); } catch {} process.exit(1); });
