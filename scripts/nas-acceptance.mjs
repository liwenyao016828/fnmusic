// 曲率 NAS 真机验收脚本（goal-a5eb2a23 的验收标准）。
//
// 为什么要有这个文件：前面几十轮里，每轮收尾的「真机复验」都是我临时手拼一段脚本 ——
// 结果是**同一个坑反复踩**。下面每一条纪律都是踩出来的，写在它对应的位置旁边。
//
// 怎么跑：在 NAS 上（ego-browser 的 nodejs 环境）取这个文件的正文并求值，
// 注入 `page`（浏览器）、`cp`（child_process）。见文件末尾的 RUN 说明。
//
// 用法（宿主侧）：
//   node --input-type=module -e "…取正文 + new Function('page','cp','http', 'return (async()=>{'+src+'})()')…"
// 或者直接把它当 ego_script 的脚本体（本文件就是那个脚本体，不用改一行）。

const run = (cmd, t = 200000) => { try { return cp.execSync(cmd, { timeout: t }).toString(); } catch (e) { return 'EXIT ' + e.status + ' ' + (e.stdout || '') + ' ' + (e.stderr || ''); } };
const out = {};
const say = (k, v) => { out[k] = v; };

const API = 'http://127.0.0.1:8898';       // 曲率自己的 API（回环免鉴权）
const PAGE = 'http://127.0.0.1:4398/music/'; // 飞牛官方页面（要浏览器的会话才能调它的 API）

const setCfg = (o) => run(`curl -s -X POST -H 'Content-Type: application/json' -d '${JSON.stringify(o)}' ${API}/api/config -o /dev/null -w '%{http_code}'`).trim();
const reg = () => run(`sudo -n sh -c 'cat /vol1/@appdata/yinshu-ai/data/online/downloaded.json 2>/dev/null'`).trim();
const srch = (kw, src) => run(`curl -s --max-time 60 '${API}/api/search?keyword=${encodeURIComponent(kw)}&source=${src}&page=1&pageSize=3' | python3 -c "
import json,sys
d=json.load(sys.stdin); d=d.get('data',d); L=d.get('list',[]) if isinstance(d,dict) else []
print(len(L))"`).trim();
// ⚠️ page.evaluate(fn, ...args) **只传第一个参数**。多传的参数会静默变成 undefined，
//    表现是「服务端收到了请求但字段是空的」，看起来极像服务端的 bug（第 15 轮误报过一次）。
//    所以：要传多个值就自己拼成一个对象/字符串，只传一个。
const ev = (fn, arg) => page.evaluate(fn, arg);

say('status', run('sudo -n appcenter-cli status yinshu-ai', 60000).replace(/[\r]/g, '').trim());

// ── ① 音源页逐个启停生效 ────────────────────────────────────────────────
// ⚠️ 每一段必须换一个**没搜过**的关键词：pkg/search 有结果缓存，
//    拿同一个词验「开关前后」，测到的是缓存不是开关（第 10 轮踩过）。
const t0 = Date.now();
setCfg({ musicdl_sources: ['mg'] });
await new Promise((r) => setTimeout(r, 3000));
say('srcOn', srch('稻香', 'mg'));
setCfg({ musicdl_sources: [] });
await new Promise((r) => setTimeout(r, 5000));
say('srcOff', srch('青花瓷', 'mg'));       // 期望 0
setCfg({ musicdl_sources: ['mg'] });
await new Promise((r) => setTimeout(r, 5000));
say('srcBack', srch('七里香', 'mg'));      // 期望 >0
say('srcMs', Date.now() - t0);

await page.goto(PAGE).catch(() => {});
await new Promise((r) => setTimeout(r, 4000));

