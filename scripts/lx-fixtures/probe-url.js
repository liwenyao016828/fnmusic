/**
 * 「活性校验」守门脚本 —— 把宿主 /resolve 的探测行为钉成可断言的三种结论。
 *
 * 它不联网、不计算：把 `info.musicInfo.name` 原样当直链返回。
 * 于是测试端**完全掌控宿主拿到的那条 URL**（本地 HTTP 服务的地址、死链、连不上的端口），
 * 正好覆盖 probeAudioUrl 的三条分支：
 *   · 证明能下（206 + 音频魔数）      → 200 + probed
 *   · 证明是死链（404，有状态码）     → tried 里记 dead，全部失败才 502
 *   · 探测没结论（TCP 连不上，无状态码）→ 不判死，兜底返回 200 + unproven
 *
 * ⚠️ 它**不是**音源脚本，别丢进 preset_sources；只由 scripts/lx-utils-check.mjs 加载。
 */
const { EVENT_NAMES, on, send } = globalThis.lx;

send(EVENT_NAMES.inited, {
  openDevTools: false,
  sources: { wy: { name: 'probe-url', type: 'music', actions: ['musicUrl'], qualitys: ['320k', 'flac'] } },
});

on(EVENT_NAMES.request, async ({ action, info }) => {
  if (action !== 'musicUrl') throw new Error('unsupported');
  const url = info && info.musicInfo && info.musicInfo.name;
  if (typeof url !== 'string' || !url) throw new Error('musicInfo.name 不是 URL');
  return url;
});
