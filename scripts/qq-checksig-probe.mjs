// 验证：当前 QQ 成功扫码后返回的 jumpURL 长什么样，能否提取 uin / ptsigx
// 由于需要真人扫码，这里只验证「未扫码」时的响应格式，并打印对照库的正则
const WEB_UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36";

const hash33 = (value, seed = 0) => {
  let hash = seed;
  for (const c of value) hash = (((hash << 5) + hash + c.charCodeAt(0)) & 0xffffffff) >>> 0;
  return hash & 2147483647;
};
const responseCookies = (response, existing = {}) => {
  const out = { ...existing };
  for (const raw of (response.headers.getSetCookie?.() ?? [])) {
    const pair = raw.split(";", 1)[0];
    const i = pair.indexOf("=");
    if (i > 0) out[pair.slice(0, i).trim()] = pair.slice(i + 1).trim();
  }
  return out;
};

const qrResp = await fetch(`https://ssl.ptlogin2.qq.com/ptqrshow?appid=716027609&e=2&l=M&s=3&d=72&v=4&t=${Math.random()}&daid=383&pt_3rd_aid=100497308`, {
  headers: { Referer: "https://xui.ptlogin2.qq.com/", "User-Agent": WEB_UA },
  signal: AbortSignal.timeout(15000),
});
const key = responseCookies(qrResp).qrsig;
const qrBuf = Buffer.from(await qrResp.arrayBuffer());
// 二维码必须先落盘，否则 100 秒等待窗口内根本没东西可扫
await import("node:fs/promises").then((fs) => fs.writeFile("/tmp/qq-qr-latest.png", qrBuf));
console.log("qrsig =", key);
console.log("二维码已存 /tmp/qq-qr-latest.png —— 用 QQ 打开它或扫屏幕上的图，100 秒内确认");

// 长轮询到成功为止（最多等 100 秒），观察真实 jumpURL
console.log("\n等待扫码（最多 100 秒）...");
const deadline = Date.now() + 100_000;
let lastCode = "";
let successArgs = null;

while (Date.now() < deadline) {
  const query = new URLSearchParams({
    u1: "https://graph.qq.com/oauth2.0/login_jump", ptqrtoken: String(hash33(key)), ptredirect: "0",
    h: "1", t: "1", g: "1", from_ui: "1", ptlang: "2052", action: `0-0-${Date.now()}`,
    js_ver: "20102616", js_type: "1", pt_uistyle: "40", aid: "716027609", daid: "383",
    pt_3rd_aid: "100497308", has_onekey: "1",
  });
  const r = await fetch(`https://ssl.ptlogin2.qq.com/ptqrlogin?${query}`, {
    headers: { Referer: "https://xui.ptlogin2.qq.com/", Cookie: `qrsig=${key};`, "User-Agent": WEB_UA },
    signal: AbortSignal.timeout(15000),
  });
  const body = await r.text();
  const cb = body.match(/ptuiCB\((.*?)\)/)?.[1];
  const args = cb ? [...cb.matchAll(/'((?:\\.|[^'])*)'/g)].map((m) => m[1]) : [];
  const code = args[0] ?? "?";

  if (code !== lastCode) {
    console.log(`[${new Date().toLocaleTimeString()}] code=${code}  msg=${args[4] ?? ""}`);
    lastCode = code;
  }
  if (code === "0") { successArgs = args; break; }
  if (code === "65") { console.log("二维码已过期"); break; }
  await new Promise((r) => setTimeout(r, 1500));
}

if (successArgs) {
  const jump = successArgs[2];
  console.log("\n=== 成功！jumpURL ===");
  console.log(jump);
  const sigx = jump.match(/(?:\?|&)ptsigx=(.+?)&s_url/)?.[1];
  const uin = jump.match(/(?:\?|&)uin=(.+?)&service/)?.[1];
  console.log("\n提取 ptsigx =", sigx ?? "❌ 未匹配");
  console.log("提取 uin   =", uin ?? "❌ 未匹配");

  if (sigx && uin) {
    console.log("\n=== 用 check_sig 换 p_skey ===");
    const qs = new URLSearchParams({
      uin, pttype: "1", service: "ptqrlogin", nodirect: "0", ptsigx: sigx,
      s_url: "https://graph.qq.com/oauth2.0/login_jump", ptlang: "2052", ptredirect: "100",
      aid: "716027609", daid: "383", j_later: "0", low_login_hour: "0", regmaster: "0",
      pt_login_type: "3", pt_aid: "0", pt_aaid: "16", pt_light: "0", pt_3rd_aid: "100497308",
    });
    const sig = await fetch(`https://ssl.ptlogin2.graph.qq.com/check_sig?${qs}`, {
      headers: { Referer: "https://xui.ptlogin2.qq.com/", "User-Agent": WEB_UA },
      redirect: "manual", signal: AbortSignal.timeout(15000),
    });
    console.log("HTTP", sig.status);
    console.log("Set-Cookie 里是否有 p_skey:", /p_skey/.test(sig.headers.getSetCookie?.().join(";") ?? "") ? "✅ 有" : "❌ 没有");
    const ck = responseCookies(sig);
    console.log("拿到的 cookie:", Object.keys(ck).join(", "));
    console.log("p_skey =", ck.p_skey ? ck.p_skey.slice(0, 12) + "..." : "❌ 空");
  }
} else {
  console.log("\n（未在时限内扫码成功。请重新运行并尽快用手机 QQ 扫码）");
  const fs = await import("node:fs/promises");
  const again = await fetch(`https://ssl.ptlogin2.qq.com/ptqrshow?appid=716027609&e=2&l=M&s=3&d=72&v=4&t=${Math.random()}&daid=383&pt_3rd_aid=100497308`, {
    headers: { Referer: "https://xui.ptlogin2.qq.com/", "User-Agent": WEB_UA },
  });
  await fs.writeFile("/tmp/qq-qr-latest.png", Buffer.from(await again.arrayBuffer()));
  console.log("新二维码已存 /tmp/qq-qr-latest.png");
}