// ── ② 收藏一首网易歌 → 自动下载并绑定 ──────────────────────────────────
setCfg({ fav_auto_download: true, tee_enabled: false, lyric_auto_download: true, cover_embed: true });
const t = await ev(async (q) => {
  const s = await (await fetch('/music/api/v1/search/track?q=' + encodeURIComponent(q) + '&page=1&size=50', { headers: { Accept: 'application/json' } })).json();
  const l = ((s.data || {}).list || []).filter((x) => x.is_online && x.source === 'wy');
  return l.length ? { guid: l[0].guid, title: l[0].title, artist: l[0].artist } : null;
}, '蓝莲花');
say('favTrack', t);
if (t) {
  say('favCode', await ev(async (g) => {
    const r = await fetch('/music/api/v1/favorite-track/create', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ guid: g }) });
    return (await r.json()).code;
  }, t.guid));
  for (let i = 0; i < 15; i++) { if (reg().includes(t.guid)) break; cp.execSync('sleep 4'); }
  const n = `${t.artist} - ${t.title}`;
  say('regEntry', run(`sudo -n sh -c 'python3 -c "
import json
d=json.load(open(\\"/vol1/@appdata/yinshu-ai/data/online/downloaded.json\\"))
for it in d:
    if it[\\"guid\\"]==\\"${t.guid}\\":
        print(json.dumps({k:it.get(k) for k in (\\"title\\",\\"source\\",\\"quality\\",\\"size\\")}, ensure_ascii=False)); break
else: print(\\"（没登记）\\")"'`).trim());
  // 绑定：取流必须回 206，而且**内容就是磁盘那份**（只判 206 会假绿 —— 代理在线流也回 206）
  say('stream', run(`curl -s -o /dev/null -D - -r 0-255 --max-time 20 '${PAGE.replace(/\/$/, '')}/api/v1/track/stream?guid=${t.guid}' | head -2 | tr -d '\\r'`).trim().slice(0, 90));
  say('lrc', run(`sudo -n sh -c 'ls -l "/vol3/1000/音乐/测试下载/${n}.lrc" 2>/dev/null | awk "{print \\$5}"'`).trim() || '(无)');
  say('tags', run(`sudo -n sh -c 'python3 -c "
d=open(\\"/vol3/1000/音乐/测试下载/${n}.mp3\\",\\"rb\\").read()
print(\\"APIC\\" if b\\"APIC\\" in d else \\"无APIC\\")"'`).trim());
}

// ── ③ 开关关掉后不该再下载 ──────────────────────────────────────────────
setCfg({ fav_auto_download: false });
const t3 = await ev(async (q) => {
  const s = await (await fetch('/music/api/v1/search/track?q=' + encodeURIComponent(q) + '&page=1&size=50', { headers: { Accept: 'application/json' } })).json();
  const l = ((s.data || {}).list || []).filter((x) => x.is_online && x.source === 'wy');
  return l.length ? l[0].guid : null;
}, '海阔天空');
if (t3) {
  await ev(async (g) => { await fetch('/music/api/v1/favorite-track/create', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ guid: g }) }); }, t3);
  cp.execSync('sleep 6');
  say('switchOff', reg().includes(t3) ? '⚠️ 开关关着还是下载了' : '没下载 ✓');
}

// ── 收尾：配置还原成用户的默认 ─────────────────────────────────────────
setCfg({ fav_auto_download: false, tee_enabled: false, lyric_auto_download: true, cover_embed: true, musicdl_sources: ['mg'] });
say('finalCfg', run(`curl -s --max-time 8 ${API}/api/config | python3 -c "
import json,sys; d=json.load(sys.stdin); c=d.get('config',d)
print({k:c.get(k) for k in ('fav_auto_download','tee_enabled','lyric_auto_download','cover_embed','musicdl_sources')})"`).trim());
say('favTotal', await ev(async () => {
  const r = await fetch('/music/api/v1/favorite-track/list?page=1&size=200', { headers: { Accept: 'application/json' } });
  const j = await r.json().catch(() => ({}));
  return ((j.data || {}).list || []).length;
}));

// ── RUN ────────────────────────────────────────────────────────────────
// 宿主侧（dev box 把本文件挂到 :8893 后，在 NAS 上）：
//   const src = await (取 http://192.168.1.230:8893/nas-acceptance.mjs 的正文);
//   const fn = new Function('page','cp','http', 'return (async () => {' + src + '})()');
//   console.log(JSON.stringify(await fn(page, cp, http), null, 1));
console.log(JSON.stringify(out, null, 1));
