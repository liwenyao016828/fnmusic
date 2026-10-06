// QQ音乐 App 扫码通道 A/B 探针（Node mqtt.js = 参考实现同生态库）。
// 用途：判断「扫码事件收不到」是服务端/扫码侧问题，还是我们 Go 客户端的差异。
// 跑法：cd ~/projects/music-v2 && node scripts/qq-appqr-probe.mjs
//      然后用 **QQ音乐 App**（不是手机 QQ）扫 /tmp/qq-app-qr.png，并在手机上点确认。
import fs from "node:fs";
import { createRequire } from "node:module";
import { execSync } from "node:child_process";
let mqtt;
try {
  mqtt = createRequire("/tmp/mqtttest/x.js")("mqtt");
} catch {
  console.log("安装 mqtt.js 到 /tmp/mqtttest ...");
  execSync("mkdir -p /tmp/mqtttest && cd /tmp/mqtttest && npm init -y >/dev/null && npm install --registry=https://registry.npmmirror.com mqtt@5 --silent");
  mqtt = createRequire("/tmp/mqtttest/x.js")("mqtt");
}
const UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36";
const t0 = Date.now();
const ts = () => `[${((Date.now() - t0) / 1000).toFixed(1)}s]`;

const r = await fetch("https://u.y.qq.com/cgi-bin/musicu.fcg", {
  method: "POST",
  headers: { "Content-Type": "application/json", Origin: "https://y.qq.com", Referer: "https://y.qq.com/", "User-Agent": UA },
  body: JSON.stringify({ comm: { ct: 23, cv: 0 }, request: { module: "music.login.LoginServer", method: "CreateQRCode", param: { tmeAppID: "qqmusic", ct: 11, cv: 14090008 } } }),
});
const j = await r.json();
const id = j.request?.data?.qrcodeID;
const img = j.request?.data?.qrcode;
if (!id) { console.log("CreateQRCode 失败:", JSON.stringify(j).slice(0, 300)); process.exit(1); }
fs.writeFileSync("/tmp/qq-app-qr.png", Buffer.from(img.split(",")[1] ?? "", "base64"));
console.log(ts(), "二维码已存 /tmp/qq-app-qr.png —— 用 **QQ音乐 App** 扫码并点确认");

let path = "/ws/handshake";
function dial() {
  const client = mqtt.connect("wss://mu.y.qq.com" + path, {
    protocolVersion: 5, clientId: String(Date.now()) + String(Math.floor(Math.random() * 9000) + 1000),
    keepalive: 45, clean: true,
    headers: { Origin: "https://y.qq.com", Referer: "https://y.qq.com/", "User-Agent": UA },
    properties: { authenticationMethod: "pass", userProperties: { tmeAppID: "qqmusic", business: "management", hashTag: id, clientTag: "management.user", userID: id } },
    reconnectPeriod: 0, connectTimeout: 15000,
  });
  let sr = null;
  client.on("packetreceive", (p) => {
    if (p.cmd === "connack") { sr = p?.properties?.serverReference ?? null; if (p.reasonCode) console.log(ts(), "CONNACK reason=", p.reasonCode, "ref=", sr); }
    if (p.cmd === "publish") console.log(ts(), "PUBLISH", p.topic, "type=" + (p?.properties?.userProperties?.type ?? "?"), String(p.payload).slice(0, 100));
  });
  client.on("connect", () => {
    console.log(ts(), "已连接，订阅 management.qrcode_login/…");
    client.subscribe("management.qrcode_login/" + id, { qos: 0, properties: { userProperties: { authorization: "tmelogin", pubsub: "unicast" } } },
      (err) => console.log(ts(), err ? "订阅失败: " + err.message : "SUBACK 成功 ✅"));
  });
  client.on("message", async (topic, m) => {
    const type = String(m);
    if (!type.includes("cookies")) return;
    const ck = JSON.parse(type).cookies ?? {};
    const uin = ck.qqmusic_uin?.value, key = ck.qqmusic_key?.value;
    console.log(ts(), "收到 cookies！uin=", uin, "key=", String(key).slice(0, 10) + "…");
    const rr = await fetch("https://u.y.qq.com/cgi-bin/musicu.fcg", {
      method: "POST",
      headers: { "Content-Type": "application/json", Origin: "https://y.qq.com", Referer: "https://y.qq.com/", "User-Agent": UA },
      body: JSON.stringify({ comm: { ct: 23, cv: 0, tmeLoginType: 6 }, request: { module: "music.login.LoginServer", method: "Login", param: { musicid: Number(String(uin).replace(/^o/, "")), qrCodeID: id, token: key } } }),
    });
    const jj = await rr.json();
    const d = jj.request?.data ?? {};
    console.log(ts(), "Login 换凭据:", d.musickey ? "成功 musickey=" + String(d.musickey).slice(0, 10) + "…" : "失败 " + JSON.stringify(jj).slice(0, 200));
    process.exit(d.musickey ? 0 : 1);
  });
  client.on("error", (e) => console.log(ts(), "错误:", e.message));
  client.on("close", () => {
    if (sr) {
      const parts = path.replace(/\/$/, "").split("/");
      path = parts[parts.length - 1].includes(":") ? parts.slice(0, -1).concat(sr).join("/") : path.replace(/\/$/, "") + "/" + sr;
      console.log(ts(), "换节点 →", path); sr = null; client.end(true); setTimeout(dial, 300);
    }
  });
}
dial();
setTimeout(() => { console.log(ts(), "120 秒超时：全程没收到任何 PUBLISH（扫码动作没到服务端，或订阅收不到推送）"); process.exit(2); }, 120000);
